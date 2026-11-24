package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gpupanel/internal/gpu"
	"gpupanel/internal/lb"
	"gpupanel/internal/models"
	"gpupanel/internal/store"
	"gpupanel/internal/sysmon"
)

type State string

const (
	Stopped    State = "stopped"
	Starting   State = "starting"
	Running    State = "running"
	Restarting State = "restarting"
	Failed     State = "error"
	Waiting    State = "waiting" // on-demand / scheduled
)

type Status struct {
	ID        int64     `json:"id"`
	State     State     `json:"state"`
	PID       int       `json:"pid"`
	Since     int64     `json:"since"`
	Uptime    int64     `json:"uptime"`
	Input     int       `json:"input"`
	InputURL  string    `json:"input_url"`
	GPU       int       `json:"gpu"`
	FPS       float64   `json:"fps"`
	Speed     float64   `json:"speed"`
	Bitrate   float64   `json:"bitrate"` // kbit/s
	Frames    int64     `json:"frames"`
	Drops     int64     `json:"drops"`
	Dups      int64     `json:"dups"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"last_error"`
	Viewers   int       `json:"viewers"`
	CPU       float64   `json:"cpu"`
	RSS       uint64    `json:"rss"`
	Variants  []Variant `json:"variants,omitempty"`
	Video     string    `json:"video"`
	Audio     string    `json:"audio"`
	Black     bool      `json:"black"`
	Silent    bool      `json:"silent"`
}

type proc struct {
	mu       sync.Mutex
	st       Status
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	want     bool
	hub      *Hub
	last     time.Time // last progress
	lowSince time.Time
	ticks    uint64
	tickT    time.Time
	done     chan struct{}
	manual   int // forced input index (-1 none)
}

type Hooks struct {
	OnState func(id int64, name string, from, to State, msg string)
}

type Manager struct {
	st      *store.Store
	gm      *gpu.Monitor
	mu      sync.Mutex
	procs   map[int64]*proc
	Hooks   Hooks
	ffVer   int
	Gateway string
	PrepDRM func(ctx context.Context, s *models.Stream, input, root string) (*BuildOpts, error)
	Relay   func(s *models.Stream) string // returns local http proxy url when stream needs proxy/bind/dns
}

func NewManager(st *store.Store, gm *gpu.Monitor) *Manager {
	m := &Manager{st: st, gm: gm, procs: map[int64]*proc{}}
	m.ffVer = FFmpegMajor(m.settings().FFmpegPath)
	return m
}

func (m *Manager) settings() models.Settings {
	s := models.DefaultSettings()
	_ = m.st.Get(models.KSettings, 1, &s)
	return s
}

func (m *Manager) FFVersion() int { return m.ffVer }

var verRe = regexp.MustCompile(`ffmpeg version n?(\d+)`)

func FFmpegMajor(bin string) int {
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		return 4
	}
	if mm := verRe.FindSubmatch(out); mm != nil {
		v, _ := strconv.Atoi(string(mm[1]))
		return v
	}
	return 7 // git builds
}

func (m *Manager) get(id int64) *proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[id]
	if p == nil {
		p = &proc{st: Status{ID: id, State: Stopped, GPU: -1}, hub: NewHub(), manual: -1}
		m.procs[id] = p
	}
	return p
}

func (m *Manager) Hub(id int64) *Hub { return m.get(id).hub }

func (m *Manager) Status(id int64) Status {
	p := m.get(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.st
	if s.State == Running {
		s.Uptime = time.Now().Unix() - s.Since
	}
	s.Viewers = p.hub.Count() + viewerCount(id)
	return s
}

func (m *Manager) All() []Status {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.procs))
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Status, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.Status(id))
	}
	return out
}

// GPUAssigned counts running streams per GPU index.
func (m *Manager) GPUAssigned() map[int]int {
	r := map[int]int{}
	for _, s := range m.All() {
		if s.State == Running || s.State == Starting {
			r[s.GPU]++
		}
	}
	return r
}

func (m *Manager) Running(id int64) bool {
	p := m.get(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.want
}

func (m *Manager) Start(id int64) error {
	var s models.Stream
	if err := m.st.Get(models.KStream, id, &s); err != nil {
		return err
	}
	p := m.get(id)
	p.mu.Lock()
	if p.want {
		p.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.want, p.cancel, p.done = true, cancel, make(chan struct{})
	p.st.Restarts = 0
	p.mu.Unlock()
	go m.loop(ctx, id, p)
	return nil
}

func (m *Manager) Stop(id int64) {
	p := m.get(id)
	p.mu.Lock()
	if !p.want {
		p.mu.Unlock()
		return
	}
	p.want = false
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

func (m *Manager) Restart(id int64) error {
	m.Stop(id)
	return m.Start(id)
}

// SwitchInput forces a specific source index and restarts.
func (m *Manager) SwitchInput(id int64, idx int) error {
	p := m.get(id)
	p.mu.Lock()
	p.manual = idx
	p.mu.Unlock()
	return m.Restart(id)
}

func (m *Manager) StopAll() {
	var wg sync.WaitGroup
	for _, s := range m.All() {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); m.Stop(id) }(s.ID)
	}
	wg.Wait()
}

func (m *Manager) Forget(id int64) {
	m.Stop(id)
	m.mu.Lock()
	delete(m.procs, id)
	m.mu.Unlock()
	os.RemoveAll(filepath.Join(m.settings().HLSDir, strconv.FormatInt(id, 10)))
}

func (m *Manager) setState(id int64, name string, p *proc, to State, msg string) {
	p.mu.Lock()
	from := p.st.State
	p.st.State = to
	if msg != "" {
		p.st.LastError = msg
	}
	if to == Running {
		p.st.Since = time.Now().Unix()
	}
	p.mu.Unlock()
	if from != to && m.Hooks.OnState != nil {
		m.Hooks.OnState(id, name, from, to, msg)
	}
}

func (m *Manager) loop(ctx context.Context, id int64, p *proc) {
	defer close(p.done)
	fails := 0
	for {
		var s models.Stream
		if err := m.st.Get(models.KStream, id, &s); err != nil {
			m.setState(id, "", p, Stopped, "stream deleted")
			return
		}
		st := m.settings()
		if s.ProfileID > 0 {
			var pr models.Profile
			if m.st.Get(models.KProfile, s.ProfileID, &pr) == nil {
				s.Transcode = MergeProfile(pr.Transcode, s.Transcode)
				if pr.GPU != nil && *pr.GPU != -3 && s.GPU == -2 {
					s.GPU = *pr.GPU
				}
				if s.ServerID == 0 {
					s.ServerID = pr.ServerID
				}
			}
		}
		if len(s.Inputs) == 0 && !s.DRM.Enabled {
			m.setState(id, s.Name, p, Failed, "no source URL")
			return
		}
		// pick input
		p.mu.Lock()
		idx := p.st.Input
		if p.manual >= 0 {
			idx, p.manual = p.manual, -1
		}
		p.mu.Unlock()
		if len(s.Inputs) > 0 {
			idx = ((idx % len(s.Inputs)) + len(s.Inputs)) % len(s.Inputs)
		}
		// pick gpu
		g := m.pickGPU(&s, st)
		if g == -2 {
			m.setState(id, s.Name, p, Waiting, "no GPU capacity (load balancer)")
			if !sleepCtx(ctx, 10*time.Second) {
				m.setState(id, s.Name, p, Stopped, "")
				return
			}
			continue
		}
		root := filepath.Join(st.HLSDir, strconv.FormatInt(id, 10))
		os.RemoveAll(root)
		os.MkdirAll(root, 0o755)
		opts := BuildOpts{GPU: g, HLSRoot: root, HTTPPipe: true, FFmpegVer: m.ffVer, Gateway: m.Gateway}
		if len(s.Inputs) > 0 {
			opts.Input = s.Inputs[idx]
		}
		runCtx, runCancel := context.WithCancel(ctx)
		if s.DRM.Enabled && m.PrepDRM != nil {
			o2, err := m.PrepDRM(runCtx, &s, opts.Input, root)
			if err != nil {
				runCancel()
				m.st.Log("stream", id, "error", "DRM: "+err.Error())
				m.setState(id, s.Name, p, Restarting, "DRM: "+err.Error())
				fails++
				if !sleepCtx(ctx, backoff(st.RestartDelay, fails)) {
					m.setState(id, s.Name, p, Stopped, "")
					return
				}
				continue
			}
			opts.Input, opts.Inputs2, opts.InputKeys = o2.Input, o2.Inputs2, o2.InputKeys
			opts.CENC, opts.AudioInput, opts.Proxy = o2.CENC, o2.AudioInput, o2.Proxy
		} else if m.Relay != nil {
			opts.Proxy = m.Relay(&s)
		}
		for _, out := range s.Outputs {
			n := out.Name
			if n == "" {
				continue
			}
		}
		args, variants, err := Build(&s, &st, opts)
		if err != nil {
			runCancel()
			m.setState(id, s.Name, p, Failed, err.Error())
			p.mu.Lock()
			p.want = false
			p.mu.Unlock()
			return
		}
		for _, v := range variants {
			os.MkdirAll(filepath.Join(root, v.Dir), 0o755)
		}
		for oi, o := range s.Outputs {
			if o.Type == "dash" || (o.Type == "hls" && !o.Disabled) {
				os.MkdirAll(filepath.Join(root, def(o.Name, "o"+strconv.Itoa(oi))), 0o755)
			}
		}
		writeMaster(root, variants)
		p.mu.Lock()
		p.st.Input, p.st.InputURL, p.st.GPU, p.st.Variants = idx, redact(opts.Input), g, variants
		p.st.FPS, p.st.Speed, p.st.Bitrate, p.st.Frames = 0, 0, 0, 0
		p.mu.Unlock()
		m.setState(id, s.Name, p, Starting, "")
		m.st.Log("stream", id, "info", fmt.Sprintf("start input #%d gpu=%d", idx+1, g))
		started := time.Now()
		reason := m.run(runCtx, id, &s, &st, p, args)
		runCancel()
		if ctx.Err() != nil {
			m.setState(id, s.Name, p, Stopped, "")
			p.hub.Reset()
			return
		}
		p.mu.Lock()
		p.st.Restarts++
		p.mu.Unlock()
		m.st.Log("stream", id, "warn", "exit: "+reason)
		if time.Since(started) < 30*time.Second {
			fails++
		} else {
			fails = 0
		}
		// fallback: move to next source after a failure
		if len(s.Inputs) > 1 && (fails > 0 || strings.Contains(reason, "stall")) {
			p.mu.Lock()
			p.st.Input = (idx + 1) % len(s.Inputs)
			p.mu.Unlock()
			m.st.Log("stream", id, "warn", fmt.Sprintf("switch to source #%d", (idx+1)%len(s.Inputs)+1))
		}
		m.setState(id, s.Name, p, Restarting, reason)
		if !sleepCtx(ctx, backoff(st.RestartDelay, fails)) {
			m.setState(id, s.Name, p, Stopped, "")
			return
		}
	}
}

func backoff(base, fails int) time.Duration {
	if base <= 0 {
		base = 3
	}
	d := time.Duration(base) * time.Second
	for i := 1; i < fails && d < 60*time.Second; i++ {
		d *= 2
	}
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (m *Manager) pickGPU(s *models.Stream, st models.Settings) int {
	t := s.Transcode
	if isCopy(&t) || t.HWAccel == "" || t.HWAccel == "none" || t.HWAccel == "qsv" {
		return -1
	}
	if s.GPU >= 0 {
		return s.GPU
	}
	info := m.gm.Get()
	if !info.Available {
		if st.LBCPUFallback {
			return -1
		}
		return -2
	}
	l := lb.Limits{Strategy: st.LBMode, MaxEnc: float64(st.LBMaxGPUEnc), MaxMem: float64(st.LBMaxGPUMem), MaxTemp: float64(st.AlertGPUTemp + 2), CPUFallback: st.LBCPUFallback}
	if s.BalancerID > 0 {
		var b models.Balancer
		if m.st.Get(models.KBalancer, s.BalancerID, &b) == nil && b.Enabled {
			l = lb.Limits{Strategy: b.Strategy, MaxEnc: float64(b.MaxEnc), MaxMem: float64(b.MaxMem), MaxTemp: float64(b.MaxTemp), MaxPerGPU: b.MaxPerGPU, CPUFallback: b.CPUFallbk}
		}
	}
	return lb.PickGPU(info.Cards, m.GPUAssigned(), l)
}

var progRe = regexp.MustCompile(`^(\w+)=(.*)$`)

func (m *Manager) run(ctx context.Context, id int64, s *models.Stream, st *models.Settings, p *proc, args []string) string {
	cmd := exec.Command(st.FFmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	pr, pw, err := os.Pipe()
	if err != nil {
		return err.Error()
	}
	cmd.ExtraFiles = []*os.File{pw}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "spawn: " + err.Error()
	}
	pw.Close()
	go p.hub.Pump(pr)
	p.mu.Lock()
	p.cmd = cmd
	p.st.PID = cmd.Process.Pid
	p.last = time.Now()
	p.lowSince = time.Time{}
	p.mu.Unlock()

	errLines := make(chan string, 64)
	var tail []string
	var tmu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := sc.Text()
			tmu.Lock()
			tail = append(tail, l)
			if len(tail) > 30 {
				tail = tail[len(tail)-30:]
			}
			tmu.Unlock()
			select {
			case errLines <- l:
			default:
			}
		}
	}()
	go func() {
		seen, n, drop, win := map[string]bool{}, 0, 0, time.Now()
		for l := range errLines {
			if isNoise(l) {
				continue
			}
			if time.Since(win) > time.Minute {
				if drop > 0 {
					m.st.Log("stream", id, "ffmpeg", fmt.Sprintf("… %d repeated lines suppressed", drop))
				}
				clear(seen)
				n, drop, win = 0, 0, time.Now()
			}
			k := hexAddr.ReplaceAllString(l, "")
			if seen[k] || n >= 30 {
				drop++
				continue
			}
			seen[k] = true
			n++
			m.st.Log("stream", id, "ffmpeg", redact(l))
		}
	}()
	go func() {
		sc := bufio.NewScanner(stdout)
		running := false
		var wrote uint64
		var wroteAt time.Time
		for sc.Scan() {
			mm := progRe.FindStringSubmatch(sc.Text())
			if mm == nil {
				continue
			}
			k, v := mm[1], strings.TrimSpace(mm[2])
			p.mu.Lock()
			switch k {
			case "frame":
				n, _ := strconv.ParseInt(v, 10, 64)
				if n > p.st.Frames {
					p.last = time.Now()
				}
				p.st.Frames = n
			case "fps":
				p.st.FPS, _ = strconv.ParseFloat(v, 64)
			case "total_size", "out_time_us":
				if isCopy(&s.Transcode) {
					p.last = time.Now()
				}
			case "drop_frames":
				p.st.Drops, _ = strconv.ParseInt(v, 10, 64)
			case "dup_frames":
				p.st.Dups, _ = strconv.ParseInt(v, 10, 64)
			case "speed":
				p.st.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(v, "x"), 64)
			case "progress":
				if w := sysmon.ProcWritten(cmd.Process.Pid); w > 0 {
					if !wroteAt.IsZero() && w >= wrote {
						if dt := time.Since(wroteAt).Seconds(); dt > 0 {
							p.st.Bitrate = float64(w-wrote) * 8 / 1000 / dt
						}
					}
					wrote, wroteAt = w, time.Now()
				}
				if !running {
					running = true
					p.mu.Unlock()
					m.setState(id, s.Name, p, Running, "")
					p.mu.Lock()
				}
			}
			p.mu.Unlock()
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(errLines) }()

	timeout := time.Duration(max(st.StreamTimeout, 10)) * time.Second
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	kill := func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(4 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	}
	tailStr := func() string {
		tmu.Lock()
		defer tmu.Unlock()
		for i := len(tail) - 1; i >= 0; i-- {
			if !isNoise(tail[i]) && strings.TrimSpace(tail[i]) != "" {
				return redact(tail[i])
			}
		}
		return ""
	}
	for {
		select {
		case <-ctx.Done():
			kill()
			return "stopped"
		case err := <-exited:
			p.mu.Lock()
			p.st.PID = 0
			p.mu.Unlock()
			msg := tailStr()
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return "ffmpeg exited: " + msg
		case <-tk.C:
			p.mu.Lock()
			stalled := time.Since(p.last) > timeout
			rss, ticks := sysmon.ProcStat(cmd.Process.Pid)
			if !p.tickT.IsZero() {
				dt := time.Since(p.tickT).Seconds()
				p.st.CPU = float64(ticks-p.ticks) / dt // percent of one core (100 ticks/s)
			}
			p.ticks, p.tickT, p.st.RSS = ticks, time.Now(), rss
			low := false
			if s.SpeedMin > 0 && p.st.State == Running && p.st.Speed > 0 && p.st.Speed < s.SpeedMin {
				if p.lowSince.IsZero() {
					p.lowSince = time.Now()
				}
				low = time.Since(p.lowSince) > 30*time.Second
			} else {
				p.lowSince = time.Time{}
			}
			memOver := s.MemMaxMB > 0 && rss > uint64(s.MemMaxMB)<<20
			p.mu.Unlock()
			if stalled {
				kill()
				return "stall: no frames for " + timeout.String()
			}
			if low {
				kill()
				return "speed below limit"
			}
			if memOver {
				kill()
				return "memory limit exceeded"
			}
			if s.Restart.Enabled && scheduled(s.Restart) {
				kill()
				return "scheduled restart"
			}
		}
	}
}

var lastSched sync.Map

func scheduled(r models.Restart) bool {
	now := time.Now()
	if len(r.Weekdays) > 0 && !containsInt(r.Weekdays, int(now.Weekday())) {
		return false
	}
	if !containsInt(r.Hours, now.Hour()) {
		return false
	}
	mins := r.Minutes
	if len(mins) == 0 {
		mins = []int{0}
	}
	if !containsInt(mins, now.Minute()) {
		return false
	}
	key := fmt.Sprintf("%p-%s", &r, now.Format("200601021504"))
	_, loaded := lastSched.LoadOrStore(key, true)
	return !loaded && now.Second() < 5
}

func containsInt(a []int, v int) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

func isNoise(l string) bool {
	for _, n := range []string{"Opening '", "frame=", "Last message repeated", "deprecated pixel format", "Application provided invalid, non monotonically", "non-existing PPS"} {
		if strings.Contains(l, n) {
			return true
		}
	}
	return false
}

var credRe = regexp.MustCompile(`(?i)(://[^:/@\s]+:)[^@\s]+@|((?:pass(?:word|phrase)?|token|key)=)[^&\s]+`)

func redact(s string) string {
	return credRe.ReplaceAllString(s, "$1***@$2***")
}

func writeMaster(root string, vs []Variant) {
	byOut := map[string][]Variant{}
	for _, v := range vs {
		if v.Master {
			byOut[v.Output] = append(byOut[v.Output], v)
		}
	}
	for out, list := range byOut {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
		for _, v := range list {
			bw := v.Bitrate * 1000
			if bw == 0 {
				bw = 3000000
			}
			res := ""
			if v.Size != "" && strings.Contains(v.Size, "x") {
				res = ",RESOLUTION=" + v.Size
			}
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d%s,NAME=\"%s\"\n%s/index.m3u8\n", bw+128000, res, v.Name, v.Dir)
		}
		os.MkdirAll(root, 0o755)
		os.WriteFile(filepath.Join(root, out+".m3u8"), []byte(b.String()), 0o644)
	}
}

var hexAddr = regexp.MustCompile(`0x[0-9a-fA-F]+`)

// MergeProfile applies a profile the way Xaccel does: everything a profile
// defines (encoders, preset, overlay, audio, subtitle encoder) comes from the
// profile; per-stream maps, delogo and advanced MPEG-TS/color options stay.
func MergeProfile(p, s models.Transcode) models.Transcode {
	r := s
	r.HWAccel, r.VideoSync, r.VideoEncoders = p.HWAccel, p.VideoSync, p.VideoEncoders
	r.Preset, r.Profile, r.Level, r.Tune = p.Preset, p.Profile, p.Level, p.Tune
	r.FrameRate, r.GOP, r.BFrames, r.Deinterlace, r.PixFmt, r.DAR = p.FrameRate, p.GOP, p.BFrames, p.Deinterlace, p.PixFmt, p.DAR
	r.OverlayFile, r.OverlayX, r.OverlayY, r.OverlayOpacity, r.OverlayScale = p.OverlayFile, p.OverlayX, p.OverlayY, p.OverlayOpacity, p.OverlayScale
	r.Text, r.TextX, r.TextY, r.TextSize, r.TextColor, r.TextAlpha, r.TextScroll = p.Text, p.TextX, p.TextY, p.TextSize, p.TextColor, p.TextAlpha, p.TextScroll
	r.AudioEncoders, r.AudioChannels, r.AudioRate, r.AudioVolume = p.AudioEncoders, p.AudioChannels, p.AudioRate, p.AudioVolume
	r.AudioNormalize, r.AudioDelay = p.AudioNormalize, p.AudioDelay
	r.SubMode = p.SubMode
	if p.SubFile != "" {
		r.SubFile = p.SubFile
	}
	if p.ExtraArgs != "" {
		r.ExtraArgs = p.ExtraArgs
	}
	return r
}

// viewer tracking for HLS (session based, counted by recent playlist hits)
var viewers sync.Map // id -> *sync.Map[key]time

func TouchViewer(id int64, key string) {
	v, _ := viewers.LoadOrStore(id, &sync.Map{})
	v.(*sync.Map).Store(key, time.Now())
}

func viewerCount(id int64) int {
	v, ok := viewers.Load(id)
	if !ok {
		return 0
	}
	n := 0
	v.(*sync.Map).Range(func(k, t any) bool {
		if time.Since(t.(time.Time)) < 30*time.Second {
			n++
		} else {
			v.(*sync.Map).Delete(k)
		}
		return true
	})
	return n
}

// LastSegment returns the newest .ts file of a stream (used for thumbnails).
func LastSegment(root string) string {
	var best string
	var bt time.Time
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".ts") && fi.ModTime().After(bt) && fi.Size() > 0 {
			best, bt = p, fi.ModTime()
		}
		return nil
	})
	return best
}

var _ = io.EOF

var demand sync.Map // id -> time of last viewer activity

func TouchDemand(id int64) { demand.Store(id, time.Now()) }

// StopIdle stops on-demand streams nobody watched for idle.
func (m *Manager) StopIdle(idle time.Duration) {
	for _, s := range m.All() {
		if !m.Running(s.ID) {
			continue
		}
		var x models.Stream
		if m.st.Get(models.KStream, s.ID, &x) != nil || !x.OnDemand || x.Enabled {
			continue
		}
		t, ok := demand.Load(s.ID)
		if (!ok || time.Since(t.(time.Time)) > idle) && s.Viewers == 0 {
			m.st.Log("stream", s.ID, "info", "on-demand idle, stopping")
			go m.Stop(s.ID)
		}
	}
}
