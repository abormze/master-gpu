package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gpupanel/internal/models"
	"gpupanel/internal/version"
)

// Updater replaces the running binary in place and re-executes it.
// The previous binary is kept as <bin>.prev for rollback (mastergpu -rollback).
type Updater struct {
	Bin    string
	Before func() // stop streams etc.
}

type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Notes   string `json:"notes"`
}

func (u *Updater) Restart() {
	if u.Before != nil {
		u.Before()
	}
	// under systemd a plain exit is enough (Restart=always); exec keeps it working elsewhere too
	if os.Getenv("INVOCATION_ID") != "" {
		os.Exit(0)
	}
	syscall.Exec(u.Bin, os.Args, os.Environ())
	os.Exit(0)
}

func fetchRelease(url string) (*Release, error) {
	if url == "" {
		return nil, errors.New("update url not configured")
	}
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("update server status %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	rel, err := fetchRelease(s.settings().UpdateURL)
	res := map[string]any{"current": version.Version, "build": version.Build}
	if err != nil {
		res["error"] = err.Error()
	} else {
		res["latest"] = rel.Version
		res["notes"] = rel.Notes
		res["available"] = rel.Version != version.Version
	}
	ok(w, res)
}

// updateApply installs either an uploaded binary (multipart "file") or the release from the update URL.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var src io.Reader
	want := ""
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
		f, _, err := r.FormFile("file")
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		defer f.Close()
		src = f
		want = r.FormValue("sha256")
	} else {
		rel, err := fetchRelease(s.settings().UpdateURL)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		c := &http.Client{Timeout: 10 * time.Minute}
		resp, err := c.Get(rel.URL)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		defer resp.Body.Close()
		src = io.LimitReader(resp.Body, 256<<20)
		want = rel.SHA256
	}
	newBin := s.Updater.Bin + ".new"
	f, err := os.OpenFile(newBin, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), src)
	f.Close()
	if err != nil {
		os.Remove(newBin)
		fail(w, 500, err.Error())
		return
	}
	if want != "" && !strings.EqualFold(want, hex.EncodeToString(h.Sum(nil))) {
		os.Remove(newBin)
		fail(w, 400, "checksum mismatch")
		return
	}
	out, err := exec.Command(newBin, "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), version.Name) {
		os.Remove(newBin)
		fail(w, 400, "new binary failed self test: "+strings.TrimSpace(string(out)))
		return
	}
	prev := s.Updater.Bin + ".prev"
	os.Remove(prev)
	if err := os.Link(s.Updater.Bin, prev); err != nil {
		copyFile(s.Updater.Bin, prev)
	}
	if err := os.Rename(newBin, s.Updater.Bin); err != nil {
		fail(w, 500, err.Error())
		return
	}
	nv := strings.TrimSpace(string(out))
	s.St.Log("panel", 0, "info", "updated to "+nv+", restarting")
	ok(w, map[string]any{"ok": true, "version": nv})
	go func() { time.Sleep(700 * time.Millisecond); s.Updater.Restart() }()
}

func copyFile(a, b string) error {
	in, err := os.Open(a)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(b, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Rollback restores <bin>.prev.
func Rollback(bin string) error {
	prev := bin + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return errors.New("no previous version")
	}
	return os.Rename(prev, filepath.Clean(bin))
}
