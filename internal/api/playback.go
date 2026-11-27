package api

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpupanel/internal/engine"
	"gpupanel/internal/lb"
	"gpupanel/internal/models"
	"gpupanel/internal/store"
)

var (
	secretOnce sync.Once
	secret     []byte
)

func (s *Server) secret() []byte {
	secretOnce.Do(func() {
		p := filepath.Join(s.St.Dir(), "secret.key")
		if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
			secret = b
			return
		}
		secret = []byte(Token(32))
		os.WriteFile(p, secret, 0o600)
	})
	return secret
}

func (s *Server) signToken(id int64, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 36)
	h := hmac.New(sha256.New, s.secret())
	fmt.Fprintf(h, "%d|%s", id, exp)
	return hex.EncodeToString(h.Sum(nil))[:24] + "." + exp
}

func (s *Server) checkToken(id int64, tok string) bool {
	i := strings.IndexByte(tok, '.')
	if i < 0 {
		return false
	}
	exp, err := strconv.ParseInt(tok[i+1:], 36, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	h := hmac.New(sha256.New, s.secret())
	fmt.Fprintf(h, "%d|%s", id, tok[i+1:])
	return hmac.Equal([]byte(hex.EncodeToString(h.Sum(nil))[:24]), []byte(tok[:i]))
}

// playUser validates an IPTV subscriber for a stream (id 0 = any).
func (s *Server) playUser(user, pass string, r *http.Request, id int64) (*models.User, string) {
	for _, u := range store.List[models.User](s.St, models.KUser) {
		if u.Username != user || u.Password != pass {
			continue
		}
		if !u.Enabled {
			return nil, "disabled"
		}
		if u.Expire > 0 && time.Now().Unix() > u.Expire {
			return nil, "expired"
		}
		if u.AllowedIPs != "" && !ipIn(clientIP(r), u.AllowedIPs) {
			return nil, "ip not allowed"
		}
		if id > 0 && !s.userHas(&u, id) {
			return nil, "not in package"
		}
		if u.MaxConnections > 0 && id > 0 && s.userConns(u.ID, clientIP(r)) >= u.MaxConnections {
			return nil, "max connections"
		}
		if time.Now().Unix()-u.LastSeen > 60 {
			u.LastSeen = time.Now().Unix()
			s.St.Put(models.KUser, u.ID, u)
		}
		return &u, ""
	}
	return nil, "invalid credentials"
}

func (s *Server) userBouquets(u *models.User) []int64 {
	bq := u.Bouquets
	if len(bq) == 0 && u.PackageID > 0 {
		var p models.Package
		if s.St.Get(models.KPackage, u.PackageID, &p) == nil {
			bq = p.Bouquets
		}
	}
	return bq
}

func (s *Server) userHas(u *models.User, id int64) bool {
	bq := s.userBouquets(u)
	if len(bq) == 0 {
		return true
	}
	for _, b := range bq {
		var x models.Bouquet
		if s.St.Get(models.KBouquet, b, &x) == nil {
			for _, sid := range x.Streams {
				if sid == id {
					return true
				}
			}
		}
	}
	return false
}

// per-user active connections keyed by ip (HLS clients are counted for 30s).
var userSeen sync.Map // userID -> *sync.Map[ip]time

func (s *Server) userConns(uid int64, ip string) int {
	v, _ := userSeen.LoadOrStore(uid, &sync.Map{})
	m := v.(*sync.Map)
	n := 0
	m.Range(func(k, t any) bool {
		if time.Since(t.(time.Time)) > 30*time.Second {
			m.Delete(k)
		} else if k.(string) != ip {
			n++
		}
		return true
	})
	return n
}

func touchUser(uid int64, ip string) {
	v, _ := userSeen.LoadOrStore(uid, &sync.Map{})
	v.(*sync.Map).Store(ip, time.Now())
}

func ipIn(ip, list string) bool {
	pip := net.ParseIP(ip)
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if strings.Contains(f, "/") {
			if _, n, err := net.ParseCIDR(f); err == nil && pip != nil && n.Contains(pip) {
				return true
			}
		} else if f == ip {
			return true
		}
	}
	return false
}

var (
	ruleMu    sync.Mutex
	ruleCache []models.IPRule
	ruleTime  time.Time
)

func (s *Server) ipAllowed(r *http.Request) bool {
	ruleMu.Lock()
	if time.Since(ruleTime) > 5*time.Second {
		ruleCache = store.List[models.IPRule](s.St, models.KIPRule)
		ruleTime = time.Now()
	}
	rules := ruleCache
	ruleMu.Unlock()
	if len(rules) == 0 {
		return true
	}
	ip := clientIP(r)
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}
	scope := "stream"
	if strings.HasPrefix(r.URL.Path, "/api") || !strings.Contains(r.URL.Path[1:], "/") {
		scope = "panel"
	}
	hasAllow, allowed := false, false
	for _, ru := range rules {
		if ru.Scope != scope && ru.Scope != "all" && ru.Scope != "" {
			continue
		}
		hit := ipIn(ip, ru.CIDR)
		if ru.Action == "block" && hit {
			return false
		}
		if ru.Action == "allow" {
			hasAllow = true
			allowed = allowed || hit
		}
	}
	return !hasAllow || allowed
}

// playlistOf finds the main playlist of a running stream.
func playlistOf(root string) string {
	es, _ := os.ReadDir(root)
	var cands []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".m3u8") {
			cands = append(cands, e.Name())
		}
	}
	sort.Strings(cands)
	if len(cands) > 0 {
		return cands[0]
	}
	for _, e := range es {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, e.Name(), "index.m3u8")); err == nil {
				return e.Name() + "/index.m3u8"
			}
		}
	}
	return ""
}

// ensure starts on-demand streams and waits for the first playlist.
func (s *Server) ensure(id int64) bool {
	var x models.Stream
	if s.St.Get(models.KStream, id, &x) != nil {
		return false
	}
	root := s.streamRoot(id)
	if s.Mgr.Running(id) && playlistOf(root) != "" {
		engine.TouchDemand(id)
		return true
	}
	if !x.Enabled && !x.OnDemand {
		return false
	}
	if !s.Mgr.Running(id) {
		s.Mgr.Start(id)
	}
	engine.TouchDemand(id)
	for i := 0; i < 60; i++ {
		if pl := playlistOf(root); pl != "" {
			if _, err := os.Stat(filepath.Join(root, pl)); err == nil {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return playlistOf(root) != ""
}

func viewerKey(r *http.Request) string { return clientIP(r) + "|" + r.UserAgent() }

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Server", "MasterGPU")
}

func (s *Server) hls(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := pathID(r)
	p := filepath.Clean("/" + r.PathValue("path"))
	tok := r.URL.Query().Get("t")
	st := s.settings()
	if st.SecurePlayback && !s.checkToken(id, tok) && s.Auth.Account(r) == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	root := s.streamRoot(id)
	alias := p == "/" || p == "/auto.m3u8"
	if p == "/index.m3u8" {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			alias = true
		}
	}
	if alias {
		if !s.ensure(id) {
			http.Error(w, "stream offline", 404)
			return
		}
		p = "/" + playlistOf(root)
	}
	full := filepath.Join(root, p)
	if strings.HasSuffix(p, ".m3u8") {
		if _, err := os.Stat(full); err != nil && !s.ensure(id) {
			http.Error(w, "stream offline", 404)
			return
		}
		engine.TouchViewer(id, viewerKey(r))
		engine.TouchDemand(id)
		b, err := os.ReadFile(full)
		if err != nil {
			http.Error(w, "not ready", 404)
			return
		}
		if d := strings.TrimPrefix(filepath.Dir(p), "/"); alias && d != "" {
			b = prefixPlaylist(b, d+"/")
		}
		if alias || p == "/"+playlistOf(root) {
			b = s.withSubs(r, id, b)
		}
		if tok != "" {
			b = rewritePlaylist(b, "t="+tok)
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
		return
	}
	switch filepath.Ext(p) {
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".vtt":
		w.Header().Set("Content-Type", "text/vtt")
	case ".m4s", ".mp4":
		w.Header().Set("Content-Type", "video/iso.segment")
	case ".mpd":
		w.Header().Set("Content-Type", "application/dash+xml")
		w.Header().Set("Cache-Control", "no-cache")
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func prefixPlaylist(b []byte, pre string) []byte {
	rel := func(u string) bool { return u != "" && !strings.HasPrefix(u, "/") && !strings.Contains(u, "://") }
	var out strings.Builder
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "#") && rel(l) {
			l = pre + l
		} else if i := strings.Index(l, `URI="`); i >= 0 && rel(l[i+5:]) {
			l = l[:i+5] + pre + l[i+5:]
		}
		out.WriteString(l + "\n")
	}
	return []byte(out.String())
}

func rewritePlaylist(b []byte, q string) []byte {
	var out strings.Builder
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := sc.Text()
		if l != "" && !strings.HasPrefix(l, "#") {
			if strings.Contains(l, "?") {
				l += "&" + q
			} else {
				l += "?" + q
			}
		} else if strings.Contains(l, `URI="`) {
			i := strings.Index(l, `URI="`) + 5
			j := strings.IndexByte(l[i:], '"')
			if j > 0 {
				l = l[:i+j] + "?" + q + l[i+j:]
			}
		}
		out.WriteString(l)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

type variant struct {
	Label string
	N     int
	File  string
}

// variants lists a stream's HTTP (MPEG-TS) outputs with a quality label each, e.g. FHD/HEVC/HD/SD.
func (s *Server) variants(x *models.Stream) []variant {
	tc := x.Transcode
	if x.ProfileID > 0 {
		var pr models.Profile
		if s.St.Get(models.KProfile, x.ProfileID, &pr) == nil {
			tc = engine.MergeProfile(pr.Transcode, tc)
		}
	}
	var r []variant
	seen := map[string]int{}
	for i, o := range x.Outputs {
		if o.Disabled || outKind(o.Type) != "http" {
			continue
		}
		l := o.Name
		if l == "" && len(o.Video) > 0 && o.Video[0] < len(tc.VideoEncoders) {
			l = qualityLabel(tc.VideoEncoders[o.Video[0]])
		}
		if l == "" {
			l = strconv.Itoa(i + 1)
		}
		if seen[l]++; seen[l] > 1 {
			l += " " + strconv.Itoa(seen[l])
		}
		r = append(r, variant{l, i + 1, engine.OutputFile(x, i)})
	}
	return r
}

func qualityLabel(v models.VideoEnc) string {
	if v.Name != "" {
		return v.Name
	}
	switch v.Codec {
	case "copy":
		return "SOURCE"
	case "hevc", "h265":
		return "HEVC"
	case "av1":
		return "AV1"
	}
	h := 0
	if _, b, ok := strings.Cut(strings.ToLower(v.Size), "x"); ok {
		h, _ = strconv.Atoi(b)
	}
	switch {
	case h == 0:
		return ""
	case h >= 2160:
		return "4K"
	case h >= 1080:
		return "FHD"
	case h >= 720:
		return "HD"
	}
	return "SD"
}

// live implements Xtream style /live/user/pass/id.(ts|m3u8)
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	cors(w)
	file := r.PathValue("file")
	ext := filepath.Ext(file)
	sid, sn, _ := strings.Cut(strings.TrimSuffix(file, ext), "_")
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, msg := s.playUser(r.PathValue("user"), r.PathValue("pass"), r, id)
	if u == nil {
		http.Error(w, msg, 403)
		return
	}
	touchUser(u.ID, clientIP(r))
	if sn != "" {
		n, _ := strconv.Atoi(sn)
		var x models.Stream
		if ext != ".ts" || s.St.Get(models.KStream, id, &x) != nil || n < 1 || n > len(x.Outputs) || x.Outputs[n-1].Disabled || outKind(x.Outputs[n-1].Type) != "http" {
			http.NotFound(w, r)
			return
		}
		if !s.Mgr.Running(id) {
			if !x.Enabled && !x.OnDemand {
				http.Error(w, "stream offline", 404)
				return
			}
			s.Mgr.Start(id)
		}
		engine.TouchDemand(id)
		s.serveTS(w, r, id, s.Mgr.OutHub(id, n), x.Outputs[n-1].Buffer, func() { touchUser(u.ID, clientIP(r)) })
		return
	}
	if ext == ".m3u8" {
		if !s.ensure(id) {
			http.Error(w, "stream offline", 404)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hls/%d/%s?t=%s", id, playlistOf(s.streamRoot(id)), s.signToken(id, 6*time.Hour)), http.StatusFound)
		return
	}
	s.serveTS(w, r, id, nil, 0, func() { touchUser(u.ID, clientIP(r)) })
}

func (s *Server) tsRelay(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := pathID(r)
	if s.settings().SecurePlayback && !s.checkToken(id, r.URL.Query().Get("t")) && s.Auth.Account(r) == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	s.serveTS(w, r, id, nil, 0, nil)
}

// serveTS fans the running encoder's MPEG-TS out to this viewer.
func (s *Server) serveTS(w http.ResponseWriter, r *http.Request, id int64, hub *engine.Hub, buf int, tick func()) {
	if hub == nil {
		if !s.ensure(id) {
			http.Error(w, "stream offline", 404)
			return
		}
		hub = s.Mgr.Hub(id)
	}
	n := 256
	if buf > 0 {
		n = buf / 64
	}
	ch, cancel := hub.SubscribeAs(n, clientIP(r), "http")
	defer cancel()
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	idle := time.NewTimer(20 * time.Second)
	defer idle.Stop()
	last := time.Now()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-idle.C:
			return
		case b, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write(b); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			idle.Reset(20 * time.Second)
			if time.Since(last) > 10*time.Second {
				last = time.Now()
				engine.TouchDemand(id)
				if tick != nil {
					tick()
				}
			}
		}
	}
}

func (s *Server) baseURL(r *http.Request) string {
	sc := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		sc = "https"
	}
	return sc + "://" + r.Host
}

func (s *Server) getPHP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u, msg := s.playUser(q.Get("username"), q.Get("password"), r, 0)
	if u == nil {
		http.Error(w, msg, 403)
		return
	}
	ext := "ts"
	if q.Get("output") == "m3u8" || q.Get("output") == "hls" {
		ext = "m3u8"
	}
	cats := map[int64]string{}
	for _, c := range store.List[models.Category](s.St, models.KCategory) {
		cats[c.ID] = c.Name
	}
	base := s.baseURL(r)
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, x := range store.List[models.Stream](s.St, models.KStream) {
		if (!x.Enabled && !x.OnDemand) || !s.userHas(u, x.ID) {
			continue
		}
		if vs := s.variants(&x); ext == "ts" && len(vs) > 1 {
			for _, v := range vs {
				n := x.Name + " " + v.Label
				fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q group-title=%q,%s\n%s/live/%s/%s/%d_%d.ts\n",
					x.EPGID, n, x.Logo, cats[x.Category], n, base, u.Username, u.Password, x.ID, v.N)
			}
			continue
		}
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=%q tvg-name=%q tvg-logo=%q group-title=%q,%s\n%s/live/%s/%s/%d.%s\n",
			x.EPGID, x.Name, x.Logo, cats[x.Category], x.Name, base, u.Username, u.Password, x.ID, ext)
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", "attachment; filename=playlist.m3u")
	io.WriteString(w, b.String())
}

func (s *Server) playerAPI(w http.ResponseWriter, r *http.Request) {
	cors(w)
	q := r.URL.Query()
	u, msg := s.playUser(q.Get("username"), q.Get("password"), r, 0)
	if u == nil {
		writeJSON(w, 200, map[string]any{"user_info": map[string]any{"auth": 0, "message": msg}})
		return
	}
	switch q.Get("action") {
	case "get_live_categories", "get_vod_categories", "get_series_categories":
		typ := map[string]string{"get_live_categories": "live", "get_vod_categories": "movie", "get_series_categories": "series"}[q.Get("action")]
		out := []map[string]any{}
		for _, c := range store.List[models.Category](s.St, models.KCategory) {
			if c.Type == typ || (c.Type == "" && typ == "live") {
				out = append(out, map[string]any{"category_id": strconv.FormatInt(c.ID, 10), "category_name": c.Name, "parent_id": 0})
			}
		}
		writeJSON(w, 200, out)
	case "get_live_streams":
		out := []map[string]any{}
		for i, x := range store.List[models.Stream](s.St, models.KStream) {
			if (!x.Enabled && !x.OnDemand) || !s.userHas(u, x.ID) {
				continue
			}
			out = append(out, map[string]any{"num": i + 1, "name": x.Name, "stream_type": "live", "stream_id": x.ID, "stream_icon": x.Logo,
				"epg_channel_id": x.EPGID, "category_id": strconv.FormatInt(x.Category, 10), "tv_archive": 0, "direct_source": ""})
		}
		writeJSON(w, 200, out)
	case "get_vod_streams":
		out := []map[string]any{}
		for _, m := range store.List[models.Movie](s.St, models.KMovie) {
			out = append(out, map[string]any{"name": m.Name, "stream_type": "movie", "stream_id": m.ID, "stream_icon": m.Poster, "rating": m.Rating,
				"category_id": strconv.FormatInt(m.Category, 10), "container_extension": "mp4"})
		}
		writeJSON(w, 200, out)
	case "get_series":
		out := []map[string]any{}
		for _, x := range store.List[models.Series](s.St, models.KSeries) {
			out = append(out, map[string]any{"name": x.Name, "series_id": x.ID, "cover": x.Poster, "plot": x.Plot, "category_id": strconv.FormatInt(x.Category, 10)})
		}
		writeJSON(w, 200, out)
	default:
		host, port, _ := net.SplitHostPort(r.Host)
		if host == "" {
			host, port = r.Host, "80"
		}
		bq := s.userBouquets(u)
		writeJSON(w, 200, map[string]any{
			"user_info": map[string]any{"username": u.Username, "password": u.Password, "auth": 1, "status": "Active",
				"exp_date": strconv.FormatInt(u.Expire, 10), "is_trial": b2s(u.Trial), "active_cons": "0", "created_at": strconv.FormatInt(u.Created, 10),
				"max_connections": strconv.Itoa(u.MaxConnections), "allowed_output_formats": []string{"m3u8", "ts"}, "bouquets": bq},
			"server_info": map[string]any{"url": host, "port": port, "https_port": "", "server_protocol": "http", "timezone": "UTC",
				"timestamp_now": time.Now().Unix(), "time_now": time.Now().UTC().Format("2006-01-02 15:04:05")},
		})
	}
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

var vlb lb.ViewerBalancer

// lbRedirect sends a viewer to the least loaded edge server for a stream.
func (s *Server) lbRedirect(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	st := s.settings()
	ts := []lb.ViewerTarget{{ID: 0, Base: "", Weight: 1, Conns: s.Mgr.Status(id).Viewers, Healthy: true}}
	for _, sv := range store.List[models.Server](s.St, models.KServer) {
		if !sv.LBEnabled || sv.Disabled {
			continue
		}
		host := def(sv.PublicHost, sv.Host)
		port := sv.HTTPPort
		if port == 0 {
			port = 8090
		}
		ts = append(ts, lb.ViewerTarget{ID: sv.ID, Base: fmt.Sprintf("http://%s:%d", host, port), Weight: max(sv.LBWeight, 1), Max: sv.MaxClients, Healthy: edgeHealthy(sv.ID)})
	}
	t := vlb.Pick(ts, st.LBViewerMode)
	target := fmt.Sprintf("/hls/%d/%s", id, r.PathValue("path"))
	if t != nil && t.Base != "" {
		target = t.Base + target
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

var edgeState sync.Map // server id -> bool

func edgeHealthy(id int64) bool {
	v, ok := edgeState.Load(id)
	return !ok || v.(bool)
}

// CheckEdges pings load balancer servers so unhealthy ones get skipped.
func (s *Server) CheckEdges() {
	c := &http.Client{Timeout: 4 * time.Second}
	for _, sv := range store.List[models.Server](s.St, models.KServer) {
		if !sv.LBEnabled {
			continue
		}
		port := sv.HTTPPort
		if port == 0 {
			port = 8090
		}
		resp, err := c.Get(fmt.Sprintf("http://%s:%d/api/version", def(sv.PublicHost, sv.Host), port))
		okk := err == nil && resp.StatusCode == 200
		if resp != nil {
			resp.Body.Close()
		}
		if prev := edgeHealthy(sv.ID); prev != okk {
			s.St.Log("server", sv.ID, map[bool]string{true: "info", false: "error"}[okk], fmt.Sprintf("server %s healthy=%v", sv.Name, okk))
			if !okk {
				go s.Alert("server down: " + sv.Name)
			}
		}
		edgeState.Store(sv.ID, okk)
	}
}

// ---- m3u import/export ----

func (s *Server) importM3U(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var in struct {
		Text      string `json:"text"`
		URL       string `json:"url"`
		ProfileID int64  `json:"profile_id"`
		OnDemand  bool   `json:"on_demand"`
		Enabled   bool   `json:"enabled"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&in) != nil {
		fail(w, 400, "bad request")
		return
	}
	if in.URL != "" {
		c := &http.Client{Timeout: 30 * time.Second}
		resp, err := c.Get(in.URL)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		in.Text = string(b)
	}
	cats := map[string]int64{}
	for _, c := range store.List[models.Category](s.St, models.KCategory) {
		cats[c.Name] = c.ID
	}
	n := 0
	var cur map[string]string
	var name string
	sc := bufio.NewScanner(strings.NewReader(in.Text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "#EXTINF"):
			cur = attrs(l)
			if i := strings.LastIndex(l, ","); i >= 0 {
				name = strings.TrimSpace(l[i+1:])
			}
		case l != "" && !strings.HasPrefix(l, "#"):
			if name == "" {
				name = "Channel " + strconv.Itoa(n+1)
			}
			var cid int64
			if g := cur["group-title"]; g != "" {
				if cid = cats[g]; cid == 0 {
					cid = s.St.NextID(models.KCategory)
					s.St.Put(models.KCategory, cid, models.Category{ID: cid, Name: g, Type: "live"})
					cats[g] = cid
				}
			}
			id := s.St.NextID(models.KStream)
			s.St.Put(models.KStream, id, models.Stream{ID: id, Name: name, Inputs: []string{l}, Category: cid, Logo: cur["tvg-logo"], EPGID: cur["tvg-id"],
				Enabled: in.Enabled, OnDemand: in.OnDemand, ProfileID: in.ProfileID, GPU: -2, Outputs: []models.Output{{Type: "hls", Name: "index"}}, Watchdog: true, DailyStart: -1, DailyEnd: -1})
			n++
			name, cur = "", nil
		}
	}
	s.St.Log("panel", 0, "info", fmt.Sprintf("imported %d streams from m3u", n))
	ok(w, map[string]any{"ok": true, "count": n})
}

func attrs(l string) map[string]string {
	m := map[string]string{}
	for {
		i := strings.Index(l, `="`)
		if i < 0 {
			return m
		}
		k := l[:i]
		if j := strings.LastIndexAny(k, " \t"); j >= 0 {
			k = k[j+1:]
		}
		rest := l[i+2:]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return m
		}
		m[strings.ToLower(k)] = rest[:j]
		l = rest[j+1:]
	}
}

func (s *Server) exportM3U(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	cats := map[int64]string{}
	for _, c := range store.List[models.Category](s.St, models.KCategory) {
		cats[c.ID] = c.Name
	}
	base, secure := s.baseURL(r), s.settings().SecurePlayback
	var b strings.Builder
	only := map[int64]bool{}
	for _, v := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			only[id] = true
		}
	}
	b.WriteString("#EXTM3U\n")
	for _, x := range store.List[models.Stream](s.St, models.KStream) {
		if len(only) > 0 && !only[x.ID] {
			continue
		}
		q := ""
		if secure {
			q = "?t=" + s.signToken(x.ID, 365*24*time.Hour)
		}
		if vs := s.variants(&x); len(vs) > 1 {
			for _, v := range vs {
				fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=%q tvg-logo=%q group-title=%q,%s %s\n%s/stream/%d/%d.ts%s\n", x.EPGID, x.Logo, cats[x.Category], x.Name, v.Label, base, x.ID, v.N, q)
			}
			continue
		}
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=%q tvg-logo=%q group-title=%q,%s\n%s/hls/%d/%s\n", x.EPGID, x.Logo, cats[x.Category], x.Name, base, x.ID, q)
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", "attachment; filename=mastergpu.m3u")
	io.WriteString(w, b.String())
}

// drmManifest serves the origin manifest of a DRM stream to the local ffmpeg only.
func (s *Server) drmManifest(w http.ResponseWriter, r *http.Request) {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(h); s.DRM == nil || ip == nil || !ip.IsLoopback() {
		http.Error(w, "forbidden", 403)
		return
	}
	b, err := s.DRM.Manifest(r.Context(), pathID(r))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/dash+xml")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Server) drmTrack(w http.ResponseWriter, r *http.Request) {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	k := r.PathValue("kind")
	if ip := net.ParseIP(h); s.DRM == nil || ip == nil || !ip.IsLoopback() || k != "video" && k != "audio" {
		http.Error(w, "forbidden", 403)
		return
	}
	s.DRM.ServeTrack(w, r, pathID(r), k)
}

// subs serves the WebVTT subtitle renditions (original or live-translated) of a DRM stream.
func (s *Server) subs(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := pathID(r)
	if s.DRM == nil {
		http.NotFound(w, r)
		return
	}
	if s.settings().SecurePlayback && !s.checkToken(id, r.URL.Query().Get("t")) && s.Auth.Account(r) == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	track, file := r.PathValue("track"), r.PathValue("file")
	if track == "" {
		if !strings.HasSuffix(file, ".m3u8") {
			http.NotFound(w, r)
			return
		}
		track, file = strings.TrimSuffix(file, ".m3u8"), ""
	}
	s.DRM.ServeSubs(w, r, id, track, file)
}

// withSubs adds the DRM subtitle renditions to an HLS playlist; a media playlist
// is wrapped in a master playlist that points back to itself with ?raw=1.
func (s *Server) withSubs(r *http.Request, id int64, b []byte) []byte {
	if s.DRM == nil || r.URL.Query().Get("raw") != "" {
		return b
	}
	ts := s.DRM.SubTracks(r.Context(), id)
	if len(ts) == 0 {
		return b
	}
	var media strings.Builder
	for i, t := range ts {
		def, lang := "NO", t.Lang
		if i == 0 {
			def = "YES"
		}
		if lang == "" {
			lang = "und"
		}
		fmt.Fprintf(&media, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES,URI=\"/subs/%d/%s.m3u8\"\n", t.Name, lang, def, id, t.ID)
	}
	if !bytes.Contains(b, []byte("#EXT-X-STREAM-INF")) {
		return []byte("#EXTM3U\n#EXT-X-VERSION:3\n" + media.String() + "#EXT-X-STREAM-INF:BANDWIDTH=6000000,SUBTITLES=\"subs\"\n" + r.URL.Path + "?raw=1\n")
	}
	var out strings.Builder
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(l, "#EXT-X-STREAM-INF") && !strings.Contains(l, "SUBTITLES=") {
			l += `,SUBTITLES="subs"`
		}
		out.WriteString(l + "\n")
		if i == 0 {
			out.WriteString(media.String())
		}
	}
	return []byte(out.String())
}

func outKind(t string) string {
	switch t {
	case "hls", "hls_master":
		return "hls"
	}
	return t
}

// streamOut serves Xaccel-style output links:
// /stream/{id}/{file}.m3u8 (HLS output) and /stream/{id}/{file}.ts (HTTP output).
// {file} is the output's filename or its 1-based number.
func (s *Server) streamOut(w http.ResponseWriter, r *http.Request) {
	cors(w)
	id := pathID(r)
	file := r.PathValue("file")
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(file, ext)
	kind := map[string]string{".m3u8": "hls", ".ts": "http"}[ext]
	var x models.Stream
	if kind == "" || s.St.Get(models.KStream, id, &x) != nil {
		http.NotFound(w, r)
		return
	}
	tok := r.URL.Query().Get("t")
	if s.settings().SecurePlayback && !s.checkToken(id, tok) && s.Auth.Account(r) == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	oi := -1
	for i, o := range x.Outputs {
		if !o.Disabled && outKind(o.Type) == kind && engine.OutputFile(&x, i) == base {
			oi = i
			break
		}
	}
	if n, err := strconv.Atoi(base); oi < 0 && err == nil && n >= 1 && n <= len(x.Outputs) && !x.Outputs[n-1].Disabled && outKind(x.Outputs[n-1].Type) == kind {
		oi = n - 1
	}
	if oi < 0 {
		http.Error(w, "no such output", 404)
		return
	}
	if x.LiveOnly && s.Mgr.Status(id).State == engine.Waiting {
		http.Error(w, "stream offline", 404) // live-only stream waiting for its source to go live
		return
	}
	if kind == "http" {
		if !s.Mgr.Running(id) {
			if !x.Enabled && !x.OnDemand {
				http.Error(w, "stream offline", 404)
				return
			}
			s.Mgr.Start(id)
		}
		engine.TouchDemand(id)
		s.serveTS(w, r, id, s.Mgr.OutHub(id, oi+1), x.Outputs[oi].Buffer, nil)
		return
	}
	if !s.ensure(id) {
		http.Error(w, "stream offline", 404)
		return
	}
	root, dir := s.streamRoot(id), engine.OutputDir(&x, oi)
	p, pre := dir+".m3u8", fmt.Sprintf("/hls/%d/", id)
	var b []byte
	for i := 0; i < 40; i++ {
		var err error
		if b, err = os.ReadFile(filepath.Join(root, p)); err == nil {
			break
		}
		if b, err = os.ReadFile(filepath.Join(root, dir, "index.m3u8")); err == nil {
			pre += dir + "/"
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if b == nil {
		http.Error(w, "not ready", 404)
		return
	}
	engine.TouchViewer(id, viewerKey(r))
	engine.TouchDemand(id)
	b = s.withSubs(r, id, prefixPlaylist(b, pre))
	if tok != "" {
		b = rewritePlaylist(b, "t="+tok)
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}
