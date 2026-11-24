package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const gatewayConf = `logLevel: warn
api: no
metrics: no
pprof: no
playback: no
hls: no
webrtc: no
rtsp: yes
rtspAddress: :8554
rtmp: yes
rtmpAddress: :1935
srt: yes
srtAddress: :8890
authInternalUsers:
- user: any
  ips: ['127.0.0.1', '::1']
  permissions:
  - action: publish
  - action: read
- user: any
  permissions:
  - action: read
paths:
  all_others:
`

// findGateway locates the optional mediamtx binary that serves RTMP/RTSP/SRT to viewers.
func findGateway(dir, bin string) string {
	for _, p := range []string{filepath.Join(dir, "bin", "mediamtx"), filepath.Join(filepath.Dir(bin), "mediamtx")} {
		if fileExists(p) {
			return p
		}
	}
	p, _ := exec.LookPath("mediamtx")
	return p
}

// runGateway supervises mediamtx until stop is closed.
func runGateway(path, dir string, stop chan struct{}) {
	conf := filepath.Join(dir, "gateway.yml")
	os.WriteFile(conf, []byte(gatewayConf), 0o644)
	for {
		cmd := exec.Command(path, conf)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		if err := cmd.Start(); err != nil {
			log.Printf("gateway: %v", err)
			return
		}
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-stop:
			cmd.Process.Signal(os.Interrupt)
			<-done
			return
		case <-done:
			log.Printf("gateway exited, restarting")
			time.Sleep(2 * time.Second)
		}
	}
}
