package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"gpupanel/internal/api"
	"gpupanel/internal/drm"
	"gpupanel/internal/engine"
	"gpupanel/internal/gpu"
	"gpupanel/internal/models"
	"gpupanel/internal/srtin"
	"gpupanel/internal/store"
	"gpupanel/internal/sysmon"
	"gpupanel/internal/version"
	"gpupanel/web"
)

func main() {
	dir := flag.String("dir", "/opt/mastergpu/data", "data directory")
	listen := flag.String("listen", "", "listen address (default :<http_port> or :8080)")
	showVer := flag.Bool("version", false, "print version")
	rollback := flag.Bool("rollback", false, "restore previous binary and exit")
	resetAdmin := flag.Bool("reset-admin", false, "reset admin login to admin/admin and exit")
	flag.Parse()
	if *showVer {
		fmt.Printf("%s %s (%s)\n", version.Name, version.Version, version.Build)
		return
	}
	bin, _ := os.Executable()
	bin, _ = filepath.EvalSymlinks(bin)
	if *rollback {
		if err := api.Rollback(bin); err != nil {
			log.Fatal(err)
		}
		fmt.Println("rolled back")
		return
	}
	os.MkdirAll(*dir, 0o755)
	// systemd gives the service no HOME, so CUDA could not keep its JIT cache and
	// every ffmpeg start spent ~30s recompiling the scale_cuda kernels
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", *dir)
	}
	if os.Getenv("CUDA_CACHE_PATH") == "" {
		os.Setenv("CUDA_CACHE_PATH", filepath.Join(*dir, "cuda-cache"))
	}
	if os.Getenv("CUDA_CACHE_MAXSIZE") == "" {
		os.Setenv("CUDA_CACHE_MAXSIZE", "1073741824")
	}
	if stage := filepath.Join(*dir, "restore.db"); fileExists(stage) {
		db := filepath.Join(*dir, "panel.db")
		os.Rename(db, db+".before-restore")
		os.Remove(db + "-wal")
		os.Remove(db + "-shm")
		os.Rename(stage, db)
	}
	st, err := store.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	auth := api.NewAuth(st)
	if *resetAdmin {
		auth.ResetAdmin("admin", "admin")
		fmt.Println("admin login reset to admin/admin")
		return
	}
	created := auth.EnsureAdmin("admin", "admin")

	gm := gpu.New()
	set := models.DefaultSettings()
	_ = st.Get(models.KSettings, 1, &set)
	if !set.FFmpegNoBundle {
		if err := ensureFFmpegBundle(); err != nil {
			log.Printf("ffmpeg bundle: %v", err)
		}
	}
	if p := pickFFmpeg(set.FFmpegPath, gm); p != "" && p != set.FFmpegPath {
		set.FFmpegPath = p
		if pr := filepath.Join(filepath.Dir(p), "ffprobe"); fileExists(pr) {
			set.FFprobePath = pr
		}
		log.Printf("ffmpeg: %s", p)
	}
	st.Put(models.KSettings, 1, set)

	stop := make(chan struct{})
	sm := sysmon.New()
	go sm.Run(2*time.Second, stop)
	go gm.Run(2*time.Second, stop)
	mgr := engine.NewManager(st, gm)
	if gw := findGateway(*dir, bin); gw != "" {
		mgr.Gateway = "127.0.0.1"
		go runGateway(gw, *dir, stop)
	}

	addr := *listen
	if addr == "" {
		p := set.HTTPPort
		if p == 0 {
			p = 8080
		}
		addr = fmt.Sprintf(":%d", p)
	}
	if !set.GPUFreeClocks {
		go func() {
			for _, m := range gm.LockClocks() {
				st.Log("panel", 0, "info", m)
			}
		}()
	}
	ds := &drm.Service{St: st, LocalBase: "http://" + loopback(addr), FFmpegVer: mgr.FFVersion}
	mgr.PrepDRM, mgr.Relay = ds.Prep, ds.Relay

	srtm := srtin.New(st)
	go srtm.Run(stop)
	srv := api.New(&api.Server{SRT: srtm, St: st, Auth: auth, Mgr: mgr, Sys: sm, GPU: gm, Web: web.FS(), DRM: ds,
		Updater: &api.Updater{Bin: bin, Before: mgr.StopAll}})
	mgr.Hooks.OnState = func(id int64, name string, from, to engine.State, msg string) {
		if to == engine.Failed && from == engine.Running {
			go srv.Alert(fmt.Sprintf("Stream %s (#%d) down: %s", name, id, msg))
		}
	}
	if created {
		st.Log("panel", 0, "info", "first start, login admin/admin")
	}
	st.Log("panel", 0, "info", fmt.Sprintf("%s %s started", version.Name, version.Version))

	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		log.Printf("%s %s listening on %s", version.Name, version.Version, addr)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	go func() {
		time.Sleep(time.Duration(set.StartupDelay) * time.Second)
		ss := store.List[models.Stream](st, models.KStream)
		sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
		for _, s := range ss {
			if s.Enabled && !s.OnDemand {
				mgr.Start(s.ID)
				time.Sleep(time.Duration(set.StartInterval * float64(time.Second)))
			}
		}
	}()
	go housekeeping(st, srv, mgr, sm, gm, stop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(ctx)
	mgr.StopAll()
	st.Close()
}

func housekeeping(st *store.Store, srv *api.Server, mgr *engine.Manager, sm *sysmon.Monitor, gm *gpu.Monitor, stop chan struct{}) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	lastBackup, lastMetric, hot := "", time.Time{}, map[int]bool{}
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			set := models.DefaultSettings()
			_ = st.Get(models.KSettings, 1, &set)
			mgr.StopIdle(90 * time.Second)
			if now.Sub(lastMetric) >= time.Minute {
				lastMetric = now
				st.AddMetric(0, map[string]any{"t": now.Unix(), "sys": sm.Get(), "gpu": gm.Get().Cards})
			}
			for _, c := range gm.Get().Cards {
				h := set.AlertGPUTemp > 0 && int(c.Temp) >= set.AlertGPUTemp
				if h && !hot[c.Index] {
					go srv.Alert(fmt.Sprintf("GPU %d (%s) temperature %.0f°C", c.Index, c.Name, c.Temp))
				}
				hot[c.Index] = h
			}
			day := now.Format("2006-01-02")
			if now.Hour() == set.BackupHour && lastBackup != day {
				lastBackup = day
				if !set.BackupOff && set.BackupEvery <= 0 {
					autoBackup(st, set)
				}
				st.Prune(20000, 30)
			}
			if !set.BackupOff && set.BackupEvery > 0 {
				if t := newestBackup(st); now.Sub(t) >= time.Duration(set.BackupEvery)*time.Hour {
					autoBackup(st, set)
				}
			}
		}
	}
}

func autoBackup(st *store.Store, set models.Settings) {
	if _, err := st.Backup(); err != nil {
		st.Log("panel", 0, "error", "auto backup: "+err.Error())
		return
	}
	pruneBackups(filepath.Join(st.Dir(), "backup"), set.BackupKeep)
}

func newestBackup(st *store.Store) (t time.Time) {
	m, _ := filepath.Glob(filepath.Join(st.Dir(), "backup", "panel-*.db"))
	for _, f := range m {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return
}

func pruneBackups(dir string, keep int) {
	m, _ := filepath.Glob(filepath.Join(dir, "panel-*.db"))
	sort.Strings(m)
	for len(m) > keep && keep > 0 {
		os.Remove(m[0])
		m = m[1:]
	}
}

// pickFFmpeg chooses the newest ffmpeg that the installed NVIDIA driver can drive.
func pickFFmpeg(cur string, gm *gpu.Monitor) string {
	info := gm.Get()
	c, _ := filepath.Glob("/opt/mastergpu/ffmpeg/*/ffmpeg")
	c = append(c, cur, "/usr/bin/ffmpeg", "/usr/local/bin/ffmpeg")
	return api.FFmpegForDriver(info.Driver, c)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// loopback turns a listen address such as ":8080" or "0.0.0.0:8080" into "127.0.0.1:8080".
func loopback(addr string) string {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1:8080"
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "127.0.0.1"
	}
	return net.JoinHostPort(h, p)
}
