package drm

import (
	"testing"
	"time"
)

const liveMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic" minimumUpdatePeriod="PT3.84S" availabilityStartTime="2026-11-04T14:06:51.128Z">
<BaseURL>https://cdn.example/live/ch/</BaseURL>
<Period id="p" start="PT0S">
<AdaptationSet contentType="video" mimeType="video/mp4">
<SegmentTemplate timescale="25000" media="video/$RepresentationID$/seg_$Number$.m4s" initialization="video/$RepresentationID$/init.m4s" startNumber="100">
<SegmentTimeline><S t="1000" d="96000" r="2"/><S d="48000"/></SegmentTimeline></SegmentTemplate>
<Representation id="1080p" bandwidth="2400000" height="1080"/>
<Representation id="360p" bandwidth="500000" height="360"/>
</AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4" lang="ar">
<SegmentTemplate timescale="48000" media="audio/$Time$.m4s" initialization="audio/init.m4s" duration="96000"/>
<Representation id="a" bandwidth="128000"/>
</AdaptationSet>
</Period></MPD>`

func TestParseLive(t *testing.T) {
	v, err := parseLive([]byte(liveMPD), "https://cdn.example/live/ch/index.mpd?x=1", "video", "best", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.segs) != 4 || v.segs[3].num != 103 || v.segs[3].t != 1000+3*96000 {
		t.Fatalf("segs %+v", v.segs)
	}
	if u := v.url(v.segs[1]); u != "https://cdn.example/live/ch/video/1080p/seg_101.m4s" {
		t.Fatal(u)
	}
	if v.init != "https://cdn.example/live/ch/video/1080p/init.m4s" {
		t.Fatal(v.init)
	}
	w, _ := parseLive([]byte(liveMPD), "https://cdn.example/x.mpd", "video", "360", time.Now())
	if w.url(w.segs[0]) != "https://cdn.example/live/ch/video/360p/seg_100.m4s" {
		t.Fatal(w.url(w.segs[0]))
	}
	a, err := parseLive([]byte(liveMPD), "https://cdn.example/x.mpd", "audio", "ar", time.Date(2026, 11, 4, 15, 6, 51, 128e6, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	l := a.segs[len(a.segs)-1]
	if l.num != 1+1800-2 || a.url(l) != "https://cdn.example/live/ch/audio/"+itoa(l.t)+".m4s" {
		t.Fatalf("%+v %s", l, a.url(l))
	}
}

func itoa(v int64) string { return fmtInt(v) }

func fmtInt(v int64) string {
	b := []byte{}
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
