// Package sysmon reads host metrics straight from /proc with no external tools.
package sysmon

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Stats struct {
	TS        int64   `json:"ts"`
	Hostname  string  `json:"hostname"`
	OS        string  `json:"os"`
	Kernel    string  `json:"kernel"`
	CPUModel  string  `json:"cpu_model"`
	Cores     int     `json:"cores"`
	CPU       float64 `json:"cpu"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	MemTotal  uint64  `json:"mem_total"`
	MemUsed   uint64  `json:"mem_used"`
	SwapTotal uint64  `json:"swap_total"`
	SwapUsed  uint64  `json:"swap_used"`
	DiskTotal uint64  `json:"disk_total"`
	DiskUsed  uint64  `json:"disk_used"`
	ShmTotal  uint64  `json:"shm_total"`
	ShmUsed   uint64  `json:"shm_used"`
	NetRx     float64 `json:"net_rx"` // bits/s
	NetTx     float64 `json:"net_tx"`
	Uptime    int64   `json:"uptime"`
	PanelRSS  uint64  `json:"panel_rss"`
	PanelCPU  float64 `json:"panel_cpu"`
	Conns     int     `json:"conns"`
}

type Monitor struct {
	mu        sync.RWMutex
	last      Stats
	prevIdle  uint64
	prevTotal uint64
	prevRx    uint64
	prevTx    uint64
	prevSelf  uint64
	prevT     time.Time
	static    Stats
}

func New() *Monitor {
	m := &Monitor{}
	h, _ := os.Hostname()
	m.static.Hostname = h
	m.static.Cores = runtime.NumCPU()
	m.static.OS = osName()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		m.static.Kernel = strings.TrimSpace(string(b))
	}
	m.static.CPUModel = cpuModel()
	m.sample()
	return m
}

func (m *Monitor) Run(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.sample()
		}
	}
}

func (m *Monitor) Get() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last
}

func (m *Monitor) sample() {
	s := m.static
	now := time.Now()
	s.TS = now.Unix()
	idle, total := cpuTimes()
	rx, tx := netBytes()
	self := selfTicks()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.prevT.IsZero() {
		dt := now.Sub(m.prevT).Seconds()
		if dT := total - m.prevTotal; dT > 0 {
			s.CPU = 100 * float64(dT-(idle-m.prevIdle)) / float64(dT)
		}
		if dt > 0 {
			s.NetRx = float64(rx-m.prevRx) * 8 / dt
			s.NetTx = float64(tx-m.prevTx) * 8 / dt
			s.PanelCPU = float64(self-m.prevSelf) / 100 / dt * 100 / float64(s.Cores)
		}
	}
	m.prevIdle, m.prevTotal, m.prevRx, m.prevTx, m.prevSelf, m.prevT = idle, total, rx, tx, self, now
	mi := meminfo()
	s.MemTotal = mi["MemTotal"]
	s.MemUsed = mi["MemTotal"] - mi["MemAvailable"]
	s.SwapTotal = mi["SwapTotal"]
	s.SwapUsed = mi["SwapTotal"] - mi["SwapFree"]
	s.DiskTotal, s.DiskUsed = disk("/")
	s.ShmTotal, s.ShmUsed = disk("/dev/shm")
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
			s.Load5, _ = strconv.ParseFloat(f[1], 64)
			s.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			s.Uptime = int64(v)
		}
	}
	s.PanelRSS = selfRSS()
	s.Conns = m.last.Conns
	m.last = s
}

func (m *Monitor) SetConns(n int) {
	m.mu.Lock()
	m.last.Conns = n
	m.mu.Unlock()
}

func cpuTimes() (idle, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fs := strings.Fields(sc.Text())
		for i, v := range fs[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			total += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
	}
	return
}

func netBytes() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n")[2:] {
		i := strings.IndexByte(l, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(l[:i])
		if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
			continue
		}
		f := strings.Fields(l[i+1:])
		if len(f) >= 9 {
			r, _ := strconv.ParseUint(f[0], 10, 64)
			t, _ := strconv.ParseUint(f[8], 10, 64)
			rx += r
			tx += t
		}
	}
	return
}

func meminfo() map[string]uint64 {
	m := map[string]uint64{}
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			v, _ := strconv.ParseUint(f[1], 10, 64)
			m[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	return m
}

func disk(p string) (total, used uint64) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return
	}
	total = st.Blocks * uint64(st.Bsize)
	used = total - st.Bavail*uint64(st.Bsize)
	return
}

func selfTicks() uint64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+2:])
	if len(f) < 13 {
		return 0
	}
	u, _ := strconv.ParseUint(f[11], 10, 64)
	k, _ := strconv.ParseUint(f[12], 10, 64)
	return u + k
}

func selfRSS() uint64 {
	b, _ := os.ReadFile("/proc/self/statm")
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(f[1], 10, 64)
	return v * uint64(os.Getpagesize())
}

// ProcStat returns rss bytes and total cpu ticks for a pid.
func ProcStat(pid int) (rss uint64, ticks uint64) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+2:])
	if len(f) < 22 {
		return
	}
	u, _ := strconv.ParseUint(f[11], 10, 64)
	k, _ := strconv.ParseUint(f[12], 10, 64)
	r, _ := strconv.ParseUint(f[21], 10, 64)
	return r * uint64(os.Getpagesize()), u + k
}

// ProcWritten returns the bytes a process has written (files, pipes and sockets).
func ProcWritten(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "wchar: "); ok {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

func osName() string {
	b, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "PRETTY_NAME=") {
			return strings.Trim(l[12:], `"`)
		}
	}
	return runtime.GOOS
}

func cpuModel() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "model name") {
			if i := strings.IndexByte(l, ':'); i > 0 {
				return strings.TrimSpace(l[i+1:])
			}
		}
	}
	return ""
}
