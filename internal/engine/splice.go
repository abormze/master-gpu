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
		pmtPID: -1, vidPID: -1, last: map[int]int64{}, open: map[int]bool{}, keep: map[int]bool{},
		overlap: map[int]bool{}, hold: map[int][]byte{}, holdTS: map[int]int64{}, winTS: -1}
}

func (s *splicer) run(ctx context.Context, w io.Writer) {
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
		if _, err := w.Write(out); err != nil {
			return errWrite
		}
		s.wait += time.Since(t0)
		out = out[:0]
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
			}
		} else if pusi && pid == s.pmtPID {
			if v := pmtVideo(p); v > 0 {
				s.vidPID = v
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
			s.vTS = ts
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
