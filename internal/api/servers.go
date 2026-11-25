package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"gpupanel/internal/models"
	"gpupanel/internal/store"
)

type serverRow struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Master   bool    `json:"master"`
	Host     string  `json:"host"`
	Online   bool    `json:"online"`
	Uptime   int64   `json:"uptime"`
	Up       float64 `json:"up"`
	Down     float64 `json:"down"`
	Streams  int     `json:"streams"`
	Total    int     `json:"total"`
	Conns    int     `json:"connections"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	Version  string  `json:"version"`
	License  string  `json:"license"`
	Error    string  `json:"error,omitempty"`
	Disabled bool    `json:"disabled"`
}

type dashInfo struct {
	Version string `json:"version"`
	Sys     struct {
		Uptime int64   `json:"uptime"`
		NetRx  float64 `json:"net_rx"`
		NetTx  float64 `json:"net_tx"`
		CPU    float64 `json:"cpu"`
		MemT   float64 `json:"mem_total"`
		MemU   float64 `json:"mem_used"`
	} `json:"sys"`
	Streams map[string]int `json:"streams"`
	Viewers int            `json:"viewers"`
}

func (r *serverRow) fill(d dashInfo) {
	r.Online, r.Version, r.License = true, d.Version, "Active"
	r.Uptime, r.Up, r.Down, r.CPU = d.Sys.Uptime, d.Sys.NetTx, d.Sys.NetRx, d.Sys.CPU
	if d.Sys.MemT > 0 {
		r.Mem = d.Sys.MemU * 100 / d.Sys.MemT
	}
	r.Streams, r.Total, r.Conns = d.Streams["running"], d.Streams["total"], d.Viewers
}

func remoteBase(x *models.Server) string {
	p := x.HTTPPort
	if p == 0 {
		p = 8080
	}
	return "http://" + x.Host + ":" + strconv.Itoa(p)
}

func remoteCall(ctx context.Context, x *models.Server, method, path string, out any) error {
	if x.Host == "" || x.Token == "" {
		return fmt.Errorf("host/token not set")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, remoteBase(x)+path, nil)
	req.Header.Set("X-API-Key", x.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// serverStats returns the Xaccel-style server table: the local master plus remote load balancers.
func (s *Server) serverStats(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var d dashInfo
	b, _ := json.Marshal(s.dashboard())
	json.Unmarshal(b, &d)
	m := serverRow{ID: 0, Name: "Master", Master: true, Host: "local"}
	m.fill(d)
	rows := []serverRow{m}
	ss := store.List[models.Server](s.St, models.KServer)
	rem := make([]serverRow, len(ss))
	var wg sync.WaitGroup
	for i := range ss {
		x := &ss[i]
		rem[i] = serverRow{ID: x.ID, Name: x.Name, Host: x.Host, Disabled: x.Disabled, License: "—"}
		if x.Disabled {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var d dashInfo
			if err := remoteCall(r.Context(), x, "GET", "/api/dashboard", &d); err != nil {
				rem[i].Error = err.Error()
				return
			}
			rem[i].fill(d)
		}(i)
	}
	wg.Wait()
	ok(w, append(rows, rem...))
}

func (s *Server) reboot() {
	s.St.Log("panel", 0, "warn", "server reboot requested from panel")
	go func() {
		time.Sleep(time.Second)
		if exec.Command("systemctl", "reboot").Run() != nil {
			exec.Command("reboot").Run()
		}
	}()
}

// serverAction restarts the panel or reboots the machine of a server (id 0 = this master).
func (s *Server) serverAction(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	act := r.PathValue("action")
	if act != "restart" && act != "reboot" {
		fail(w, 400, "unknown action")
		return
	}
	id := pathID(r)
	if id == 0 {
		ok(w, map[string]any{"ok": true})
		if act == "reboot" {
			s.reboot()
		} else {
			go func() { time.Sleep(500 * time.Millisecond); s.Updater.Restart() }()
		}
		return
	}
	var x models.Server
	if s.St.Get(models.KServer, id, &x) != nil {
		fail(w, 404, "server not found")
		return
	}
	if err := remoteCall(r.Context(), &x, "POST", "/api/system/"+act, nil); err != nil {
		fail(w, 502, x.Name+": "+err.Error())
		return
	}
	s.St.Log("panel", 0, "info", act+" sent to server "+x.Name)
	ok(w, map[string]any{"ok": true})
}
