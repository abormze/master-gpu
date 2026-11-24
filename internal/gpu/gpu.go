// Package gpu polls NVIDIA cards through nvidia-smi (works on every driver
// branch from 390 up, GTX/RTX/Quadro/Tesla/datacenter) and exposes a cached view.
package gpu

import (
	"bufio"
	"bytes"
	"context"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Card struct {
	Index     int      `json:"index"`
	UUID      string   `json:"uuid"`
	Name      string   `json:"name"`
	Bus       string   `json:"bus"`
	Driver    string   `json:"driver"`
	VBIOS     string   `json:"vbios"`
	Util      float64  `json:"util"`
	Enc       float64  `json:"enc"`
	Dec       float64  `json:"dec"`
	MemTotal  float64  `json:"mem_total"` // MiB
	MemUsed   float64  `json:"mem_used"`
	Temp      float64  `json:"temp"`
	Fan       float64  `json:"fan"`
	Power     float64  `json:"power"`
	PowerMax  float64  `json:"power_max"`
	ClockGr   float64  `json:"clock_gr"`
	ClockMem  float64  `json:"clock_mem"`
	PState    string   `json:"pstate"`
	EncSess   int      `json:"enc_sessions"`
	EncFPS    float64  `json:"enc_fps"`
	PCIeGen   string   `json:"pcie_gen"`
	PCIeWidth string   `json:"pcie_width"`
	Procs     []Proc   `json:"procs"`
	Arch      string   `json:"arch"`
	NVENCGen  string   `json:"nvenc_gen"`
	Codecs    []string `json:"codecs"`
	SessLimit int      `json:"session_limit"` // 0 = unlimited (patched/pro)
}

type Proc struct {
	PID int     `json:"pid"`
	Mem float64 `json:"mem"`
}

type Info struct {
	Available bool   `json:"available"`
	Driver    string `json:"driver"`
	CUDA      string `json:"cuda"`
	Patched   bool   `json:"patched"`
	Error     string `json:"error,omitempty"`
	Cards     []Card `json:"cards"`
}

type Monitor struct {
	mu   sync.RWMutex
	info Info
	smi  string
	fake int
}

func New() *Monitor {
	m := &Monitor{smi: "nvidia-smi"}
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		m.smi = p
	}
	m.fake, _ = strconv.Atoi(os.Getenv("MGPU_FAKE_GPU"))
	m.poll()
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
			m.poll()
		}
	}
}

func (m *Monitor) Get() Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.info
}

// Fake reports whether simulated GPUs are used (local development only).
func (m *Monitor) Fake() bool { return m.fake > 0 }

const query = "index,uuid,name,pci.bus_id,driver_version,vbios_version,utilization.gpu,utilization.encoder,utilization.decoder,memory.total,memory.used,temperature.gpu,fan.speed,power.draw,power.limit,clocks.gr,clocks.mem,pstate,encoder.stats.sessionCount,encoder.stats.averageFps,pcie.link.gen.current,pcie.link.width.current"

func (m *Monitor) poll() {
	if m.fake > 0 {
		m.setFake()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.smi, "--query-gpu="+query, "--format=csv,noheader,nounits").Output()
	info := Info{}
	if err != nil {
		info.Error = strings.TrimSpace(err.Error())
		m.mu.Lock()
		m.info = info
		m.mu.Unlock()
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 22 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		c := Card{UUID: f[1], Name: f[2], Bus: f[3], Driver: f[4], VBIOS: f[5], PState: f[17], PCIeGen: f[20], PCIeWidth: f[21]}
		c.Index = atoi(f[0])
		c.Util, c.Enc, c.Dec = num(f[6]), num(f[7]), num(f[8])
		c.MemTotal, c.MemUsed = num(f[9]), num(f[10])
		c.Temp, c.Fan, c.Power, c.PowerMax = num(f[11]), num(f[12]), num(f[13]), num(f[14])
		c.ClockGr, c.ClockMem = num(f[15]), num(f[16])
		c.EncSess, c.EncFPS = atoi(f[18]), num(f[19])
		Classify(&c)
		info.Cards = append(info.Cards, c)
		info.Driver = c.Driver
	}
	info.Available = len(info.Cards) > 0
	if out, err := exec.CommandContext(ctx, m.smi, "--query-compute-apps=gpu_uuid,pid,used_memory", "--format=csv,noheader,nounits").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			f := strings.Split(l, ",")
			if len(f) < 3 {
				continue
			}
			for i := range info.Cards {
				if info.Cards[i].UUID == strings.TrimSpace(f[0]) {
					info.Cards[i].Procs = append(info.Cards[i].Procs, Proc{PID: atoi(strings.TrimSpace(f[1])), Mem: num(strings.TrimSpace(f[2]))})
				}
			}
		}
	}
	info.CUDA = cudaVersion(ctx, m.smi)
	info.Patched = Patched()
	for i := range info.Cards {
		if info.Patched {
			info.Cards[i].SessLimit = 0
		}
	}
	m.mu.Lock()
	m.info = info
	m.mu.Unlock()
}

var cudaCache string

func cudaVersion(ctx context.Context, smi string) string {
	if cudaCache != "" {
		return cudaCache
	}
	out, err := exec.CommandContext(ctx, smi).Output()
	if err != nil {
		return ""
	}
	s := string(out)
	if i := strings.Index(s, "CUDA Version:"); i >= 0 {
		f := strings.Fields(s[i+13:])
		if len(f) > 0 {
			cudaCache = f[0]
		}
	}
	return cudaCache
}

// Patched detects the keylase nvidia-patch (NVENC session limit removed).
func Patched() bool {
	for _, p := range []string{"/opt/nvidia/libnvidia-encode-backup", "/opt/mastergpu/nvenc-patched"} {
		if st, err := os.Stat(p); err == nil && (st.IsDir() || st.Size() >= 0) {
			return true
		}
	}
	return false
}

func (m *Monitor) setFake() {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := Info{Available: true, Driver: "550.120", CUDA: "12.4", Patched: true}
	names := []string{"NVIDIA GeForce RTX 3060", "NVIDIA GeForce GTX 1080", "NVIDIA RTX A4000", "Tesla T4"}
	for i := 0; i < m.fake; i++ {
		prev := Card{}
		if i < len(m.info.Cards) {
			prev = m.info.Cards[i]
		}
		c := Card{Index: i, UUID: "GPU-fake-" + strconv.Itoa(i), Name: names[i%len(names)], Bus: "00000000:0" + strconv.Itoa(i+1) + ":00.0",
			Driver: info.Driver, MemTotal: 12288, Fan: 40, PowerMax: 170, ClockGr: 1800, ClockMem: 7500, PState: "P2", PCIeGen: "3", PCIeWidth: "16"}
		j := func(v, d, lo, hi float64) float64 {
			v += (rand.Float64()*2 - 1) * d
			if v < lo {
				v = lo
			}
			if v > hi {
				v = hi
			}
			return v
		}
		c.Util = j(prev.Util+20, 5, 0, 100)
		c.Enc = j(prev.Enc+25, 6, 0, 100)
		c.Dec = j(prev.Dec+15, 4, 0, 100)
		c.MemUsed = j(prev.MemUsed+2500, 150, 300, c.MemTotal)
		c.Temp = j(prev.Temp+55, 1, 30, 90)
		c.Power = j(prev.Power+70, 5, 20, c.PowerMax)
		if prev.UUID != "" {
			c.Util, c.Enc, c.Dec = j(prev.Util, 5, 0, 100), j(prev.Enc, 6, 0, 100), j(prev.Dec, 4, 0, 100)
			c.MemUsed, c.Temp, c.Power = j(prev.MemUsed, 150, 300, c.MemTotal), j(prev.Temp, 1, 30, 90), j(prev.Power, 5, 20, c.PowerMax)
		}
		c.EncSess = 4 + i
		c.EncFPS = 25
		Classify(&c)
		info.Cards = append(info.Cards, c)
	}
	m.info = info
}

func num(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func atoi(s string) int { v, _ := strconv.Atoi(s); return v }
