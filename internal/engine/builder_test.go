package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gpupanel/internal/models"
)

func runFF(t *testing.T, args []string, secs int) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	r, w, _ := os.Pipe()
	cmd.ExtraFiles = []*os.File{w}
	go func() {
		b := make([]byte, 65536)
		for {
			if _, err := r.Read(b); err != nil {
				return
			}
		}
	}()
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestBuildCopyAndTranscode(t *testing.T) {
	src := os.Getenv("HOME") + "/testmedia/src.ts"
	st := models.DefaultSettings()
	cases := map[string]models.Stream{
		"copy": {Inputs: []string{src}, Outputs: []models.Output{{Type: "hls", Name: "live"}}},
		"abr": {Inputs: []string{src}, Transcode: models.Transcode{HWAccel: "none", Text: "Nova: test", Deinterlace: true, FrameRate: "25", GOP: 50,
			VideoEncoders: []models.VideoEnc{{Codec: "h264", Size: "1280x720", Bitrate: 1500, Name: "HD"}, {Codec: "h264", Size: "640x360", Bitrate: 600, Name: "SD"}},
			AudioEncoders: []models.AudioEnc{{Codec: "aac", Bitrate: 96}}, AudioVolume: 1.2},
			Outputs: []models.Output{{Type: "hls_master", Name: "abr"}, {Type: "hls", Name: "sd", Video: []int{1}}, {Type: "file", URL: "/tmp/novatest/rec.ts"}, {Type: "udp", URL: "udp://127.0.0.1:12345"}}},
	}
	for name, s := range cases {
		root := filepath.Join(t.TempDir(), name)
		os.MkdirAll("/tmp/novatest", 0o755)
		args, vs, err := Build(&s, &st, BuildOpts{Input: src, GPU: -1, HLSRoot: root, HTTPPipe: true, FFmpegVer: 4})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			os.MkdirAll(filepath.Join(root, v.Dir), 0o755)
		}
		for _, o := range s.Outputs {
			os.MkdirAll(filepath.Join(root, o.Name), 0o755)
		}
		writeMaster(root, vs)
		out := runFF(t, args, 8)
		var files []string
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				files = append(files, strings.TrimPrefix(p, root))
			}
			return nil
		})
		t.Logf("%s: args=%q", name, args)
		t.Logf("%s files: %v", name, files)
		if !strings.Contains(strings.Join(files, " "), "index.m3u8") {
			t.Errorf("%s: no playlist; ffmpeg: %s", name, out)
		}
	}
}

func TestMapList(t *testing.T) {
	for _, c := range []struct{ in, kind, want string }{
		{"", "a", "0:a:0?"},
		{"a:0, a:1", "a", "0:a:0 0:a:1"},
		{"i:0x101", "a", "0:i:0x101"},
		{"p:1234:a:0", "a", "0:p:1234:a:0"},
		{"m:language:eng", "a", "0:a:m:language:eng"},
		{"s:0, -cc", "s", "0:s:0"},
	} {
		got, _ := mapList(c.in, c.kind)
		if strings.Join(got, " ") != c.want {
			t.Errorf("%q: got %v want %s", c.in, got, c.want)
		}
	}
	if _, cc := mapList("s:0, -cc", "s"); !cc {
		t.Error("-cc not detected")
	}
}

func TestInfoFPS(t *testing.T) {
	if f := infoFPS([]string{"Stream #0:1: Audio: aac, 48000 Hz", "Stream #0:0: Video: h264, yuv420p, 1920x1080, 25 fps"}); f != 25 {
		t.Fatal(f)
	}
}
