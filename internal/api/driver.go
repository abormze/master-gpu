package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"gpupanel/internal/gpu"
	"gpupanel/internal/models"
)

const driverLog = "/var/log/mastergpu-driver.log"

var driverScripts = map[string]string{
	"nvidia": `apt-get update
apt-get install -y ubuntu-drivers-common pciutils curl
if ! nvidia-smi >/dev/null 2>&1; then
  V=$(ubuntu-drivers devices 2>/dev/null | awk '/nvidia-driver/ && /recommended/{print $3}' | grep -o '[0-9]\+' | head -1)
  [ -n "$V" ] || V=$(ubuntu-drivers devices 2>/dev/null | grep -o 'nvidia-driver-[0-9]\+' | grep -o '[0-9]\+' | sort -n | tail -1)
  [ -n "$V" ] || V=535
  echo "installing NVIDIA driver $V"
  apt-get install -y nvidia-driver-$V-server || apt-get install -y nvidia-driver-$V
  apt-get install -y libnvidia-encode-$V-server libnvidia-decode-$V-server || apt-get install -y libnvidia-encode-$V libnvidia-decode-$V || true
fi
if nvidia-smi >/dev/null 2>&1; then
  nvidia-smi -L
  curl -fsSL https://raw.githubusercontent.com/keylase/nvidia-patch/master/patch.sh -o /tmp/nvpatch.sh && bash /tmp/nvpatch.sh || echo "NVENC patch: this driver version is not supported by keylase"
else
  echo "REBOOT REQUIRED: reboot the server, then press Install again to apply the NVENC patch"
fi`,
	"amd": `apt-get update
apt-get install -y mesa-va-drivers vainfo linux-firmware pciutils
modprobe amdgpu || true
vainfo --display drm --device $(ls /dev/dri/renderD* 2>/dev/null | head -1) || echo "REBOOT REQUIRED: reboot the server to load the amdgpu driver"`,
	"intel": `apt-get update
apt-get install -y intel-media-va-driver-non-free vainfo pciutils || apt-get install -y intel-media-va-driver vainfo pciutils
apt-get install -y libvpl2 libmfx-gen1.2 || true
modprobe i915 || true
vainfo --display drm --device $(ls /dev/dri/renderD* 2>/dev/null | head -1) || echo "REBOOT REQUIRED: reboot the server to load the i915 driver"`,
}

var drvRun struct {
	sync.Mutex
	on bool
}

func (s *Server) drivers(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	b, _ := os.ReadFile(driverLog)
	if len(b) > 16<<10 {
		b = b[len(b)-16<<10:]
	}
	drvRun.Lock()
	on := drvRun.on
	drvRun.Unlock()
	ok(w, map[string]any{"adapters": gpu.Adapters(), "running": on, "log": string(b), "reboot_required": bytes.Contains(b, []byte("REBOOT REQUIRED")) && !on})
}

func (s *Server) driverInstall(w http.ResponseWriter, r *http.Request, _ *models.Account) {
	var in struct{ Vendor string }
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	sc, found := driverScripts[in.Vendor]
	if !found {
		fail(w, 400, "unknown vendor")
		return
	}
	drvRun.Lock()
	if drvRun.on {
		drvRun.Unlock()
		fail(w, 409, "driver install already running")
		return
	}
	drvRun.on = true
	drvRun.Unlock()
	f, err := os.Create(driverLog)
	if err != nil {
		drvRun.Lock()
		drvRun.on = false
		drvRun.Unlock()
		fail(w, 500, err.Error())
		return
	}
	s.St.Log("panel", 0, "info", "driver install started: "+in.Vendor)
	go func() {
		c := exec.Command("bash", "-c", "set -x\nexport DEBIAN_FRONTEND=noninteractive\n"+sc)
		c.Stdout, c.Stderr = f, f
		err := c.Run()
		res := "driver install finished: " + in.Vendor
		if err != nil {
			res += " (" + err.Error() + ")"
		}
		f.WriteString("\n" + res + "\n")
		f.Close()
		s.St.Log("panel", 0, "info", res)
		drvRun.Lock()
		drvRun.on = false
		drvRun.Unlock()
	}()
	ok(w, map[string]any{"ok": true})
}
