package drm

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Live DASH track fetcher: follows one representation of a live MPD and writes
// init + media segments in order as one continuous fragmented MP4. FFmpeg reads
// video and audio as separate inputs, which avoids the dash demuxer's timeline
// drift on long-running live manifests.

type mpdX struct {
	AST     string    `xml:"availabilityStartTime,attr"`
	MUP     string    `xml:"minimumUpdatePeriod,attr"`
	BaseURL []string  `xml:"BaseURL"`
	Periods []periodX `xml:"Period"`
}

type periodX struct {
	Start   string `xml:"start,attr"`
	BaseURL []string
	Sets    []setX `xml:"AdaptationSet"`
}

type setX struct {
	ContentType string   `xml:"contentType,attr"`
	MimeType    string   `xml:"mimeType,attr"`
	Codecs      string   `xml:"codecs,attr"`
	Lang        string   `xml:"lang,attr"`
	BaseURL     []string `xml:"BaseURL"`
	Tmpl        *tmplX   `xml:"SegmentTemplate"`
	Reps        []repX   `xml:"Representation"`
}

type repX struct {
	ID        string   `xml:"id,attr"`
	Bandwidth int64    `xml:"bandwidth,attr"`
	Height    int      `xml:"height,attr"`
	MimeType  string   `xml:"mimeType,attr"`
	Codecs    string   `xml:"codecs,attr"`
	BaseURL   []string `xml:"BaseURL"`
	Tmpl      *tmplX   `xml:"SegmentTemplate"`
}

type tmplX struct {
	Timescale   string     `xml:"timescale,attr"`
	Media       string     `xml:"media,attr"`
	Init        string     `xml:"initialization,attr"`
	StartNumber string     `xml:"startNumber,attr"`
	Duration    string     `xml:"duration,attr"`
	PTO         string     `xml:"presentationTimeOffset,attr"`
	Timeline    *timelineX `xml:"SegmentTimeline"`
}

type timelineX struct {
	S []struct {
		T *int64 `xml:"t,attr"`
		D int64  `xml:"d,attr"`
		R int64  `xml:"r,attr"`
	} `xml:"S"`
}

type seg struct{ num, t, d int64 }

type liveTrack struct {
	init  string
	media string
	ts    int64
	segs  []seg
	every time.Duration
	pto   int64
}

func (t *liveTrack) url(s seg) string {
	return segVarRe.ReplaceAllStringFunc(t.media, func(m string) string {
		p := segVarRe.FindStringSubmatch(m)
		v := s.num
		if p[1] == "Time" {
			v = s.t
		}
		if p[3] != "" {
			w, _ := strconv.Atoi(p[3])
			return fmt.Sprintf("%0*d", w, v)
		}
		return strconv.FormatInt(v, 10)
	})
}

var (
	segVarRe = regexp.MustCompile(`\$(Number|Time)(%0(\d+)d)?\$`)
	isoDurRe = regexp.MustCompile(`^P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?)?$`)
)

func isoDur(s string) time.Duration {
	m := isoDurRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	f := func(i int) float64 { v, _ := strconv.ParseFloat(m[i], 64); return v }
	sec := f(1)*365*86400 + f(2)*30*86400 + f(3)*86400 + f(4)*3600 + f(5)*60 + f(6)
	return time.Duration(sec * float64(time.Second))
}

func setKind(s setX) string {
	c := strings.ToLower(s.ContentType + " " + s.MimeType + " " + s.Codecs)
	if len(s.Reps) > 0 {
		c += " " + strings.ToLower(s.Reps[0].MimeType+" "+s.Reps[0].Codecs)
	}
	switch {
	case strings.Contains(c, "audio"):
		return "audio"
	case strings.Contains(c, "text") || strings.Contains(c, "ttml") || strings.Contains(c, "wvtt") || strings.Contains(c, "stpp"):
		return "text"
	}
	return "video"
}

func resolve(base *url.URL, refs []string) *url.URL {
	if len(refs) == 0 || strings.TrimSpace(refs[0]) == "" {
		return base
	}
	r, err := url.Parse(strings.TrimSpace(refs[0]))
	if err != nil {
		return base
	}
	return base.ResolveReference(r)
}

func mergeTmpl(a, b *tmplX) tmplX {
	var t tmplX
	if a != nil {
		t = *a
	}
	if b != nil {
		for _, p := range [][2]*string{{&t.Timescale, &b.Timescale}, {&t.Media, &b.Media}, {&t.Init, &b.Init}, {&t.StartNumber, &b.StartNumber}, {&t.Duration, &b.Duration}, {&t.PTO, &b.PTO}} {
			if *p[1] != "" {
				*p[0] = *p[1]
			}
		}
		if b.Timeline != nil {
			t.Timeline = b.Timeline
		}
	}
	return t
}

// pickRep chooses a representation: video "best"/"worst"/height/id, audio by language.
func pickRep(sets []setX, kind, want string) (setX, repX, bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	var cand []setX
	for _, s := range sets {
		if setKind(s) == kind && len(s.Reps) > 0 {
			cand = append(cand, s)
		}
	}
	if len(cand) == 0 {
		return setX{}, repX{}, false
	}
	set := cand[0]
	if kind == "audio" && len(want) >= 2 && len(want) <= 3 {
		for _, s := range cand {
			if strings.HasPrefix(strings.ToLower(s.Lang), want) {
				set = s
				break
			}
		}
	}
	reps := append([]repX(nil), set.Reps...)
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].Bandwidth > reps[j].Bandwidth })
	if kind == "video" {
		if want == "worst" {
			return set, reps[len(reps)-1], true
		}
		for _, r := range reps {
			if strings.EqualFold(r.ID, want) {
				return set, r, true
			}
		}
		if h, err := strconv.Atoi(strings.TrimSuffix(want, "p")); err == nil && h > 0 {
			best, bd := reps[0], math.MaxInt
			for _, r := range reps {
				if d := abs(r.Height - h); d < bd {
					best, bd = r, d
				}
			}
			return set, best, true
		}
	}
	return set, reps[0], true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func parseLive(b []byte, final, kind, want string, now time.Time) (*liveTrack, error) {
	var m mpdX
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if len(m.Periods) == 0 {
		return nil, fmt.Errorf("manifest has no period")
	}
	p := m.Periods[len(m.Periods)-1]
	set, rep, ok := pickRep(p.Sets, kind, want)
	if !ok {
		return nil, fmt.Errorf("no %s track", kind)
	}
	return liveFrom(m, p, set, rep, final, kind, now)
}

func liveFrom(m mpdX, p periodX, set setX, rep repX, final, kind string, now time.Time) (*liveTrack, error) {
	base, err := url.Parse(final)
	if err != nil {
		return nil, err
	}
	base = resolve(resolve(resolve(resolve(base, m.BaseURL), p.BaseURL), set.BaseURL), rep.BaseURL)
	t := mergeTmpl(set.Tmpl, rep.Tmpl)
	if t.Media == "" {
		return nil, fmt.Errorf("%s track has no SegmentTemplate", kind)
	}
	sub := strings.NewReplacer("$RepresentationID$", rep.ID, "$Bandwidth$", strconv.FormatInt(rep.Bandwidth, 10), "$$", "$")
	abs := func(x string) string {
		r, err := url.Parse(sub.Replace(x))
		if err != nil {
			return x
		}
		return base.ResolveReference(r).String()
	}
	lt := &liveTrack{media: abs(t.Media), ts: 1}
	if t.Init != "" {
		lt.init = abs(t.Init)
	}
	if v, _ := strconv.ParseInt(t.Timescale, 10, 64); v > 0 {
		lt.ts = v
	}
	lt.pto, _ = strconv.ParseInt(t.PTO, 10, 64)
	num := int64(1)
	if v, err := strconv.ParseInt(t.StartNumber, 10, 64); err == nil {
		num = v
	}
	if t.Timeline != nil {
		var cur int64
		for i, s := range t.Timeline.S {
			if s.T != nil {
				cur = *s.T
			}
			r := s.R
			if r < 0 {
				r = 0
				if i+1 < len(t.Timeline.S) && t.Timeline.S[i+1].T != nil && s.D > 0 {
					r = (*t.Timeline.S[i+1].T-cur)/s.D - 1
				}
			}
			for k := int64(0); k <= r; k++ {
				lt.segs = append(lt.segs, seg{num, cur, s.D})
				num++
				cur += s.D
			}
		}
	} else {
		d, _ := strconv.ParseInt(t.Duration, 10, 64)
		ast, err := time.Parse(time.RFC3339Nano, m.AST)
		if d <= 0 || err != nil {
			return nil, fmt.Errorf("%s track: unsupported segment addressing", kind)
		}
		pto := lt.pto
		el := now.Sub(ast) - isoDur(p.Start)
		last := num + int64(el.Seconds()*float64(lt.ts))/d - 2
		for n := max(num, last-5); n <= last; n++ {
			lt.segs = append(lt.segs, seg{n, pto + (n-num)*d, d})
		}
	}
	if len(lt.segs) == 0 {
		return nil, fmt.Errorf("%s track has no segments", kind)
	}
	lt.every = isoDur(m.MUP)
	if sd := time.Duration(float64(lt.segs[len(lt.segs)-1].d) / float64(lt.ts) * float64(time.Second)); lt.every == 0 || lt.every > sd {
		lt.every = sd
	}
	lt.every = min(max(lt.every/2, 500*time.Millisecond), 5*time.Second)
	return lt, nil
}

type mcache struct {
	b     []byte
	final string
	at    time.Time
}

func (d *Service) liveManifest(ctx context.Context, id int64) ([]byte, string, *source, error) {
	d.mu.Lock()
	src, ok := d.srcs[id]
	c := d.mc[id]
	d.mu.Unlock()
	if !ok {
		return nil, "", nil, fmt.Errorf("unknown stream")
	}
	if c.b != nil && time.Since(c.at) < time.Second {
		return c.b, c.final, &src, nil
	}
	b, final, err := d.fetchManifest(ctx, &src.stream, src.url)
	if err != nil {
		return nil, "", &src, err
	}
	d.mu.Lock()
	if d.mc == nil {
		d.mc = map[int64]mcache{}
	}
	d.mc[id] = mcache{b, final, time.Now()}
	d.mu.Unlock()
	return b, final, &src, nil
}

func (d *Service) get(ctx context.Context, c *http.Client, src *source, u string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	d.headers(req, &src.stream)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// anchor returns the shared start time (seconds) for a stream's tracks so audio
// and video begin at the same point; the first track to start sets it.
func (d *Service) anchor(id int64, sec float64) float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.anch == nil {
		d.anch = map[int64]anchorT{}
	}
	if a, ok := d.anch[id]; ok && time.Since(a.at) < 30*time.Second {
		return a.sec
	}
	d.anch[id] = anchorT{sec, time.Now()}
	return sec
}

type anchorT struct {
	sec float64
	at  time.Time
}

// ServeTrack streams one live track (kind "video"/"audio") of a DRM stream as fMP4.
func (d *Service) ServeTrack(w http.ResponseWriter, r *http.Request, id int64, kind string) {
	if rg := r.Header.Get("Range"); rg != "" && rg != "bytes=0-" {
		http.Error(w, "live track is not seekable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	ctx := r.Context()
	b, final, src, err := d.liveManifest(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	want := src.stream.DRM.Video
	if kind == "audio" {
		want = src.stream.DRM.Audio
	}
	lt, err := parseLive(b, final, kind, want, time.Now())
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	c, err := d.client(&src.stream, "media")
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	if lt.init != "" {
		if err := d.get(ctx, c, src, lt.init, w); err != nil {
			d.St.Log("stream", id, "error", "DRM "+kind+" init: "+err.Error())
			return
		}
	}
	start := lt.segs[max(0, len(lt.segs)-3)]
	a := d.anchor(id, float64(start.t)/float64(lt.ts))
	next := start.t
	for _, s := range lt.segs {
		if float64(s.t+s.d)/float64(lt.ts) > a {
			next = s.t
			break
		}
	}
	last := time.Now()
	for {
		wrote := false
		if len(lt.segs) > 0 && next < lt.segs[0].t {
			d.St.Log("stream", id, "warn", fmt.Sprintf("DRM %s fell behind live window, skipping %.1fs", kind, float64(lt.segs[0].t-next)/float64(lt.ts)))
			next = lt.segs[0].t
		}
		for _, s := range lt.segs {
			if s.t < next {
				continue
			}
			if err := d.get(ctx, c, src, lt.url(s), w); err != nil {
				if ctx.Err() != nil {
					return
				}
				break
			}
			next, wrote, last = s.t+s.d, true, time.Now()
			if fl != nil {
				fl.Flush()
			}
		}
		if time.Since(last) > 30*time.Second {
			d.St.Log("stream", id, "error", "DRM "+kind+": no new segments for 30s")
			return
		}
		wait := lt.every
		if wrote {
			wait = 200 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		b, final, _, err := d.liveManifest(ctx, id)
		if err != nil {
			continue
		}
		if n, err := parseLive(b, final, kind, want, time.Now()); err == nil {
			lt.segs, lt.every, lt.media = n.segs, n.every, n.media
		}
	}
}
