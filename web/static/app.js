"use strict";
/* Master GPU v1.0 — single-file UI, no build step */
const $ = (s, e = document) => e.querySelector(s), $$ = (s, e = document) => [...e.querySelectorAll(s)];
const S = { me: null, ver: {}, lang: localStorage.lang || "ar", theme: localStorage.theme || "light", dash: null, rows: [], hist: { cpu: [], rx: [], tx: [], gpu: {} }, cache: {}, ws: null };
const esc = v => String(v ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const L = s => { const p = String(s).split("|"); return S.lang === "ar" && p[1] ? p[1] : p[0]; };
const fmtB = (b, d = 1) => { b = +b || 0; const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0; while (b >= 1024 && i < 4) { b /= 1024; i++; } return b.toFixed(i ? d : 0) + " " + u[i]; };
const fmtBits = b => { b = +b || 0; const u = ["bps", "Kbps", "Mbps", "Gbps"]; let i = 0; while (b >= 1000 && i < 3) { b /= 1000; i++; } return b.toFixed(1) + " " + u[i]; };
const fmtDur = s => { s = +s || 0; const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60); return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m ${Math.floor(s % 60)}s`; };
const fmtTime = t => t ? new Date(t * 1000).toLocaleString(S.lang === "ar" ? "ar-EG-u-nu-latn" : "en-GB") : "—";
const pct = (a, b) => b ? Math.min(100, a / b * 100) : 0;
const get = (o, p) => p == null ? undefined : String(p).split(".").reduce((a, k) => a == null ? a : a[k], o);
const set = (o, p, v) => { const k = p.split("."); let a = o; k.slice(0, -1).forEach(x => { if (a[x] == null || typeof a[x] !== "object") a[x] = {}; a = a[x]; }); a[k[k.length - 1]] = v; };

async function api(path, opt = {}) {
  const o = { method: opt.method || "GET", headers: {}, credentials: "same-origin" };
  if (opt.body !== undefined) { if (opt.body instanceof FormData) o.body = opt.body; else { o.body = JSON.stringify(opt.body); o.headers["Content-Type"] = "application/json"; } }
  const r = await fetch("/api/" + path, o);
  if (r.status === 401 && path !== "login") { S.me = null; render(); throw new Error("unauthorized"); }
  if (r.status === 428) { S.me && (S.me.must_change = true); passwordModal(true); throw new Error(L("Password change required|يجب تغيير كلمة المرور")); }
  const ct = r.headers.get("content-type") || "";
  const d = ct.includes("json") ? await r.json() : await r.text();
  if (!r.ok || (d && d.ok === false)) throw new Error((d && d.error) || r.statusText);
  return d;
}
function toast(msg, err) { const t = document.createElement("div"); t.className = "toast" + (err ? " e" : ""); t.textContent = msg; $("#toasts").append(t); setTimeout(() => t.remove(), err ? 6000 : 3000); }
const tryDo = async (fn, okMsg) => { try { const r = await fn(); okMsg && toast(okMsg); return r; } catch (e) { toast(e.message, 1); throw e; } };

/* icons */
const IC = {
  dash: '<rect x="3" y="3" width="7" height="9" rx="2"/><rect x="14" y="3" width="7" height="5" rx="2"/><rect x="14" y="12" width="7" height="9" rx="2"/><rect x="3" y="16" width="7" height="5" rx="2"/>',
  gpu: '<rect x="2" y="6" width="20" height="12" rx="2"/><circle cx="9" cy="12" r="3"/><path d="M15 10h4M15 14h4M6 18v3M18 18v3"/>',
  stream: '<path d="M4 12a8 8 0 0 1 16 0M7.5 12a4.5 4.5 0 0 1 9 0"/><circle cx="12" cy="12" r="1.5"/><path d="M12 14v7"/>',
  profile: '<path d="M4 6h16M4 12h10M4 18h7"/><circle cx="18" cy="16" r="3"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  lb: '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v4M12 11.5 5 16.5M12 11.5l7 5"/>',
  drm: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
  proxy: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>',
  dvb: '<path d="M4 10a8 8 0 0 1 14-5M2 14a12 12 0 0 1 3-9"/><circle cx="16" cy="16" r="5"/>',
  cam: '<rect x="3" y="7" width="18" height="12" rx="2"/><path d="M8 7V5h8v2"/><circle cx="12" cy="13" r="3"/>',
  vod: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m10 9 5 3-5 3z"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  bouquet: '<path d="M4 7h16v13H4zM8 3h8v4"/>',
  pkg: '<path d="m12 3 9 5v8l-9 5-9-5V8z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  acc: '<circle cx="9" cy="8" r="3.5"/><path d="M2 20a7 7 0 0 1 14 0M17 4a3.5 3.5 0 0 1 0 7M22 20a7 7 0 0 0-4-6.3"/>',
  shield: '<path d="M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6z"/>',
  file: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/>',
  log: '<path d="M4 6h16M4 10h16M4 14h10M4 18h7"/>',
  tools: '<path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18l3 3 6.3-6.3a4 4 0 0 0 5.4-5.4l-2.5 2.5-2.5-.5-.5-2.5z"/>',
  set: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M17 6l3 3M15 8l2 2"/>',
  play: '<path d="m7 4 13 8-13 8z"/>', stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>', re: '<path d="M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5M21 12a9 9 0 0 1-15 6.7L3 16M3 21v-5h5"/>',
  edit: '<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>', del: '<path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/>', copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  plus: '<path d="M12 5v14M5 12h14"/>', eye: '<path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>', menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>', out: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  up: '<path d="M12 19V5M5 12l7-7 7 7"/>', down: '<path d="M12 5v14M5 12l7 7 7-7"/>', cpu: '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>', ram: '<rect x="2" y="7" width="20" height="10" rx="2"/><path d="M6 11v2M10 11v2M14 11v2M18 11v2"/>', net: '<path d="M7 17 17 7M7 7h10v10"/>', eyes: '<circle cx="12" cy="12" r="3"/><path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z"/>'
};
const ico = n => `<svg class="i" viewBox="0 0 24 24">${IC[n] || ""}</svg>`;

/* ---------- field schemas ---------- */
const opts = (...a) => a.map(x => Array.isArray(x) ? x : [x, x]);
const F = {
  t: (k, l, x = {}) => ({ k, l, ty: "text", ...x }), n: (k, l, x = {}) => ({ k, l, ty: "num", ...x }), b: (k, l, x = {}) => ({ k, l, ty: "bool", ...x }),
  s: (k, l, o, x = {}) => ({ k, l, ty: "sel", o, ...x }), r: (k, l, kind, x = {}) => ({ k, l, ty: "ref", kind, ...x }), m: (k, l, kind, x = {}) => ({ k, l, ty: "refs", kind, ...x }),
  a: (k, l, x = {}) => ({ k, l, ty: "area", ...x }), li: (k, l, x = {}) => ({ k, l, ty: "list", ...x }), p: (k, l, x = {}) => ({ k, l, ty: "pass", ...x }),
  d: (k, l, x = {}) => ({ k, l, ty: "date", ...x }), ints: (k, l, x = {}) => ({ k, l, ty: "ints", ...x }), kvs: (k, l, x = {}) => ({ k, l, ty: "kv", ...x }),
  rows: (k, l, f, x = {}) => ({ k, l, ty: "rows", f, ...x }), h: l => ({ ty: "head", l }),
  yn: (k, l, x = {}) => ({ k, l, ty: "yn", ...x }), g: (l, f, x = {}) => ({ k: "", l, ty: "grp", f, ...x }), fs: (k, l, x = {}) => ({ k, l, ty: "fsel", ...x }),
  urls: (k, l, x = {}) => ({ k, l, ty: "urls", ...x }), rg: (k, l, x = {}) => ({ k, l, ty: "range", ...x }), ms: (k, l, o, x = {}) => ({ k, l, ty: "ms", o, ...x }), note: (id, l) => ({ k: "", ty: "note", id, l })
};
const VENC = [F.s("codec", "Codec|الترميز", opts("copy", "h264", "hevc", "av1", "mpeg2")), F.t("size", "Size (WxH)|الدقة", { ph: "1920x1080" }), F.s("rc", "Rate control|التحكم", opts("cbr", "vbr", "cq")), F.n("bitrate", "Bitrate kbps|البت ريت"), F.n("maxrate", "Max kbps|أقصى"), F.n("cq", "CQ"), F.t("name", "Label|الاسم", { ph: "FHD" })];
const AENC = [F.s("codec", "Codec|الترميز", opts("copy", "aac", "fdk_aac", "mp3", "mp2", "ac3", "opus")), F.n("bitrate", "Bitrate kbps|البت ريت")];
const OUTF = [F.s("type", "Type|النوع", opts("hls", "hls_master", "http", "dash", "rtmp", "srt", "srt_listener", "udp", "rtp", "tcp", "rtsp", "file")), F.t("url", "URL / address|الرابط", { ph: "srt://0.0.0.0:9000 / rtmp://host/app/key" }), F.t("name", "Name|الاسم"), F.n("hls_time", "HLS segment s|مدة المقطع"), F.n("hls_list", "HLS list size|عدد المقاطع"), F.n("port", "Port|المنفذ"), F.p("passphrase", "SRT passphrase|كلمة SRT"), F.n("latency", "SRT latency ms|التأخير"), F.b("disabled", "Disabled|معطل")];

const PO = { po: 1 }, IMG = /\.(png|jpe?g|gif|webp|bmp|svg)$/i, SUBX = /\.(srt|ass|ssa|vtt|ttml|xml)$/i;
const XO = {
  vsync: opts(["", "Auto"], ["passthrough", "Pass Through"], ["cfr", "Constant Frame Rate"], ["vfr", "Variable Frame Rate"], ["drop", "Drop"]),
  hwm: opts(["full", "Full"], ["partial", "Partial"], ["encode", "Encoding Only"], ["none", "None (CPU)|بدون (CPU)"], ["qsv", "Intel QSV"]),
  vcodec: opts(["copy", "Copy"], ["mpeg2", "H.262 / MPEG-2"], ["h264", "H.264 / MPEG-4"], ["hevc", "H.265 / HEVC"], ["av1", "AV1"]),
  preset: opts(["", "Auto"], ["ultrafast", "Ultrafast"], ["superfast", "Superfast"], ["veryfast", "Veryfast"], ["faster", "Faster"], ["fast", "Fast"], ["medium", "Medium"], ["slow", "Slow"], ["slower", "Slower"], ["veryslow", "Veryslow"], ["hp", "High Performance"], ["hq", "High Quality"], ["llhp", "Low Latency High Performance"], ["llhq", "Low Latency High Quality"], ["p1", "P1 (fastest)"], ["p2", "P2"], ["p3", "P3"], ["p4", "P4 (medium)"], ["p5", "P5"], ["p6", "P6"], ["p7", "P7 (best quality)"]),
  vprof: opts(["", "Auto"], ["baseline", "Baseline"], ["main", "Main"], ["high", "High"], ["high444p", "High444P"], ["main10", "Main10"], ["main12", "Main12"], ["mainsp", "MainSP"], ["rext", "Rext"]),
  level: opts(["", "Auto"], "3.0", "3.1", "3.2", "4.0", "4.1", "4.2", "5.0", "5.1", "5.2", "6.0", "6.1", "6.2"),
  fps: opts(["", "Auto"], ["24000/1001", "23.98"], "24", "25", ["30000/1001", "29.97"], "30", "50", ["60000/1001", "59.94"], "60"),
  pix: opts(["", "Auto"], ["yuv420p", "YUV420P"], ["yuv422p", "YUV422P"], ["yuv444p", "YUV444P"]),
  dar: opts(["", "Auto"], "21:9", "17:9", "16:9", "16:10", "5:3", "3:2", "4:3", "5:4", "1:1"),
  acodec: opts(["copy", "Copy"], ["aac", "AAC"], ["mp3", "MP3"], ["mp2", "MP2"], ["fdk_aac", "AAC libfdk_aac"], ["ac3", "AC3"], ["opus", "Opus"]),
  ach: [[0, "Auto"], [1, "1"], [2, "2"], [6, "5.1"]], arate: [[0, "Auto"], ...[8000, 11025, 22050, 44100, 48000, 96000].map(x => [x, String(x)])],
  sub: opts(["none", "None|بدون"], ["copy", "Copy"], ["burn", "Overlay"], ["webvtt", "Copy Restream (WebVTT)"], ["burn_file", "Overlay from file|حرق من ملف"]),
  crange: opts(["", "Auto"], ["tv", "TV"], ["pc", "PC"]),
  cspace: opts(["", "Auto"], ["bt709", "BT.709"], ["bt470bg", "BT.470 BG"], ["bt2020nc", "BT.2020 NCL"], ["bt2020c", "BT.2020 CL"], ["smpte170m", "SMPTE 170 M"], ["smpte240m", "SMPTE 240 M"], ["smpte2085", "SMPTE 2085"], ["rgb", "RGB"], ["fcc", "FCC"], ["ycgco", "YCGCO"]),
  cprim: opts(["", "Auto"], ["bt709", "BT.709"], ["bt470m", "BT.470 M"], ["bt470bg", "BT.470 BG"], ["bt2020", "BT.2020"], ["smpte170m", "SMPTE 170 M"], ["smpte240m", "SMPTE 240 M"], ["smpte428", "SMPTE 428-1"], ["smpte431", "SMPTE 431-2"], ["smpte432", "SMPTE 432-1"], ["film", "Film"], ["jedec-p22", "JEDEC P22"]),
  ctrc: opts(["", "Auto"], ["bt709", "BT.709"], ["bt470m", "BT.470 M"], ["bt470bg", "BT.470 BG"], ["bt1361e", "BT.1361"], ["bt2020-10", "BT.2020 - 10 bit"], ["bt2020-12", "BT.2020 - 12 bit"], ["smpte170m", "SMPTE 170 M"], ["smpte240m", "SMPTE 240 M"], ["smpte2084", "SMPTE 2084"], ["smpte428", "SMPTE 428-1"], ["linear", "Linear"], ["log100", "Log"], ["log316", "Log square root"], ["iec61966-2-1", "IEC 61966-2-1"], ["iec61966-2-4", "IEC 61966-2-4"], ["arib-std-b67", "ARIB STD-B67"]),
  fflags: opts(["genpts", "genpts"], ["igndts", "igndts"], ["discardcorrupt", "discardcorrupt"], ["nobuffer", "nobuffer"], ["ignidx", "ignidx"], ["sortdts", "sortdts"], ["nofillin", "nofillin"], ["noparse", "noparse"], ["fastseek", "fastseek"]),
  out: opts(["hls", "HLS"], ["hls_master", "HLS-Master"], ["http", "HTTP"], ["rtmp", "RTMP"], ["rtmp_push", "RTMP-Push"], ["dash", "Dash"], ["rtsp", "RTSP"], ["rtp", "RTP"], ["udp", "UDP"], ["tcp", "TCP"], ["srt", "SRT"], ["srt_listener", "SRT Listener"], ["file", "FILE"]),
  sizes: ["3840x2160", "2560x1440", "1920x1080", "1600x900", "1280x720", "1024x576", "960x540", "854x480", "720x576", "720x480", "640x360", "480x270"]
};
const XVENC = [F.s("codec", "codec", XO.vcodec), F.t("size", "size", { ph: "Auto", dl: XO.sizes }), F.s("rc", "rc", opts(["cbr", "CBR"], ["vbr", "VBR"], ["cq", "CQ"])), F.n("bitrate", "bitrate kbps"), F.n("maxrate", "max kbps"), F.t("name", "label", { ph: "FHD" })];
const XAENC = [F.s("codec", "codec", XO.acodec), F.n("bitrate", "bitrate kbps")];
const XOUT = [F.s("type", "type", XO.out), F.ints("video", "Video Encoders", { ph: "0,1" }), F.ints("audio", "Audio Encoders", { ph: "0" }), F.n("hls_time", "Time"), F.n("hls_list", "List Size"), F.t("name", "Name (path)|الاسم (المسار)", { ph: "o0" }),
  F.t("url", "URL", { ph: "rtmp://host/app/key · srt://0.0.0.0:9000 · udp://239.0.0.1:1234" }), F.n("port", "Port"), F.p("passphrase", "SRT passphrase"), F.n("latency", "SRT latency ms"), F.b("disabled", "Disabled|معطل")];
const XVIDEO = p => [
  F.s(p + "video_sync", "Video Sync Method", XO.vsync, { ...PO, hint: "Pass Through keeps source timestamps|يحافظ على توقيت المصدر" }),
  F.s(p + "hwaccel", "Hardware Acceleration Method", XO.hwm, { ...PO, hint: "Full = decode+scale+encode on GPU|Full = فك وتحجيم وترميز على GPU" })];
const XVIDEO2 = p => [
  F.rows(p + "video_encoders", "Video Encoder|ترميز الفيديو", XVENC, PO),
  F.s(p + "preset", "Video Preset", XO.preset, { ...PO, hint: "x264 names are mapped to NVENC presets automatically|أسماء x264 بتتحول لـ NVENC تلقائياً" }), F.s(p + "profile", "Video Profile", XO.vprof, PO), F.s(p + "level", "Video Level", XO.level, PO),
  F.s(p + "frame_rate", "Video Frame Rate", XO.fps, { ...PO, u: "fps" }), F.n(p + "gop", "Keyframe Interval", { ...PO, u: "frames" }), F.yn(p + "deinterlace", "Video Deinterlace", PO),
  F.s(p + "pix_fmt", "Video Pixel Format", XO.pix, PO), F.s(p + "dar", "Display Aspect Ratio", XO.dar, PO),
  F.g("Overlay Image", [F.fs(p + "overlay_file", "file", { ext: IMG }), F.t(p + "overlay_x", "x", { ph: "W-w" }), F.t(p + "overlay_y", "y", { ph: "0" }), F.n(p + "overlay_opacity", "opacity", { ph: "1" })], { ...PO, more: [F.n(p + "overlay_scale", "width px")] }),
  F.g("Overlay Text", [F.t(p + "text", "text"), F.t(p + "text_x", "x"), F.t(p + "text_y", "y")], { ...PO, more: [F.n(p + "text_size", "size"), F.t(p + "text_color", "color", { ph: "white" }), F.yn(p + "text_scroll", "scroll")] })];
const XAUDIO = p => [F.rows(p + "audio_encoders", "Audio Encoder|ترميز الصوت", XAENC, PO), F.s(p + "audio_channels", "Audio Channels", XO.ach, PO), F.s(p + "audio_rate", "Audio Sample Rate", XO.arate, { ...PO, u: "Hz" }), F.rg(p + "audio_volume", "Audio Volume", { ...PO, min: 0, max: 3, step: 0.05, def: 1 })];
const XSUB = p => [F.s(p + "sub_mode", "Subtitle Encoder", XO.sub, { ...PO, hint: "Overlay = burn into picture, Copy Restream = WebVTT in HLS|Overlay = حرق على الصورة، Copy Restream = WebVTT بالـ HLS" }),
  F.g("Subtitle Options|خيارات الترجمة", [F.fs(p + "sub_file", "file", { ext: SUBX }), F.t(p + "sub_lang", "lang", { ph: "ar" }), F.n(p + "sub_font_size", "font"), F.t(p + "txt_page", "teletext")], PO)];
const XMORE = p => [F.t(p + "tune", "Video Tune", { ...PO, ph: "ll / zerolatency" }), F.n(p + "bframes", "B-Frames", PO), F.yn(p + "audio_normalize", "Audio Normalize", PO), F.n(p + "audio_delay", "Audio Delay", { ...PO, u: "ms" }), F.a(p + "extra_args", "Extra ffmpeg args|خيارات ffmpeg إضافية", PO)];
const XN = (o, v) => (o.find(([a]) => String(a) === String(v ?? "")) || [, v || ""])[1].split("|")[0];
const isCp = x => (x.transcode?.video_encoders || []).every(v => v.codec === "copy");
const tagc = t => { let h = 0; for (const c of t) h = (h * 31 + c.charCodeAt(0)) % 360; return `<span class="tg" style="background:hsl(${h} 45% 52%)">${esc(t.replace(/\s+/g, " ").trim())}</span>`; };
const TRANS = p => [
  F.h("Video|الفيديو"), F.s(p + "hwaccel", "Acceleration|التسريع", opts(["full", "NVIDIA full (decode+encode)|NVIDIA كامل"], ["encode", "NVIDIA encode only|ترميز NVIDIA فقط"], ["partial", "NVIDIA partial|NVIDIA جزئي"], ["qsv", "Intel QSV"], ["none", "CPU"])),
  F.rows(p + "video_encoders", "Video variants (multi-bitrate)|نسخ الفيديو (عدة جودات)", VENC),
  F.s(p + "preset", "Preset", opts("", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "ultrafast", "veryfast", "fast", "medium", "slow")), F.s(p + "profile", "Profile", opts("", "baseline", "main", "high", "main10")), F.t(p + "level", "Level"), F.t(p + "tune", "Tune", { ph: "ll / zerolatency" }),
  F.t(p + "frame_rate", "FPS"), F.n(p + "gop", "GOP"), F.n(p + "bframes", "B-frames"), F.b(p + "deinterlace", "Deinterlace|إزالة التشابك"), F.t(p + "pix_fmt", "Pixel format"), F.t(p + "dar", "Aspect (DAR)"), F.t(p + "video_map", "Video map", { ph: "0:v:0" }), F.t(p + "video_sync", "Video sync"),
  F.h("Overlay & text|اللوغو والنص"), F.t(p + "overlay_file", "Logo file|ملف اللوغو"), F.t(p + "overlay_x", "Logo X"), F.t(p + "overlay_y", "Logo Y"), F.n(p + "overlay_opacity", "Opacity 0-1|الشفافية"), F.n(p + "overlay_scale", "Logo width px|عرض اللوغو"),
  F.t(p + "text", "Text|النص"), F.t(p + "text_x", "Text X"), F.t(p + "text_y", "Text Y"), F.n(p + "text_size", "Font size|حجم الخط"), F.t(p + "text_color", "Color|اللون"), F.b(p + "text_scroll", "Scroll text|نص متحرك"),
  F.n(p + "delogo_x", "Delogo X"), F.n(p + "delogo_y", "Delogo Y"), F.n(p + "delogo_w", "Delogo W"), F.n(p + "delogo_h", "Delogo H"),
  F.h("Audio|الصوت"), F.rows(p + "audio_encoders", "Audio tracks|مسارات الصوت", AENC), F.t(p + "audio_map", "Audio map", { ph: "0:a" }), F.n(p + "audio_channels", "Channels|القنوات"), F.n(p + "audio_rate", "Sample rate"), F.n(p + "audio_volume", "Volume x|مستوى الصوت"), F.b(p + "audio_normalize", "Loudness normalize|توحيد الصوت"), F.n(p + "audio_delay", "Audio delay ms|تأخير الصوت"),
  F.h("Subtitles|الترجمة"), F.s(p + "sub_mode", "Subtitle mode|وضع الترجمة", opts(["none", "None|بدون"], ["copy", "Pass-through|تمرير"], ["webvtt", "WebVTT in HLS|WebVTT بالـ HLS"], ["burn", "Burn-in from stream|حرق من المصدر"], ["burn_file", "Burn-in from file|حرق من ملف"])), F.t(p + "sub_map", "Subtitle map"), F.t(p + "sub_file", "Subtitle file|ملف الترجمة"), F.t(p + "sub_lang", "Language|اللغة"), F.n(p + "sub_font_size", "Font size|حجم الخط"), F.t(p + "txt_page", "Teletext page"),
  F.h("MPEG-TS & color|MPEG-TS والألوان"), F.t(p + "pid_map", "PID map"), F.t(p + "service_name", "Service name"), F.t(p + "service_provider", "Provider"), F.t(p + "color_range", "Color range"), F.t(p + "colorspace", "Colorspace"), F.t(p + "color_primaries", "Primaries"), F.t(p + "color_trc", "TRC"), F.b(p + "remove_cc", "Remove CC|حذف CC"),
  F.a(p + "extra_args", "Extra ffmpeg args|خيارات ffmpeg إضافية", { w: 1 })
];
const GPUSEL = () => { const c = (S.dash?.gpu?.cards || []).map(g => [g.index, `GPU ${g.index} · ${g.name}`]); return [[-2, "Auto (load balancer)|تلقائي (موزع الحمل)"], [-1, "CPU"], ...c]; };
const SCHEMAS = {
  stream: {
    xc: 1, tabs: {
      "_src": [F.t("name", "Stream Name|اسم القناة", { req: 1 }), F.urls("inputs", "Source Stream|المصدر", { hint: "First URL is main, others are fallbacks · ⇄ make main · ⚙ source options|الأول رئيسي والباقي احتياط · ⇄ اجعله رئيسي · ⚙ خيارات المصدر" }), F.ms("fflags", "Format Flags", XO.fflags, { hint: "ffmpeg -fflags" }), F.yn("rate_emulation", "Rate Emulation", { hint: "Read input at native rate (-re), for files|قراءة بسرعة البث الأصلية (-re) للملفات" }),
        F.b("return_main", "Return to main source|الرجوع للمصدر الرئيسي", { cls: "srcopt" }), F.t("user_agent", "User-Agent", { cls: "srcopt" }), F.t("referer", "Referer", { cls: "srcopt" }), F.a("headers", "Headers (Key: Value)|الهيدرز", { cls: "srcopt" }), F.t("proxy", "Input proxy|بروكسي المصدر", { cls: "srcopt", ph: "http://user:pass@host:port · socks5://host:1080 · pool:name" }), F.t("bind_ip", "Bind IP|IP الخروج", { cls: "srcopt" }), F.t("dns", "DNS / DoH", { cls: "srcopt", ph: "1.1.1.1" }), F.b("relay", "Stream proxy / relay (no transcode)|ريلاي بدون ترانسكود", { cls: "srcopt" }),
        F.b("rtsp_tcp", "RTSP over TCP", { cls: "srcopt" }), F.b("loop_file", "Loop file|تكرار الملف", { cls: "srcopt" }), F.n("probe_size", "Probe size", { cls: "srcopt" }), F.n("analyze", "Analyze duration µs", { cls: "srcopt" }), F.fs("offair_file", "Off-air file|ملف البديل", { cls: "srcopt" })],
      "_video": [F.r("profile_id", "Stream Profile|بروفايل القناة", "profile", { hint: "Pick a profile created in PROFILE; its video/audio settings replace the fields below|اختار بروفايل من قسم PROFILE وإعداداته بتنطبق بدل الحقول تحت" }), F.note("pnote", "Video & audio settings come from profile <b></b> — choose (—) to set them here.|إعدادات الفيديو والصوت مأخوذة من البروفايل <b></b> — اختار (—) لتعدّلها هون."),
        F.r("server_id", "Server|السيرفر", "server", { none: "Master" }), F.t("transcode.video_map", "Video Map", { ph: "v:0", hint: "Video stream to use|مسار الفيديو" }), ...XVIDEO("transcode."), F.s("gpu", "Hardware Acceleration Device", GPUSEL, { hint: "Auto = GPU load balancer|Auto = موزع حمل GPU" }), ...XVIDEO2("transcode."),
        F.g("Video Delogo", [F.n("transcode.delogo_x", "x"), F.n("transcode.delogo_y", "y"), F.n("transcode.delogo_w", "width"), F.n("transcode.delogo_h", "height")])],
      "_audio": [F.t("transcode.audio_map", "Audio Map", { ph: "a" }), ...XAUDIO("transcode.")],
      "_sub": [F.t("transcode.sub_map", "Subtitle Map", { ph: "s:0?" }), ...XSUB("transcode.")],
      "Advanced Options|خيارات متقدمة": [F.t("transcode.pid_map", "PID Map"), F.t("transcode.service_name", "Service Name"), F.t("transcode.service_provider", "Service Provider"), F.s("transcode.color_range", "Color Range", XO.crange),
        F.g("Color Space", [F.s("transcode.colorspace", "colorspace", XO.cspace), F.s("transcode.color_primaries", "color primaries", XO.cprim), F.s("transcode.color_trc", "color trc", XO.ctrc)]),
        F.g("Memory Limit", [F.n("mem_max_mb", "max system memory MB"), F.n("gpu_mem_max_mb", "max graphics memory MB")]), F.g("Speed Limit", [F.n("speed_min", "min speed")]), F.r("balancer_id", "Balancers", "balancer", { none: "None" }), F.yn("transcode.remove_cc", "Remove CC"), ...XMORE("transcode.")],
      "_out": [F.rows("outputs", "Add Output|إضافة مخرج", XOUT)],
      "General|عام": [F.r("category", "Category|التصنيف", "category"), F.s("type", "Type|النوع", opts(["linear", "Live|مباشر"], ["event", "Event|حدث"], ["vod", "VOD"])), F.yn("enabled", "Enabled (always on)|مفعّل (دائماً)"), F.yn("on_demand", "On demand|عند الطلب"), F.fs("logo", "Logo|اللوغو", { ext: IMG }), F.t("epg_id", "EPG ID"), F.n("order", "Order|الترتيب"), F.a("note", "Note|ملاحظة")],
      "DRM & subtitles|DRM والترجمة": [F.b("drm.enabled", "DRM source (DASH/HLS CENC)|مصدر DRM"), F.s("drm.vendor", "System|النظام", opts("clearkey", "widevine", "playready")), F.s("drm.mode", "Key mode|المفاتيح", opts(["keys", "Static KID:KEY|مفاتيح ثابتة"], ["keyserver", "Key server URL|سيرفر مفاتيح"], ["script", "Script"])), F.li("drm_keys", "KID:KEY (one per line)|المفاتيح KID:KEY", { w: 1, mono: 1 }), F.t("drm.key_server", "Key server URL ({kid} {pssh} {id})|رابط المفاتيح"), F.t("drm.license_url", "License URL"), F.kvs("drm.headers", "Request headers|الهيدرز", { w: 1 }),
        F.t("drm.video", "Video track|مسار الفيديو", { ph: "best / worst / id" }), F.t("drm.audio", "Audio tracks|مسارات الصوت", { ph: "all / first / ar" }), F.h("Embedded subtitles|الترجمة المدمجة"), F.t("drm.subtitles", "Subtitles (WebVTT/TTML from manifest)|الترجمة من الرابط", { ph: "all / none / ar / en" }), F.b("drm.sub_burn", "Burn into video|حرق على الصورة"), F.t("drm.sub_translate", "Live translate to|ترجمة فورية إلى", { ph: "ar" }),
        F.h("Timing & proxies|التوقيت والبروكسي"), F.n("drm.live_delay", "Live delay (segments)|التأخير عن البث"), F.n("drm.stall_sec", "Stall timeout s|مهلة التوقف"), F.t("drm.heartbeat", "Heartbeat URL"), F.n("drm.heartbeat_sec", "Heartbeat every s"), F.t("drm.proxy_manifest", "Manifest proxy|بروكسي المانيفست"), F.t("drm.proxy_media", "Media proxy|بروكسي الميديا"), F.t("drm.proxy_keys", "Keys proxy|بروكسي المفاتيح")],
      "Watchdog & schedule|المراقبة والجدولة": [F.b("watchdog", "Freeze / black / silence watchdog|مراقبة التجمد والسواد والصمت"), F.b("record_ts", "Record TS|تسجيل"), F.d("start_at", "Event start|بداية الحدث"), F.d("end_at", "Event end|نهاية الحدث"), F.n("daily_start", "Daily start (min, -1 off)|بداية يومية"), F.n("daily_end", "Daily end (min)|نهاية يومية"), F.b("restart.enabled", "Scheduled restart|إعادة تشغيل مجدولة"), F.ints("restart.hours", "Hours|الساعات", { ph: "4,16" }), F.ints("restart.minutes", "Minutes|الدقائق", { ph: "0" }), F.ints("restart.weekdays", "Weekdays 0-6|الأيام")]
    }, def: { enabled: true, type: "linear", gpu: -2, inputs: [], transcode: { hwaccel: "full", video_encoders: [{ codec: "h264", rc: "cbr", bitrate: 4000, name: "FHD" }], audio_encoders: [{ codec: "aac", bitrate: 128 }], preset: "medium", profile: "high", gop: 50 }, outputs: [{ type: "hls", hls_time: 2, hls_list: 6 }], drm: { vendor: "clearkey", mode: "keys", subtitles: "all", audio: "all", video: "best" }, watchdog: true, category: 0, daily_start: -1, daily_end: -1 }
  },
  profile: {
    xc: 1, tabs: {
      "_p": [F.t("name", "Profile Name|اسم البروفايل", { req: 1 }), F.r("server_id", "Server|السيرفر", "server", { none: "Master" }), ...XVIDEO("transcode."), F.s("gpu", "Hardware Acceleration Device", () => [[-3, "From stream|حسب القناة"], ...GPUSEL()]), ...XVIDEO2("transcode.")],
      "_a": XAUDIO("transcode."), "_s": [XSUB("transcode.")[0]], "Advanced Options|خيارات متقدمة": [XSUB("transcode.")[1], ...XMORE("transcode.")]
    }, def: { gpu: -2, transcode: { hwaccel: "full", video_encoders: [{ codec: "h264", rc: "cbr", bitrate: 4000 }], audio_encoders: [{ codec: "aac", bitrate: 128 }], preset: "medium", profile: "high", gop: 50, sub_mode: "copy" } },
    cols: [[x => `<span class="nb">${esc(x.name)}</span>`, "Name|الاسم"], [x => `<span class="tg">${esc(x.server_id ? refName("server", x.server_id) : "Master")}</span>`, "Server|السيرفر"],
      [x => { const t = x.transcode || {}, cp = (t.video_encoders || []).every(v => v.codec === "copy"); return cp || t.hwaccel === "none" ? "" : `<span class="tg nv">${t.hwaccel === "qsv" ? "QSV" : "NVENC"}</span>${x.gpu >= 0 ? `<span class="tg">GPU${x.gpu}</span>` : ""}`; }, "HWAccel"],
      [x => (x.transcode?.video_encoders || []).map(v => v.codec === "copy" ? `<span class="tg cp">COPY</span>` : tagc(`${XN(XO.vcodec, v.codec).split(" /")[0]} ${v.size || ""} ${v.bitrate ? v.bitrate + " kbps" : ""}`)).join("<br>"), "Video Encoders"],
      [x => (x.transcode?.audio_encoders || []).map(a => a.codec === "copy" ? `<span class="tg cp">COPY</span>` : `<span class="tg au">${esc(XN(XO.acodec, a.codec))} ${a.bitrate || ""} kbps</span>`).join("<br>"), "Audio Encoders"],
      [x => esc(isCp(x) ? "-" : XN(XO.preset, x.transcode?.preset) || "Auto"), "Preset"], [x => esc(isCp(x) ? "-" : XN(XO.vprof, x.transcode?.profile) || "Auto"), "Profile"],
      [x => esc(isCp(x) ? "-" : XN(XO.fps, x.transcode?.frame_rate) || "Auto"), "FPS"], [x => isCp(x) ? "-" : x.transcode?.gop || "Auto", "Keyint"]]
  },
  server: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.t("host", "Host / IP"), F.t("public_host", "Public domain|الدومين"), F.n("http_port", "HTTP port"), F.n("https_port", "HTTPS port"), F.n("ssh_port", "SSH port"), F.t("ssh_user", "SSH user"), F.p("ssh_password", "SSH password"), F.n("max_clients", "Max clients|أقصى مشاهدين"), F.n("max_download", "Max down Mbps"), F.n("max_upload", "Max up Mbps"), F.b("lb_enabled", "In viewer balancer|ضمن موزع المشاهدين"), F.n("lb_weight", "LB weight|الوزن"), F.b("disabled", "Disabled|معطل"), F.a("note", "Note|ملاحظة", { w: 1 })], def: { http_port: 8080, ssh_port: 22, ssh_user: "root", lb_weight: 1 }, cols: [["name", "Name|الاسم"], ["host", "Host"], ["public_host", "Domain|الدومين"], [x => x.lb_enabled ? '<span class="chip ok">LB</span>' : "", "LB"], [x => x.disabled ? '<span class="chip err">off</span>' : '<span class="chip ok">on</span>', "State|الحالة"]] },
  balancer: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.s("type", "Type|النوع", opts(["gpu", "GPU (place streams)|GPU (توزيع القنوات)"], ["viewer", "Viewers (HTTP redirect)|المشاهدين"])), F.s("strategy", "Strategy|الطريقة", opts(["least_load", "Least load|الأقل حمل"], ["round_robin", "Round robin|بالدور"], ["fill_first", "Fill first|تعبئة الأول"], ["least_conn", "Least connections|أقل اتصالات"], ["weighted", "Weighted|بالوزن"])), F.m("servers", "Servers|السيرفرات", "server", { w: 1 }), F.n("max_enc", "Max NVENC %|أقصى ترميز"), F.n("max_mem", "Max VRAM %|أقصى ذاكرة"), F.n("max_temp", "Max temp °C|أقصى حرارة"), F.n("max_per_gpu", "Max streams / GPU|أقصى قنوات لكل كرت"), F.b("cpu_fallback", "CPU fallback when GPUs full|التحويل للمعالج"), F.b("auto_move", "Auto-migrate on failure|نقل تلقائي عند العطل"), F.b("enabled", "Enabled|مفعل")], def: { type: "gpu", strategy: "least_load", max_enc: 90, max_mem: 92, max_temp: 85, enabled: true, auto_move: true }, cols: [["name", "Name|الاسم"], [x => `<span class="chip pri">${esc(x.type)}</span>`, "Type|النوع"], ["strategy", "Strategy|الطريقة"], [x => (x.servers || []).length || L("local|محلي"), "Servers|السيرفرات"], [x => x.enabled ? '<span class="chip ok">on</span>' : '<span class="chip">off</span>', "State|الحالة"]] },
  drmkey: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.t("kid", "KID (hex)", { mono: 1 }), F.p("key", "KEY (hex)"), F.t("content_id", "Content ID"), F.a("pssh", "PSSH", { w: 1 }), F.a("note", "Note|ملاحظة", { w: 1 })], cols: [["name", "Name|الاسم"], [x => `<span class="mono">${esc(x.kid)}</span>`, "KID"], ["content_id", "Content ID"]] },
  proxy: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.s("type", "Type|النوع", opts("http", "https", "socks5")), F.t("host", "Host"), F.n("port", "Port"), F.t("username", "Username|المستخدم"), F.p("password", "Password|كلمة المرور"), F.t("bind_ip", "Bind IP"), F.t("pool", "Pool (rotation group)|مجموعة التدوير"), F.b("disabled", "Disabled|معطل"), F.a("note", "Note|ملاحظة", { w: 1 })], def: { type: "http" }, cols: [["name", "Name|الاسم"], [x => `<span class="chip pri">${esc(x.type)}</span>`, "Type|النوع"], [x => esc(`${x.host}:${x.port}`), "Address|العنوان"], ["pool", "Pool|المجموعة"]] },
  adapter: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.r("server_id", "Server|السيرفر", "server"), F.n("device", "Adapter #"), F.s("system", "System", opts("DVB-S", "DVB-S2", "DVB-T", "DVB-T2", "DVB-C", "ATSC")), F.s("modulation", "Modulation", opts("AUTO", "QPSK", "8PSK", "16APSK", "32APSK", "QAM16", "QAM64", "QAM256")), F.n("frequency", "Frequency MHz"), F.n("symbol_rate", "Symbol rate"), F.n("voltage", "LNB voltage", {}), F.t("diseqc", "DiSEqC"), F.t("fec", "FEC"), F.t("pilot", "Pilot"), F.t("rolloff", "Rolloff"), F.t("inversion", "Inversion"), F.b("budget", "Budget mode"), F.t("pls_mode", "PLS mode"), F.n("pls_code", "PLS code"), F.n("stream_id", "Stream ID"), F.t("charset", "Charset"), F.r("softcam_id", "SoftCAM", "softcam"), F.t("udp_addr", "UDP output", { ph: "239.0.0.1:1234" }), F.b("enabled", "Enabled|مفعل")], def: { system: "DVB-S2", modulation: "AUTO", enabled: true }, cols: [["name", "Name|الاسم"], ["system", "System"], ["frequency", "Freq"], ["symbol_rate", "SR"], ["udp_addr", "UDP"]] },
  softcam: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.s("protocol", "Protocol", opts("cs378x", "newcamd")), F.t("host", "Host"), F.n("port", "Port"), F.t("username", "Username|المستخدم"), F.p("password", "Password|كلمة المرور"), F.t("caid", "CAID"), F.p("des_key", "DES key")], def: { protocol: "cs378x" }, cols: [["name", "Name|الاسم"], ["protocol", "Protocol"], [x => esc(`${x.host}:${x.port}`), "Address|العنوان"], ["caid", "CAID"]] },
  category: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.s("type", "Type|النوع", opts(["live", "Live|مباشر"], ["movie", "Movies|أفلام"], ["series", "Series|مسلسلات"]))], def: { type: "live" }, cols: [["name", "Name|الاسم"], ["type", "Type|النوع"]] },
  movie: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.r("category", "Category|التصنيف", "category"), F.t("source", "Source URL / path|المصدر", { w: 1 }), F.r("profile_id", "Transcode profile|البروفايل", "profile"), F.r("server_id", "Server|السيرفر", "server"), F.t("poster", "Poster URL|البوستر"), F.n("year", "Year|السنة"), F.n("rating", "Rating|التقييم"), F.n("tmdb", "TMDB ID"), F.t("sub_file", "Subtitle file|ملف الترجمة"), F.a("plot", "Plot|القصة", { w: 1 })], cols: [["name", "Name|الاسم"], ["year", "Year|السنة"], [x => esc(x.status || "—"), "Status|الحالة"]] },
  series: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.r("category", "Category|التصنيف", "category"), F.t("poster", "Poster URL|البوستر"), F.n("year", "Year|السنة"), F.n("tmdb", "TMDB ID"), F.a("plot", "Plot|القصة", { w: 1 })], cols: [["name", "Name|الاسم"], ["year", "Year|السنة"]] },
  episode: { f: [F.r("series_id", "Series|المسلسل", "series"), F.n("season", "Season|الموسم"), F.n("number", "Episode|الحلقة"), F.t("name", "Name|الاسم"), F.t("source", "Source|المصدر", { w: 1 }), F.r("profile_id", "Profile|البروفايل", "profile"), F.r("server_id", "Server|السيرفر", "server")], def: { season: 1, number: 1 }, cols: [[x => esc(refName("series", x.series_id)), "Series|المسلسل"], [x => `S${x.season}E${x.number}`, "#"], ["name", "Name|الاسم"]] },
  user: { f: [F.t("username", "Username|اسم المستخدم", { req: 1 }), F.t("password", "Password|كلمة المرور", { req: 1 }), F.r("package_id", "Package|الباقة", "package"), F.d("expire", "Expires (empty = never)|ينتهي"), F.n("max_connections", "Max connections|أقصى اتصالات"), F.s("max_resolution", "Max resolution|أقصى دقة", opts([0, "Any|الكل"], [480, "480p"], [720, "720p"], [1080, "1080p"], [2160, "4K"])), F.m("bouquets", "Bouquets|البوكيهات", "bouquet", { w: 1 }), F.t("allowed_ips", "Allowed IPs (comma)|IPs مسموحة"), F.b("enabled", "Enabled|مفعل"), F.b("trial", "Trial|تجريبي"), F.a("note", "Note|ملاحظة", { w: 1 })], def: { enabled: true, max_connections: 1 }, gen: 1,
    cols: [["username", "Username|المستخدم"], [x => esc(refName("package", x.package_id)), "Package|الباقة"], [x => x.expire ? (x.expire * 1000 < Date.now() ? `<span class="chip err">${fmtTime(x.expire)}</span>` : fmtTime(x.expire)) : "∞", "Expires|ينتهي"], ["max_connections", "Conn|اتصالات"], [x => fmtTime(x.last_seen), "Last seen|آخر ظهور"], [x => x.enabled ? '<span class="chip ok">on</span>' : '<span class="chip err">off</span>', "State|الحالة"]] },
  bouquet: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.m("streams", "Channels|القنوات", "stream", { w: 1 }), F.m("movies", "Movies|الأفلام", "movie", { w: 1 }), F.m("series", "Series|المسلسلات", "series", { w: 1 })], cols: [["name", "Name|الاسم"], [x => (x.streams || []).length, "Channels|القنوات"], [x => (x.movies || []).length, "Movies|الأفلام"], [x => (x.series || []).length, "Series|المسلسلات"]] },
  package: { f: [F.t("name", "Name|الاسم", { req: 1 }), F.n("credits", "Credits|الكريدت"), F.n("duration", "Duration|المدة"), F.s("unit", "Unit|الوحدة", opts(["hours", "Hours|ساعات"], ["days", "Days|أيام"], ["months", "Months|أشهر"], ["years", "Years|سنوات"])), F.n("max_connections", "Max connections|أقصى اتصالات"), F.s("max_resolution", "Max resolution|أقصى دقة", opts([0, "Any|الكل"], [720, "720p"], [1080, "1080p"], [2160, "4K"])), F.m("bouquets", "Bouquets|البوكيهات", "bouquet", { w: 1 }), F.b("trial", "Trial|تجريبي")], def: { duration: 1, unit: "months", max_connections: 1, credits: 1 }, cols: [["name", "Name|الاسم"], [x => `${x.duration} ${esc(x.unit)}`, "Duration|المدة"], ["credits", "Credits|الكريدت"], ["max_connections", "Conn|اتصالات"]] },
  account: { f: [F.t("username", "Username|اسم المستخدم", { req: 1 }), F.p("password", "Password (leave empty to keep)|كلمة المرور (فارغ = بدون تغيير)"), F.t("email", "Email"), F.s("role", "Role|الصلاحية", opts(["admin", "Admin|مدير"], ["reseller", "Reseller|موزع"], ["subreseller", "Sub-reseller|موزع فرعي"])), F.n("credits", "Credits|الكريدت"), F.b("disabled", "Disabled|معطل"), F.a("note", "Note|ملاحظة", { w: 1 })], def: { role: "reseller" }, cols: [["username", "Username|المستخدم"], [x => `<span class="chip pri">${esc(x.role)}</span>`, "Role|الصلاحية"], ["credits", "Credits|الكريدت"], [x => fmtTime(x.last_login), "Last login|آخر دخول"], ["last_ip", "IP"]] },
  iprule: { f: [F.t("cidr", "IP / CIDR", { req: 1, ph: "1.2.3.4/32" }), F.s("action", "Action|الإجراء", opts(["block", "Block|حظر"], ["allow", "Allow (whitelist)|سماح"])), F.s("scope", "Scope|النطاق", opts(["all", "All|الكل"], ["panel", "Panel only|اللوحة"], ["stream", "Streams only|البث"])), F.t("note", "Note|ملاحظة")], def: { action: "block", scope: "all" }, cols: [[x => `<span class="mono">${esc(x.cidr)}</span>`, "CIDR"], [x => `<span class="chip ${x.action === "allow" ? "ok" : "err"}">${esc(x.action)}</span>`, "Action|الإجراء"], ["scope", "Scope|النطاق"], ["note", "Note|ملاحظة"]] }
};
const refName = (kind, id) => (S.cache[kind] || []).find(x => x.id === id)?.[kind === "user" || kind === "account" ? "username" : "name"] || (id ? "#" + id : "—");
async function loadRef(kind, force) { if (!S.cache[kind] || force) S.cache[kind] = await api(kind).catch(() => []); return S.cache[kind]; }

/* ---------- router & shell ---------- */
const NAV = [
  ["", [["dash", "dash", "Dashboard|لوحة التحكم"], ["gpu", "gpu", "GPUs|كروت الشاشة"]]],
  ["Streaming|البث", [["streams", "stream", "Streams|القنوات"], ["profile", "profile", "Profiles|البروفايلات"], ["drmkey", "drm", "DRM keys|مفاتيح DRM"], ["proxy", "proxy", "Proxies|البروكسيات"], ["adapter", "dvb", "DVB"], ["softcam", "cam", "SoftCAM"], ["category", "log", "Categories|التصنيفات"]]],
  ["VOD", [["movie", "vod", "Movies|الأفلام"], ["series", "vod", "Series|المسلسلات"], ["episode", "vod", "Episodes|الحلقات"]]],
  ["Infrastructure|البنية", [["server", "server", "Servers|السيرفرات"], ["balancer", "lb", "Load balancer|موزع الحمل"], ["iprule", "shield", "Firewall (NGINX)|الجدار الناري"]]],
  ["Subscribers|المشتركين", [["user", "user", "Users|المستخدمين"], ["bouquet", "bouquet", "Bouquets|البوكيهات"], ["package", "pkg", "Packages|الباقات"], ["account", "acc", "Accounts|الحسابات", 1]]],
  ["System|النظام", [["files", "file", "Files|الملفات"], ["logs", "log", "Logs|السجلات"], ["tools", "tools", "Tools & update|الأدوات والتحديث", 1], ["settings", "set", "Settings|الإعدادات", 1]]]
];
const TITLES = {}; NAV.forEach(g => g[1].forEach(x => TITLES[x[0]] = x[2]));
const route = () => location.hash.slice(2) || "dash";
window.addEventListener("hashchange", () => S.me && page());

function applyPrefs() { document.documentElement.dir = S.lang === "ar" ? "rtl" : "ltr"; document.documentElement.lang = S.lang; document.documentElement.dataset.theme = S.theme; }
async function boot() {
  applyPrefs();
  S.ver = await fetch("/api/version").then(r => r.json()).catch(() => ({}));
  try { S.me = await api("me"); } catch { S.me = null; }
  render();
}
function render() {
  if (!S.me) return loginView();
  const isAdmin = S.me.role === "admin";
  $("#app").innerHTML = `<svg width="0" height="0" style="position:absolute"><defs><linearGradient id="rg"><stop offset="0" stop-color="#1a6dff"/><stop offset="1" stop-color="#e6007e"/></linearGradient></defs></svg>
  <div class="shell"><aside class="side" id="side"><div class="brand"><img src="logo.svg" alt=""><div><span class="grad">Master GPU</span><small>v${esc(S.ver.version || "1.0")} · ${esc(S.ver.build || "")}</small></div></div>
  <nav class="nav">${NAV.map(([g, items]) => (g ? `<div class="grp">${L(g)}</div>` : "") + items.filter(x => !x[3] || isAdmin).map(([r, i, t]) => `<a href="#/${r}" data-r="${r}">${ico(i)}<span>${L(t)}</span></a>`).join("")).join("")}</nav></aside>
  <main class="main"><div class="top"><button class="btn i burger" id="burger">${ico("menu")}</button><h1 id="title"></h1>
  <button class="btn s" id="lang">${S.lang === "ar" ? "EN" : "ع"}</button><button class="btn i" id="theme" title="theme">${ico("sun")}</button>
  <button class="btn s" id="meBtn">${ico("user")}<span>${esc(S.me.username)}</span></button><button class="btn i" id="logout" title="logout">${ico("out")}</button></div>
  <div class="page" id="page"></div></main></div>`;
  $("#burger").onclick = () => $("#side").classList.toggle("open");
  $("#lang").onclick = () => { S.lang = localStorage.lang = S.lang === "ar" ? "en" : "ar"; applyPrefs(); render(); };
  $("#theme").onclick = () => { S.theme = localStorage.theme = S.theme === "dark" ? "light" : "dark"; applyPrefs(); };
  $("#logout").onclick = async () => { await api("logout", { method: "POST" }).catch(() => { }); S.me = null; S.ws?.close(); render(); };
  $("#meBtn").onclick = () => passwordModal(false);
  connectWS();
  page();
  if (S.me.must_change) passwordModal(true);
}
function page() {
  const r = route();
  $$(".nav a").forEach(a => a.classList.toggle("on", a.dataset.r === r));
  $("#side")?.classList.remove("open");
  $("#title").textContent = L(TITLES[r] || r);
  S.onLive = null; const el = $("#page"); el.innerHTML = "";
  const v = VIEWS[r] || (SCHEMAS[r] ? () => crudView(r) : VIEWS.dash);
  Promise.resolve(v(el)).catch(e => e.message !== "unauthorized" && toast(e.message, 1));
}

/* ---------- login & password ---------- */
function loginView() {
  $("#app").innerHTML = `<div class="login"><form class="card" id="lf"><div class="logo"><img src="logo.svg" alt=""><h2 class="grad">Master GPU</h2><p>${L("GPU transcoding & streaming panel|لوحة ترانسكود وبث GPU")} · v${esc(S.ver.version || "1.0")}</p></div>
  <div class="f"><label>${L("Username|اسم المستخدم")}</label><input class="in" name="u" autocomplete="username" autofocus></div>
  <div class="f"><label>${L("Password|كلمة المرور")}</label><input class="in" name="p" type="password" autocomplete="current-password"></div>
  <div class="err" id="le"></div><button class="btn p">${L("Sign in|تسجيل الدخول")}</button>
  <p style="text-align:center;margin-top:16px"><a href="#" id="ll">${S.lang === "ar" ? "English" : "العربية"}</a></p></form></div>`;
  $("#ll").onclick = e => { e.preventDefault(); S.lang = localStorage.lang = S.lang === "ar" ? "en" : "ar"; applyPrefs(); loginView(); };
  $("#lf").onsubmit = async e => {
    e.preventDefault(); const f = e.target; $("#le").textContent = "";
    try { const r = await api("login", { method: "POST", body: { username: f.u.value.trim(), password: f.p.value } }); S.me = r.account; location.hash = "#/dash"; render(); }
    catch (err) { $("#le").textContent = err.message; }
  };
}
function passwordModal(forced) {
  if ($("#pwm")) return;
  const m = modal(`<div class="hd">${ico("key")} ${L("Change login|تغيير بيانات الدخول")}</div><div class="bd">
   ${forced ? `<p class="chip warn" style="display:block;margin:0 0 14px;padding:10px">${L("For security, change the default admin/admin login now.|لأمان اللوحة غيّر بيانات الدخول الافتراضية admin/admin الآن.")}</p>` : ""}
   <div class="f" style="margin-bottom:12px"><label>${L("Current password|كلمة المرور الحالية")}</label><input class="in" type="password" id="pc" autocomplete="current-password"></div>
   <div class="f" style="margin-bottom:12px"><label>${L("Username|اسم المستخدم")}</label><input class="in" id="pu" value="${esc(S.me.username)}"></div>
   <div class="f" style="margin-bottom:12px"><label>${L("New password (min 6)|كلمة المرور الجديدة (6 أحرف على الأقل)")}</label><input class="in" type="password" id="pn" autocomplete="new-password"></div>
   <div class="f"><label>${L("Confirm|تأكيد")}</label><input class="in" type="password" id="pn2" autocomplete="new-password"></div>
   ${!forced ? `<div class="f" style="margin-top:16px"><label>API key (X-API-Key)</label><div style="display:flex;gap:8px"><input class="in mono ltr" readonly value="${esc(S.me.token || "")}" id="tok"><button class="btn s" id="rt">${L("Regenerate|تجديد")}</button></div></div>` : ""}
   <div class="err" id="pe" style="margin-top:10px"></div></div>
   <div class="ft">${forced ? "" : `<button class="btn" data-x>${L("Cancel|إلغاء")}</button>`}<button class="btn p" id="ps">${L("Save|حفظ")}</button></div>`, "sm", forced);
  m.id = "pwm";
  $("#rt", m) && ($("#rt", m).onclick = async () => { const r = await tryDo(() => api("me/token", { method: "POST" }), L("New API key created|تم إنشاء مفتاح جديد")); $("#tok", m).value = r.token; S.me.token = r.token; });
  $("#ps", m).onclick = async () => {
    const pn = $("#pn", m).value, pe = $("#pe", m);
    if (pn !== $("#pn2", m).value) return pe.textContent = L("Passwords do not match|كلمتا المرور غير متطابقتين");
    try { await api("me/password", { method: "POST", body: { current: $("#pc", m).value, username: $("#pu", m).value.trim(), password: pn } }); S.me = await api("me"); m.close(); toast(L("Login updated|تم تحديث بيانات الدخول")); render(); }
    catch (e) { pe.textContent = e.message; }
  };
}

/* ---------- modal & form engine ---------- */
function modal(html, cls = "", locked) {
  const ov = document.createElement("div"); ov.className = "ov";
  ov.innerHTML = `<div class="card modal ${cls}">${html}</div>`;
  ov.close = () => { ov.remove(); ov.onclose && ov.onclose(); };
  if (!locked) { ov.addEventListener("mousedown", e => e.target === ov && ov.close()); document.addEventListener("keydown", function k(e) { if (e.key === "Escape") { ov.close(); document.removeEventListener("keydown", k); } }); }
  ov.addEventListener("click", e => e.target.closest("[data-x]") && ov.close());
  document.body.append(ov); return ov;
}
const toLocal = t => t ? new Date(t * 1000 - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : "";
async function fieldHTML(f, obj, path) {
  if (f.ty === "head") return `<div class="f w"><div style="font-weight:700;margin-top:8px;padding-bottom:6px;border-bottom:1px solid var(--line)">${L(f.l)}</div></div>`;
  const k = path || f.k, v = get(obj, f.k), id = "f_" + k.replace(/\W/g, "_"), lab = `<label for="${id}">${L(f.l)}</label>`, ph = f.ph ? ` placeholder="${esc(f.ph)}"` : "";
  const hint = f.hint ? `<span class="hq" title="${esc(L(f.hint))}">?</span>` : "", U = c => f.u ? `<div class="ig">${c}<span>${esc(f.u)}</span></div>` : c;
  const dl = f.dl ? ` list="${id}_dl"` : "", dlh = f.dl ? `<datalist id="${id}_dl">${f.dl.map(x => `<option value="${esc(x)}">`).join("")}</datalist>` : "";
  const wrap = (inner, w) => `<div class="f${f.w || w ? " w" : ""}${f.cls ? " " + f.cls : ""}" data-k="${esc(f.k)}" data-ty="${f.ty}"${f.po ? " data-po" : ""}>${inner}${hint}</div>`;
  switch (f.ty) {
    case "head": return `<div class="f w"><div style="font-weight:700;margin-top:8px;padding-bottom:6px;border-bottom:1px solid var(--line)">${L(f.l)}</div></div>`;
    case "text": case "pass": return wrap(lab + U(`<input class="in${f.mono ? " mono ltr" : ""}" id="${id}" type="${f.ty === "pass" ? "password" : "text"}" value="${esc(v ?? "")}"${ph}${dl} autocomplete="off">${dlh}`));
    case "num": return wrap(lab + U(`<input class="in" id="${id}" type="number" step="any" value="${v ?? ""}"${ph}>`));
    case "yn": return wrap(lab + `<select class="in" id="${id}"><option value="1"${v ? " selected" : ""}>${L("Yes|نعم")}</option><option value="0"${v ? "" : " selected"}>${L("No|لا")}</option></select>`);
    case "range": { const x = v || f.def || 0; return wrap(lab + `<div class="rg"><input type="range" id="${id}" min="${f.min}" max="${f.max}" step="${f.step}" value="${x}" oninput="this.nextElementSibling.textContent=this.value"><b>${x}</b></div>`); }
    case "ms": { const set = new Set(v || []); return wrap(lab + `<div class="ms">${f.o.map(([a, b]) => `<label><input type="checkbox" value="${esc(a)}"${set.has(a) ? " checked" : ""}>${esc(L(b))}</label>`).join("")}</div>`); }
    case "note": return `<div class="f w pnote" id="${f.id}" style="display:none">${L(f.l)}</div>`;
    case "urls": { const a = v && v.length ? v : [""]; return wrap(lab + `<div class="urls">${a.map(urlRow).join("")}</div>`); }
    case "fsel": { const l = (await loadFiles()).filter(x => !f.ext || f.ext.test(x.name)), cur = v || "", has = l.some(x => x.path === cur);
      return wrap(lab + `<div class="ib"><select class="in" id="${id}"><option value="">—</option>${cur && !has ? `<option value="${esc(cur)}" selected>${esc(cur.split("/").pop())}</option>` : ""}${l.map(x => `<option value="${esc(x.path)}"${x.path === cur ? " selected" : ""}>${esc(x.name)}</option>`).join("")}</select><label class="btn p" title="${L("Upload|رفع")}">${ico("up")}<input type="file" hidden data-fup></label></div>`); }
    case "grp": { let h = "", mh = ""; for (const sf of f.f) h += await fieldHTML(sf, obj); for (const sf of f.more || []) mh += await fieldHTML(sf, obj);
      return `<div class="f grp" data-k="" data-ty="grp"${f.po ? " data-po" : ""}><label>${L(f.l)}</label><div><div class="g">${h}${f.more ? `<button type="button" class="btn p gm" data-more>${ico("set")}</button>` : ""}</div>${f.more ? `<div class="g gmore" style="display:none">${mh}</div>` : ""}</div>${hint}</div>`; }
    case "bool": return wrap(`<label class="sw"><input type="checkbox" id="${id}"${v ? " checked" : ""}><i></i><span>${L(f.l)}</span></label>`);
    case "area": return wrap(lab + `<textarea class="in" id="${id}"${ph}>${esc(v ?? "")}</textarea>`);
    case "list": return wrap(lab + `<textarea class="in ltr" id="${id}"${ph}>${esc((v || []).join("\n"))}</textarea>`);
    case "ints": return wrap(lab + `<input class="in" id="${id}" value="${esc((v || []).join(","))}"${ph}>`);
    case "date": return wrap(lab + `<input class="in" id="${id}" type="datetime-local" value="${toLocal(v)}">`);
    case "kv": return wrap(lab + `<textarea class="in ltr" id="${id}" placeholder="Key: Value">${esc(Object.entries(v || {}).map(([a, b]) => a + ": " + b).join("\n"))}</textarea>`);
    case "sel": { const o = typeof f.o === "function" ? f.o() : f.o; return wrap(lab + U(`<select class="in" id="${id}">${o.map(([a, b]) => `<option value="${esc(a)}"${String(a) === String(v ?? o[0][0]) ? " selected" : ""}>${esc(L(b))}</option>`).join("")}</select>`)); }
    case "ref": { const l = await loadRef(f.kind); return wrap(lab + `<select class="in" id="${id}"><option value="0">${f.none ? esc(L(f.none)) : "—"}</option>${l.map(x => `<option value="${x.id}"${x.id === v ? " selected" : ""}>${esc(x.name || x.username)}</option>`).join("")}</select>`); }
    case "refs": { const l = await loadRef(f.kind), sel = new Set(v || []); return wrap(lab + `<input class="in" placeholder="${L("Filter…|بحث…")}" data-filter style="margin-bottom:6px"><div class="multi">${l.map(x => `<label><input type="checkbox" value="${x.id}"${sel.has(x.id) ? " checked" : ""}>${esc(x.name || x.username)}</label>`).join("") || `<span class="mut">${L("Nothing yet|لا يوجد")}</span>`}</div>`); }
    case "rows": { const arr = v || []; let h = ""; for (let i = 0; i < arr.length; i++) h += await rowHTML(f, arr[i]); return wrap(lab + `<div class="rows">${h}</div><button type="button" class="btn s" data-add>${ico("plus")} ${L("Add|إضافة")}</button>`, 1); }
  }
  return "";
}
const urlRow = (u, i) => `<div class="ib url"><div class="ul"><small>URL #${i + 1}</small><input class="in ltr" value="${esc(u)}" placeholder="http:// · https:// · srt:// · rtmp:// · udp:// · rtsp://"></div><button type="button" class="btn p" data-umain title="Main|رئيسي">⇄</button><button type="button" class="btn p" data-uopt title="Source options|خيارات المصدر">${ico("set")}</button><button type="button" class="btn" data-uadd title="+">${ico("plus")}</button>${i ? `<button type="button" class="btn d" data-urm>${ico("del")}</button>` : ""}</div>`;
const loadFiles = async () => S.cache.files || (S.cache.files = await api("files").catch(() => []));
async function rowHTML(f, o) { let h = ""; for (const sf of f.f) h += await fieldHTML(sf, o || {}); return `<div class="row"><button type="button" class="btn i s d x" data-rm>${ico("del")}</button><div class="form">${h}</div></div>`; }
function readField(el, obj) {
  const k = el.dataset.k, ty = el.dataset.ty, inp = $("input,select,textarea", el);
  let v;
  if (ty === "grp") return $$(":scope > div > .g > .f[data-k]", el).forEach(e => readField(e, obj));
  switch (ty) {
    case "yn": v = inp.value === "1"; break;
    case "range": v = +inp.value; break;
    case "ms": v = $$(".ms input:checked", el).map(x => x.value); break;
    case "urls": v = $$(".url input", el).map(x => x.value.trim()).filter(Boolean); break;
    case "fsel": v = inp.value; break;
    case "text": case "pass": case "area": v = inp.value; if (ty === "pass" && v === "" && get(obj, k) === undefined) return; break;
    case "num": v = inp.value === "" ? 0 : +inp.value; break;
    case "bool": v = inp.checked; break;
    case "list": v = inp.value.split("\n").map(s => s.trim()).filter(Boolean); break;
    case "ints": v = inp.value.split(/[,\s]+/).filter(Boolean).map(Number).filter(n => !isNaN(n)); break;
    case "date": v = inp.value ? Math.floor(new Date(inp.value).getTime() / 1000) : 0; break;
    case "kv": v = {}; inp.value.split("\n").forEach(l => { const i = l.indexOf(":"); if (i > 0) v[l.slice(0, i).trim()] = l.slice(i + 1).trim(); }); break;
    case "sel": v = inp.value; if (/^-?\d+$/.test(v) && typeof get(obj, k) !== "string") v = +v; break;
    case "ref": v = +inp.value; break;
    case "refs": v = $$(".multi input:checked", el).map(x => +x.value); break;
    case "rows": v = $$(":scope > .rows > .row", el).map(r => { const o = {}; $$(":scope > .form > .f[data-k]", r).forEach(e => readField(e, o)); return o; }); break;
    default: return;
  }
  set(obj, k, v);
}
function bindForm(root, fields) {
  root.addEventListener("click", async e => {
    const add = e.target.closest("[data-add]"), rm = e.target.closest("[data-rm]");
    if (rm) rm.closest(".row").remove();
    const ub = e.target.closest("[data-uadd],[data-urm],[data-umain],[data-uopt],[data-more]");
    if (ub) {
      const row = ub.closest(".url"), box = ub.closest(".urls"), d = ub.dataset;
      if ("uadd" in d) row.insertAdjacentHTML("afterend", urlRow("", 1));
      if ("urm" in d) row.remove();
      if ("umain" in d) box.prepend(row);
      if ("uopt" in d) $$(".srcopt", root).forEach(x => x.classList.toggle("on"));
      if ("more" in d) { const g = $(".gmore", ub.closest(".grp")); g.style.display = g.style.display === "none" ? "" : "none"; }
      if (box) $$(".url", box).forEach((r, i) => { $("small", r).textContent = "URL #" + (i + 1); const x = $("[data-urm]", r); if (!i && x) x.remove(); if (i && !x) r.insertAdjacentHTML("beforeend", `<button type="button" class="btn d" data-urm>${ico("del")}</button>`); });
    }
    if (add) { const fe = add.closest(".f"), f = findField(fields, fe.dataset.k); if (f) $(".rows", fe).insertAdjacentHTML("beforeend", await rowHTML(f, {})); }
  });
  root.addEventListener("change", async e => {
    if (!e.target.matches("[data-fup]")) return; const fl = e.target.files[0]; if (!fl) return;
    const fd = new FormData(); fd.append("file", fl); const r = await tryDo(() => api("files", { method: "POST", body: fd }), fl.name + " ✓"); if (!r) return;
    delete S.cache.files; const l = await loadFiles(), x = l.find(y => y.name === r.file), sel = $("select", e.target.closest(".ib"));
    if (x) { sel.insertAdjacentHTML("beforeend", `<option value="${esc(x.path)}">${esc(x.name)}</option>`); sel.value = x.path; }
  });
  root.addEventListener("input", e => { if (e.target.matches("[data-filter]")) { const q = e.target.value.toLowerCase(); $$(".multi label", e.target.parentNode).forEach(l => l.style.display = l.textContent.toLowerCase().includes(q) ? "" : "none"); } });
}
const findField = (fields, k) => fields.find(f => f.k === k);
async function editModal(kind, item, onSaved) {
  const sc = SCHEMAS[kind], isNew = !item?.id, obj = structuredClone(item?.id ? item : { ...(sc.def ? structuredClone(sc.def) : {}), ...(item || {}) });
  if (kind === "user" && isNew && !obj.password) { obj.username = obj.username || "u" + Math.random().toString(36).slice(2, 8); obj.password = Math.random().toString(36).slice(2, 12); }
  const tabs = sc.tabs || { "": sc.f }, names = Object.keys(tabs), all = names.flatMap(n => tabs[n]);
  const xc = sc.xc; let body = ""; for (const [i, n] of names.entries()) { let h = ""; for (const f of tabs[n]) h += await fieldHTML(f, obj); body += `<div class="form tp${xc ? " xsec" : ""}" data-t="${i}"${!xc && i ? ' style="display:none"' : ""}>${xc && !n.startsWith("_") ? `<div class="xsh">${L(n)}</div>` : ""}${h}</div>`; }
  const m = modal(`<div class="hd">${ico(isNew ? "plus" : "edit")} ${isNew ? L("New|جديد") : L("Edit|تعديل")} · ${L(TITLES[kind] || TITLES[kind + "s"] || kind)}${obj.id ? ` <span class="chip">#${obj.id}</span>` : ""}<span class="sp"></span>${kind === "stream" ? `<button class="btn s" id="probe">${ico("eye")} ${L("Probe source|فحص المصدر")}</button>` : ""}</div>
   ${!xc && names.length > 1 ? `<div class="tabs">${names.map((n, i) => `<a data-tab="${i}" class="${i ? "" : "on"}">${L(n)}</a>`).join("")}</div>` : ""}
   <div class="bd">${body}</div><div class="ft"><button class="btn w8" data-x>${L("Cancel|إلغاء")}</button><button class="btn p" id="sv">${L("Save|حفظ")}</button></div>`, xc ? "xpage xcf" : "");
  const ps = $("[data-k=profile_id] select", m), pn = $("#pnote", m);
  if (ps && pn) { const upd = () => { const on = +ps.value > 0; $$("[data-po]", m).forEach(e => e.style.display = on ? "none" : ""); pn.style.display = on ? "" : "none"; $("b", pn).textContent = on ? ps.selectedOptions[0].textContent : ""; }; ps.addEventListener("change", upd); upd(); }
  if ($$(".srcopt", m).some(e => { const i = $("input,textarea,select", e); return i && (i.type === "checkbox" ? i.checked : i.value && i.value !== "0"); })) $$(".srcopt", m).forEach(x => x.classList.add("on"));
  bindForm(m, all);
  $$("[data-tab]", m).forEach(a => a.onclick = () => { $$("[data-tab]", m).forEach(x => x.classList.toggle("on", x === a)); $$(".tp", m).forEach(p => p.style.display = p.dataset.t === a.dataset.tab ? "" : "none"); });
  $("#probe", m) && ($("#probe", m).onclick = async () => {
    const url = ($("[data-k=inputs] .url input", m)?.value || "").trim(); if (!url) return toast(L("Add a source first|أضف مصدراً أولاً"), 1);
    const r = await tryDo(() => api("probe", { method: "POST", body: { url, user_agent: $("[data-k=user_agent] input", m).value, proxy: $("[data-k=proxy] input", m).value } }));
    modal(`<div class="hd">${ico("eye")} ${L("Source info|معلومات المصدر")}</div><div class="bd"><pre class="log">${esc(JSON.stringify(r, null, 2))}</pre></div><div class="ft"><button class="btn" data-x>OK</button></div>`);
  });
  $("#sv", m).onclick = async () => {
    const out = structuredClone(obj); $$(".tp > .f[data-k]", m).forEach(e => readField(e, out));
    const miss = all.find(f => f.req && !get(out, f.k)); if (miss) return toast(L(miss.l) + " " + L("is required|مطلوب"), 1);
    if (kind === "account" && !out.password) delete out.password;
    const saved = await tryDo(() => isNew ? api(kind, { method: "POST", body: out }) : api(`${kind}/${obj.id}`, { method: "PUT", body: out }), L("Saved|تم الحفظ"));
    delete S.cache[kind]; m.close(); onSaved && onSaved(saved);
  };
}

/* ---------- generic CRUD ---------- */
async function crudView(kind, el = $("#page")) {
  const sc = SCHEMAS[kind];
  await Promise.all(["category", "package", "series", "server"].map(k => loadRef(k)));
  const list = await api(kind); S.cache[kind] = list;
  el.innerHTML = `<div class="tbar"><input class="in search" placeholder="${L("Search…|بحث…")}" id="q"><span class="sp"></span><span class="mut">${list.length} ${L("items|عنصر")}</span><button class="btn p" id="add">${ico("plus")} ${L("Add|إضافة")}</button></div>
  <div class="card tw"><table><thead><tr><th>#</th>${sc.cols.map(c => `<th>${L(c[1])}</th>`).join("")}<th></th></tr></thead><tbody id="tb"></tbody></table></div>`;
  const draw = () => {
    const q = $("#q").value.toLowerCase(), rows = list.filter(x => !q || JSON.stringify(x).toLowerCase().includes(q));
    $("#tb").innerHTML = rows.map(x => `<tr><td class="mut">${x.id}</td>${sc.cols.map(([c]) => `<td>${typeof c === "function" ? c(x) : esc(x[c])}</td>`).join("")}<td><div class="acts"><button class="btn i s" data-e="${x.id}">${ico("edit")}</button><button class="btn i s" data-c="${x.id}" title="clone">${ico("copy")}</button><button class="btn i s d" data-d="${x.id}">${ico("del")}</button></div></td></tr>`).join("") || `<tr><td colspan="99" class="empty">${L("Nothing here yet|لا يوجد بيانات بعد")}</td></tr>`;
  };
  draw(); $("#q").oninput = draw;
  const reload = () => crudView(kind, el);
  $("#add").onclick = () => editModal(kind, null, reload);
  $("#tb").onclick = e => {
    const b = e.target.closest("button"); if (!b) return; const id = +(b.dataset.e || b.dataset.c || b.dataset.d), x = list.find(i => i.id === id);
    if (b.dataset.e) editModal(kind, x, reload);
    if (b.dataset.c) { const c = structuredClone(x); delete c.id; if (c.name) c.name += " copy"; if (c.username) c.username += "_2"; editModal(kind, c, reload); }
    if (b.dataset.d && confirm(L("Delete this item?|حذف هذا العنصر؟"))) tryDo(() => api(`${kind}/${id}`, { method: "DELETE" }), L("Deleted|تم الحذف")).then(reload);
  };
}

/* ---------- live data ---------- */
function connectWS() {
  if (S.ws && S.ws.readyState <= 1) return;
  const ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/api/ws"); S.ws = ws;
  ws.onmessage = e => {
    const d = JSON.parse(e.data); S.dash = d.dash; S.rows = d.streams || [];
    const h = S.hist, sy = d.dash.sys, push = (a, v) => { a.push(v); a.length > 90 && a.shift(); };
    push(h.cpu, sy.cpu); push(h.rx, sy.net_rx); push(h.tx, sy.net_tx);
    (d.dash.gpu.cards || []).forEach(c => { const g = h.gpu[c.index] ||= { enc: [], util: [] }; push(g.enc, c.enc); push(g.util, c.util); });
    S.onLive && S.onLive();
  };
  ws.onclose = () => { if (S.me) setTimeout(connectWS, 3000); };
}
function spark(cv, series, colors, max) {
  const r = devicePixelRatio || 1, w = cv.clientWidth, h = cv.clientHeight; if (!w) return; cv.width = w * r; cv.height = h * r;
  const c = cv.getContext("2d"); c.scale(r, r); const m = max || Math.max(1, ...series.flat()) * 1.15;
  series.forEach((s, i) => {
    if (s.length < 2) return; c.beginPath(); s.forEach((v, j) => { const x = j / (90 - 1) * w, y = h - v / m * (h - 4) - 2; j ? c.lineTo(x, y) : c.moveTo(x, y); });
    c.strokeStyle = colors[i]; c.lineWidth = 2; c.lineJoin = "round"; c.stroke();
    c.lineTo((s.length - 1) / 89 * w, h); c.lineTo(0, h); c.closePath(); const g = c.createLinearGradient(0, 0, 0, h); g.addColorStop(0, colors[i] + "40"); g.addColorStop(1, colors[i] + "00"); c.fillStyle = g; c.fill();
  });
}
const ring = (v, l) => { const C = 2 * Math.PI * 32; return `<div class="ring"><svg viewBox="0 0 78 78"><circle class="bgc" cx="39" cy="39" r="32"/><circle class="fgc" cx="39" cy="39" r="32" stroke-dasharray="${C}" stroke-dashoffset="${C * (1 - Math.min(100, v || 0) / 100)}"/></svg><span>${Math.round(v || 0)}%</span>${l}</div>`; };
const meter = (l, v, max, txt) => { const p = pct(v, max); return `<div class="meter"><div class="t"><span>${l}</span><b>${txt}</b></div><div class="bar${p > 90 ? " e" : p > 75 ? " w" : ""}"><i style="width:${p}%"></i></div></div>`; };
function gpuCard(c) {
  const lim = c.session_limit ? `${c.enc_sessions}/${c.session_limit}` : `${c.enc_sessions} / ∞`;
  return `<div class="card gpu"><div class="hd">${ico("gpu")} GPU ${c.index} · ${esc(c.name)} <span class="sp"></span><small>${esc(c.arch || "")} · NVENC ${esc(c.nvenc_gen || "")}</small></div><div class="bd">
  <div class="rings">${ring(c.util, "GPU")}${ring(c.enc, "ENC")}${ring(c.dec, "DEC")}</div>
  ${meter("VRAM", c.mem_used, c.mem_total, `${fmtB(c.mem_used * 1048576)} / ${fmtB(c.mem_total * 1048576)}`)}
  ${meter(L("Temperature|الحرارة"), c.temp, 100, `${Math.round(c.temp)}°C`)}${meter(L("Power|الطاقة"), c.power, c.power_max, `${Math.round(c.power)} / ${Math.round(c.power_max)} W`)}
  <div class="chips" style="margin-top:10px"><span class="chip pri">NVENC ${lim}</span><span class="chip">${esc(c.pstate)}</span><span class="chip">Fan ${Math.round(c.fan)}%</span><span class="chip">PCIe ${esc(c.pcie_gen)}x${esc(c.pcie_width)}</span>${(c.codecs || []).map(x => `<span class="chip ok">${esc(x)}</span>`).join("")}<span class="chip">${L("Streams|قنوات")}: ${(S.dash.assigned || {})[c.index] || 0}</span></div>
  <canvas class="spark" data-g="${c.index}" style="margin-top:10px"></canvas></div></div>`;
}

/* ---------- views ---------- */
const VIEWS = {};
VIEWS.dash = el => {
  const tile = (i, id, l) => `<div class="card tile"><div class="ic">${ico(i)}</div><div><div class="v" id="${id}">—</div><div class="l">${L(l)}</div></div></div>`;
  el.innerHTML = `<div class="grid g4">${tile("stream", "t_run", "Running streams|قنوات شغالة")}${tile("eyes", "t_view", "Viewers|المشاهدين")}${tile("cpu", "t_cpu", "CPU")}${tile("ram", "t_ram", "RAM")}${tile("net", "t_net", "Network out|الخروج")}${tile("gpu", "t_gpu", "GPUs|الكروت")}</div>
  <div class="grid g2" style="margin-top:16px"><div class="card"><div class="hd">${ico("cpu")} ${L("System|النظام")}<span class="sp"></span><span class="mut" id="host"></span></div><div class="bd"><canvas class="spark" id="cpuC"></canvas><div id="sysm"></div></div></div>
  <div class="card"><div class="hd">${ico("net")} ${L("Network|الشبكة")}<span class="sp"></span><span class="chip ok">IN</span><span class="chip pri">OUT</span></div><div class="bd"><canvas class="spark" id="netC"></canvas><div class="kv" id="sysk" style="margin-top:12px"></div></div></div></div>
  <div class="grid g2" id="gpus" style="margin-top:16px"></div>
  <div class="card" style="margin-top:16px"><div class="hd">${ico("stream")} ${L("Streams with problems|قنوات فيها مشاكل")}</div><div class="tw"><table><tbody id="bad"></tbody></table></div></div>`;
  S.onLive = () => {
    const d = S.dash; if (!d || !$("#t_run")) return; const sy = d.sys, st = d.streams || {};
    $("#t_run").textContent = `${st.running || 0} / ${st.total || 0}`; $("#t_view").textContent = d.viewers; $("#t_cpu").textContent = sy.cpu.toFixed(1) + "%";
    $("#t_ram").textContent = pct(sy.mem_used, sy.mem_total).toFixed(0) + "%"; $("#t_net").textContent = fmtBits(sy.net_tx); $("#t_gpu").textContent = (d.gpu.cards || []).length;
    $("#host").textContent = `${sy.hostname} · ${sy.os}`;
    $("#sysm").innerHTML = meter("CPU", sy.cpu, 100, `${sy.cpu.toFixed(1)}% · ${sy.cores} cores · load ${sy.load1}`) + meter("RAM", sy.mem_used, sy.mem_total, `${fmtB(sy.mem_used)} / ${fmtB(sy.mem_total)}`) + meter(L("Disk|القرص"), sy.disk_used, sy.disk_total, `${fmtB(sy.disk_used)} / ${fmtB(sy.disk_total)}`) + meter("RAM disk (HLS)", sy.shm_used, sy.shm_total, `${fmtB(sy.shm_used)} / ${fmtB(sy.shm_total)}`);
    $("#sysk").innerHTML = `<b>IN</b><span>${fmtBits(sy.net_rx)}</span><b>OUT</b><span>${fmtBits(sy.net_tx)}</span><b>${L("Uptime|مدة التشغيل")}</b><span>${fmtDur(sy.uptime)}</span><b>${L("Panel|اللوحة")}</b><span>${fmtB(sy.panel_rss)} · ${sy.panel_cpu.toFixed(1)}% CPU · ${L("up|يعمل")} ${fmtDur(d.uptime)}</span><b>NVIDIA</b><span>${d.gpu.available ? `driver ${esc(d.gpu.driver)} · CUDA ${esc(d.gpu.cuda)}${d.gpu.patched ? " · patched" : ""}` : `<span class="chip warn">${esc(d.gpu.error || "not detected")}</span>`}</span><b>FFmpeg</b><span>v${d.ffmpeg}</span>`;
    spark($("#cpuC"), [S.hist.cpu], ["#1a6dff"], 100); spark($("#netC"), [S.hist.rx, S.hist.tx], ["#16c172", "#1a6dff"]);
    const g = $("#gpus"); g.innerHTML = (d.gpu.cards || []).map(gpuCard).join(""); $$("canvas[data-g]", g).forEach(cv => { const h = S.hist.gpu[cv.dataset.g]; h && spark(cv, [h.util, h.enc], ["#e6007e", "#1a6dff"], 100); });
    const bad = S.rows.filter(r => r.enabled && r.state !== "running");
    $("#bad").innerHTML = bad.map(r => `<tr><td><span class="dot ${r.state}"></span>${esc(r.name)}</td><td>${esc(r.state)}</td><td class="mut" style="white-space:normal">${esc(r.last_error || "")}</td><td>${r.restarts} ↻</td></tr>`).join("") || `<tr><td class="empty">${L("All streams healthy|كل القنوات سليمة")} ✓</td></tr>`;
  };
  S.onLive();
};
VIEWS.gpu = el => {
  el.innerHTML = `<div class="tbar"><span class="sp"></span><button class="btn" id="bn">${ico("tools")} ${L("Benchmark|اختبار الأداء")}</button></div><div class="grid g2" id="gl"></div><div class="card" style="margin-top:16px"><div class="hd">${L("GPU processes|عمليات الكرت")}</div><div class="tw"><table><thead><tr><th>GPU</th><th>PID</th><th>${L("Process|العملية")}</th><th>VRAM</th></tr></thead><tbody id="gp"></tbody></table></div></div>`;
  $("#bn").onclick = benchModal;
  S.onLive = () => {
    if (!$("#gl") || !S.dash) return; const cs = S.dash.gpu.cards || [];
    $("#gl").innerHTML = cs.map(c => gpuCard(c).replace('<div class="bd">', `<div class="bd"><div class="kv" style="margin-bottom:10px"><b>UUID</b><span class="mono">${esc(c.uuid)}</span><b>Bus</b><span class="mono">${esc(c.bus)}</span><b>Driver</b><span>${esc(c.driver)} · VBIOS ${esc(c.vbios)}</span><b>Clocks</b><span>${c.clock_gr} / ${c.clock_mem} MHz</span><b>Encode FPS</b><span>${c.enc_fps}</span></div>`)).join("") || `<div class="card empty">${esc(S.dash.gpu.error || L("No NVIDIA GPU detected|لا يوجد كرت NVIDIA"))}</div>`;
    $$("canvas[data-g]").forEach(cv => { const h = S.hist.gpu[cv.dataset.g]; h && spark(cv, [h.util, h.enc], ["#e6007e", "#1a6dff"], 100); });
    $("#gp").innerHTML = cs.flatMap(c => (c.procs || []).map(p => `<tr><td>${c.index}</td><td>${p.pid}</td><td class="mono">${esc(p.name)}</td><td>${p.mem} MiB</td></tr>`)).join("") || `<tr><td colspan="4" class="empty">—</td></tr>`;
  };
  S.onLive();
};
function benchModal() {
  const m = modal(`<div class="hd">${ico("tools")} ${L("Encoder benchmark|اختبار قدرة الترميز")}</div><div class="bd"><div class="form">
   <div class="f"><label>GPU</label><select class="in" id="bg">${(S.dash?.gpu?.cards || []).map(c => `<option value="${c.index}">GPU ${c.index} · ${esc(c.name)}</option>`).join("")}<option value="-1">CPU (libx264)</option></select></div>
   <div class="f"><label>Codec</label><select class="in" id="bc"><option>h264</option><option>hevc</option><option>av1</option></select></div>
   <div class="f"><label>${L("Resolution|الدقة")}</label><select class="in" id="bs"><option>1920x1080</option><option>1280x720</option><option>3840x2160</option></select></div></div>
   <div id="br" style="margin-top:16px"></div></div><div class="ft"><button class="btn" data-x>${L("Close|إغلاق")}</button><button class="btn p" id="go">${ico("play")} ${L("Run (≈20s)|تشغيل (≈20 ثانية)")}</button></div>`, "sm");
  $("#go", m).onclick = async () => {
    const g = +$("#bg", m).value, b = $("#go", m); b.disabled = true; $("#br", m).innerHTML = `<div class="bar"><i style="width:100%;opacity:.5"></i></div>`;
    try { const r = await api("system/bench", { method: "POST", body: { gpu: Math.max(0, g), cpu: g < 0, codec: $("#bc", m).value, size: $("#bs", m).value } });
      $("#br", m).innerHTML = r.ok ? `<div class="grid g4"><div class="card tile"><div><div class="v">${Math.round(r.fps)}</div><div class="l">FPS</div></div></div><div class="card tile"><div><div class="v">${r.speed}x</div><div class="l">${L("Speed|السرعة")}</div></div></div><div class="card tile"><div><div class="v grad">≈ ${r.channels_est}</div><div class="l">${L("channels @25fps (encode only)|قناة @25fps (ترميز فقط)")}</div></div></div></div>` : `<pre class="log">${esc(r.error)}</pre>`;
    } catch (e) { $("#br", m).innerHTML = `<pre class="log">${esc(e.message)}</pre>`; }
    b.disabled = false;
  };
}
VIEWS.streams = async el => {
  await Promise.all([loadRef("category", 1), loadRef("profile")]);
  const cats = S.cache.category.filter(c => c.type !== "movie" && c.type !== "series");
  el.innerHTML = `<div class="tbar"><input class="in search" id="q" placeholder="${L("Search…|بحث…")}"><select class="in" id="fc" style="width:auto"><option value="">${L("All categories|كل التصنيفات")}</option>${cats.map(c => `<option value="${c.id}">${esc(c.name)}</option>`).join("")}</select>
   <select class="in" id="fs" style="width:auto"><option value="">${L("All states|كل الحالات")}</option>${["running", "error", "stopped", "starting", "waiting"].map(s => `<option>${s}</option>`).join("")}</select><span class="sp"></span>
   <span id="selb" style="display:none" class="acts"><button class="btn s" data-b="start">${ico("play")}</button><button class="btn s" data-b="stop">${ico("stop")}</button><button class="btn s" data-b="restart">${ico("re")}</button><button class="btn s d" data-b="delete">${ico("del")}</button></span>
   <button class="btn" id="imp">${ico("down")} M3U</button><a class="btn" href="/api/export/m3u" download="mastergpu.m3u">${ico("up")} M3U</a><button class="btn p" id="add">${ico("plus")} ${L("Add stream|إضافة قناة")}</button></div>
   <div class="card tw"><table><thead><tr><th><input type="checkbox" id="all"></th><th>ID</th><th></th><th>${L("Name|الاسم")}</th><th>${L("State|الحالة")}</th><th>${L("Mode|الوضع")}</th><th>${L("Source|المصدر")}</th><th>GPU</th><th>FPS / ${L("Speed|سرعة")}</th><th>${L("Bitrate|البت ريت")}</th><th>${L("Viewers|مشاهدين")}</th><th>${L("Uptime|مدة")}</th><th></th></tr></thead><tbody id="tb"></tbody></table></div>`;
  const sel = new Set(), tstamp = Math.floor(Date.now() / 60000);
  const draw = () => {
    if (!$("#tb")) return; const q = $("#q").value.toLowerCase(), fc = $("#fc").value, fs = $("#fs").value;
    const rows = S.rows.filter(r => (!q || r.name.toLowerCase().includes(q) || String(r.id) === q) && (!fc || r.category == fc) && (!fs || r.state === fs));
    $("#tb").innerHTML = rows.map(r => `<tr><td><input type="checkbox" data-s="${r.id}"${sel.has(r.id) ? " checked" : ""}></td><td class="mut">${r.id}</td><td>${r.state === "running" ? `<img class="thumb" loading="lazy" src="/api/stream/${r.id}/thumb.jpg?${tstamp}" onerror="this.style.visibility='hidden'">` : `<div class="thumb"></div>`}</td>
     <td><span class="nb">${esc(r.name)}</span><div class="mut" style="font-size:11px;margin-top:3px">${r.profile ? `<span class="tg">${esc(refName("profile", r.profile))}</span> ` : ""}${esc(refName("category", r.category))}${r.on_demand ? " · on-demand" : ""}</div></td>
     <td><span class="dot ${r.state}"></span>${esc(r.state)}${r.black ? ' <span class="chip err">black</span>' : ""}${r.silent ? ' <span class="chip warn">silent</span>' : ""}${r.last_error && r.state !== "running" ? `<div class="mut" style="font-size:11px;max-width:220px;white-space:normal">${esc(r.last_error).slice(0, 120)}</div>` : ""}</td>
     <td><span class="chip ${r.mode === "gpu" ? "pri" : r.mode === "drm" ? "warn" : ""}">${esc(r.mode)}</span></td><td class="mut">${r.state === "running" ? `#${r.input + 1} · ${esc(r.video || "")}` : ""}</td>
     <td>${r.gpu >= 0 ? r.gpu : r.state === "running" ? "CPU" : ""}</td><td>${r.state === "running" ? `${r.fps.toFixed(0)} · ${r.speed.toFixed(2)}x` : ""}</td><td>${r.bitrate ? fmtBits(r.bitrate * 1000) : ""}</td><td>${r.viewers || ""}</td><td>${r.state === "running" ? fmtDur(r.uptime) : ""}</td>
     <td><div class="acts">${r.state === "running" || r.state === "starting" || r.state === "restarting" ? `<button class="btn i s" data-a="stop" data-id="${r.id}" title="stop">${ico("stop")}</button><button class="btn i s" data-a="restart" data-id="${r.id}">${ico("re")}</button>` : `<button class="btn i s" data-a="start" data-id="${r.id}" title="start">${ico("play")}</button>`}
     <button class="btn i s" data-a="play" data-id="${r.id}" title="preview">${ico("eye")}</button><button class="btn i s" data-a="log" data-id="${r.id}" title="log">${ico("log")}</button><button class="btn i s" data-a="edit" data-id="${r.id}">${ico("edit")}</button><button class="btn i s" data-a="clone" data-id="${r.id}">${ico("copy")}</button><button class="btn i s d" data-a="del" data-id="${r.id}">${ico("del")}</button></div></td></tr>`).join("") || `<tr><td colspan="99" class="empty">${L("No streams yet — add one or import an M3U|لا توجد قنوات — أضف قناة أو استورد M3U")}</td></tr>`;
    $("#selb").style.display = sel.size ? "flex" : "none";
  };
  const refresh = async () => { S.rows = await api("status"); draw(); };
  S.onLive = draw; await refresh();
  ["q", "fc", "fs"].forEach(i => $("#" + i).oninput = draw);
  $("#add").onclick = () => editModal("stream", null, refresh);
  $("#imp").onclick = () => importModal(refresh);
  $("#all").onchange = e => { $$("[data-s]").forEach(c => { c.checked = e.target.checked; c.checked ? sel.add(+c.dataset.s) : sel.delete(+c.dataset.s); }); draw(); };
  $("#selb").onclick = async e => { const b = e.target.closest("[data-b]"); if (!b) return; if (b.dataset.b === "delete" && !confirm(L("Delete selected streams?|حذف القنوات المحددة؟"))) return; await tryDo(() => api("streams/bulk", { method: "POST", body: { ids: [...sel], action: b.dataset.b } }), "OK"); sel.clear(); refresh(); };
  $("#tb").onclick = async e => {
    const c = e.target.closest("[data-s]"); if (c) { c.checked ? sel.add(+c.dataset.s) : sel.delete(+c.dataset.s); $("#selb").style.display = sel.size ? "flex" : "none"; return; }
    const b = e.target.closest("[data-a]"); if (!b) return; const id = +b.dataset.id, a = b.dataset.a;
    if (["start", "stop", "restart"].includes(a)) { b.disabled = true; await tryDo(() => api(`stream/${id}/${a}`, { method: "POST" })).catch(() => { }); setTimeout(refresh, 600); }
    if (a === "edit" || a === "clone") { const s = await api("stream/" + id); if (a === "clone") { delete s.id; s.name += " copy"; } editModal("stream", s, refresh); }
    if (a === "del" && confirm(L("Delete this stream?|حذف هذه القناة؟"))) { await tryDo(() => api("stream/" + id, { method: "DELETE" }), L("Deleted|تم الحذف")); refresh(); }
    if (a === "log") logModal("stream", id, S.rows.find(r => r.id === id)?.name);
    if (a === "play") playerModal(id);
  };
};
function importModal(done) {
  const m = modal(`<div class="hd">${ico("down")} ${L("Import M3U|استيراد M3U")}</div><div class="bd"><div class="form">
   <div class="f w"><label>URL</label><input class="in ltr" id="iu" placeholder="http://…/get.php?…&type=m3u_plus"></div>
   <div class="f w"><label>${L("…or paste playlist|…أو الصق القائمة")}</label><textarea class="in" id="it" style="min-height:160px"></textarea></div>
   <div class="f"><label>${L("Profile|البروفايل")}</label><select class="in" id="ip"><option value="0">${L("Copy (no transcode)|نسخ بدون ترانسكود")}</option>${(S.cache.profile || []).map(p => `<option value="${p.id}">${esc(p.name)}</option>`).join("")}</select></div>
   <div class="f"><label class="sw"><input type="checkbox" id="io" checked><i></i><span>${L("On demand|عند الطلب")}</span></label></div></div></div>
   <div class="ft"><button class="btn" data-x>${L("Cancel|إلغاء")}</button><button class="btn p" id="ig">${L("Import|استيراد")}</button></div>`);
  $("#ig", m).onclick = async () => { const r = await tryDo(() => api("import/m3u", { method: "POST", body: { url: $("#iu", m).value.trim(), text: $("#it", m).value, profile_id: +$("#ip", m).value, on_demand: $("#io", m).checked, enabled: !$("#io", m).checked } })); toast(`${L("Imported|تم استيراد")} ${r.added ?? r.count ?? ""}`); m.close(); done(); };
}
async function logModal(scope, ref, name) {
  const m = modal(`<div class="hd">${ico("log")} ${L("Log|السجل")} · ${esc(name || scope)}<span class="sp"></span><button class="btn s d" id="lc">${L("Clear|مسح")}</button></div><div class="bd"><pre class="log" id="lg">…</pre></div><div class="ft"><button class="btn" data-x>${L("Close|إغلاق")}</button></div>`);
  const load = async () => { const l = await api(`logs?scope=${scope}&ref=${ref}&limit=500`); $("#lg", m).innerHTML = l.map(x => `<span class="lv-${x.level}">${fmtTime(x.ts)}  [${x.level}]  ${esc(x.msg)}</span>`).join("\n") || "—"; };
  load(); const t = setInterval(load, 3000); m.onclose = () => clearInterval(t);
  $("#lc", m).onclick = async () => { await api(`logs?scope=${scope}&ref=${ref}`, { method: "DELETE" }); load(); };
}
function playerModal(id) {
  const url = `${location.origin}/hls/${id}/`, ts = `${location.origin}/ts/${id}`;
  const m = modal(`<div class="hd">${ico("eye")} ${L("Preview|معاينة")} #${id}</div><div class="bd"><video id="vp" controls autoplay muted playsinline></video>
   <div class="kv" style="margin-top:12px"><b>HLS</b><span class="mono ltr">${esc(url)}</span><b>MPEG-TS</b><span class="mono ltr">${esc(ts)}</span></div></div><div class="ft"><button class="btn" data-x>${L("Close|إغلاق")}</button></div>`);
  const v = $("#vp", m); let hls;
  const start = () => { if (window.Hls && Hls.isSupported()) { hls = new Hls({ liveDurationInfinity: true }); hls.loadSource(url); hls.attachMedia(v); } else v.src = url; };
  if (v.canPlayType("application/vnd.apple.mpegurl")) v.src = url;
  else if (window.Hls) start();
  else { const s = document.createElement("script"); s.src = "hls.min.js"; s.onload = start; s.onerror = () => toast(L("Player library unavailable, copy the URL into VLC|تعذر تحميل المشغل، انسخ الرابط إلى VLC"), 1); document.head.append(s); }
  m.onclose = () => { hls && hls.destroy(); v.removeAttribute("src"); v.load(); };
}
VIEWS.logs = async el => {
  el.innerHTML = `<div class="tbar"><select class="in" id="sc" style="width:auto"><option value="">${L("All|الكل")}</option><option value="panel">panel</option><option value="stream">stream</option><option value="auth">auth</option><option value="gpu">gpu</option></select><input class="in search" id="q" placeholder="${L("Filter…|فلترة…")}"><span class="sp"></span><button class="btn d" id="cl">${L("Clear|مسح")}</button></div><div class="card"><div class="bd"><pre class="log" id="lg" style="max-height:72vh">…</pre></div></div>`;
  let all = [];
  const draw = () => { const q = $("#q").value.toLowerCase(); $("#lg").innerHTML = all.filter(x => !q || x.msg.toLowerCase().includes(q)).map(x => `<span class="lv-${x.level}">${fmtTime(x.ts)}  ${esc(x.scope)}${x.ref ? "#" + x.ref : ""}  [${x.level}]  ${esc(x.msg)}</span>`).join("\n") || "—"; };
  const load = async () => { all = await api(`logs?scope=${$("#sc").value}&limit=1000`); draw(); };
  $("#sc").onchange = load; $("#q").oninput = draw; $("#cl").onclick = async () => { if (confirm("?")) { await api(`logs?scope=${$("#sc").value}&ref=-1`, { method: "DELETE" }); load(); } };
  await load();
};
VIEWS.files = async el => {
  const list = await api("files"); S.cache.files = list;
  el.innerHTML = `<div class="tbar"><input class="in search" id="q" placeholder="${L("Search…|بحث…")}"><span class="sp"></span>${S.me.role === "admin" ? `<label class="btn p">${ico("up")} ${L("Upload|رفع")}<input type="file" id="up" multiple hidden></label>` : ""}</div>
  <div class="card tw"><table><thead><tr><th>${L("Filename|اسم الملف")}</th><th>${L("Date|التاريخ")}</th><th>${L("Size|الحجم")}</th><th>${L("Settings|الإعدادات")}</th></tr></thead><tbody id="tb"></tbody></table></div>`;
  const draw = () => { const q = $("#q").value.toLowerCase(); $("#tb").innerHTML = list.filter(f => !q || f.name.toLowerCase().includes(q)).map(f => `<tr><td title="${esc(f.path)}">${esc(f.name)}</td><td>${fmtTime(f.time)}</td><td>${fmtB(f.size)}</td><td><div class="acts"><a class="btn i s" href="/files/${encodeURIComponent(f.name)}" download title="download">${ico("down")}</a>${S.me.role === "admin" ? `<button class="btn i s d" data-d="${esc(f.name)}">${ico("del")}</button>` : ""}</div></td></tr>`).join("") || `<tr><td colspan="4" class="empty">—</td></tr>`; };
  draw(); $("#q").oninput = draw;
  $("#up") && ($("#up").onchange = async e => { for (const f of e.target.files) { const fd = new FormData(); fd.append("file", f); await tryDo(() => api("files", { method: "POST", body: fd }), f.name + " ✓"); } delete S.cache.files; VIEWS.files(el); });
  el.onclick = async e => { const b = e.target.closest("[data-d]"); if (b && confirm(L("Delete file?|حذف الملف؟"))) { await tryDo(() => api("files/" + encodeURIComponent(b.dataset.d), { method: "DELETE" })); delete S.cache.files; VIEWS.files(el); } };
};

VIEWS.tools = async el => {
  el.innerHTML = `<div class="grid g2"><div class="card"><div class="hd">${ico("up")} ${L("Update|التحديث")}<span class="sp"></span><span class="chip pri">v${esc(S.ver.version)} · ${esc(S.ver.build)}</span></div><div class="bd" id="upd"><p class="mut">${L("Checks the update URL from Settings, verifies SHA-256, self-tests the new binary, keeps the old one for rollback (mastergpu -rollback).|يفحص رابط التحديث من الإعدادات، يتحقق من SHA-256، يختبر النسخة الجديدة، ويحتفظ بالقديمة للرجوع.")}</p>
    <div class="acts" style="gap:8px;flex-wrap:wrap"><button class="btn" id="uc">${L("Check for updates|فحص التحديثات")}</button><button class="btn p" id="ua" disabled>${L("Install update|تثبيت التحديث")}</button><label class="btn">${ico("up")} ${L("Upload binary|رفع نسخة")}<input type="file" id="ub" hidden></label><button class="btn d" id="rs">${ico("re")} ${L("Restart panel|إعادة تشغيل اللوحة")}</button></div><div id="ur" style="margin-top:12px"></div></div></div>
   <div class="card"><div class="hd">${ico("file")} ${L("Backups|النسخ الاحتياطي")}<span class="sp"></span><button class="btn s p" id="bk">${L("Backup now|نسخة الآن")}</button><label class="btn s">${L("Restore|استرجاع")}<input type="file" id="rf" hidden accept=".db"></label></div><div class="tw"><table><tbody id="bl"></tbody></table></div></div>
   <div class="card"><div class="hd">${ico("tools")} ${L("Diagnostics|التشخيص")}<span class="sp"></span><button class="btn s" id="bn">${L("Benchmark|اختبار الأداء")}</button><button class="btn s" id="tg">${L("Test Telegram|تجربة تلغرام")}</button></div><div class="bd" id="dg">…</div></div></div>`;
  const loadB = async () => { const l = await api("system/backups"); $("#bl").innerHTML = l.sort((a, b) => b.time - a.time).map(b => `<tr><td class="mono">${esc(b.name)}</td><td>${fmtB(b.size)}</td><td>${fmtTime(b.time)}</td><td><a class="btn s" href="/api/system/backups/${encodeURIComponent(b.name)}">${ico("down")}</a></td></tr>`).join("") || `<tr><td class="empty">—</td></tr>`; };
  loadB();
  api("system/diag").then(d => { const yes = v => v ? '<span class="chip ok">✓</span>' : '<span class="chip">✗</span>';
    $("#dg").innerHTML = `<div class="kv"><b>Panel</b><span>${esc(d.panel)} (${esc(d.build)}) · ${esc(d.go)}</span><b>OS</b><span>${esc(d.os)} · ${esc(d.kernel)}</span><b>CPU</b><span>${esc(d.cpu)} · ${d.cores} cores</span><b>FFmpeg</b><span class="mono">${esc(d.ffmpeg)}<br>${esc(d.ffmpeg_path)}</span><b>NVIDIA</b><span>${d.nvidia_driver ? `driver ${esc(d.nvidia_driver)} · CUDA ${esc(d.cuda)} · ${d.gpus} GPU(s)` : esc(d.gpu_error || "—")}</span><b>NVENC patch</b><span>${yes(d.nvenc_patched)}</span><b>HW accels</b><span>${(d.hwaccels || []).map(x => `<span class="chip">${esc(x)}</span>`).join(" ")}</span><b>Encoders</b><span class="chips">${Object.entries(d.encoders).map(([k, v]) => `<span class="chip ${v ? "ok" : ""}">${k}</span>`).join("")}</span><b>Intel /dev/dri</b><span>${yes(d.dri)}</span></div>`; }).catch(e => $("#dg").textContent = e.message);
  let rel = null;
  $("#uc").onclick = async () => { const r = await tryDo(() => api("system/update")); rel = r; $("#ur").innerHTML = r.error ? `<span class="chip warn">${esc(r.error)}</span>` : r.available ? `<span class="chip ok">v${esc(r.latest)} ${L("available|متوفرة")}</span><pre class="log" style="margin-top:8px">${esc(r.notes || "")}</pre>` : `<span class="chip ok">${L("Up to date|أحدث نسخة")}</span>`; $("#ua").disabled = !r.available; };
  const afterUpdate = r => { $("#ur").innerHTML = `<span class="chip ok">${esc(r.version)} — ${L("restarting…|جاري إعادة التشغيل…")}</span>`; setTimeout(() => location.reload(), 6000); };
  $("#ua").onclick = async () => { if (!confirm(L("Install update and restart? Streams restart briefly.|تثبيت التحديث وإعادة التشغيل؟ القنوات بتعيد التشغيل لثواني."))) return; afterUpdate(await tryDo(() => api("system/update", { method: "POST" }))); };
  $("#ub").onchange = async e => { const f = e.target.files[0]; if (!f || !confirm(f.name + " → " + L("install?|تثبيت؟"))) return; const fd = new FormData(); fd.append("file", f); afterUpdate(await tryDo(() => api("system/update", { method: "POST", body: fd }))); };
  $("#rs").onclick = async () => { if (confirm(L("Restart panel?|إعادة تشغيل اللوحة؟"))) { await tryDo(() => api("system/restart", { method: "POST" })); setTimeout(() => location.reload(), 5000); } };
  $("#bk").onclick = async () => { await tryDo(() => api("system/backup", { method: "POST" }), L("Backup created|تم إنشاء نسخة")); loadB(); };
  $("#rf").onchange = async e => { const f = e.target.files[0]; if (!f || !confirm(L("Restore this backup? Current data will be replaced.|استرجاع هذه النسخة؟ سيتم استبدال البيانات الحالية."))) return; const fd = new FormData(); fd.append("file", f); await tryDo(() => api("system/restore", { method: "POST", body: fd }), L("Restoring…|جاري الاسترجاع…")); setTimeout(() => location.reload(), 6000); };
  $("#bn").onclick = benchModal;
  $("#tg").onclick = () => tryDo(() => api("system/telegram-test", { method: "POST" }), "Telegram ✓");
};
const SETTINGS = {
  "General|عام": [F.t("panel_name", "Panel name|اسم اللوحة"), F.s("language", "Default language|اللغة", opts(["ar", "العربية"], ["en", "English"])), F.t("public_host", "Public host / domain|الدومين العام"), F.n("http_port", "HTTP port (restart)|منفذ HTTP"), F.t("update_url", "Update URL (release JSON)|رابط التحديث", { w: 1, ph: "https://updates.example.com/mastergpu/latest.json" })],
  "Engine|المحرك": [F.t("ffmpeg_path", "FFmpeg path"), F.t("ffprobe_path", "FFprobe path"), F.t("hls_dir", "HLS dir (RAM)|مجلد HLS"), F.n("stream_timeout", "Input timeout s|مهلة المصدر"), F.n("restart_delay", "Restart delay s|تأخير إعادة التشغيل"), F.n("start_interval", "Start interval s|الفاصل بين القنوات"), F.n("startup_delay", "Startup delay s|تأخير البدء"), F.n("freeze_seconds", "Freeze detect s|كشف التجمد"), F.b("black_detect", "Black screen detect|كشف الشاشة السوداء"), F.n("thumb_interval", "Thumbnail every s|الصورة المصغرة"), F.n("default_hls_time", "HLS segment s"), F.n("default_hls_list", "HLS list size"), F.t("user_agent", "Default User-Agent", { w: 1 })],
  "Load balancer|موزع الحمل": [F.s("lb_mode", "GPU placement|توزيع GPU", opts(["least_load", "Least load|الأقل حمل"], ["round_robin", "Round robin|بالدور"], ["fill_first", "Fill first|تعبئة الأول"])), F.n("lb_max_gpu_enc", "Max NVENC %"), F.n("lb_max_gpu_mem", "Max VRAM %"), F.b("lb_cpu_fallback", "CPU fallback|التحويل للمعالج"), F.b("lb_auto_migrate", "Auto-migrate|نقل تلقائي"), F.s("lb_viewer_mode", "Viewer balancing|توزيع المشاهدين", opts(["least_conn", "Least connections|أقل اتصالات"], ["round_robin", "Round robin|بالدور"], ["weighted", "Weighted|بالوزن"]))],
  "Proxy & DRM|البروكسي و DRM": [F.t("default_proxy", "Default proxy|البروكسي الافتراضي", { w: 1 }), F.li("proxy_list", "Rotating proxy pool|قائمة البروكسيات للتدوير", { w: 1 }), F.s("drm_provider", "Key provider|مزود المفاتيح", opts(["", "Stored keys|مفاتيح مخزنة"], ["url", "URL"])), F.t("drm_request_url", "Key request URL|رابط طلب المفاتيح", { w: 1 })],
  "Security & alerts|الأمان والتنبيهات": [F.b("secure_playback", "Signed playback URLs|روابط تشغيل موقعة"), F.n("max_login_tries", "Max login tries|محاولات الدخول"), F.p("telegram_token", "Telegram bot token"), F.t("telegram_chat", "Telegram chat ID"), F.n("alert_gpu_temp", "GPU temp alert °C|تنبيه حرارة الكرت"), F.n("backup_hour", "Daily backup hour|ساعة النسخ اليومي"), F.n("backup_keep", "Backups to keep|عدد النسخ"), F.t("epg_url", "EPG URL", { w: 1 })]
};
VIEWS.settings = async el => {
  const st = await api("settings"), names = Object.keys(SETTINGS), all = names.flatMap(n => SETTINGS[n]);
  let body = ""; for (const n of names) { let h = ""; for (const f of SETTINGS[n]) h += await fieldHTML(f, st); body += `<div class="card" style="margin-bottom:16px"><div class="hd">${L(n)}</div><div class="bd form">${h}</div></div>`; }
  el.innerHTML = body + `<div style="position:sticky;bottom:16px;text-align:end"><button class="btn p" id="ss">${L("Save settings|حفظ الإعدادات")}</button></div>`;
  bindForm(el, all);
  $("#ss").onclick = async () => { const o = structuredClone(st); $$(".form > .f[data-k]", el).forEach(e => readField(e, o)); await tryDo(() => api("settings", { method: "PUT", body: o }), L("Settings saved|تم حفظ الإعدادات")); };
};

boot();
