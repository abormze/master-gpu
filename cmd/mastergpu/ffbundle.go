package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bundled FFmpeg build (NVENC SDK 12.2, runs with driver >= 550); see scripts/build-ffmpeg.sh.
const (
	ffBundleVer = "9.1"
	ffBundleURL = "https://github.com/abormze/master-gpu/releases/download/ffmpeg-" + ffBundleVer + "/ffmpeg-" + ffBundleVer + "-mastergpu-linux-amd64.tar.gz"
	ffBundleDir = "/opt/mastergpu/ffmpeg"
)

func ensureFFmpegBundle() error {
	dst := filepath.Join(ffBundleDir, ffBundleVer)
	if fileExists(filepath.Join(dst, "ffmpeg")) {
		return nil
	}
	cl := &http.Client{Timeout: 3 * time.Minute}
	sum, err := httpText(cl, ffBundleURL+".sha256")
	if err != nil {
		return err
	}
	want := strings.Fields(sum)[0]
	resp, err := cl.Get(ffBundleURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	tmp := dst + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	h := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(resp.Body, h))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Base(hd.Name)
		if hd.Typeflag != tar.TypeReg || (name != "ffmpeg" && name != "ffprobe") {
			continue
		}
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			return err
		}
	}
	io.Copy(io.Discard, resp.Body)
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("sha256 mismatch")
	}
	if !fileExists(filepath.Join(tmp, "ffmpeg")) {
		return fmt.Errorf("ffmpeg missing in archive")
	}
	return os.Rename(tmp, dst)
}

func httpText(cl *http.Client, url string) (string, error) {
	resp, err := cl.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: status %d", filepath.Base(url), resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if len(strings.Fields(string(b))) == 0 {
		return "", fmt.Errorf("empty checksum")
	}
	return string(b), err
}
