package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gpupanel/internal/models"
	"gpupanel/internal/srtin"
	"gpupanel/internal/store"
)

func (s *Server) srtRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/srtin/status", s.authed(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		if s.SRT == nil {
			ok(w, []any{})
			return
		}
		ok(w, s.SRT.Status())
	}))
	// bulk: {"from_port":10001,"count":34,"output":"udp://239.200.0.{n}:1234","start":1,"latency":1000,"prefix":"hot"}
	m.HandleFunc("POST /api/srtin/bulk", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		var q struct {
			FromPort int    `json:"from_port"`
			Count    int    `json:"count"`
			Output   string `json:"output"`
			Start    int    `json:"start"`
			Latency  int    `json:"latency"`
			Prefix   string `json:"prefix"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&q) != nil || q.FromPort <= 0 || q.Count <= 0 || q.Count > 1000 || !strings.Contains(q.Output, "{n}") {
			fail(w, 400, "need from_port, count (1-1000) and output containing {n}")
			return
		}
		var l []models.SRTIngest
		for i := 0; i < q.Count; i++ {
			n := q.Start + i
			l = append(l, models.SRTIngest{Name: fmt.Sprintf("%s%d", q.Prefix, n), Port: q.FromPort + i, Latency: q.Latency,
				Output: strings.ReplaceAll(q.Output, "{n}", fmt.Sprint(n))})
		}
		ok(w, map[string]any{"added": s.addSRT(l)})
	}))
	// import: {"text":"srt-live-transmit srt://@10005?latency=1000 udp://239.200.0.5:1234\n..."}; empty text scans /etc/init.d/iptv
	m.HandleFunc("POST /api/srtin/import", s.admin(func(w http.ResponseWriter, r *http.Request, _ *models.Account) {
		var q struct {
			Text     string `json:"text"`
			Disabled bool   `json:"disabled"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&q)
		names := map[int]string{}
		if strings.TrimSpace(q.Text) == "" {
			q.Text, names = srtin.ScanInitScripts("/etc/init.d/iptv")
		}
		l := srtin.Parse(q.Text, "srt")
		for i := range l {
			if n := names[l[i].Port]; n != "" {
				l[i].Name = n
			}
			l[i].Disabled = q.Disabled
		}
		ok(w, map[string]any{"found": len(l), "added": s.addSRT(l)})
	}))
}

// addSRT stores entries whose port is not used yet and returns how many were added.
func (s *Server) addSRT(l []models.SRTIngest) int {
	used := map[int]bool{}
	for _, x := range store.List[models.SRTIngest](s.St, models.KSRTIn) {
		used[x.Port] = true
	}
	n := 0
	for _, e := range l {
		if used[e.Port] {
			continue
		}
		used[e.Port] = true
		e.ID = s.St.NextID(models.KSRTIn)
		if s.St.Put(models.KSRTIn, e.ID, e) == nil {
			n++
		}
	}
	if s.SRT != nil {
		s.SRT.Sync()
	}
	return n
}
