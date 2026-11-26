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

// DefaultUpdateURL is the GitHub Releases API of the official repo.
const DefaultUpdateURL = "https://api.github.com/repos/abormze/master-gpu/releases/latest"

// ReleaseAsset is the binary name attached to each GitHub release.
const ReleaseAsset = "mastergpu-linux-amd64"

type ghRelease struct {
	Tag    string `json:"tag_name"`
	Body   string `json:"body"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func updateGet(url, token, accept string, timeout time.Duration) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mastergpu/"+version.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		if resp.StatusCode == 404 && strings.HasPrefix(url, "https://api.github.com/") {
			return nil, errors.New("no release found (private repo needs an update token)")
		}
		return nil, fmt.Errorf("update server status %d", resp.StatusCode)
	}
	return resp, nil
}

// fetchRelease reads either a GitHub release (api.github.com) or a plain release JSON.
func fetchRelease(url, token string) (*Release, error) {
	if url == "" {
		url = DefaultUpdateURL
	}
	resp, err := updateGet(url, token, "application/vnd.github+json", 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var gh ghRelease
	if json.Unmarshal(b, &gh) == nil && gh.Tag != "" {
		rel := &Release{Version: strings.TrimPrefix(gh.Tag, "v"), Notes: gh.Body}
		for _, a := range gh.Assets {
			if a.Name == ReleaseAsset {
				rel.URL = a.URL
				rel.SHA256 = strings.TrimPrefix(a.Digest, "sha256:")
			}
		}
		if rel.URL == "" {
			return nil, fmt.Errorf("release %s has no %s asset", gh.Tag, ReleaseAsset)
		}
		return rel, nil
	}
	var rel Release
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, err
	}
	if rel.Version == "" {
		return nil, errors.New("invalid release json")
	}
	return &rel, nil
}

// newer reports whether version a is greater than b (dotted numeric).
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	st := s.settings()
	rel, err := fetchRelease(st.UpdateURL, st.UpdateToken)
	res := map[string]any{"current": version.Version, "build": version.Build}
	if err != nil {
		res["error"] = err.Error()
	} else {
		res["latest"] = rel.Version
		res["notes"] = rel.Notes
		res["available"] = newer(rel.Version, version.Version)
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
		st := s.settings()
		rel, err := fetchRelease(st.UpdateURL, st.UpdateToken)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		resp, err := updateGet(rel.URL, st.UpdateToken, "application/octet-stream", 10*time.Minute)
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
