package drm

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"html"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Embedded subtitle relay: follows the live WebVTT/TTML text tracks of a DASH
// manifest (wvtt / stpp fMP4 or plain .vtt/.ttml segments), optionally
// translates every cue, and serves them as HLS WebVTT renditions aligned to
// the copied video timeline. No transcoding is involved.

// SubTrack is one subtitle rendition offered in the HLS master playlist.
type SubTrack struct {
	ID   string // t<set>[-<target lang>]
	Set  int
	Lang string
	Name string
	To   string // translation target ("" = original)
}

type cue struct {
	s, e float64
	text string
}

type subCache struct {
	mu   sync.Mutex
	segs map[string]subSeg
	init map[string]int64
	wait map[string]chan struct{}
}

type subSeg struct {
	b  []byte
	at time.Time
}

var sc = &subCache{segs: map[string]subSeg{}, init: map[string]int64{}, wait: map[string]chan struct{}{}}

var langNames = map[string]string{"ar": "العربية", "en": "English", "fr": "Français", "tr": "Türkçe", "es": "Español", "de": "Deutsch", "fa": "فارسی", "ur": "اردو", "hi": "हिन्दी", "ru": "Русский", "it": "Italiano", "pt": "Português"}

func langName(l string) string {
	l = strings.ToLower(l)
	if n, ok := langNames[strings.SplitN(l, "-", 2)[0]]; ok {
		return n
	}
	if l == "" {
		return "Subtitles"
	}
	return l
}

func textSets(m mpdX) (periodX, []int) {
	p := m.Periods[len(m.Periods)-1]
	var idx []int
	for i, s := range p.Sets {
		if setKind(s) == "text" && len(s.Reps) > 0 {
			idx = append(idx, i)
		}
	}
	return p, idx
}

// SubTracks lists subtitle renditions of a running DRM stream (nil when none).
func (d *Service) SubTracks(ctx context.Context, id int64) []SubTrack {
	b, _, src, err := d.liveManifest(ctx, id)
	if err != nil {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(src.stream.DRM.Subtitles))
	if want == "" || want == "none" {
		return nil
	}
	var m mpdX
	if xml.Unmarshal(b, &m) != nil || len(m.Periods) == 0 {
		return nil
	}
	p, idx := textSets(m)
	var out []SubTrack
	for _, i := range idx {
		l := p.Sets[i].Lang
		if want != "all" && !strings.HasPrefix(strings.ToLower(l), want) && p.Sets[i].Reps[0].ID != want {
			continue
		}
		out = append(out, SubTrack{ID: "t" + strconv.Itoa(i), Set: i, Lang: l, Name: langName(l)})
	}
	if to := strings.ToLower(strings.TrimSpace(src.stream.DRM.SubTranslate)); to != "" && len(out) > 0 {
		from := out[0]
		for _, t := range out {
			if !strings.HasPrefix(strings.ToLower(t.Lang), to) {
				from = t
				break
			}
		}
		if !strings.HasPrefix(strings.ToLower(from.Lang), to) {
			tt := SubTrack{ID: from.ID + "-" + to, Set: from.Set, Lang: to, Name: langName(to) + " (AI)", To: to}
			out = append([]SubTrack{tt}, out...)
		}
	}
	return out
}

func parseTrackID(s string) (set int, to string, ok bool) {
	if !strings.HasPrefix(s, "t") {
		return 0, "", false
	}
	n, to, _ := strings.Cut(s[1:], "-")
	set, err := strconv.Atoi(n)
	return set, to, err == nil && set >= 0
}

func (d *Service) subTrack(ctx context.Context, id int64, set int) (*liveTrack, *liveTrack, *source, error) {
	b, final, src, err := d.liveManifest(ctx, id)
	if err != nil {
		return nil, nil, src, err
	}
	var m mpdX
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, nil, src, err
	}
	if len(m.Periods) == 0 {
		return nil, nil, src, fmt.Errorf("manifest has no period")
	}
	p := m.Periods[len(m.Periods)-1]
	if set >= len(p.Sets) || setKind(p.Sets[set]) != "text" || len(p.Sets[set].Reps) == 0 {
		return nil, nil, src, fmt.Errorf("no such subtitle track")
	}
	now := time.Now()
	lt, err := liveFrom(m, p, p.Sets[set], p.Sets[set].Reps[0], final, "text", now)
	if err != nil {
		return nil, nil, src, err
	}
	vt, _ := parseLive(b, final, "video", src.stream.DRM.Video, now)
	return lt, vt, src, nil
}

// ServeSubs serves /subs/{id}/{track}.m3u8 and /subs/{id}/{track}/{n}.vtt.
func (d *Service) ServeSubs(w http.ResponseWriter, r *http.Request, id int64, track, file string) {
	set, to, ok := parseTrackID(track)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	lt, vt, src, err := d.subTrack(ctx, id, set)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if file == "" {
		var b strings.Builder
		td := 1.0
		for _, s := range lt.segs {
			td = math.Max(td, float64(s.d)/float64(lt.ts))
		}
		fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n", int(math.Ceil(td)), lt.segs[0].num)
		for _, s := range lt.segs {
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s/%d.vtt\n", float64(s.d)/float64(lt.ts), track, s.num)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte(b.String()))
		// warm the newest segments (fetch + translate) before the player asks
		for _, s := range lt.segs[max(0, len(lt.segs)-2):] {
			go d.subSegment(context.Background(), id, track, to, lt, vt, src, s)
		}
		return
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(file, ".vtt"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var sg *seg
	for i := range lt.segs {
		if lt.segs[i].num == n {
			sg = &lt.segs[i]
		}
	}
	if last := lt.segs[len(lt.segs)-1]; sg == nil {
		// the window moved between playlist and segment request: extrapolate
		if n < last.num+3 && n > last.num-30 {
			x := seg{n, last.t + (n-last.num)*last.d, last.d}
			sg = &x
		}
	}
	if sg == nil {
		http.Error(w, "segment out of window", 404)
		return
	}
	b, err := d.subSegment(ctx, id, track, to, lt, vt, src, *sg)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Write(b)
}

func (d *Service) subSegment(ctx context.Context, id int64, track, to string, lt, vt *liveTrack, src *source, s seg) ([]byte, error) {
	key := fmt.Sprintf("%d|%s|%d", id, track, s.num)
	for {
		sc.mu.Lock()
		if c, ok := sc.segs[key]; ok {
			sc.mu.Unlock()
			return c.b, nil
		}
		if ch, ok := sc.wait[key]; ok {
			sc.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		sc.wait[key] = ch
		sc.mu.Unlock()
		b, err := d.buildSubSeg(ctx, lt, vt, src, s, to)
		sc.mu.Lock()
		delete(sc.wait, key)
		if err == nil {
			for k, v := range sc.segs {
				if time.Since(v.at) > 3*time.Minute {
					delete(sc.segs, k)
				}
			}
			sc.segs[key] = subSeg{b, time.Now()}
		}
		sc.mu.Unlock()
		close(ch)
		if err != nil {
			d.St.Log("stream", id, "warn", "subtitles "+track+": "+err.Error())
		}
		return b, err
	}
}

func (d *Service) buildSubSeg(ctx context.Context, lt, vt *liveTrack, src *source, s seg, to string) ([]byte, error) {
	c, err := d.client(&src.stream, "media")
	if err != nil {
		return nil, err
	}
	ts := lt.ts
	if lt.init != "" {
		sc.mu.Lock()
		v, ok := sc.init[lt.init]
		sc.mu.Unlock()
		if !ok {
			var ib strings.Builder
			if err := d.get(ctx, c, src, lt.init, &ib); err != nil {
				return nil, fmt.Errorf("init: %w", err)
			}
			v = mdhdTimescale([]byte(ib.String()))
			sc.mu.Lock()
			sc.init[lt.init] = v
			sc.mu.Unlock()
		}
		if v > 0 {
			ts = v
		}
	}
	var sb strings.Builder
	if err := d.get(ctx, c, src, lt.url(s), &sb); err != nil {
		return nil, err
	}
	start := float64(s.t) / float64(lt.ts)
	cues := parseSubSegment([]byte(sb.String()), ts, start)
	if to != "" {
		st := d.settings()
		var wg sync.WaitGroup
		for i := range cues {
			wg.Add(1)
			go func(c *cue) {
				defer wg.Done()
				if t, err := tr.translate(ctx, st, c.text, to); err == nil && t != "" {
					c.text = t
				}
			}(&cues[i])
		}
		wg.Wait()
	}
	// presentation time of the text track mapped onto the copied video timestamps
	off := -float64(lt.pto) / float64(lt.ts)
	if vt != nil {
		off += float64(vt.pto) / float64(vt.ts)
	}
	return writeVTT(cues, start+off, off), nil
}

func writeVTT(cues []cue, segStart, off float64) []byte {
	var b strings.Builder
	mp := int64(math.Round(segStart*90000)) % (1 << 33)
	fmt.Fprintf(&b, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:%d,LOCAL:00:00:00.000\n\n", mp)
	for _, c := range cues {
		s, e := c.s+off-segStart, c.e+off-segStart
		if e <= 0 || strings.TrimSpace(c.text) == "" {
			continue
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttTime(math.Max(s, 0)), vttTime(e), c.text)
	}
	return []byte(b.String())
}

func vttTime(t float64) string {
	ms := int64(math.Round(t * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// ---- segment parsing ----

func boxes(b []byte, f func(typ string, body []byte, off int) bool) {
	for i := 0; i+8 <= len(b); {
		sz := int(binary.BigEndian.Uint32(b[i:]))
		typ, h := string(b[i+4:i+8]), 8
		if sz == 1 && i+16 <= len(b) {
			sz, h = int(binary.BigEndian.Uint64(b[i+8:])), 16
		} else if sz == 0 {
			sz = len(b) - i
		}
		if sz < h || i+sz > len(b) {
			return
		}
		if !f(typ, b[i+h:i+sz], i) {
			return
		}
		i += sz
	}
}

func child(b []byte, path ...string) []byte {
	for _, p := range path {
		var nb []byte
		boxes(b, func(t string, body []byte, _ int) bool {
			if t == p {
				nb = body
				return false
			}
			return true
		})
		if nb == nil {
			return nil
		}
		b = nb
	}
	return b
}

func mdhdTimescale(init []byte) int64 {
	m := child(init, "moov", "trak", "mdia", "mdhd")
	if len(m) < 24 {
		return 0
	}
	if m[0] == 1 {
		if len(m) < 24 {
			return 0
		}
		return int64(binary.BigEndian.Uint32(m[20:]))
	}
	return int64(binary.BigEndian.Uint32(m[12:]))
}

type sample struct {
	t, d int64
	data []byte
}

// fragSamples returns the samples of every moof/mdat pair in a media segment.
func fragSamples(b []byte) []sample {
	var out []sample
	var moofOff int
	var moof []byte
	boxes(b, func(typ string, body []byte, off int) bool {
		switch typ {
		case "moof":
			moof, moofOff = body, off
		case "mdat":
			if moof == nil {
				return true
			}
			mdatStart := off + 8
			if binary.BigEndian.Uint32(b[off:]) == 1 {
				mdatStart = off + 16
			}
			out = append(out, trafSamples(b, moof, moofOff, mdatStart)...)
			moof = nil
		}
		return true
	})
	return out
}

func trafSamples(file, moof []byte, moofOff, mdatStart int) []sample {
	var out []sample
	boxes(moof, func(typ string, traf []byte, _ int) bool {
		if typ != "traf" {
			return true
		}
		var defDur, defSize uint32
		var base int64
		dataBase := moofOff
		boxes(traf, func(t string, x []byte, _ int) bool {
			switch t {
			case "tfhd":
				if len(x) < 8 {
					return true
				}
				fl := binary.BigEndian.Uint32(x) & 0xffffff
				p := 8
				if fl&0x01 != 0 && len(x) >= p+8 {
					dataBase = int(binary.BigEndian.Uint64(x[p:]))
					p += 8
				}
				if fl&0x02 != 0 {
					p += 4
				}
				if fl&0x08 != 0 && len(x) >= p+4 {
					defDur = binary.BigEndian.Uint32(x[p:])
					p += 4
				}
				if fl&0x10 != 0 && len(x) >= p+4 {
					defSize = binary.BigEndian.Uint32(x[p:])
				}
			case "tfdt":
				if len(x) >= 12 && x[0] == 1 {
					base = int64(binary.BigEndian.Uint64(x[4:]))
				} else if len(x) >= 8 {
					base = int64(binary.BigEndian.Uint32(x[4:]))
				}
			case "trun":
				if len(x) < 8 {
					return true
				}
				fl := binary.BigEndian.Uint32(x) & 0xffffff
				n := int(binary.BigEndian.Uint32(x[4:]))
				p := 8
				pos := mdatStart
				if fl&0x01 != 0 && len(x) >= p+4 {
					pos = dataBase + int(int32(binary.BigEndian.Uint32(x[p:])))
					p += 4
				}
				if fl&0x04 != 0 {
					p += 4
				}
				t := base
				for i := 0; i < n; i++ {
					dur, size := defDur, defSize
					if fl&0x100 != 0 && len(x) >= p+4 {
						dur = binary.BigEndian.Uint32(x[p:])
						p += 4
					}
					if fl&0x200 != 0 && len(x) >= p+4 {
						size = binary.BigEndian.Uint32(x[p:])
						p += 4
					}
					if fl&0x400 != 0 {
						p += 4
					}
					if fl&0x800 != 0 {
						p += 4
					}
					if size == 0 && n == 1 {
						size = uint32(len(file) - pos)
					}
					if pos < 0 || pos+int(size) > len(file) {
						break
					}
					out = append(out, sample{t, int64(dur), file[pos : pos+int(size)]})
					pos += int(size)
					t += int64(dur)
				}
			}
			return true
		})
		return true
	})
	return out
}

// parseSubSegment extracts cues (absolute media seconds) from a text segment.
func parseSubSegment(b []byte, ts int64, segStart float64) []cue {
	trim := strings.TrimSpace(strings.TrimPrefix(string(b[:min(len(b), 64)]), "\ufeff"))
	switch {
	case strings.HasPrefix(trim, "WEBVTT"):
		return parseVTT(string(b), segStart)
	case strings.HasPrefix(trim, "<"):
		return parseTTML(string(b), segStart, segStart)
	}
	if ts <= 0 {
		ts = 1000
	}
	var out []cue
	for _, s := range fragSamples(b) {
		st, en := float64(s.t)/float64(ts), float64(s.t+s.d)/float64(ts)
		if bytes := s.data; len(bytes) > 0 && (bytes[0] == '<' || strings.HasPrefix(strings.TrimSpace(string(bytes[:min(len(bytes), 64)])), "<")) {
			out = append(out, parseTTML(string(bytes), st, segStart)...)
			continue
		}
		boxes(s.data, func(t string, body []byte, _ int) bool {
			if t == "vttc" {
				if p := child(body, "payl"); p != nil {
					out = append(out, cue{st, en, strings.TrimSpace(string(p))})
				}
			}
			return true
		})
	}
	return out
}

var (
	vttLineRe = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{2}[.,]\d{1,3})\s+-->\s+((?:\d+:)?\d{1,2}:\d{2}[.,]\d{1,3})`)
	tsMapRe   = regexp.MustCompile(`MPEGTS:(\d+)`)
	tsLocalRe = regexp.MustCompile(`LOCAL:((?:\d+:)?\d{1,2}:\d{2}[.,]\d{1,3})`)
	pRe       = regexp.MustCompile(`(?s)<(?:\w+:)?p\b([^>]*)>(.*?)</(?:\w+:)?p>`)
	attrRe    = regexp.MustCompile(`\b(begin|end|dur)\s*=\s*"([^"]+)"`)
	brRe      = regexp.MustCompile(`<(?:\w+:)?br\s*/?>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	tickRe    = regexp.MustCompile(`ttp:tickRate\s*=\s*"(\d+)"`)
	frameRe   = regexp.MustCompile(`ttp:frameRate\s*=\s*"(\d+)"`)
)

func clock(s string) float64 {
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ":")
	var t float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		t = t*60 + v
	}
	return t
}

func parseVTT(s string, segStart float64) []cue {
	off := 0.0
	if m := tsMapRe.FindStringSubmatch(s); m != nil {
		mp, _ := strconv.ParseFloat(m[1], 64)
		loc := 0.0
		if l := tsLocalRe.FindStringSubmatch(s); l != nil {
			loc = clock(l[1])
		}
		off = mp/90000 - loc
	}
	var out []cue
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		m := vttLineRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		var txt []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			txt = append(txt, lines[i])
		}
		out = append(out, cue{clock(m[1]) + off, clock(m[2]) + off, strings.Join(txt, "\n")})
	}
	return out
}

func ttmlTime(v string, tick, fps float64) float64 {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasSuffix(v, "ms"):
		f, _ := strconv.ParseFloat(strings.TrimSuffix(v, "ms"), 64)
		return f / 1000
	case strings.HasSuffix(v, "s"):
		f, _ := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64)
		return f
	case strings.HasSuffix(v, "t"):
		f, _ := strconv.ParseFloat(strings.TrimSuffix(v, "t"), 64)
		return f / tick
	case strings.HasSuffix(v, "m"):
		f, _ := strconv.ParseFloat(strings.TrimSuffix(v, "m"), 64)
		return f * 60
	case strings.HasSuffix(v, "h"):
		f, _ := strconv.ParseFloat(strings.TrimSuffix(v, "h"), 64)
		return f * 3600
	}
	if p := strings.Split(v, ":"); len(p) == 4 {
		fr, _ := strconv.ParseFloat(p[3], 64)
		return clock(strings.Join(p[:3], ":")) + fr/fps
	}
	return clock(v)
}

// parseTTML reads <p begin end> cues. Times are media-absolute unless they fall
// before the sample, in which case they are relative to the sample start.
func parseTTML(s string, sampleStart, segStart float64) []cue {
	tick, fps := 10000000.0, 30.0
	if m := tickRe.FindStringSubmatch(s); m != nil {
		tick, _ = strconv.ParseFloat(m[1], 64)
	}
	if m := frameRe.FindStringSubmatch(s); m != nil {
		fps, _ = strconv.ParseFloat(m[1], 64)
	}
	var out []cue
	for _, m := range pRe.FindAllStringSubmatch(s, -1) {
		var b, e, dur float64
		hasE := false
		for _, a := range attrRe.FindAllStringSubmatch(m[1], -1) {
			switch a[1] {
			case "begin":
				b = ttmlTime(a[2], tick, fps)
			case "end":
				e, hasE = ttmlTime(a[2], tick, fps), true
			case "dur":
				dur = ttmlTime(a[2], tick, fps)
			}
		}
		if !hasE {
			e = b + dur
		}
		txt := brRe.ReplaceAllString(m[2], "\n")
		txt = html.UnescapeString(tagRe.ReplaceAllString(txt, ""))
		var ls []string
		for _, l := range strings.Split(txt, "\n") {
			if l = strings.Join(strings.Fields(l), " "); l != "" {
				ls = append(ls, l)
			}
		}
		if sampleStart > 60 && e < sampleStart-30 {
			b, e = b+sampleStart, e+sampleStart
		}
		out = append(out, cue{b, e, strings.Join(ls, "\n")})
	}
	return out
}
