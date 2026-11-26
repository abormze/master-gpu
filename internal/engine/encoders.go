package engine

import (
	"os/exec"
	"strings"
	"sync"
)

var encCache sync.Map // ffmpeg path -> map[string]bool (nil when listing failed)

// hasEncoder reports whether the ffmpeg binary was built with encoder name.
// Unknown (listing failed) counts as available so the command is left unchanged.
func hasEncoder(ffmpeg, name string) bool {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	v, ok := encCache.Load(ffmpeg)
	if !ok {
		var m map[string]bool
		if out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output(); err == nil {
			m = map[string]bool{}
			for _, l := range strings.Split(string(out), "\n") {
				if f := strings.Fields(l); len(f) >= 2 && len(f[0]) == 6 {
					m[f[1]] = true
				}
			}
		}
		v, _ = encCache.LoadOrStore(ffmpeg, m)
	}
	m := v.(map[string]bool)
	return m == nil || m[name]
}

// audioEncFor maps a model audio codec to an encoder this ffmpeg actually has
// (libfdk_aac is non-free and missing from distro builds: use native aac).
func audioEncFor(ffmpeg, c string) string {
	e := audioEnc(c)
	if e == "libfdk_aac" && !hasEncoder(ffmpeg, e) {
		return "aac"
	}
	return e
}
