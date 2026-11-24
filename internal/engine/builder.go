package engine

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"gpupanel/internal/models"
)

// BuildOpts carries the runtime decisions the manager made before building.
type BuildOpts struct {
	Input     string   // effective input URL (may be a local relay / FIFO)
	Inputs2   []string // extra inputs (DRM audio/subtitle FIFOs)
	InputKeys []string // per-input decryption key (hex) for CENC fMP4
	GPU       int      // -1 cpu
	HLSRoot   string   // HLSDir/<id>
	HTTPPipe  bool     // send an MPEG-TS copy to fd 3 for HTTP/relay clients
	Proxy     string   // http proxy for ffmpeg (local relay)
	Gateway   string   // host of the protocol gateway (mediamtx) for served rtmp/rtsp/srt
	FFmpegVer int      // major version
}

func isCopy(t *models.Transcode) bool {
	if len(t.VideoEncoders) == 0 {
		return true
	}
	for _, v := range t.VideoEncoders {
		if v.Codec != "copy" {
			return false
		}
	}
	return true
}

func encName(codec, hw string) string {
	nv := hw == "full" || hw == "partial" || hw == "encode" || hw == "nvenc"
	qsv := hw == "qsv"
	switch codec {
	case "hevc", "h265":
		if nv {
			return "hevc_nvenc"
		} else if qsv {
			return "hevc_qsv"
		}
		return "libx265"
	case "av1":
		if nv {
			return "av1_nvenc"
		} else if qsv {
			return "av1_qsv"
		}
		return "libsvtav1"
	case "mpeg2":
		if qsv {
			return "mpeg2_qsv"
		}
		return "mpeg2video"
	case "copy":
		return "copy"
	default:
		if nv {
			return "h264_nvenc"
		} else if qsv {
			return "h264_qsv"
		}
		return "libx264"
	}
}

func audioEnc(c string) string {
	switch c {
	case "fdk_aac":
		return "libfdk_aac"
	case "mp3":
		return "libmp3lame"
	case "opus":
		return "libopus"
	case "", "aac":
		return "aac"
	}
	return c
}

func inputArgs(s *models.Stream, o BuildOpts, st *models.Settings) []string {
	a := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-progress", "pipe:1", "-stats_period", "2"}
	in := o.Input
	u, _ := url.Parse(in)
	scheme := ""
	if u != nil {
		scheme = strings.ToLower(u.Scheme)
	}
	if s.RateEmu || scheme == "" || scheme == "file" {
		a = append(a, "-re")
	}
	if len(s.FFlags) > 0 {
		a = append(a, "-fflags", "+"+strings.Join(s.FFlags, "+"))
	} else {
		a = append(a, "-fflags", "+genpts+discardcorrupt")
	}
	if s.ProbeSize > 0 {
		a = append(a, "-probesize", strconv.Itoa(s.ProbeSize))
	}
	if s.Analyze > 0 {
		a = append(a, "-analyzeduration", strconv.Itoa(s.Analyze))
	}
	switch scheme {
	case "http", "https":
		ua := s.UserAgent
		if ua == "" {
			ua = st.UserAgent
		}
		if ua != "" {
			a = append(a, "-user_agent", ua)
		}
		h := strings.TrimSpace(s.Headers)
		if s.Referer != "" {
			h = "Referer: " + s.Referer + "\r\n" + h
		}
		if h != "" {
			h = strings.ReplaceAll(strings.ReplaceAll(h, "\r\n", "\n"), "\n", "\r\n")
			if !strings.HasSuffix(h, "\r\n") {
				h += "\r\n"
			}
			a = append(a, "-headers", h)
		}
		if o.Proxy != "" {
			a = append(a, "-http_proxy", o.Proxy)
		}
		a = append(a, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "4", "-rw_timeout", strconv.Itoa(st.StreamTimeout*1000000))
	case "rtsp", "rtsps":
		if s.RTSPTCP {
			a = append(a, "-rtsp_transport", "tcp")
		}
		a = append(a, "-timeout", strconv.Itoa(st.StreamTimeout*1000000))
	case "rtmp", "rtmps":
		a = append(a, "-rw_timeout", strconv.Itoa(st.StreamTimeout*1000000))
	case "srt":
		if !strings.Contains(in, "timeout=") {
			sep := "?"
			if strings.Contains(in, "?") {
				sep = "&"
			}
			in += sep + "timeout=" + strconv.Itoa(st.StreamTimeout*1000000)
		}
	case "udp", "rtp":
		if !strings.Contains(in, "fifo_size") {
			sep := "?"
			if strings.Contains(in, "?") {
				sep = "&"
			}
			in += sep + "fifo_size=1000000&overrun_nonfatal=1&timeout=" + strconv.Itoa(st.StreamTimeout*1000000)
		}
	case "", "file":
		if s.LoopFile {
			a = append(a, "-stream_loop", "-1")
		}
	}
	t := &s.Transcode
	if !isCopy(t) && o.GPU >= 0 && t.HWAccel == "full" {
		a = append(a, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda", "-hwaccel_device", strconv.Itoa(o.GPU), "-extra_hw_frames", "4")
	} else if !isCopy(t) && o.GPU >= 0 && t.HWAccel == "partial" {
		a = append(a, "-hwaccel", "cuda", "-hwaccel_device", strconv.Itoa(o.GPU))
	} else if !isCopy(t) && t.HWAccel == "qsv" {
		a = append(a, "-hwaccel", "qsv", "-hwaccel_output_format", "qsv")
	}
	if len(o.InputKeys) > 0 && o.InputKeys[0] != "" {
		a = append(a, "-decryption_key", o.InputKeys[0])
	}
	a = append(a, "-thread_queue_size", "1024", "-i", in)
	for i, x := range o.Inputs2 {
		if i+1 < len(o.InputKeys) && o.InputKeys[i+1] != "" {
			a = append(a, "-decryption_key", o.InputKeys[i+1])
		}
		a = append(a, "-thread_queue_size", "1024", "-i", x)
	}
	return a
}

// mapSpec turns a user map like "0:v:0", "v:0", "#0x100", "p:1" into an ffmpeg -map value.
func mapSpec(m, kind string) string {
	m = strings.TrimSpace(m)
	if m == "" {
		return "0:" + kind + ":0?"
	}
	if strings.HasPrefix(m, "#") {
		return "0:" + m
	}
	if strings.HasPrefix(m, "p:") {
		return "0:" + m + ":" + kind
	}
	if !strings.Contains(m, ":") || strings.HasPrefix(m, kind+":") {
		if _, err := strconv.Atoi(m); err == nil {
			return "0:" + m
		}
		return "0:" + m
	}
	return m
}

// Build returns ffmpeg arguments and the list of HLS variant directories (for master playlists).
func Build(s *models.Stream, st *models.Settings, o BuildOpts) ([]string, []Variant, error) {
	t := &s.Transcode
	a := inputArgs(s, o, st)
	copyMode := isCopy(t)
	nv := o.GPU >= 0 && (t.HWAccel == "full" || t.HWAccel == "partial" || t.HWAccel == "encode" || t.HWAccel == "nvenc")
	fullHW := nv && t.HWAccel == "full"
	venc := t.VideoEncoders
	if len(venc) == 0 {
		venc = []models.VideoEnc{{Codec: "copy"}}
	}
	aenc := t.AudioEncoders
	if len(aenc) == 0 {
		aenc = []models.AudioEnc{{Codec: "copy"}}
	}
	vmap := mapSpec(t.VideoMap, "v")
	amap := mapSpec(t.AudioMap, "a")

	// ---- video filter graph
	var vlabels []string
	if !copyMode {
		var chain []string
		src := "[" + strings.TrimSuffix(vmap, "?") + "]"
		if fullHW {
			if t.Deinterlace {
				chain = append(chain, "yadif_cuda=0:-1:1")
			}
		} else if t.Deinterlace {
			chain = append(chain, "yadif=0:-1:1")
		}
		if t.FrameRate != "" && !fullHW {
			chain = append(chain, "fps="+t.FrameRate)
		}
		soft := t.OverlayFile != "" || t.Text != "" || t.DelogoW > 0 || t.SubMode == "burn" || t.SubMode == "burn_file"
		if soft && fullHW {
			chain = append(chain, "hwdownload", "format=nv12")
		}
		if t.DelogoW > 0 {
			chain = append(chain, fmt.Sprintf("delogo=x=%d:y=%d:w=%d:h=%d", t.DelogoX, t.DelogoY, t.DelogoW, t.DelogoH))
		}
		if t.SubMode == "burn_file" && t.SubFile != "" {
			fs := ""
			if t.SubFontSize > 0 {
				fs = fmt.Sprintf(":force_style='FontSize=%d'", t.SubFontSize)
			}
			chain = append(chain, "subtitles="+escFilter(t.SubFile)+fs)
		}
		if t.Text != "" {
			size := t.TextSize
			if size == 0 {
				size = 28
			}
			col := t.TextColor
			if col == "" {
				col = "white"
			}
			x, y := def(t.TextX, "w-tw-20"), def(t.TextY, "20")
			if t.TextScroll {
				x = "w-mod(t*120\\,w+tw)"
			}
			alpha := t.TextAlpha
			if alpha == 0 {
				alpha = 1
			}
			chain = append(chain, fmt.Sprintf("drawtext=text='%s':fontsize=%d:fontcolor=%s@%.2f:x=%s:y=%s:box=1:boxcolor=black@0.35:boxborderw=6", escText(t.Text), size, col, alpha, x, y))
		}
		pre := src
		var g []string
		if len(chain) > 0 {
			g = append(g, pre+strings.Join(chain, ",")+"[vpre]")
			pre = "[vpre]"
		}
		if t.OverlayFile != "" {
			op := t.OverlayOpacity
			if op == 0 {
				op = 1
			}
			lg := fmt.Sprintf("movie=%s,format=rgba,colorchannelmixer=aa=%.2f", escFilter(t.OverlayFile), op)
			if t.OverlayScale > 0 {
				lg += fmt.Sprintf(",scale=%d:-1", t.OverlayScale)
			}
			g = append(g, lg+"[logo]")
			g = append(g, fmt.Sprintf("%s[logo]overlay=%s:%s[vov]", pre, def(t.OverlayX, "W-w-20"), def(t.OverlayY, "20")))
			pre = "[vov]"
		}
		if t.SubMode == "burn" {
			sm := t.SubMap
			if sm == "" {
				sm = "0:s:0"
			}
			g = append(g, fmt.Sprintf("%s[%s]overlay=eof_action=pass[vsub]", pre, strings.TrimSuffix(sm, "?")))
			pre = "[vsub]"
		}
		uploadBack := soft && fullHW
		n := len(venc)
		if n > 1 {
			outs := ""
			for i := 0; i < n; i++ {
				outs += fmt.Sprintf("[vs%d]", i)
			}
			split := "split"
			if fullHW && !uploadBack {
				split = "split" // cuda frames can be split as references
			}
			g = append(g, fmt.Sprintf("%s%s=%d%s", pre, split, n, outs))
		}
		for i, v := range venc {
			in := pre
			if n > 1 {
				in = fmt.Sprintf("[vs%d]", i)
			}
			var f []string
			if uploadBack {
				f = append(f, "hwupload_cuda")
			}
			if v.Size != "" && v.Codec != "copy" {
				w, h := splitSize(v.Size)
				switch {
				case fullHW:
					f = append(f, fmt.Sprintf("scale_cuda=%s:%s", w, h))
				case t.HWAccel == "qsv":
					f = append(f, fmt.Sprintf("scale_qsv=w=%s:h=%s", w, h))
				default:
					f = append(f, fmt.Sprintf("scale=%s:%s:flags=bicubic", w, h))
				}
			}
			if t.PixFmt != "" && !fullHW && t.HWAccel != "qsv" {
				f = append(f, "format="+t.PixFmt)
			}
			if t.DAR != "" {
				f = append(f, "setdar="+t.DAR)
			}
			if len(f) == 0 {
				f = append(f, "null")
			}
			lab := fmt.Sprintf("[v%d]", i)
			g = append(g, in+strings.Join(f, ",")+lab)
			vlabels = append(vlabels, lab)
		}
		a = append(a, "-filter_complex", strings.Join(g, ";"))
	}

	// ---- maps + codecs (one global set of encoded streams; outputs select from them)
	for i, v := range venc {
		if copyMode {
			a = append(a, "-map", vmap)
		} else {
			a = append(a, "-map", vlabels[i])
		}
		vi := strconv.Itoa(i)
		name := encName(v.Codec, map[bool]string{true: "nvenc", false: t.HWAccel}[nv])
		if copyMode || v.Codec == "copy" {
			a = append(a, "-c:v:"+vi, "copy")
			continue
		}
		a = append(a, "-c:v:"+vi, name)
		if nv {
			a = append(a, "-gpu:v:"+vi, strconv.Itoa(o.GPU))
		}
		if p := nvPreset(t.Preset, name, o.FFmpegVer); p != "" {
			a = append(a, "-preset:v:"+vi, p)
		}
		if t.Profile != "" && !strings.HasPrefix(name, "mpeg2") {
			a = append(a, "-profile:v:"+vi, t.Profile)
		}
		if t.Level != "" {
			a = append(a, "-level:v:"+vi, t.Level)
		}
		if t.Tune != "" && (strings.HasPrefix(name, "libx26") || nv) {
			a = append(a, "-tune:v:"+vi, t.Tune)
		}
		rc := v.RC
		switch rc {
		case "cq":
			if nv {
				a = append(a, "-rc:v:"+vi, "vbr", "-cq:v:"+vi, strconv.Itoa(max(v.CQ, 23)), "-b:v:"+vi, "0")
			} else {
				a = append(a, "-crf:v:"+vi, strconv.Itoa(max(v.CQ, 23)))
			}
		default:
			if v.Bitrate > 0 {
				br := strconv.Itoa(v.Bitrate) + "k"
				a = append(a, "-b:v:"+vi, br)
				mr := v.Maxrate
				if mr == 0 {
					mr = v.Bitrate
					if rc == "vbr" {
						mr = v.Bitrate * 3 / 2
					}
				}
				a = append(a, "-maxrate:v:"+vi, strconv.Itoa(mr)+"k", "-bufsize:v:"+vi, strconv.Itoa(mr*2)+"k")
				if nv {
					if rc == "vbr" {
						a = append(a, "-rc:v:"+vi, "vbr")
					} else {
						a = append(a, "-rc:v:"+vi, "cbr")
					}
				}
			}
		}
		if t.GOP > 0 {
			a = append(a, "-g:v:"+vi, strconv.Itoa(t.GOP), "-keyint_min:v:"+vi, strconv.Itoa(t.GOP))
		}
		if nv {
			a = append(a, "-bf:v:"+vi, strconv.Itoa(t.BFrames), "-forced-idr:v:"+vi, "1")
		}
		if fullHW && t.FrameRate != "" {
			a = append(a, "-r:v:"+vi, t.FrameRate)
		}
		for k, val := range map[string]string{"color_range": t.ColorRange, "colorspace": t.ColorSpace, "color_primaries": t.ColorPrim, "color_trc": t.ColorTRC} {
			if val != "" {
				a = append(a, "-"+k+":v:"+vi, val)
			}
		}
	}
	if t.VideoSync != "" && t.VideoSync != "auto" {
		if o.FFmpegVer >= 5 {
			a = append(a, "-fps_mode", t.VideoSync)
		} else {
			a = append(a, "-vsync", t.VideoSync)
		}
	}
	var af []string
	if t.AudioVolume > 0 && t.AudioVolume != 1 {
		af = append(af, fmt.Sprintf("volume=%.2f", t.AudioVolume))
	}
	if t.AudioNormalize {
		af = append(af, "loudnorm=I=-16:TP=-1.5:LRA=11")
	}
	if t.AudioDelay != 0 {
		af = append(af, fmt.Sprintf("adelay=%d:all=1", t.AudioDelay))
	}
	for i, ae := range aenc {
		ai := strconv.Itoa(i)
		a = append(a, "-map", amap)
		if ae.Codec == "copy" || ae.Codec == "" {
			a = append(a, "-c:a:"+ai, "copy")
			continue
		}
		a = append(a, "-c:a:"+ai, audioEnc(ae.Codec))
		if ae.Bitrate > 0 {
			a = append(a, "-b:a:"+ai, strconv.Itoa(ae.Bitrate)+"k")
		}
		if t.AudioChannels > 0 {
			a = append(a, "-ac:a:"+ai, strconv.Itoa(t.AudioChannels))
		}
		if t.AudioRate > 0 {
			a = append(a, "-ar:a:"+ai, strconv.Itoa(t.AudioRate))
		}
		if len(af) > 0 {
			a = append(a, "-filter:a:"+ai, strings.Join(af, ","))
		}
	}
	// extra DRM audio tracks from secondary inputs
	extraA := 0
	for i := range o.Inputs2 {
		if strings.Contains(o.Inputs2[i], "audio") {
			a = append(a, "-map", fmt.Sprintf("%d:a?", i+1), "-c:a:"+strconv.Itoa(len(aenc)+extraA), "copy")
			extraA++
		}
	}
	hasSubs := false
	if t.SubMode == "copy" || t.SubMode == "webvtt" {
		sm := t.SubMap
		if sm == "" {
			sm = "0:s?"
		}
		a = append(a, "-map", sm)
		if t.SubMode == "webvtt" {
			a = append(a, "-c:s", "webvtt")
		} else {
			a = append(a, "-c:s", "copy")
		}
		hasSubs = true
	}
	if t.PIDMap != "" {
		for _, p := range strings.Split(t.PIDMap, ",") {
			if kv := strings.SplitN(strings.TrimSpace(p), "=", 2); len(kv) == 2 {
				a = append(a, "-streamid", kv[0]+":"+kv[1])
			}
		}
	}
	if t.ServiceName != "" {
		a = append(a, "-metadata", "service_name="+t.ServiceName)
	}
	if t.ServiceProv != "" {
		a = append(a, "-metadata", "service_provider="+t.ServiceProv)
	}
	if t.RemoveCC {
		a = append(a, "-a53cc", "0")
	}
	if s.MemMaxMB > 0 {
		a = append(a, "-max_muxing_queue_size", "1024")
	}
	if x := strings.TrimSpace(t.ExtraArgs); x != "" {
		a = append(a, strings.Fields(x)...)
	}

	// ---- outputs via tee
	nv_ := len(venc)
	na := len(aenc) + extraA
	var slaves []string
	var variants []Variant
	for oi, out := range s.Outputs {
		if out.Disabled {
			continue
		}
		vs := out.Video
		as := out.Audio
		if len(vs) == 0 {
			vs = []int{0}
		}
		if len(as) == 0 {
			as = seq(na)
		}
		hlsTime := out.HLSTime
		if hlsTime == 0 {
			hlsTime = max(st.DefaultHLSTime, 2)
		}
		hlsList := out.HLSList
		if hlsList == 0 {
			hlsList = max(st.DefaultHLSList, 4)
		}
		name := out.Name
		if name == "" {
			name = "o" + strconv.Itoa(oi)
		}
		switch out.Type {
		case "hls", "hls_master":
			if out.Type == "hls_master" {
				vs = seq(nv_)
			}
			for _, v := range vs {
				if v >= nv_ {
					continue
				}
				dir := filepath.Join(o.HLSRoot, fmt.Sprintf("%s_v%d", name, v))
				if out.Type == "hls" && len(vs) == 1 {
					dir = filepath.Join(o.HLSRoot, name)
				}
				sel := fmt.Sprintf("v:%d", v)
				for _, x := range as {
					if x < na {
						sel += fmt.Sprintf(",a:%d", x)
					}
				}
				flags := "delete_segments+omit_endlist+temp_file+independent_segments"
				seg := "%d.ts"
				opt := fmt.Sprintf("select=\\'%s\\':f=hls:hls_time=%d:hls_list_size=%d:hls_flags=%s:hls_delete_threshold=2:hls_allow_cache=0:hls_segment_filename=%s", sel, hlsTime, hlsList, flags, escTee(filepath.Join(dir, seg)))
				if o.FFmpegVer >= 5 && hasSubs {
					// subtitles go as separate webvtt rendition handled by the manager
				}
				slaves = append(slaves, "["+opt+"]"+escTee(filepath.Join(dir, "index.m3u8")))
				ve := venc[v]
				variants = append(variants, Variant{Dir: filepath.Base(dir), Name: def(ve.Name, ve.Size), Size: ve.Size, Bitrate: ve.Bitrate, Master: out.Type == "hls_master", Output: name})
			}
		case "dash":
			dir := filepath.Join(o.HLSRoot, name)
			sel := selector(vs, as, nv_, na)
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=dash:seg_duration=%d:window_size=%d:remove_at_exit=1:use_template=1:use_timeline=1:streaming=1]%s", sel, hlsTime, hlsList, escTee(filepath.Join(dir, "manifest.mpd"))))
		case "http":
			// served by the panel from the shared TS pipe
		case "rtmp", "rtmp_push":
			u := out.URL
			if u == "" {
				u = "rtmp://" + def(o.Gateway, "127.0.0.1") + ":1935/live/" + name
			}
			sel := selector(vs[:1], as[:min(1, len(as))], nv_, na)
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=flv:onfail=ignore]%s", sel, escTee(u)))
		case "rtsp":
			u := out.URL
			if u == "" {
				u = "rtsp://" + def(o.Gateway, "127.0.0.1") + ":8554/live/" + name
			}
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=rtsp:rtsp_transport=tcp:onfail=ignore]%s", selector(vs, as, nv_, na), escTee(u)))
		case "srt", "srt_listener":
			u := out.URL
			if u == "" {
				u = "srt://" + def(o.Gateway, "127.0.0.1") + ":8890?streamid=publish:live/" + name + "&pkt_size=1316"
			}
			if out.Type == "srt_listener" && out.Port > 0 {
				u = fmt.Sprintf("srt://0.0.0.0:%d?mode=listener&pkt_size=1316", out.Port)
			}
			if out.Passwd != "" && !strings.Contains(u, "passphrase") {
				u += "&passphrase=" + url.QueryEscape(out.Passwd)
			}
			if out.Latency > 0 && !strings.Contains(u, "latency") {
				u += "&latency=" + strconv.Itoa(out.Latency*1000)
			}
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=mpegts:onfail=ignore]%s", selector(vs, as, nv_, na), escTee(u)))
		case "udp", "tcp":
			u := out.URL
			if out.Type == "udp" && !strings.Contains(u, "pkt_size") {
				u += sepq(u) + "pkt_size=1316"
			}
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=mpegts:onfail=ignore]%s", selector(vs, as, nv_, na), escTee(u)))
		case "rtp":
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=rtp_mpegts:onfail=ignore]%s", selector(vs, as, nv_, na), escTee(out.URL)))
		case "file":
			u := out.URL
			f := "mpegts"
			if strings.HasSuffix(u, ".mp4") || strings.HasSuffix(u, ".mkv") {
				f = map[bool]string{true: "mp4", false: "matroska"}[strings.HasSuffix(u, ".mp4")]
				if f == "mp4" {
					f = "mp4:movflags=+frag_keyframe+empty_moov"
				}
			}
			slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=%s:strftime=1:onfail=ignore]%s", selector(vs, as, nv_, na), f, escTee(u)))
		}
	}
	if o.HTTPPipe {
		sel := selector([]int{0}, seq(na), nv_, na)
		slaves = append(slaves, fmt.Sprintf("[select=\\'%s\\':f=mpegts:mpegts_flags=+resend_headers:onfail=ignore]pipe\\:3", sel))
	}
	if len(slaves) == 0 {
		return nil, nil, fmt.Errorf("stream has no enabled outputs")
	}
	a = append(a, "-flags", "+global_header", "-f", "tee", "-use_fifo", "1", strings.Join(slaves, "|"))
	return a, variants, nil
}

type Variant struct {
	Dir     string `json:"dir"`
	Name    string `json:"name"`
	Size    string `json:"size"`
	Bitrate int    `json:"bitrate"`
	Master  bool   `json:"master"`
	Output  string `json:"output"`
}

func nvPreset(p, enc string, ver int) string {
	if p == "" {
		if strings.Contains(enc, "nvenc") {
			if ver >= 5 {
				return "p4"
			}
			return "fast"
		}
		if strings.HasPrefix(enc, "libx26") {
			return "veryfast"
		}
		return ""
	}
	if strings.Contains(enc, "nvenc") && ver < 5 && len(p) == 2 && p[0] == 'p' {
		m := map[string]string{"p1": "fast", "p2": "fast", "p3": "medium", "p4": "medium", "p5": "slow", "p6": "slow", "p7": "slow"}
		return m[p]
	}
	if strings.HasPrefix(enc, "libx26") && len(p) == 2 && p[0] == 'p' {
		m := map[string]string{"p1": "ultrafast", "p2": "superfast", "p3": "veryfast", "p4": "faster", "p5": "fast", "p6": "medium", "p7": "slow"}
		return m[p]
	}
	return p
}

func selector(vs, as []int, nv, na int) string {
	var p []string
	for _, v := range vs {
		if v < nv {
			p = append(p, fmt.Sprintf("v:%d", v))
		}
	}
	for _, x := range as {
		if x < na {
			p = append(p, fmt.Sprintf("a:%d", x))
		}
	}
	return strings.Join(p, ",")
}

func seq(n int) []int {
	r := make([]int, n)
	for i := range r {
		r[i] = i
	}
	return r
}

func splitSize(s string) (string, string) {
	s = strings.ToLower(strings.ReplaceAll(s, ":", "x"))
	if p := strings.SplitN(s, "x", 2); len(p) == 2 {
		return def(p[0], "-2"), def(p[1], "-2")
	}
	return "-2", s
}

func def(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func sepq(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

func escTee(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `|`, `\|`, `[`, `\[`, `]`, `\]`, `:`, `\:`)
	return r.Replace(s)
}

func escFilter(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `,`, `\,`, `;`, `\;`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

func escText(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `'`, `\\\'`, `:`, `\\:`, `%`, `\\%`, `,`, `\,`)
	return r.Replace(s)
}
