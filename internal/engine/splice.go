package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// splicer relays a live MPEG-TS HTTP source to ffmpeg. Many IPTV providers
// drop the connection every few minutes and resend a few seconds on
// reconnect; the splicer reconnects at once and discards the resent packets
// (per-PID DTS) so ffmpeg sees one continuous stream.
type splicer struct {
	url, ua, ref, hdr string
	hc                *http.Client
	idle              time.Duration
	log               func(string)

	pmtPID, vidPID int
	last           map[int]int64 // DTS of the last complete PES forwarded
	open           map[int]bool
	keep           map[int]bool
	overlap        map[int]bool
	hold           map[int][]byte // current PES, forwarded once the next one starts
	holdTS         map[int]int64
	resync         bool
	since          time.Time
	dropped        int

	// rate window: source media time vs wall time, and time spent blocked on ffmpeg
	winT       time.Time
	winTS, vTS int64
	wait       time.Duration
	haveTS     bool

	// pace releases input to ffmpeg at the source's real-time rate (video DTS
	// against wall clock), at most this far ahead; 0 passes bursts straight on.
	// Providers deliver in bursts, which made ffmpeg decode and NVENC encode
	// in bursts: ENC jumping 0%..100% and speed swinging around 1x.
	pace time.Duration
	// trim lets the pacer catch up when it holds more than it needs (low latency)
	trim bool
	pc   atomic.Pointer[pacer]
	// latest PAT/PMT and the offset in the pending output of the last video keyframe
	pat, pmt []byte
	keyAt    int

	// persistent (low latency) mode: the source connection outlives ffmpeg
	// restarts; each new ffmpeg attaches and starts on the cached GOP
	mu   sync.Mutex
	gop  []paceChunk
	gopB int
	dead chan struct{}
}

var errWrite = errors.New("ffmpeg closed input")

func spliceable(in string) bool {
	u, err := url.Parse(in)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	ext := strings.ToLower(path.Ext(u.Path))
	return ext == ".ts" || ext == ""
}

func newSplicer(in, ua, ref, hdr, proxy string, idle time.Duration, log func(string)) *splicer {
	if idle < 5*time.Second {
		idle = 5 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = idle
	tr.DisableCompression = true
	if proxy != "" {
		if pu, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &splicer{url: in, ua: ua, ref: ref, hdr: hdr, idle: idle, log: log, hc: &http.Client{Transport: tr},
		dead: make(chan struct{}), pmtPID: -1, vidPID: -1, last: map[int]int64{}, open: map[int]bool{}, keep: map[int]bool{},
		overlap: map[int]bool{}, hold: map[int][]byte{}, holdTS: map[int]int64{}, winTS: -1, keyAt: -1, pace: 700 * time.Millisecond}
}

func (s *splicer) run(ctx context.Context, w io.Writer) {
	if s.pace > 0 {
		p := newPacerWith(w, s.pace, s.log, s.trim, nil)
		s.pc.Store(p)
		defer p.close()
		w = p
	}
	s.loop(ctx, w)
}

// serve keeps the source connection open until ctx ends, feeding whichever
// ffmpeg is attached (attach) and dropping data while none is.
func (s *splicer) serve(ctx context.Context) {
	defer close(s.dead)
	s.loop(ctx, feedW{s})
}

type feedW struct{ s *splicer }

func (feedW) Write(b []byte) (int, error) { return len(b), nil }

func (s *splicer) alive() bool {
	select {
	case <-s.dead:
		return false
	default:
		return true
	}
}

func (s *splicer) feed(b []byte, ts int64, key int, psi []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key >= 0 && psi != nil {
		s.gop, s.gopB = s.gop[:0], 0
	}
	if len(s.gop) > 0 || key >= 0 && psi != nil {
		if s.gopB += len(b); s.gopB > 32<<20 {
			s.gop, s.gopB = nil, 0
		} else {
			s.gop = append(s.gop, paceChunk{b: append([]byte(nil), b...), ts: ts, key: key, psi: psi})
		}
	}
	if p := s.pc.Load(); p != nil {
		if p.pushKey(b, ts, key, psi) != nil {
			s.pc.CompareAndSwap(p, nil)
		}
	}
}

// attach feeds w until ffmpeg stops reading, ctx ends or the source dies.
// A reattach starts on the newest cached keyframe at the live edge.
func (s *splicer) attach(ctx context.Context, w io.Writer) {
	s.mu.Lock()
	var pre []paceChunk
	if len(s.gop) > 0 {
		pre = make([]paceChunk, len(s.gop))
		copy(pre, s.gop)
		c := pre[0]
		c.b = append(append([]byte(nil), c.psi...), c.b[c.key:]...)
		pre[0] = c
	}
	p := newPacerWith(w, s.pace, s.log, s.trim, pre)
	s.pc.Store(p)
	s.mu.Unlock()
	select {
	case <-p.done:
	case <-ctx.Done():
	case <-s.dead:
	}
	s.pc.CompareAndSwap(p, nil)
	p.close()
}

func (s *splicer) loop(ctx context.Context, w io.Writer) {
	fails := 0
	for ctx.Err() == nil {
		got, err := s.once(ctx, w)
		if errors.Is(err, errWrite) || ctx.Err() != nil {
			return
		}
		if got {
			fails = 0
		} else if fails++; fails > 5 {
			s.log(fmt.Sprintf("input: giving up after %d failed reconnects: %v", fails-1, err))
			return
		}
		// the PES cut by the disconnect is incomplete: drop it, the resend has it whole
		s.resync, s.since, s.dropped = true, time.Now(), 0
		s.winT, s.winTS, s.wait = time.Now(), -1, 0
		clear(s.open)
		clear(s.overlap)
		clear(s.hold)
		clear(s.holdTS)
		for k := range s.keep {
			s.keep[k] = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(fails) * 500 * time.Millisecond):
		}
	}
}

func (s *splicer) once(ctx context.Context, w io.Writer) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.url, nil)
	if err != nil {
		return false, err
	}
	// same request ffmpeg's http protocol sends; some panels pace other clients
	req.Header.Set("User-Agent", "Lavf/60.16.100")
	if s.ua != "" {
		req.Header.Set("User-Agent", s.ua)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Range", "bytes=0-")
	req.Header.Set("Icy-MetaData", "1")
	req.Close = true
	if s.ref != "" {
		req.Header.Set("Referer", s.ref)
	}
	for _, l := range strings.Split(s.hdr, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k != "" {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	wd := time.AfterFunc(s.idle, func() { resp.Body.Close() })
	defer wd.Stop()
	br := bufio.NewReaderSize(resp.Body, 256<<10)
	out := make([]byte, 0, 188*128)
	pkt := make([]byte, 188)
	got := false
	flush := func() error {
		if len(out) == 0 {
			return nil
		}
		t0 := time.Now()
		var err error
		if _, ok := w.(feedW); ok {
			ts := int64(-1)
			if s.haveTS {
				ts = s.vTS
			}
			var psi []byte
			if s.keyAt >= 0 && s.pat != nil && s.pmt != nil {
				psi = append(append([]byte(nil), s.pat...), s.pmt...)
			}
			s.feed(out, ts, s.keyAt, psi)
		} else if p, ok := w.(*pacer); ok {
			ts := int64(-1)
			if s.haveTS {
				ts = s.vTS
			}
			var psi []byte
			if s.keyAt >= 0 && s.pat != nil && s.pmt != nil {
				psi = append(append([]byte(nil), s.pat...), s.pmt...)
			}
			err = p.pushKey(out, ts, s.keyAt, psi)
		} else {
			_, err = w.Write(out)
		}
		if err != nil {
			return errWrite
		}
		s.wait += time.Since(t0)
		out, s.keyAt = out[:0], -1
		return nil
	}
	skipped := 0
	for {
		if _, err := io.ReadFull(br, pkt); err != nil {
			if e := flush(); e != nil {
				return got, e
			}
			return got, err
		}
		wd.Reset(s.idle)
		got = true
		for pkt[0] != 0x47 {
			if skipped++; skipped > 64<<10 {
				return got, errors.New("not an MPEG-TS stream")
			}
			copy(pkt, pkt[1:])
			c, err := br.ReadByte()
			if err != nil {
				return got, err
			}
			pkt[187] = c
		}
		out = s.pass(pkt, out)
		if el := time.Since(s.winT); el >= time.Minute {
			if s.winTS >= 0 {
				media := float64((s.vTS-s.winTS)&(1<<33-1)) / 90000
				s.log(fmt.Sprintf("input: source %.2fx realtime, waiting on ffmpeg %.0f%%", media/el.Seconds(), 100*s.wait.Seconds()/el.Seconds()))
			}
			s.winT, s.winTS, s.wait = time.Now(), -1, 0
		}
		if len(out) >= cap(out) || (br.Buffered() < 188 && len(out) > 0) {
			if err := flush(); err != nil {
				return got, err
			}
		}
	}
}

// pass appends the packet to out if it is forwarded. PES packets are held
// per PID until the next PES of that PID starts.
func (s *splicer) pass(p, out []byte) []byte {
	pid := int(p[1]&0x1f)<<8 | int(p[2])
	pusi := p[1]&0x40 != 0
	if pid == 0 || pid == 0x1fff || pid == s.pmtPID || pid < 0x20 {
		if pusi && pid == 0 {
			if pm := patPMT(p); pm > 0 {
				s.pmtPID = pm
				s.pat = append(s.pat[:0], p...)
			}
		} else if pusi && pid == s.pmtPID {
			if v := pmtVideo(p); v > 0 {
				s.vidPID = v
				s.pmt = append(s.pmt[:0], p...)
			}
		}
		return append(out, p...)
	}
	if pusi {
		ts, ok := pesTS(p)
		if ok && pid == s.vidPID {
			if s.winTS < 0 {
				s.winTS = ts
			}
			s.vTS, s.haveTS = ts, true
		}
		if !ok {
			s.keep[pid] = true
			return append(out, p...)
		}
		keep := true
		if s.resync && !s.open[pid] {
			l, seen := s.last[pid]
			if seen && !newer(ts, l) {
				s.overlap[pid] = true
				keep = false
			} else {
				// right after an overlap the next frame continues the stream;
				// after a gap video has to restart on a keyframe
				keep = pid != s.vidPID || !seen || s.overlap[pid] || p[3]&0x20 != 0 && p[4] > 0 && p[5]&0x40 != 0 || time.Since(s.since) > 4*time.Second
			}
			if keep {
				s.open[pid] = true
				if pid == s.vidPID {
					s.resync = false
					s.log(fmt.Sprintf("input reconnected, skipped %d resent packets", s.dropped))
				}
			}
		}
		s.keep[pid] = keep
		if keep {
			if h := s.hold[pid]; len(h) > 0 {
				if pid == s.vidPID && h[3]&0x20 != 0 && h[4] > 0 && h[5]&0x40 != 0 {
					s.keyAt = len(out)
				}
				out = append(out, h...)
				s.last[pid] = s.holdTS[pid]
			}
			s.hold[pid], s.holdTS[pid] = append(s.hold[pid][:0], p...), ts
			return out
		}
	}
	if k, ok := s.keep[pid]; ok && !k {
		s.dropped++
		return out
	}
	if _, held := s.holdTS[pid]; held {
		s.hold[pid] = append(s.hold[pid], p...)
		return out
	}
	return append(out, p...)
}

// newer reports whether ts is after last (33-bit wrap). A jump back of more
// than a minute is a new timeline and counts as newer.
func newer(ts, last int64) bool {
	const m = 1<<33 - 1
	d := (ts - last) & m
	if d == 0 {
		return false
	}
	if d < 1<<32 {
		return true
	}
	return (m+1)-d > 60*90000
}

func pesTS(p []byte) (int64, bool) {
	if p[3]&0x10 == 0 {
		return 0, false
	}
	o := 4
	if p[3]&0x20 != 0 {
		o += 1 + int(p[4])
	}
	if o+19 > 188 || p[o] != 0 || p[o+1] != 0 || p[o+2] != 1 {
		return 0, false
	}
	f := p[o+7] >> 6
	switch {
	case f == 3:
		return tsval(p[o+14:]), true
	case f&2 != 0:
		return tsval(p[o+9:]), true
	}
	return 0, false
}

func tsval(b []byte) int64 {
	return int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}

// buffered is how much media (ms) the pacer holds back from ffmpeg.
func (s *splicer) buffered() int64 {
	if p := s.pc.Load(); p != nil {
		return p.held.Load()
	}
	return 0
}

type paceChunk struct {
	b   []byte
	ts  int64  // 90kHz video DTS, -1 = unknown
	key int    // offset of a video keyframe in b, -1 none
	psi []byte // PAT+PMT to prepend when starting at key
	// fast: backlog released at once after a live-edge jump
	fast bool
}

// pacer decouples reading the source from feeding ffmpeg: the network side
// never blocks, and chunks reach ffmpeg no earlier than their media time.
type pacer struct {
	w    io.Writer
	lead time.Duration
	log  func(string)
	ch   chan paceChunk
	done chan struct{}
	stop chan struct{}
	err  error
	trim bool
	pre  []paceChunk
	last atomic.Int64 // newest video DTS pushed, -1 none
	held atomic.Int64 // ms of media queued behind the chunk being written
}

func newPacer(w io.Writer, lead time.Duration, log func(string)) *pacer {
	return newPacerWith(w, lead, log, false, nil)
}

// pre: cached GOP of a kept-open source, released at once at the live edge
func newPacerWith(w io.Writer, lead time.Duration, log func(string), trim bool, pre []paceChunk) *pacer {
	p := &pacer{w: w, lead: lead, log: log, ch: make(chan paceChunk, 8192), done: make(chan struct{}), stop: make(chan struct{}), trim: trim, pre: pre}
	p.last.Store(-1)
	for i := len(pre) - 1; i >= 0; i-- {
		if pre[i].ts >= 0 {
			p.last.Store(pre[i].ts)
			break
		}
	}
	go p.loop()
	return p
}

func (p *pacer) Write(b []byte) (int, error) { return len(b), p.push(b, -1) }

func (p *pacer) push(b []byte, ts int64) error { return p.pushKey(b, ts, -1, nil) }

func (p *pacer) pushKey(b []byte, ts int64, key int, psi []byte) error {
	select {
	case <-p.done:
		return errWrite
	default:
	}
	if ts >= 0 {
		p.last.Store(ts)
	}
	select {
	case p.ch <- paceChunk{b: append([]byte(nil), b...), ts: ts, key: key, psi: psi}:
		return nil
	case <-p.done:
		return errWrite
	}
}

func (p *pacer) close() {
	close(p.stop)
	<-p.done
}

func (p *pacer) loop() {
	defer close(p.done)
	const mask = 1<<33 - 1
	var t0 time.Time
	var ts0 int64 = -1
	// trim: keep the smallest backlog seen per window near keep; drop the rest
	// by releasing up to 10% faster than real time, which ffmpeg absorbs evenly
	const win, keep, step = 5 * time.Second, 300 * time.Millisecond, 500 * time.Millisecond
	minHeld, winAt := time.Duration(1<<62), time.Now()
	var pend []paceChunk
	if len(p.pre) > 0 {
		pend, p.pre = p.pre, nil
		for i := range pend {
			pend[i].fast = true
		}
		for _, x := range pend {
			if x.ts >= 0 {
				ts0 = x.ts
				break
			}
		}
		t0 = time.Now()
		if l := p.last.Load(); ts0 >= 0 && l >= 0 {
			t0 = t0.Add(-time.Duration((l-ts0)&mask) * time.Second / 90000)
		}
		p.log("input: restarted on the open source connection at the live edge")
	} else if p.trim {
		var ok bool
		if pend, t0, ts0, ok = p.liveStart(); !ok {
			return
		}
	}
	for {
		if len(pend) == 0 {
			select {
			case x := <-p.ch:
				pend = append(pend, x)
			case <-p.stop:
				return
			}
		}
		if p.trim {
			for drained := false; !drained; {
				select {
				case x := <-p.ch:
					pend = append(pend, x)
				default:
					drained = true
				}
			}
			pend = p.jump(pend, &t0, &ts0)
		}
		c := pend[0]
		pend = pend[1:]
		if c.ts >= 0 {
			now := time.Now()
			if ts0 < 0 {
				t0, ts0 = now, c.ts
			}
			if l := p.last.Load(); l >= 0 {
				if q := (l - c.ts) & mask; q < mask/2 {
					h := time.Duration(q) * time.Second / 90000
					p.held.Store(h.Milliseconds())
					minHeld = min(minHeld, h)
				}
			}
			if now.Sub(winAt) >= win {
				if p.trim && minHeld > keep && minHeld < 1<<62 {
					t0 = t0.Add(-min(minHeld-keep, step))
				}
				minHeld, winAt = 1<<62, now
			}
			d := (c.ts - ts0) & mask
			media := time.Duration(d) * time.Second / 90000
			wait := t0.Add(media).Sub(now) - p.lead
			switch {
			case d > mask/2 || wait > 5*time.Second:
				// timeline jump (new programme, provider clock reset): rebase
				p.log(fmt.Sprintf("input: timestamp jump %+.1fs, pacing rebased", (media - now.Sub(t0)).Seconds()))
				t0, ts0 = now, c.ts
			case wait < -p.lead-2*time.Second && !c.fast:
				// source fell behind: don't replay the gap as a burst later
				t0 = now.Add(-media)
			case wait > 0:
				select {
				case <-time.After(wait):
				case <-p.stop:
					return
				}
			}
		}
		if _, err := p.w.Write(c.b); err != nil {
			p.err = err
			return
		}
	}
}

// liveStart swallows the backlog a provider sends on connect (often tens of
// seconds) and starts ffmpeg on the newest keyframe in it, so the output runs
// at the live edge instead of replaying that backlog at real time forever.
func (p *pacer) liveStart() (pend []paceChunk, t0 time.Time, ts0 int64, ok bool) {
	const mask = 1<<33 - 1
	media := func(d int64) time.Duration { return time.Duration(d&mask) * time.Second / 90000 }
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	var w0, grew time.Time
	var f0, last int64 = -1, -1
	var ahead time.Duration
	for {
		select {
		case c := <-p.ch:
			pend = append(pend, c)
			if c.ts >= 0 {
				now := time.Now()
				if f0 < 0 {
					w0, f0, grew = now, c.ts, now
				}
				last = c.ts
				if a := media(c.ts-f0) - now.Sub(w0); a > ahead+200*time.Millisecond && a < time.Hour {
					ahead, grew = a, now
				}
			}
		case <-tick.C:
		case <-p.stop:
			return nil, t0, ts0, false
		}
		if el := time.Since(start); f0 >= 0 && time.Since(grew) >= time.Second || el >= 10*time.Second {
			break
		}
	}
	k := -1
	for i := len(pend) - 1; i >= 0; i-- {
		if pend[i].key >= 0 && pend[i].psi != nil {
			k = i
			break
		}
	}
	if k < 0 {
		return pend, time.Now(), -1, true
	}
	c := pend[k]
	skipped := media(c.ts - f0)
	c.b = append(c.psi, c.b[c.key:]...)
	pend = append([]paceChunk{c}, pend[k+1:]...)
	for i := range pend {
		pend[i].fast = true
	}
	ts0 = -1
	for _, x := range pend {
		if x.ts >= 0 {
			ts0 = x.ts
			break
		}
	}
	t0 = time.Now()
	if ts0 >= 0 && last >= 0 {
		t0 = t0.Add(-media(last - ts0))
	}
	p.log(fmt.Sprintf("input: started at live edge, skipped %.1fs of provider backlog", skipped.Seconds()))
	return pend, t0, ts0, true
}

// jump skips to the newest keyframe once the queued input runs more than
// 1.5s behind the live edge (a provider burst after connect or reconnect).
func (p *pacer) jump(pend []paceChunk, t0 *time.Time, ts0 *int64) []paceChunk {
	const mask = 1<<33 - 1
	media := func(d int64) time.Duration { return time.Duration(d&mask) * time.Second / 90000 }
	newest, head := p.last.Load(), int64(-1)
	for _, c := range pend {
		if c.ts >= 0 {
			head = c.ts
			break
		}
	}
	if newest < 0 || head < 0 || media(newest-head) < 1500*time.Millisecond || media(newest-head) > time.Hour {
		return pend
	}
	k := -1
	for i := len(pend) - 1; i > 0; i-- {
		if pend[i].key >= 0 && pend[i].psi != nil {
			k = i
			break
		}
	}
	if k < 0 || pend[k].ts < 0 || media(pend[k].ts-head) < 500*time.Millisecond || media(pend[k].ts-head) > time.Hour {
		return pend
	}
	c := pend[k]
	p.log(fmt.Sprintf("input: %.1fs behind live, jumped to newest keyframe", media(newest-head).Seconds()))
	c.b = append(c.psi, c.b[c.key:]...)
	pend = append(pend[:0], pend[k:]...)
	pend[0] = c
	for i := range pend {
		pend[i].fast = true
	}
	*t0, *ts0 = time.Now().Add(-media(newest-c.ts)), c.ts
	return pend
}
