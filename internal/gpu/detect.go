package gpu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Adapter is any display/3D PCI device, whatever the vendor or driver state.
type Adapter struct {
	Bus      string  `json:"bus"`
	Vendor   string  `json:"vendor"` // nvidia, amd, intel
	Name     string  `json:"name"`
	Driver   string  `json:"driver"` // bound kernel driver, "" = none
	Render   string  `json:"render"` // /dev/dri/renderD*
	Util     float64 `json:"util"`
	Temp     float64 `json:"temp"`
	MemTotal float64 `json:"mem_total"` // MiB
	MemUsed  float64 `json:"mem_used"`
}

var vendorIDs = map[string]string{"0x10de": "nvidia", "0x1002": "amd", "0x8086": "intel"}

var (
	nameMu    sync.Mutex
	nameCache = map[string]string{}
)

func readSys(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }

func pciName(bus, dev string) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := nameCache[bus]; ok {
		return n
	}
	n := dev
	if out, err := exec.Command("lspci", "-mm", "-s", bus).Output(); err == nil {
		if f := strings.Split(string(out), "\""); len(f) >= 6 {
			n = strings.TrimSpace(f[3] + " " + f[5])
		}
	}
	nameCache[bus] = n
	return n
}

// Adapters lists NVIDIA, AMD and Intel GPUs from sysfs (works before any driver is installed).
func Adapters() []Adapter {
	var r []Adapter
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		cl := readSys(d + "/class")
		if !strings.HasPrefix(cl, "0x0300") && !strings.HasPrefix(cl, "0x0302") && !strings.HasPrefix(cl, "0x0380") {
			continue
		}
		v, ok := vendorIDs[readSys(d+"/vendor")]
		if !ok {
			continue
		}
		bus := filepath.Base(d)
		a := Adapter{Bus: bus, Vendor: v, Name: pciName(bus, readSys(d+"/device"))}
		if l, err := os.Readlink(d + "/driver"); err == nil {
			a.Driver = filepath.Base(l)
		}
		if m, _ := filepath.Glob(d + "/drm/renderD*"); len(m) > 0 {
			a.Render = "/dev/dri/" + filepath.Base(m[0])
		}
		if v == "amd" {
			a.Util, _ = strconv.ParseFloat(readSys(d+"/gpu_busy_percent"), 64)
			t, _ := strconv.ParseFloat(readSys(d+"/mem_info_vram_total"), 64)
			u, _ := strconv.ParseFloat(readSys(d+"/mem_info_vram_used"), 64)
			a.MemTotal, a.MemUsed = t/(1<<20), u/(1<<20)
		}
		if h, _ := filepath.Glob(d + "/hwmon/hwmon*/temp1_input"); len(h) > 0 {
			t, _ := strconv.ParseFloat(readSys(h[0]), 64)
			a.Temp = t / 1000
		}
		r = append(r, a)
	}
	return r
}

// VAAPIDevice returns the render node of the first AMD/Intel GPU with a loaded driver.
func VAAPIDevice() string {
	for _, a := range Adapters() {
		if (a.Vendor == "amd" || a.Vendor == "intel") && a.Driver != "" && a.Render != "" {
			return a.Render
		}
	}
	return ""
}
