// Package drm prepares CENC (ClearKey / operator-supplied keys) DASH inputs for the engine:
// it proxies the manifest locally, resolves KID:KEY pairs and wires per-stage proxies.
package drm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gpupanel/internal/engine"
	"gpupanel/internal/models"
	"gpupanel/internal/netx"
	"gpupanel/internal/store"
)

// Service implements the engine DRM and relay hooks.
type Service struct {
	St        *store.Store
	LocalBase string // http://127.0.0.1:<port> of this panel
	FFmpegVer func() int

	mu   sync.Mutex
	srcs map[int64]source
	mc   map[int64]mcache
	anch map[int64]anchorT
}

type source struct {
	url    string
	stream models.Stream
}

func (d *Service) settings() models.Settings {
	s := models.DefaultSettings()
	_ = d.St.Get(models.KSettings, 1, &s)
	return s
}

// ProxySpec resolves a proxy field ("", URL, host:port, "pool:name" or a stored proxy name).
func (d *Service) ProxySpec(p, bind, dns string) netx.Spec {
	p = strings.TrimSpace(p)
	if p == "" {
		p = d.settings().DefaultProxy
	}
	sp := netx.Spec{BindIP: bind, DNS: dns}
	if strings.HasPrefix(p, "https://") && strings.Contains(p, "/dns-query") {
		p = ""
	}
	pool := strings.TrimPrefix(p, "pool:")
	var cands []models.Proxy
	for _, x := range store.List[models.Proxy](d.St, models.KProxy) {
		if !x.Disabled && (x.Name == p || (strings.HasPrefix(p, "pool:") && x.Pool == pool)) {
			cands = append(cands, x)
		}
	}
	if len(cands) > 0 {
		x := cands[int(time.Now().Unix()/60)%len(cands)]
		u := &url.URL{Scheme: x.Type, Host: x.Host + ":" + strconv.Itoa(x.Port)}
		if u.Scheme == "" {
			u.Scheme = "http"
		}
		if x.Username != "" {
			u.User = url.UserPassword(x.Username, x.Password)
		}
		sp.Upstream = u.String()
		if sp.BindIP == "" {
			sp.BindIP = x.BindIP
		}
		return sp
	}
	if strings.HasPrefix(p, "pool:") {
		if l := d.settings().ProxyList; len(l) > 0 {
			p = l[int(time.Now().Unix()/60)%len(l)]
		} else {
			p = ""
		}
	}
	sp.Upstream = netx.Normalize(p)
	return sp
}

func plainDNS(dns string) string {
	if strings.Contains(dns, "://") {
		return ""
	}
	return dns
}

// Relay returns an ffmpeg -http_proxy value for the stream input, or "".
func (d *Service) Relay(s *models.Stream) string {
	sp := d.ProxySpec(s.Proxy, s.BindIP, plainDNS(s.DNS))
	if sp.Empty() {
		return ""
	}
	if strings.HasPrefix(sp.Upstream, "http://") && sp.BindIP == "" && sp.DNS == "" {
		return sp.Upstream
	}
	r, err := netx.Get(sp)
	if err != nil {
		d.St.Log("stream", s.ID, "error", "proxy: "+err.Error())
		return ""
	}
	return r.Addr
}

func (d *Service) client(s *models.Stream, stage string) (*http.Client, error) {
	p := s.Proxy
	switch stage {
	case "manifest":
		p = firstNonEmpty(s.DRM.ProxyManifest, p)
	case "media":
		p = firstNonEmpty(s.DRM.ProxyMedia, p)
	case "keys":
		p = firstNonEmpty(s.DRM.ProxyKeys, p)
	}
	return netx.Client(d.ProxySpec(p, s.BindIP, plainDNS(s.DNS)), 20*time.Second)
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func (d *Service) headers(req *http.Request, s *models.Stream) {
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	if s.Referer != "" {
		req.Header.Set("Referer", s.Referer)
	}
	for _, l := range strings.Split(strings.ReplaceAll(s.Headers, "\r", ""), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) != "" {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	for k, v := range s.DRM.Headers {
		req.Header.Set(k, v)
	}
}

func (d *Service) fetchManifest(ctx context.Context, s *models.Stream, u string) ([]byte, string, error) {
	c, err := d.client(s, "manifest")
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	d.headers(req, s)
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}
	return b, resp.Request.URL.String(), nil
}

// Prep implements engine.Manager.PrepDRM.
func (d *Service) Prep(ctx context.Context, s *models.Stream, input, root string) (*engine.BuildOpts, error) {
	if d.FFmpegVer != nil && d.FFmpegVer() > 0 && d.FFmpegVer() < 6 {
		return nil, fmt.Errorf("DRM decryption needs FFmpeg 6.1 or newer")
	}
	if input == "" {
		return nil, fmt.Errorf("no input URL")
	}
	body, final, err := d.fetchManifest(ctx, s, input)
	if err != nil {
		return nil, err
	}
	info := ParseMPD(body)
	keys := map[string]string{}
	plain := len(info.KIDs()) == 0 && len(info.PSSH) == 0 && len(s.DRMKeys) == 0
	if !plain {
		if keys, err = d.resolveKeys(ctx, s, info); err != nil {
			return nil, err
		}
	}
	vkey := pick(keys, info.KID("video"))
	akey := pick(keys, info.KID("audio"))
	if vkey == "" && akey == "" && !plain {
		return nil, fmt.Errorf("no key for KID %s", strings.Join(info.KIDs(), ","))
	}
	if vkey == "" {
		vkey = akey
	}
	d.mu.Lock()
	if d.srcs == nil {
		d.srcs = map[int64]source{}
	}
	d.srcs[s.ID] = source{url: final, stream: *s}
	delete(d.anch, s.ID)
	delete(d.mc, s.ID)
	d.mu.Unlock()

	t := &s.Transcode
	sub := strings.ToLower(strings.TrimSpace(s.DRM.Subtitles))
	wantSubs := sub != "" && sub != "none" && info.HasText
	if (!wantSubs || !s.DRM.SubBurn) && info.Has("video") {
		// subtitles (and live translation) are served as WebVTT HLS renditions by the relay
		// segment fetcher: video and audio as separate fMP4 inputs on one timeline
		o := &engine.BuildOpts{Input: fmt.Sprintf("%s/drm/%d/track/video", d.LocalBase, s.ID), InputKeys: []string{vkey}, CopyTS: true}
		if info.Has("audio") {
			o.Inputs2 = []string{fmt.Sprintf("%s/drm/%d/track/audio", d.LocalBase, s.ID)}
			o.InputKeys = append(o.InputKeys, firstNonEmpty(akey, vkey))
			o.AudioInput = 1
		}
		d.St.Log("stream", s.ID, "info", fmt.Sprintf("DRM ready: %d key(s), kids=%s, live segment fetcher, subtitles=%v", len(keys), strings.Join(info.KIDs(), ","), wantSubs))
		return o, nil
	}
	local := fmt.Sprintf("%s/drm/%d/manifest.mpd", d.LocalBase, s.ID)
	o := &engine.BuildOpts{Input: local, InputKeys: []string{vkey}, CENC: true}
	if akey != "" && akey != vkey {
		o.Inputs2 = []string{local}
		o.InputKeys = append(o.InputKeys, akey)
		o.AudioInput = 1
	}
	media := firstNonEmpty(s.DRM.ProxyMedia, s.Proxy)
	if sp := d.ProxySpec(media, s.BindIP, plainDNS(s.DNS)); !sp.Empty() {
		if r, err := netx.Get(sp); err == nil {
			o.Proxy = r.Addr
		}
	}
	if wantSubs && t.SubMode == "" {
		t.SubMode = "webvtt"
		if s.DRM.SubBurn {
			t.SubMode = "burn"
		}
		if len(sub) >= 2 && len(sub) <= 3 {
			t.SubMap = "0:s:m:language:" + sub + "?"
		}
	}
	d.St.Log("stream", s.ID, "info", fmt.Sprintf("DRM ready: %d key(s), kids=%s, subtitles=%v", len(keys), strings.Join(info.KIDs(), ","), info.HasText))
	return o, nil
}

// Manifest returns the upstream manifest for a running DRM stream with absolute BaseURLs.
func (d *Service) Manifest(ctx context.Context, id int64) ([]byte, error) {
	d.mu.Lock()
	src, ok := d.srcs[id]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown stream")
	}
	b, final, err := d.fetchManifest(ctx, &src.stream, src.url)
	if err != nil {
		return nil, err
	}
	return Rebase(b, final), nil
}

var (
	mpdTagRe  = regexp.MustCompile(`(?s)<MPD\b[^>]*>`)
	baseURLRe = regexp.MustCompile(`(?s)<BaseURL([^>]*)>([^<]*)</BaseURL>`)
	periodRe  = regexp.MustCompile(`<Period\b`)
)

// Rebase makes MPD-level BaseURL absolute so the local copy resolves media at the origin.
func Rebase(b []byte, final string) []byte {
	fu, err := url.Parse(final)
	if err != nil {
		return b
	}
	head := len(b)
	if p := periodRe.FindIndex(b); p != nil {
		head = p[0]
	}
	if m := baseURLRe.FindSubmatchIndex(b[:head]); m != nil {
		ref, err := url.Parse(strings.TrimSpace(string(b[m[4]:m[5]])))
		if err != nil {
			return b
		}
		abs := fu.ResolveReference(ref).String()
		return append(append(append([]byte{}, b[:m[4]]...), []byte(xmlEsc(abs))...), b[m[5]:]...)
	}
	m := mpdTagRe.FindIndex(b)
	if m == nil {
		return b
	}
	dir := fu.ResolveReference(&url.URL{Path: "./"}).String()
	if fu.RawQuery != "" {
		dir = strings.SplitN(dir, "?", 2)[0]
	}
	ins := []byte("\n<BaseURL>" + xmlEsc(dir) + "</BaseURL>")
	return append(append(append([]byte{}, b[:m[1]]...), ins...), b[m[1]:]...)
}

func xmlEsc(s string) string {
	var buf bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		default:
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// Info is what Prep needs from a manifest.
type Info struct {
	Sets    []Set
	PSSH    []string
	HasText bool
}

// Set is one adaptation set.
type Set struct {
	Kind string // video, audio, text
	Lang string
	KID  string
}

var (
	asRe   = regexp.MustCompile(`(?s)<AdaptationSet\b(.*?)(?:</AdaptationSet>|$)`)
	kidRe  = regexp.MustCompile(`default_KID\s*=\s*"([0-9a-fA-F-]{32,36})"`)
	psshRe = regexp.MustCompile(`(?s)<(?:\w+:)?pssh[^>]*>([^<]+)</`)
	langRe = regexp.MustCompile(`\blang\s*=\s*"([^"]+)"`)
)

// ParseMPD extracts adaptation set kinds, default KIDs and PSSH boxes.
func ParseMPD(b []byte) Info {
	var in Info
	top := ""
	if m := kidRe.FindSubmatch(b); m != nil {
		top = NormKID(string(m[1]))
	}
	for _, m := range asRe.FindAllSubmatch(b, -1) {
		c := string(m[1])
		k := "video"
		switch {
		case strings.Contains(c, `contentType="audio"`) || strings.Contains(c, `mimeType="audio/`):
			k = "audio"
		case strings.Contains(c, `contentType="text"`) || strings.Contains(c, `mimeType="text/`) || strings.Contains(c, `mimeType="application/ttml`) || strings.Contains(c, `codecs="wvtt"`) || strings.Contains(c, `codecs="stpp`):
			k = "text"
			in.HasText = true
		}
		st := Set{Kind: k}
		if x := langRe.FindStringSubmatch(c); x != nil {
			st.Lang = x[1]
		}
		if x := kidRe.FindStringSubmatch(c); x != nil {
			st.KID = NormKID(x[1])
		} else if k != "text" {
			st.KID = top
		}
		in.Sets = append(in.Sets, st)
	}
	for _, m := range psshRe.FindAllSubmatch(b, -1) {
		in.PSSH = append(in.PSSH, strings.TrimSpace(string(m[1])))
	}
	return in
}

// Has reports whether the manifest has an adaptation set of the given kind.
func (in Info) Has(kind string) bool {
	for _, s := range in.Sets {
		if s.Kind == kind {
			return true
		}
	}
	return false
}

// KID returns the first KID of the given kind.
func (in Info) KID(kind string) string {
	for _, s := range in.Sets {
		if s.Kind == kind && s.KID != "" {
			return s.KID
		}
	}
	return ""
}

// KIDs lists distinct KIDs.
func (in Info) KIDs() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in.Sets {
		if s.KID != "" && !seen[s.KID] {
			seen[s.KID] = true
			out = append(out, s.KID)
		}
	}
	return out
}

// NormKID lowercases and strips dashes; base64/base64url KIDs are converted to hex.
func NormKID(s string) string {
	s = strings.TrimSpace(s)
	h := strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if len(h) == 32 {
		if _, err := hex.DecodeString(h); err == nil {
			return h
		}
	}
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 16 {
			return hex.EncodeToString(b)
		}
	}
	return h
}

func pick(keys map[string]string, kid string) string {
	if kid != "" {
		if k, ok := keys[kid]; ok {
			return k
		}
	}
	if k, ok := keys[""]; ok {
		return k
	}
	if len(keys) == 1 {
		for _, k := range keys {
			return k
		}
	}
	return ""
}

var pairRe = regexp.MustCompile(`([0-9a-fA-F-]{32,36})\s*:\s*([0-9a-fA-F]{32})`)

// ParseKeys accepts ClearKey license JSON, {kid:key} maps, [{kid,key}] lists or KID:KEY text.
func ParseKeys(b []byte) map[string]string {
	out := map[string]string{}
	var ck struct {
		Keys []struct {
			KID string `json:"kid"`
			K   string `json:"k"`
			Key string `json:"key"`
		} `json:"keys"`
	}
	if json.Unmarshal(b, &ck) == nil && len(ck.Keys) > 0 {
		for _, k := range ck.Keys {
			if v := NormKID(firstNonEmpty(k.K, k.Key)); v != "" {
				out[NormKID(k.KID)] = v
			}
		}
		return out
	}
	var list []struct {
		KID string `json:"kid"`
		Key string `json:"key"`
	}
	if json.Unmarshal(b, &list) == nil && len(list) > 0 {
		for _, k := range list {
			out[NormKID(k.KID)] = NormKID(k.Key)
		}
		return out
	}
	var mp map[string]string
	if json.Unmarshal(b, &mp) == nil && len(mp) > 0 {
		for k, v := range mp {
			out[NormKID(k)] = NormKID(v)
		}
		return out
	}
	for _, m := range pairRe.FindAllSubmatch(b, -1) {
		out[NormKID(string(m[1]))] = strings.ToLower(string(m[2]))
	}
	return out
}

func (d *Service) resolveKeys(ctx context.Context, s *models.Stream, info Info) (map[string]string, error) {
	keys := map[string]string{}
	for _, k := range s.DRMKeys {
		k = strings.TrimSpace(k)
		if kid, key, ok := strings.Cut(k, ":"); ok {
			keys[NormKID(kid)] = NormKID(key)
		} else if len(k) == 32 {
			keys[""] = strings.ToLower(k)
		}
	}
	want := info.KIDs()
	for _, k := range store.List[models.DRMKey](d.St, models.KDRMKey) {
		kid := NormKID(k.KID)
		if k.Key == "" {
			continue
		}
		match := k.ContentID != "" && (k.ContentID == s.Name || k.ContentID == strconv.FormatInt(s.ID, 10))
		for _, w := range want {
			match = match || w == kid
		}
		if match {
			keys[kid] = NormKID(k.Key)
		}
	}
	if covered(keys, want) {
		return keys, nil
	}
	c, err := d.client(s, "keys")
	if err != nil {
		return nil, err
	}
	srv := s.DRM.KeyServer
	if srv == "" && d.settings().DRMProvider == "url" {
		srv = d.settings().DRMRequestURL
	}
	var errs []string
	if srv != "" {
		pssh := ""
		if len(info.PSSH) > 0 {
			pssh = info.PSSH[0]
		}
		u := strings.NewReplacer("{kid}", strings.Join(want, ","), "{pssh}", url.QueryEscape(pssh), "{id}", strconv.FormatInt(s.ID, 10), "{name}", url.QueryEscape(s.Name)).Replace(srv)
		if b, err := d.do(ctx, c, s, "GET", u, nil); err == nil {
			for k, v := range ParseKeys(b) {
				keys[k] = v
			}
		} else {
			errs = append(errs, "key server: "+err.Error())
		}
	}
	if s.DRM.LicenseURL != "" && (s.DRM.Vendor == "" || s.DRM.Vendor == "clearkey") && len(want) > 0 {
		var kids []string
		for _, w := range want {
			b, _ := hex.DecodeString(w)
			kids = append(kids, base64.RawURLEncoding.EncodeToString(b))
		}
		body, _ := json.Marshal(map[string]any{"kids": kids, "type": "temporary"})
		if b, err := d.do(ctx, c, s, "POST", s.DRM.LicenseURL, body); err == nil {
			for k, v := range ParseKeys(b) {
				keys[k] = v
			}
		} else {
			errs = append(errs, "license: "+err.Error())
		}
	}
	if len(keys) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return keys, nil
}

func covered(keys map[string]string, want []string) bool {
	if len(keys) == 0 {
		return false
	}
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if _, ok := keys[w]; !ok {
			return false
		}
	}
	return true
}

func (d *Service) do(ctx context.Context, c *http.Client, s *models.Stream, method, u string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	d.headers(req, s)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}
