// Package gpu polls NVIDIA cards through nvidia-smi (works on every driver
// branch from 390 up, GTX/RTX/Quadro/Tesla/datacenter) and exposes a cached view.
package gpu

import (
	"bufio"
	"bytes"
	"context"
	"math"
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
	pids map[int]ProcUse
	hist map[int][]float64 // per card: last samples of util, enc, dec (flattened triples)
}

// smoothWin is how many polls the util/enc/dec figures are averaged over.
// NVML reports encoder busy time over a short, irregular window, so a single
// reading jumps 0%..90% while the real load is steady.
const smoothWin = 8

func (m *Monitor) smooth(c *Card) {
	if m.hist == nil {
		m.hist = map[int][]float64{}
	}
	h := append(m.hist[c.Index], c.Util, c.Enc, c.Dec)
	if len(h) > 3*smoothWin {
		h = h[len(h)-3*smoothWin:]
	}
	m.hist[c.Index] = h
	var u, e, d float64
	n := float64(len(h) / 3)
	for i := 0; i < len(h); i += 3 {
		u, e, d = u+h[i], e+h[i+1], d+h[i+2]
	}
	c.Util, c.Enc, c.Dec = math.Round(u/n), math.Round(e/n), math.Round(d/n)
}

// ProcUse is one process's share of a card, from nvidia-smi pmon.
type ProcUse struct {
	GPU               int
	SM, Enc, Dec, Mem float64 // %
	FB                float64 // MiB
}

// Proc returns the GPU usage of pid, if it runs on a card.
func (m *Monitor) Proc(pid int) (ProcUse, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.pids[pid]
	return u, ok
}

func (m *Monitor) pollPids(ctx context.Context) map[int]ProcUse {
	out, err := exec.CommandContext(ctx, m.smi, "pmon", "-c", "1", "-s", "um").Output()
	if err != nil {
		return nil
	}
	r := map[int]ProcUse{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 8 || strings.HasPrefix(f[0], "#") {
			continue
		}
		pid := atoi(f[1])
		if pid <= 0 {
			continue
		}
		r[pid] = ProcUse{GPU: atoi(f[0]), SM: num(f[3]), Mem: num(f[4]), Enc: num(f[5]), Dec: num(f[6]), FB: num(f[7])}
	}
	return r
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
	pids := m.pollPids(ctx)
	info.CUDA = cudaVersion(ctx, m.smi)
	info.Patched = Patched()
	for i := range info.Cards {
		if info.Patched {
			info.Cards[i].SessLimit = 0
		}
	}
	m.mu.Lock()
	for i := range info.Cards {
		m.smooth(&info.Cards[i])
	}
	for pid, u := range pids {
		if o, ok := m.pids[pid]; ok && o.GPU == u.GPU {
			ema := func(o, n float64) float64 { return (o*(smoothWin-1) + n) / smoothWin }
			u.SM, u.Enc, u.Dec, u.Mem = ema(o.SM, u.SM), ema(o.Enc, u.Enc), ema(o.Dec, u.Dec), ema(o.Mem, u.Mem)
			pids[pid] = u
		}
	}
	m.pids = pids
	// NVML's card-wide encoder/decoder figure is sampled over a short window
	// and reads 0% about a third of the time under steady load; the per-process
	// figures cover the same work without those gaps
	for i := range info.Cards {
		c := &info.Cards[i]
		var e, d float64
		n := 0
		for _, u := range pids {
			if u.GPU == c.Index {
				e, d, n = e+u.Enc, d+u.Dec, n+1
			}
		}
		if n > 0 {
			c.Enc, c.Dec = math.Round(min(e, 100)), math.Round(min(d, 100))
		}
	}
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

// LockClocks enables persistence mode and pins every card's SM/video and memory
// clocks at their maximum so NVENC never runs on a down-clocked GPU.
func (m *Monitor) LockClocks() []string {
	if m.fake > 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(a ...string) error { return exec.CommandContext(ctx, m.smi, a...).Run() }
	out, err := exec.CommandContext(ctx, m.smi, "--query-gpu=index,clocks.max.sm,clocks.max.memory", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var msgs []string
	if err := run("-pm", "1"); err != nil {
		msgs = append(msgs, "persistence mode: "+err.Error())
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(ln, ",")
		if len(f) < 3 {
			continue
		}
		i, sm, mem := strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
		if atoi(sm) <= 0 {
			continue
		}
		if atoi(mem) > 0 {
			_ = run("-i", i, "-ac", mem+","+sm)
		}
		if err := run("-i", i, "-lgc", sm+","+sm); err != nil {
			msgs = append(msgs, "GPU"+i+" lock clocks: "+err.Error())
		} else {
			msgs = append(msgs, "GPU"+i+" clocks locked at "+sm+" MHz")
		}
	}
	return msgs
}
