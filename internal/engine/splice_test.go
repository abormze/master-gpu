package engine

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A source that drops the connection every ~1.5MB and resends the previous
// ~400KB on reconnect must come out of the splicer with no repeated frames.
func TestSplicerOverlap(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ts")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-f", "lavfi", "-i", "sine",
		"-t", "40", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-b:v", "1500k", "-c:a", "aac", "-f", "mpegts", src).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	data, _ := os.ReadFile(src)
	var pos atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := pos.Load()
		if start > 0 {
			start -= 400_000 / 188 * 188
		}
		end := min(start+1_500_000/188*188, int64(len(data)))
		if start >= int64(len(data)) {
			w.WriteHeader(404)
			return
		}
		w.Write(data[start:end])
		pos.Store(end)
	}))
	defer srv.Close()
	var logs []string
	sp := newSplicer(srv.URL+"/live.ts", "", "", "", "", 5*time.Second, func(s string) { logs = append(logs, s) })
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sp.run(ctx, &buf)
	if len(logs) < 3 {
		t.Fatalf("expected reconnects, logs=%v", logs)
	}
	out := filepath.Join(dir, "out.ts")
	os.WriteFile(out, buf.Bytes(), 0o644)
	pts, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "packet=dts", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	var prev int64 = -1
	n := 0
	for _, l := range strings.Fields(string(pts)) {
		v, _ := strconv.ParseInt(strings.TrimSuffix(l, ","), 10, 64)
		if v <= prev {
			t.Fatalf("dts went back at frame %d: %d <= %d", n, v, prev)
		}
		prev, n = v, n+1
	}
	if n < 900 {
		t.Fatalf("only %d frames", n)
	}
	t.Log(n, "frames,", logs[0])
	if b, _ := exec.Command("ffmpeg", "-v", "error", "-i", out, "-f", "null", "-").CombinedOutput(); len(b) > 0 {
		t.Log("decode:", string(b))
	}
}
