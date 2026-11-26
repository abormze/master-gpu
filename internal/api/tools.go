package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gpupanel/internal/models"
	"gpupanel/internal/version"
)

// FFmpegForDriver picks the newest bundled ffmpeg whose NVENC API the installed driver supports.
// NVENC SDK 13 needs driver >= 570, SDK 12.x >= 520 (12.1: 530), SDK 11.1 >= 470, SDK 11.0 >= 455.
func FFmpegForDriver(driver string, candidates []string) string {
	maj, _ := strconv.Atoi(strings.SplitN(driver, ".", 2)[0])
	best, bestVer := "", -1
	for _, c := range candidates {
		if _, err := os.Stat(c); err != nil {
			if _, err := exec.LookPath(c); err != nil {
				continue
			}
		}
		v, sdk12 := ffMajor(c), isSDK12Build(c)
		need := map[bool]int{true: 0}[true]
		switch {
		case v >= 8 && sdk12:
			need = 550 // Master GPU build pinned to NVENC SDK 12.2
		case v >= 8:
			need = 570
		case v >= 6:
			need = 530
		case v == 5:
			need = 470
		default:
			need = 455
		}
		if maj > 0 && maj < need {
			continue
		}
		if v > bestVer {
			best, bestVer = c, v
		}
	}
	return best
}

// isSDK12Build reports the Master GPU FFmpeg bundle, built against NVENC SDK 12.2 (driver >= 550).
func isSDK12Build(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "-hide_banner", "-version").Output()
	return strings.Contains(string(out), "-mastergpu")
}

var ffVerRe = regexp.MustCompile(`ffmpeg version n?(\d+)`)

func ffMajor(bin string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-hide_banner", "-version").Output()
	if err != nil {
		return -1
	}
	if m := ffVerRe.FindSubmatch(out); m != nil {
		v, _ := strconv.Atoi(string(m[1]))
		return v
	}
	return 7
}

func run(timeout time.Duration, bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out)
}

func (s *Server) diag(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	st := s.settings()
	enc := run(10*time.Second, st.FFmpegPath, "-hide_banner", "-encoders")
	hw := run(10*time.Second, st.FFmpegPath, "-hide_banner", "-hwaccels")
	has := func(n string) bool { return strings.Contains(enc, " "+n+" ") }
	g := s.GPU.Get()
	ffv := strings.SplitN(run(5*time.Second, st.FFmpegPath, "-hide_banner", "-version"), "\n", 2)[0]
	_, nvencLib := os.Stat("/usr/lib/x86_64-linux-gnu/libnvidia-encode.so.1")
	sy := s.Sys.Get()
	ok(w, map[string]any{
		"panel": version.Name + " " + version.Version, "build": version.Build, "go": runtime.Version(),
		"os": sy.OS, "kernel": sy.Kernel, "cpu": sy.CPUModel, "cores": sy.Cores,
		"ffmpeg": ffv, "ffmpeg_path": st.FFmpegPath, "hwaccels": strings.Fields(strings.TrimPrefix(hw, "Hardware acceleration methods:")),
		"encoders": map[string]bool{"h264_nvenc": has("h264_nvenc"), "hevc_nvenc": has("hevc_nvenc"), "av1_nvenc": has("av1_nvenc"),
			"h264_qsv": has("h264_qsv"), "hevc_qsv": has("hevc_qsv"), "libx264": has("libx264"), "libx265": has("libx265"), "libfdk_aac": has("libfdk_aac")},
		"nvidia_driver": g.Driver, "cuda": g.CUDA, "gpus": len(g.Cards), "nvenc_lib": nvencLib == nil, "nvenc_patched": g.Patched, "gpu_error": g.Error,
		"dri": exists("/dev/dri/renderD128"),
	})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var speedRe = regexp.MustCompile(`fps=\s*([\d.]+).*speed=\s*([\d.]+)x`)

// bench encodes a synthetic 1080p25 source as fast as possible and estimates channel capacity.
func (s *Server) bench(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var in struct {
		GPU   int    `json:"gpu"`
		Codec string `json:"codec"` // h264, hevc, av1
		Size  string `json:"size"`
		CPU   bool   `json:"cpu"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Size == "" {
		in.Size = "1920x1080"
	}
	if in.Codec == "" {
		in.Codec = "h264"
	}
	st := s.settings()
	args := []string{"-hide_banner", "-nostats", "-stats_period", "1", "-progress", "pipe:2", "-y"}
	src := "testsrc2=s=" + in.Size + ":r=25,format=yuv420p"
	if in.CPU {
		args = append(args, "-f", "lavfi", "-i", src, "-t", "15", "-c:v", "libx264", "-preset", "veryfast", "-b:v", "4M", "-f", "null", "-")
	} else {
		// decode-free GPU path: generate once, loop frames on the GPU
		args = append(args, "-init_hw_device", "cuda=gpu:"+strconv.Itoa(in.GPU), "-filter_hw_device", "gpu",
			"-f", "lavfi", "-i", src+",trim=end_frame=50,loop=-1:50", "-t", "20",
			"-vf", "hwupload_cuda", "-c:v", in.Codec+"_nvenc", "-gpu", "0", "-preset", "p4", "-b:v", "4M", "-f", "null", "-")
	}
	out := run(60*time.Second, st.FFmpegPath, args...)
	fps, speed := 0.0, 0.0
	for _, m := range speedRe.FindAllStringSubmatch(out, -1) {
		fps, _ = strconv.ParseFloat(m[1], 64)
		speed, _ = strconv.ParseFloat(m[2], 64)
	}
	var lastFPS float64
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "fps=") {
			lastFPS, _ = strconv.ParseFloat(strings.TrimPrefix(l, "fps="), 64)
		}
		if strings.HasPrefix(l, "speed=") {
			speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(l, "speed="), "x"), 64)
		}
	}
	if lastFPS > 0 {
		fps = lastFPS
	}
	res := map[string]any{"ok": fps > 0, "fps": fps, "speed": speed, "channels_est": int(fps / 25), "size": in.Size, "codec": in.Codec, "gpu": in.GPU, "cpu": in.CPU}
	if fps == 0 {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		res["error"] = strings.Join(lines[max(0, len(lines)-6):], "\n")
	}
	s.St.Log("panel", 0, "info", "benchmark "+in.Codec+" "+in.Size+" fps="+strconv.FormatFloat(fps, 'f', 0, 64))
	ok(w, res)
}
