package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gpupanel/internal/drm"
	"hash/crc32"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gpupanel/internal/engine"
	"gpupanel/internal/gpu"
	"gpupanel/internal/models"
	"gpupanel/internal/store"
	"gpupanel/internal/sysmon"
	"gpupanel/internal/version"
)

type Server struct {
	St      *store.Store
	Auth    *Auth
	Mgr     *engine.Manager
	Sys     *sysmon.Monitor
	GPU     *gpu.Monitor
	Web     fs.FS
	Updater *Updater
	DRM     *drm.Service
	Started time.Time
	mux     *http.ServeMux
	kinds   map[string]kindOps
	conns   sync.Map
}

type kindOps struct {
	list   func() any
	decode func(b []byte, id int64) (any, error)
	admin  bool
}

func reg[T any](setID func(*T, int64)) kindOps {
	return kindOps{
		list: func() any { return nil },
		decode: func(b []byte, id int64) (any, error) {
			var v T
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			setID(&v, id)
			return v, nil
		},
	}
}

func New(s *Server) *Server {
	s.Started = time.Now()
	s.mux = http.NewServeMux()
	s.kinds = map[string]kindOps{}
	add := func(kind string, o kindOps, list func() any) { o.list = list; s.kinds[kind] = o }
	st := s.St
	add(models.KServer, reg(func(v *models.Server, id int64) { v.ID = id }), func() any { return store.List[models.Server](st, models.KServer) })
	add(models.KStream, reg(func(v *models.Stream, id int64) { v.ID = id }), func() any { return store.List[models.Stream](st, models.KStream) })
	add(models.KProfile, reg(func(v *models.Profile, id int64) { v.ID = id }), func() any { return store.List[models.Profile](st, models.KProfile) })
	add(models.KCategory, reg(func(v *models.Category, id int64) { v.ID = id }), func() any { return store.List[models.Category](st, models.KCategory) })
	add(models.KAdapter, reg(func(v *models.Adapter, id int64) { v.ID = id }), func() any { return store.List[models.Adapter](st, models.KAdapter) })
	add(models.KSoftcam, reg(func(v *models.Softcam, id int64) { v.ID = id }), func() any { return store.List[models.Softcam](st, models.KSoftcam) })
	add(models.KMovie, reg(func(v *models.Movie, id int64) { v.ID = id }), func() any { return store.List[models.Movie](st, models.KMovie) })
	add(models.KSeries, reg(func(v *models.Series, id int64) { v.ID = id }), func() any { return store.List[models.Series](st, models.KSeries) })
	add(models.KEpisode, reg(func(v *models.Episode, id int64) { v.ID = id }), func() any { return store.List[models.Episode](st, models.KEpisode) })
	add(models.KUser, reg(func(v *models.User, id int64) { v.ID = id }), func() any { return store.List[models.User](st, models.KUser) })
	add(models.KBouquet, reg(func(v *models.Bouquet, id int64) { v.ID = id }), func() any { return store.List[models.Bouquet](st, models.KBouquet) })
	add(models.KPackage, reg(func(v *models.Package, id int64) { v.ID = id }), func() any { return store.List[models.Package](st, models.KPackage) })
	add(models.KBalancer, reg(func(v *models.Balancer, id int64) { v.ID = id }), func() any { return store.List[models.Balancer](st, models.KBalancer) })
	add(models.KIPRule, reg(func(v *models.IPRule, id int64) { v.ID = id }), func() any { return store.List[models.IPRule](st, models.KIPRule) })
	add(models.KProxy, reg(func(v *models.Proxy, id int64) { v.ID = id }), func() any { return store.List[models.Proxy](st, models.KProxy) })
	add(models.KDRMKey, reg(func(v *models.DRMKey, id int64) { v.ID = id }), func() any { return store.List[models.DRMKey](st, models.KDRMKey) })
	acc := reg(func(v *models.Account, id int64) { v.ID = id })
	acc.admin = true
	add(models.KAccount, acc, func() any {
		l := store.List[models.Account](st, models.KAccount)
		for i := range l {
			l[i].Password = ""
		}
		return l
	})
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if e := recover(); e != nil {
			s.St.Log("panel", 0, "error", fmt.Sprintf("panic %s %s: %v", r.Method, r.URL.Path, e))
			http.Error(w, "internal error", 500)
		}
	}()
	if !s.ipAllowed(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) settings() models.Settings {
	v := models.DefaultSettings()
	_ = s.St.Get(models.KSettings, 1, &v)
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func ok(w http.ResponseWriter, v any) {
	if v == nil {
		v = map[string]any{"ok": true}
	}
	writeJSON(w, 200, v)
}

type ctxKey int

func (s *Server) authed(h func(http.ResponseWriter, *http.Request, *models.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac := s.Auth.Account(r)
		if ac == nil {
			fail(w, 401, "unauthorized")
			return
		}
		if ac.MustChange && !strings.HasPrefix(r.URL.Path, "/api/me") && r.URL.Path != "/api/logout" {
			fail(w, 428, "password change required")
			return
		}
		h(w, r, ac)
	}
}

func (s *Server) admin(h func(http.ResponseWriter, *http.Request, *models.Account)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, ac *models.Account) {
		if ac.Role != "admin" {
			fail(w, 403, "admin only")
			return
		}
		h(w, r, ac)
	})
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)
	m.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"name": version.Name, "version": version.Version, "build": version.Build})
	})
	m.HandleFunc("GET /api/me", s.authed(func(w http.ResponseWriter, r *http.Request, ac *models.Account) {
		ac.Password = ""
		ok(w, ac)
	}))
	m.HandleFunc("POST /api/me/password", s.authed(s.changePassword))
	m.HandleFunc("POST /api/me/token", s.authed(func(w http.ResponseWriter, r *http.Request, ac *models.Account) {
		ac.Token = Token(16)
		s.St.Put(models.KAccount, ac.ID, *ac)
		ok(w, map[string]any{"ok": true, "token": ac.Token})
	}))
	m.HandleFunc("GET /api/dashboard", s.authed(func(w http.ResponseWriter, r *http.Request, _ *models.Account) { ok(w, s.dashboard()) }))
	m.HandleFunc("GET /api/ws", s.ws)
	m.HandleFunc("GET /api/metrics", s.authed(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		h, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if h <= 0 {
			h = 24
		}
		ok(w, s.St.Metrics(0, time.Now().Add(-time.Duration(h)*time.Hour).Unix()))
	}))
	m.HandleFunc("GET /api/settings", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) { ok(w, s.settings()) }))
	m.HandleFunc("PUT /api/settings", s.admin(s.putSettings))
	m.HandleFunc("GET /api/logs", s.authed(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		q := r.URL.Query()
		ref, err := strconv.ParseInt(q.Get("ref"), 10, 64)
		if err != nil {
			ref = -1
		}
		n, _ := strconv.Atoi(q.Get("limit"))
		if n <= 0 || n > 2000 {
			n = 300
		}
		ok(w, s.St.Logs(q.Get("scope"), ref, n))
	}))
	m.HandleFunc("DELETE /api/logs", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		ref, _ := strconv.ParseInt(r.URL.Query().Get("ref"), 10, 64)
		s.St.ClearLogs(r.URL.Query().Get("scope"), ref)
		ok(w, nil)
	}))

	// streams
	m.HandleFunc("GET /api/status", s.authed(func(w http.ResponseWriter, r *http.Request, _ *models.Account) { ok(w, s.statuses()) }))
	m.HandleFunc("POST /api/stream/{id}/{action}", s.authed(s.streamAction))
	m.HandleFunc("POST /api/streams/bulk", s.authed(s.bulk))
	m.HandleFunc("GET /api/stream/{id}/thumb.jpg", s.authed(s.thumb))
	m.HandleFunc("POST /api/probe", s.authed(s.probe))
	m.HandleFunc("POST /api/import/m3u", s.admin(s.importM3U))
	m.HandleFunc("GET /api/export/m3u", s.authed(s.exportM3U))

	// generic CRUD
	m.HandleFunc("GET /api/{kind}", s.authed(s.list))
	m.HandleFunc("POST /api/{kind}", s.authed(s.create))
	m.HandleFunc("GET /api/{kind}/{id}", s.authed(s.get))
	m.HandleFunc("PUT /api/{kind}/{id}", s.authed(s.update))
	m.HandleFunc("DELETE /api/{kind}/{id}", s.authed(s.del))

	// system tools
	m.HandleFunc("POST /api/system/backup", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		p, err := s.St.Backup()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, map[string]any{"ok": true, "file": filepath.Base(p)})
	}))
	m.HandleFunc("GET /api/system/backups", s.admin(s.backups))
	m.HandleFunc("GET /api/system/backups/{name}", s.admin(s.downloadBackup))
	m.HandleFunc("POST /api/system/restore", s.admin(s.restore))
	m.HandleFunc("GET /api/system/update", s.admin(s.updateCheck))
	m.HandleFunc("POST /api/system/update", s.admin(s.updateApply))
	m.HandleFunc("POST /api/system/restart", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		ok(w, nil)
		go func() { time.Sleep(500 * time.Millisecond); s.Updater.Restart() }()
	}))
	m.HandleFunc("POST /api/system/reboot", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		ok(w, nil)
		s.reboot()
	}))
	m.HandleFunc("GET /api/servers/stats", s.authed(s.serverStats))
	m.HandleFunc("POST /api/servers/{id}/{action}", s.admin(s.serverAction))
	m.HandleFunc("GET /api/system/diag", s.admin(s.diag))
	m.HandleFunc("POST /api/system/bench", s.admin(s.bench))
	m.HandleFunc("POST /api/system/telegram-test", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		if err := s.Alert("Master GPU: test alert"); err != nil {
			fail(w, 400, err.Error())
			return
		}
		ok(w, nil)
	}))
	m.HandleFunc("POST /api/files", s.admin(s.upload))
	m.HandleFunc("GET /api/files", s.authed(s.files))
	m.HandleFunc("DELETE /api/files/{name}", s.admin(s.delFile))

	// playback
	m.HandleFunc("GET /hls/{id}/{path...}", s.hls)
	m.HandleFunc("GET /drm/{id}/manifest.mpd", s.drmManifest)
	m.HandleFunc("GET /drm/{id}/track/{kind}", s.drmTrack)
	m.HandleFunc("GET /subs/{id}/{file}", s.subs)
	m.HandleFunc("GET /subs/{id}/{track}/{file}", s.subs)
	m.HandleFunc("GET /live/{user}/{pass}/{file}", s.live)
	m.HandleFunc("GET /ts/{id}", s.tsRelay)
	m.HandleFunc("GET /stream/{id}/{file}", s.streamOut)
	m.HandleFunc("GET /get.php", s.getPHP)
	m.HandleFunc("GET /player_api.php", s.playerAPI)
	m.HandleFunc("GET /lb/{id}/{path...}", s.lbRedirect)
	m.HandleFunc("GET /files/{name}", s.serveFile)

	// web UI
	m.Handle("GET /", s.web())
}

func (s *Server) web() http.Handler {
	fsrv := http.FileServer(http.FS(s.Web))
	b, err := fs.ReadFile(s.Web, "index.html")
	if err == nil {
		// content-hash asset URLs so browsers pick up new versions immediately
		for _, a := range []string{"app.js", "app.css"} {
			if d, e := fs.ReadFile(s.Web, a); e == nil {
				b = []byte(strings.ReplaceAll(string(b), `"`+a+`"`, fmt.Sprintf(`"%s?v=%08x"`, a, crc32.ChecksumIEEE(d))))
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || !strings.Contains(p, ".") {
			if err != nil {
				http.Error(w, "ui missing", 500)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(b)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		fail(w, 400, "bad request")
		return
	}
	tok, ac, msg := s.Auth.Login(r, strings.TrimSpace(in.Username), in.Password)
	if ac == nil {
		s.St.Log("auth", 0, "warn", "failed login for "+in.Username+" from "+clientIP(r))
		fail(w, 401, msg)
		return
	}
	s.setCookie(w, r, tok)
	s.St.Log("auth", ac.ID, "info", "login "+ac.Username+" from "+clientIP(r))
	ac.Password = ""
	ok(w, map[string]any{"ok": true, "account": ac})
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{Name: "mgpu_session", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil, MaxAge: 7 * 86400})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("mgpu_session"); err == nil {
		s.Auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "mgpu_session", Value: "", Path: "/", MaxAge: -1})
	ok(w, nil)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	var in struct {
		Current  string `json:"current"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		fail(w, 400, "bad request")
		return
	}
	if msg := s.Auth.ChangePassword(ac, in.Current, strings.TrimSpace(in.Username), in.Password); msg != "" {
		fail(w, 400, msg)
		return
	}
	s.setCookie(w, r, s.Auth.NewSession(ac.ID))
	s.St.Log("auth", ac.ID, "info", "credentials changed for "+ac.Username)
	ok(w, nil)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	v := s.settings()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.St.Put(models.KSettings, 1, v)
	s.St.Log("panel", 0, "info", "settings updated")
	ok(w, v)
}

// ---- generic CRUD ----

func (s *Server) kind(w http.ResponseWriter, r *http.Request, ac *models.Account, write bool) (string, kindOps, bool) {
	k := r.PathValue("kind")
	o, found := s.kinds[k]
	if !found {
		fail(w, 404, "unknown resource "+k)
		return "", o, false
	}
	if (o.admin || write && k != models.KUser) && ac.Role != "admin" {
		fail(w, 403, "admin only")
		return "", o, false
	}
	return k, o, true
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	_, o, good := s.kind(w, r, ac, false)
	if good {
		ok(w, o.list())
	}
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	k, _, good := s.kind(w, r, ac, false)
	if !good {
		return
	}
	var v json.RawMessage
	if err := s.St.Get(k, pathID(r), &v); err != nil {
		fail(w, 404, "not found")
		return
	}
	if k == models.KAccount {
		var a models.Account
		json.Unmarshal(v, &a)
		a.Password = ""
		ok(w, a)
		return
	}
	ok(w, v)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	k, o, good := s.kind(w, r, ac, true)
	if !good {
		return
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	id := s.St.NextID(k)
	s.save(w, k, o, id, b, ac, true)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	k, o, good := s.kind(w, r, ac, true)
	if !good {
		return
	}
	id := pathID(r)
	var old json.RawMessage
	if s.St.Get(k, id, &old) != nil {
		fail(w, 404, "not found")
		return
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	// merge partial updates onto the stored object
	var a, p map[string]any
	json.Unmarshal(old, &a)
	if err := json.Unmarshal(b, &p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	for kk, vv := range p {
		a[kk] = vv
	}
	mb, _ := json.Marshal(a)
	s.save(w, k, o, id, mb, ac, false)
}

func (s *Server) save(w http.ResponseWriter, k string, o kindOps, id int64, b []byte, ac *models.Account, isNew bool) {
	v, err := o.decode(b, id)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	switch x := v.(type) {
	case models.Account:
		if x.Username == "" {
			fail(w, 400, "username required")
			return
		}
		var old models.Account
		s.St.Get(models.KAccount, id, &old)
		if x.Password == "" {
			x.Password = old.Password
		} else if !strings.HasPrefix(x.Password, "$2") {
			x.Password = HashPassword(x.Password)
		}
		if x.Token == "" {
			x.Token = Token(16)
		}
		v = x
	case models.User:
		if x.Username == "" {
			fail(w, 400, "username required")
			return
		}
		if x.Password == "" {
			x.Password = Token(5)
		}
		if isNew {
			x.Created = time.Now().Unix()
			x.Owner = ac.ID
		}
		v = x
	case models.Stream:
		if x.Name == "" {
			fail(w, 400, "name required")
			return
		}
		if isNew && x.GPU == 0 && x.ServerID == 0 && len(x.Outputs) == 0 {
			x.GPU = -2
		}
		if len(x.Outputs) == 0 {
			x.Outputs = []models.Output{{Type: "hls", Name: "index"}}
		}
		v = x
	case models.Server:
		if x.Token == "" {
			x.Token = Token(16)
		}
		v = x
	}
	if err := s.St.Put(k, id, v); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if k == models.KStream {
		st := v.(models.Stream)
		if s.Mgr.Running(id) {
			go s.Mgr.Restart(id)
		} else if isNew && st.Enabled && !st.OnDemand {
			go s.Mgr.Start(id)
		}
	}
	if k == models.KAccount {
		x := v.(models.Account)
		x.Password = ""
		v = x
	}
	ok(w, v)
}

func (s *Server) del(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	k, _, good := s.kind(w, r, ac, true)
	if !good {
		return
	}
	id := pathID(r)
	if k == models.KAccount && id == ac.ID {
		fail(w, 400, "cannot delete yourself")
		return
	}
	if k == models.KStream {
		s.Mgr.Forget(id)
	}
	s.St.Delete(k, id)
	ok(w, nil)
}

// ---- streams ----

type streamRow struct {
	engine.Status
	Name     string `json:"name"`
	Category int64  `json:"category"`
	Enabled  bool   `json:"enabled"`
	Logo     string `json:"logo"`
	Mode     string `json:"mode"`
	Profile  int64  `json:"profile"`
}

func (s *Server) statuses() []streamRow {
	ss := store.List[models.Stream](s.St, models.KStream)
	out := make([]streamRow, 0, len(ss))
	for _, x := range ss {
		if x.ProfileID > 0 {
			var pr models.Profile
			if s.St.Get(models.KProfile, x.ProfileID, &pr) == nil {
				x.Transcode = engine.MergeProfile(pr.Transcode, x.Transcode)
				if pr.GPU != nil && *pr.GPU != -3 && (x.GPU == -2 || x.GPU == -3) {
					x.GPU = *pr.GPU
				}
			}
		}
		mode := "copy"
		if x.Relay {
			mode = "relay"
		} else if x.DRM.Enabled {
			mode = "drm"
		} else if len(x.Transcode.VideoEncoders) > 0 && x.Transcode.VideoEncoders[0].Codec != "copy" {
			mode = "cpu"
			if x.GPU != -1 && x.Transcode.HWAccel != "none" && x.Transcode.HWAccel != "" {
				mode = "gpu"
			}
		}
		out = append(out, streamRow{Status: s.Mgr.Status(x.ID), Name: x.Name, Category: x.Category, Enabled: x.Enabled, Logo: x.Logo, Mode: mode, Profile: x.ProfileID})
	}
	return out
}

func (s *Server) streamAction(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	id := pathID(r)
	if ac.Role != "admin" {
		fail(w, 403, "admin only")
		return
	}
	var err error
	switch r.PathValue("action") {
	case "start":
		s.setEnabled(id, true)
		err = s.Mgr.Start(id)
	case "stop":
		s.setEnabled(id, false)
		go s.Mgr.Stop(id)
	case "restart":
		go s.Mgr.Restart(id)
	case "switch":
		idx, _ := strconv.Atoi(r.URL.Query().Get("input"))
		go s.Mgr.SwitchInput(id, idx)
	case "clone":
		var x models.Stream
		if err = s.St.Get(models.KStream, id, &x); err == nil {
			x.ID = s.St.NextID(models.KStream)
			x.Name += " (copy)"
			x.Enabled = false
			s.St.Put(models.KStream, x.ID, x)
			ok(w, x)
			return
		}
	default:
		fail(w, 400, "unknown action")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ok(w, nil)
}

func (s *Server) setEnabled(id int64, on bool) {
	var x models.Stream
	if s.St.Get(models.KStream, id, &x) == nil && x.Enabled != on {
		x.Enabled = on
		s.St.Put(models.KStream, id, x)
	}
}

func (s *Server) bulk(w http.ResponseWriter, r *http.Request, ac *models.Account) {
	if ac.Role != "admin" {
		fail(w, 403, "admin only")
		return
	}
	var in struct {
		IDs    []int64         `json:"ids"`
		Action string          `json:"action"`
		Patch  json.RawMessage `json:"patch"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in) != nil {
		fail(w, 400, "bad request")
		return
	}
	if len(in.IDs) == 0 {
		for _, x := range store.List[models.Stream](s.St, models.KStream) {
			in.IDs = append(in.IDs, x.ID)
		}
	}
	go func() {
		st := s.settings()
		for _, id := range in.IDs {
			switch in.Action {
			case "start":
				s.setEnabled(id, true)
				s.Mgr.Start(id)
				time.Sleep(time.Duration(st.StartInterval * float64(time.Second)))
			case "stop":
				s.setEnabled(id, false)
				s.Mgr.Stop(id)
			case "restart":
				s.Mgr.Restart(id)
				time.Sleep(time.Duration(st.StartInterval * float64(time.Second)))
			case "delete":
				s.Mgr.Forget(id)
				s.St.Delete(models.KStream, id)
			case "patch":
				var old json.RawMessage
				if s.St.Get(models.KStream, id, &old) == nil {
					var a, p map[string]any
					json.Unmarshal(old, &a)
					json.Unmarshal(in.Patch, &p)
					for k, v := range p {
						a[k] = v
					}
					b, _ := json.Marshal(a)
					var x models.Stream
					if json.Unmarshal(b, &x) == nil {
						x.ID = id
						s.St.Put(models.KStream, id, x)
						if s.Mgr.Running(id) {
							s.Mgr.Restart(id)
						}
					}
				}
			}
		}
	}()
	ok(w, map[string]any{"ok": true, "count": len(in.IDs)})
}

func (s *Server) streamRoot(id int64) string {
	return filepath.Join(s.settings().HLSDir, strconv.FormatInt(id, 10))
}

func (s *Server) thumb(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	id := pathID(r)
	root := s.streamRoot(id)
	out := filepath.Join(root, "thumb.jpg")
	if fi, err := os.Stat(out); err == nil && time.Since(fi.ModTime()) < 20*time.Second {
		http.ServeFile(w, r, out)
		return
	}
	seg := engine.LastSegment(root)
	if seg == "" {
		http.Error(w, "no picture", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tmp := out + ".tmp.jpg"
	err := exec.CommandContext(ctx, s.settings().FFmpegPath, "-hide_banner", "-loglevel", "error", "-y", "-i", seg, "-frames:v", "1", "-vf", "scale=320:-2", "-q:v", "6", tmp).Run()
	if err != nil {
		http.Error(w, "thumb failed", 500)
		return
	}
	os.Rename(tmp, out)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, out)
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var in struct {
		URL       string `json:"url"`
		UserAgent string `json:"user_agent"`
		Proxy     string `json:"proxy"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil || in.URL == "" {
		fail(w, 400, "url required")
		return
	}
	st := s.settings()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-v", "error", "-show_format", "-show_streams", "-print_format", "json", "-rw_timeout", "15000000"}
	if strings.HasPrefix(in.URL, "http") {
		args = append(args, "-user_agent", def(in.UserAgent, st.UserAgent))
		if in.Proxy != "" && strings.HasPrefix(in.Proxy, "http") {
			args = append(args, "-http_proxy", in.Proxy)
		}
	}
	args = append(args, in.URL)
	out, err := exec.CommandContext(ctx, st.FFprobePath, args...).CombinedOutput()
	if err != nil {
		fail(w, 400, "probe failed: "+strings.TrimSpace(string(out)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func def(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- dashboard / websocket ----

func (s *Server) dashboard() map[string]any {
	rows := s.statuses()
	cnt := map[string]int{}
	viewers := 0
	for _, x := range rows {
		cnt[string(x.State)]++
		viewers += x.Viewers
	}
	cnt["total"] = len(rows)
	return map[string]any{
		"version": version.Version, "name": s.settings().PanelName, "uptime": int64(time.Since(s.Started).Seconds()),
		"sys": s.Sys.Get(), "gpu": s.GPU.Get(), "streams": cnt, "viewers": viewers,
		"users": len(s.St.IDs(models.KUser)), "servers": len(s.St.IDs(models.KServer)) + 1,
		"ffmpeg": s.Mgr.FFVersion(), "assigned": s.Mgr.GPUAssigned(),
	}
}

var upgrader = websocket.Upgrader{ReadBufferSize: 512, WriteBufferSize: 8192, CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	ac := s.Auth.Account(r)
	if ac == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.WriteJSON(map[string]any{"dash": s.dashboard(), "streams": s.statuses()}); err != nil {
			return
		}
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

// ---- files / backups ----

func (s *Server) filesDir() string {
	d := filepath.Join(s.St.Dir(), "files")
	os.MkdirAll(d, 0o755)
	return d
}

func safeName(n string) (string, bool) {
	n = filepath.Base(n)
	if n == "." || n == "/" || n == "" || strings.HasPrefix(n, ".") {
		return "", false
	}
	return n, true
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	f, h, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	defer f.Close()
	n, good := safeName(h.Filename)
	if !good {
		fail(w, 400, "bad name")
		return
	}
	dst, err := os.Create(filepath.Join(s.filesDir(), n))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer dst.Close()
	io.Copy(dst, f)
	ok(w, map[string]any{"ok": true, "name": n, "path": filepath.Join(s.filesDir(), n)})
}

func (s *Server) files(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	es, _ := os.ReadDir(s.filesDir())
	out := []map[string]any{}
	for _, e := range es {
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			out = append(out, map[string]any{"name": e.Name(), "size": fi.Size(), "time": fi.ModTime().Unix(), "path": filepath.Join(s.filesDir(), e.Name())})
		}
	}
	ok(w, out)
}

func (s *Server) delFile(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	n, good := safeName(r.PathValue("name"))
	if !good {
		fail(w, 400, "bad name")
		return
	}
	os.Remove(filepath.Join(s.filesDir(), n))
	ok(w, nil)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	n, good := safeName(r.PathValue("name"))
	if !good {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.filesDir(), n))
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	es, _ := os.ReadDir(filepath.Join(s.St.Dir(), "backup"))
	out := []map[string]any{}
	for _, e := range es {
		if fi, err := e.Info(); err == nil {
			out = append(out, map[string]any{"name": e.Name(), "size": fi.Size(), "time": fi.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["time"].(int64) > out[j]["time"].(int64) })
	ok(w, out)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	n, good := safeName(r.PathValue("name"))
	if !good {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+n)
	http.ServeFile(w, r, filepath.Join(s.St.Dir(), "backup", n))
}

// restore stages an uploaded database; it is swapped in on restart.
func (s *Server) restore(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var src io.Reader
	if name := r.URL.Query().Get("name"); name != "" {
		n, good := safeName(name)
		if !good {
			fail(w, 400, "bad name")
			return
		}
		f, err := os.Open(filepath.Join(s.St.Dir(), "backup", n))
		if err != nil {
			fail(w, 404, "backup not found")
			return
		}
		defer f.Close()
		src = f
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
		f, _, err := r.FormFile("file")
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		defer f.Close()
		src = f
	}
	head := make([]byte, 16)
	stage := filepath.Join(s.St.Dir(), "restore.db")
	dst, err := os.Create(stage)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	n, _ := io.ReadFull(src, head)
	dst.Write(head[:n])
	io.Copy(dst, src)
	dst.Close()
	if string(head[:min(n, 15)]) != "SQLite format 3" {
		os.Remove(stage)
		fail(w, 400, "not a sqlite backup")
		return
	}
	ok(w, map[string]any{"ok": true, "msg": "restoring, panel restarts"})
	go func() { time.Sleep(500 * time.Millisecond); s.Updater.Restart() }()
}

// ---- alerts ----

func (s *Server) Alert(msg string) error {
	st := s.settings()
	if st.TelegramToken == "" || st.TelegramChat == "" {
		return errors.New("telegram not configured")
	}
	c := &http.Client{Timeout: 10 * time.Second}
	body := strings.NewReader(fmt.Sprintf(`{"chat_id":%q,"text":%q}`, st.TelegramChat, "["+st.PanelName+"] "+msg))
	resp, err := c.Post("https://api.telegram.org/bot"+st.TelegramToken+"/sendMessage", "application/json", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram status %d", resp.StatusCode)
	}
	return nil
}
