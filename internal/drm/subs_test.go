package drm

import (
	"os"
	"strings"
	"testing"
)

func TestWVTTSegment(t *testing.T) {
	init, _ := os.ReadFile("testdata/wvtt_init.mp4")
	b, _ := os.ReadFile("testdata/wvtt_seg.m4s")
	ts := mdhdTimescale(init)
	if ts != 1000 {
		t.Fatalf("timescale %d", ts)
	}
	c := parseSubSegment(b, ts, 0)
	if len(c) != 2 || !strings.Contains(c[0].text, "en # ") || c[0].e-c[0].s < 0.8 || c[1].s-c[0].s != 1 {
		t.Fatalf("cues %+v", c)
	}
}

func TestSTPPSegment(t *testing.T) {
	b, _ := os.ReadFile("testdata/stpp_seg.m4s")
	c := parseSubSegment(b, 1000, 0)
	if len(c) != 2 || !strings.Contains(c[0].text, "\nen # ") || c[0].s < 1.7e9 {
		t.Fatalf("cues %+v", c)
	}
}

func TestVTTWrite(t *testing.T) {
	c := parseVTT("WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\n00:00:01.000 --> 00:00:02.500\nhello\nworld\n", 0)
	if len(c) != 1 || c[0].s != 11 || c[0].text != "hello\nworld" {
		t.Fatalf("%+v", c)
	}
	out := string(writeVTT(c, 10, 0))
	if !strings.Contains(out, "MPEGTS:900000") || !strings.Contains(out, "00:00:01.000 --> 00:00:02.500\nhello") {
		t.Fatal(out)
	}
	if ttmlTime("00:00:01:15", 1e7, 30) != 1.5 || ttmlTime("2.5s", 1e7, 30) != 2.5 || ttmlTime("20000000t", 1e7, 30) != 2 {
		t.Fatal("ttml time")
	}
}
