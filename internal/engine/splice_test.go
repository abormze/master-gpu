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
	sp.pace = 0
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

type tsWriter struct {
	at []time.Duration
	t0 time.Time
}

func (w *tsWriter) Write(b []byte) (int, error) {
	w.at = append(w.at, time.Since(w.t0))
	return len(b), nil
}

// The pacer must hold chunks until their media time (minus the lead), pass
// unknown-timestamp chunks straight on, and rebase on a timeline jump.
func TestPacer(t *testing.T) {
	w := &tsWriter{t0: time.Now()}
	p := newPacer(w, 200*time.Millisecond, func(string) {})
	for i := 0; i <= 10; i++ {
		p.push([]byte{1}, int64(i)*9000) // 0..1s of media at once
	}
	p.push([]byte{2}, 3600*90000) // 1h jump: no 1h wait
	p.push([]byte{3}, 3600*90000+9000)
	deadline := time.Now().Add(3 * time.Second)
	for len(w.at) < 13 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p.close()
	if len(w.at) != 13 {
		t.Fatalf("wrote %d chunks", len(w.at))
	}
	if w.at[0] > 50*time.Millisecond || w.at[10] < 750*time.Millisecond || w.at[10] > 1100*time.Millisecond {
		t.Fatalf("pacing off: first %v, 1s-media chunk %v", w.at[0], w.at[10])
	}
	if w.at[11]-w.at[10] > 100*time.Millisecond {
		t.Fatalf("waited %v on a timestamp jump", w.at[11]-w.at[10])
	}
}

func TestPacerTrim(t *testing.T) {
	// a 2s backlog queued up front (provider connect burst), then real-time arrival
	for _, trim := range []bool{false, true} {
		w := &tsWriter{t0: time.Now()}
		p := newPacer(w, 0, func(string) {})
		p.trim = trim
		var ts int64
		for ; ts < 2*90000; ts += 1800 {
			p.push([]byte{1}, ts)
		}
		start := time.Now()
		for ts0 := ts; time.Since(start) < 17*time.Second; ts += 1800 {
			time.Sleep(time.Until(start.Add(time.Duration(ts-ts0) * time.Second / 90000)))
			p.push([]byte{1}, ts)
		}
		held := p.held.Load()
		p.close()
		if trim && held > 1500 || !trim && held < 1800 {
			t.Fatalf("trim=%v held %dms", trim, held)
		}
		t.Logf("trim=%v held %dms", trim, held)
	}
}

type dataWriter struct{ b [][]byte }

func (w *dataWriter) Write(b []byte) (int, error) {
	w.b = append(w.b, append([]byte(nil), b...))
	return len(b), nil
}

// A connect backlog is dropped up to its newest keyframe, which starts with PAT/PMT.
func TestPacerLiveStart(t *testing.T) {
	w := &dataWriter{}
	p := newPacer(w, 0, func(string) {})
	p.trim = true
	var ts int64
	for ; ts < 4*90000; ts += 1800 { // 4s burst, keyframe every 1s
		key, psi := -1, []byte(nil)
		if ts%90000 == 0 {
			key, psi = 1, []byte{9}
		}
		p.pushKey([]byte{0, byte(ts / 90000)}, ts, key, psi)
	}
	start := time.Now()
	for ts0 := ts; time.Since(start) < 3*time.Second; ts += 1800 {
		time.Sleep(time.Until(start.Add(time.Duration(ts-ts0) * time.Second / 90000)))
		p.push([]byte{0, 7}, ts)
	}
	held := p.held.Load()
	p.close()
	if len(w.b) == 0 || len(w.b[0]) != 2 || w.b[0][0] != 9 || w.b[0][1] != 3 {
		t.Fatalf("first write %v, want PSI + keyframe at 3s", w.b[0])
	}
	if held > 400 {
		t.Fatalf("held %dms after live start", held)
	}
}

// A burst arriving mid-stream (reconnect) is skipped to its newest keyframe.
func TestPacerJump(t *testing.T) {
	w := &dataWriter{}
	p := newPacer(w, 0, func(string) {})
	p.trim = true
	start := time.Now()
	var ts int64
	push := func(n int64) {
		for end := ts + n; ts < end; ts += 1800 {
			key, psi := -1, []byte(nil)
			if ts%90000 == 0 {
				key, psi = 1, []byte{9}
			}
			p.pushKey([]byte{0, 1}, ts, key, psi)
		}
	}
	for ts < 2*90000 { // 2s real time
		time.Sleep(time.Until(start.Add(time.Duration(ts) * time.Second / 90000)))
		push(1800)
	}
	push(5 * 90000) // 5s burst
	off := ts - 2*90000
	for time.Since(start) < 5*time.Second {
		time.Sleep(time.Until(start.Add(time.Duration(ts-off) * time.Second / 90000)))
		push(1800)
	}
	held := p.held.Load()
	p.close()
	if held > 1000 {
		t.Fatalf("held %dms after mid-stream burst", held)
	}
}
