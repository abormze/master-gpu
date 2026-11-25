package models

// Kinds used in the store.
const (
	KSettings = "settings"
	KServer   = "server"
	KStream   = "stream"
	KProfile  = "profile"
	KCategory = "category"
	KAdapter  = "adapter"
	KSoftcam  = "softcam"
	KMovie    = "movie"
	KSeries   = "series"
	KEpisode  = "episode"
	KUser     = "user"
	KBouquet  = "bouquet"
	KPackage  = "package"
	KAccount  = "account"
	KBalancer = "balancer"
	KIPRule   = "iprule"
	KProxy    = "proxy"
	KDRMKey   = "drmkey"
)

type Settings struct {
	PanelName      string   `json:"panel_name"`
	Language       string   `json:"language"`
	Theme          string   `json:"theme"`
	FFmpegPath     string   `json:"ffmpeg_path"`
	FFprobePath    string   `json:"ffprobe_path"`
	HLSDir         string   `json:"hls_dir"`
	StreamTimeout  int      `json:"stream_timeout"`
	RestartDelay   int      `json:"restart_delay"`
	StartInterval  float64  `json:"start_interval"`
	FreezeSeconds  int      `json:"freeze_seconds"`
	BlackDetect    bool     `json:"black_detect"`
	ThumbInterval  int      `json:"thumb_interval"`
	TelegramToken  string   `json:"telegram_token"`
	TelegramChat   string   `json:"telegram_chat"`
	AlertGPUTemp   int      `json:"alert_gpu_temp"`
	BackupKeep     int      `json:"backup_keep"`
	BackupHour     int      `json:"backup_hour"`
	SecurePlayback bool     `json:"secure_playback"`
	DRMProvider    string   `json:"drm_provider"`
	DRMRequestURL  string   `json:"drm_request_url"`
	MaxLoginTries  int      `json:"max_login_tries"`
	EPGURL         string   `json:"epg_url"`
	LBMode         string   `json:"lb_mode"`
	LBMaxGPUEnc    int      `json:"lb_max_gpu_enc"`
	LBMaxGPUMem    int      `json:"lb_max_gpu_mem"`
	LBCPUFallback  bool     `json:"lb_cpu_fallback"`
	LBViewerMode   string   `json:"lb_viewer_mode"`
	LBAutoMigrate  bool     `json:"lb_auto_migrate"`
	GPUFreeClocks  bool     `json:"gpu_free_clocks"` // don't lock GPU/NVENC clocks at max on start
	InputDirect    bool     `json:"input_direct"`    // ffmpeg reads HTTP TS sources itself (no reconnect splicer)
	DefaultHLSTime int      `json:"default_hls_time"`
	DefaultHLSList int      `json:"default_hls_list"`
	UserAgent      string   `json:"user_agent"`
	DefaultProxy   string   `json:"default_proxy"`
	ProxyList      []string `json:"proxy_list"` // rotating pool
	UpdateURL      string   `json:"update_url"`
	PublicHost     string   `json:"public_host"`
	HTTPPort       int      `json:"http_port"`
	StartupDelay   int      `json:"startup_delay"`
	// live subtitle translation: google (free), libre, deepl, openai
	TranslateProvider string `json:"translate_provider"`
	TranslateURL      string `json:"translate_url"`
	TranslateKey      string `json:"translate_key"`
	TranslateModel    string `json:"translate_model"`
}

func DefaultSettings() Settings {
	return Settings{
		PanelName: "Master GPU", Language: "ar", Theme: "dark",
		FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", HLSDir: "/dev/shm/mastergpu",
		StreamTimeout: 20, RestartDelay: 3, StartInterval: 0.3, FreezeSeconds: 15,
		ThumbInterval: 60, AlertGPUTemp: 85, BackupKeep: 14, BackupHour: 3,
		MaxLoginTries: 5, LBMode: "least_load", LBMaxGPUEnc: 90, LBMaxGPUMem: 92,
		LBCPUFallback: false, LBViewerMode: "least_conn", DefaultHLSTime: 2, DefaultHLSList: 6,
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36",
	}
}

type Server struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Master      bool   `json:"master"`
	Host        string `json:"host"`
	PublicHost  string `json:"public_host"`
	HTTPPort    int    `json:"http_port"`
	HTTPSPort   int    `json:"https_port"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password,omitempty"`
	Token       string `json:"token"`
	MaxDownload int    `json:"max_download"`
	MaxUpload   int    `json:"max_upload"`
	MaxClients  int    `json:"max_clients"`
	LBWeight    int    `json:"lb_weight"`
	LBEnabled   bool   `json:"lb_enabled"`
	Disabled    bool   `json:"disabled"`
	Note        string `json:"note"`
}

type VideoEnc struct {
	Codec   string `json:"codec"` // copy, h264, hevc, av1, mpeg2
	Size    string `json:"size"`  // 1920x1080 / empty = source
	RC      string `json:"rc"`    // cbr, vbr, cq
	Bitrate int    `json:"bitrate"`
	Maxrate int    `json:"maxrate"`
	CQ      int    `json:"cq"`
	Name    string `json:"name"`   // variant label e.g. FHD
	Device  string `json:"device"` // "" = stream hwaccel, "cpu" = encode on CPU to offload NVENC
}

type AudioEnc struct {
	Codec   string `json:"codec"` // copy, aac, fdk_aac, mp3, mp2, ac3, opus
	Bitrate int    `json:"bitrate"`
}

// Transcode is shared by streams and profiles.
type Transcode struct {
	HWAccel        string     `json:"hwaccel"` // none(cpu), full, partial, encode, qsv
	VideoMap       string     `json:"video_map"`
	VideoSync      string     `json:"video_sync"`
	VideoEncoders  []VideoEnc `json:"video_encoders"`
	Preset         string     `json:"preset"`
	Profile        string     `json:"profile"`
	Level          string     `json:"level"`
	Tune           string     `json:"tune"`
	FrameRate      string     `json:"frame_rate"`
	GOP            int        `json:"gop"`
	BFrames        int        `json:"bframes"`
	Deinterlace    bool       `json:"deinterlace"`
	PixFmt         string     `json:"pix_fmt"`
	DAR            string     `json:"dar"`
	OverlayFile    string     `json:"overlay_file"`
	OverlayX       string     `json:"overlay_x"`
	OverlayY       string     `json:"overlay_y"`
	OverlayOpacity float64    `json:"overlay_opacity"`
	OverlayScale   int        `json:"overlay_scale"`
	Text           string     `json:"text"`
	TextX          string     `json:"text_x"`
	TextY          string     `json:"text_y"`
	TextSize       int        `json:"text_size"`
	TextColor      string     `json:"text_color"`
	TextAlpha      float64    `json:"text_alpha"`
	TextScroll     bool       `json:"text_scroll"`
	DelogoX        int        `json:"delogo_x"`
	DelogoY        int        `json:"delogo_y"`
	DelogoW        int        `json:"delogo_w"`
	DelogoH        int        `json:"delogo_h"`
	AudioMap       string     `json:"audio_map"`
	AudioEncoders  []AudioEnc `json:"audio_encoders"`
	AudioChannels  int        `json:"audio_channels"`
	AudioRate      int        `json:"audio_rate"`
	AudioVolume    float64    `json:"audio_volume"`
	AudioNormalize bool       `json:"audio_normalize"`
	AudioDelay     int        `json:"audio_delay"`
	SubMode        string     `json:"sub_mode"` // none, copy, burn, burn_file, webvtt
	SubMap         string     `json:"sub_map"`
	SubFile        string     `json:"sub_file"`
	SubLang        string     `json:"sub_lang"`
	SubFontSize    int        `json:"sub_font_size"`
	TxtPage        string     `json:"txt_page"`
	PIDMap         string     `json:"pid_map"`
	ServiceName    string     `json:"service_name"`
	ServiceProv    string     `json:"service_provider"`
	ColorRange     string     `json:"color_range"`
	ColorSpace     string     `json:"colorspace"`
	ColorPrim      string     `json:"color_primaries"`
	ColorTRC       string     `json:"color_trc"`
	RemoveCC       bool       `json:"remove_cc"`
	ExtraArgs      string     `json:"extra_args"`
}

type Output struct {
	Type     string `json:"type"` // hls, hls_master, http, dash, rtmp, srt, srt_listener, udp, rtp, tcp, rtsp, file
	URL      string `json:"url"`
	Name     string `json:"name"`
	Video    []int  `json:"video"`
	Audio    []int  `json:"audio"`
	HLSTime  int    `json:"hls_time"`
	HLSList  int    `json:"hls_list"`
	Port     int    `json:"port"`
	Passwd   string `json:"passphrase"`
	Latency  int    `json:"latency"`
	File     string `json:"filename"`
	Buffer   int    `json:"buffer"`
	Disabled bool   `json:"disabled"`
}

type Restart struct {
	Enabled  bool  `json:"enabled"`
	Hours    []int `json:"hours"`
	Minutes  []int `json:"minutes"`
	Weekdays []int `json:"weekdays"`
}

type Stream struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Category    int64     `json:"category"`
	Logo        string    `json:"logo"`
	EPGID       string    `json:"epg_id"`
	Enabled     bool      `json:"enabled"`
	OnDemand    bool      `json:"on_demand"`
	Inputs      []string  `json:"inputs"`
	InputIndex  int       `json:"input_index"`
	ReturnMain  bool      `json:"return_main"`
	UserAgent   string    `json:"user_agent"`
	Referer     string    `json:"referer"`
	Headers     string    `json:"headers"`
	Proxy       string    `json:"proxy"` // http://, socks5:// for input
	BindIP      string    `json:"bind_ip"`
	DNS         string    `json:"dns"`   // 1.1.1.1 or https://dns/ (DoH)
	Relay       bool      `json:"relay"` // stream proxy: pull once, serve many, no transcode
	FFlags      []string  `json:"fflags"`
	RateEmu     bool      `json:"rate_emulation"`
	ProbeSize   int       `json:"probe_size"`
	Analyze     int       `json:"analyze"`
	RTSPTCP     bool      `json:"rtsp_tcp"`
	LoopFile    bool      `json:"loop_file"`
	DRMKeys     []string  `json:"drm_keys"` // KID:KEY
	DRMURL      string    `json:"drm_url"`
	DRM         DRM       `json:"drm"`
	Type        string    `json:"type"` // linear, event, vod
	StartAt     int64     `json:"start_at"`
	EndAt       int64     `json:"end_at"`
	DailyStart  int       `json:"daily_start"` // minutes from midnight, -1 off
	DailyEnd    int       `json:"daily_end"`
	RecordTS    bool      `json:"record_ts"`
	OffAirFile  string    `json:"offair_file"`
	ProfileID   int64     `json:"profile_id"`
	ServerID    int64     `json:"server_id"` // 0 = auto (load balancer)
	GPU         int       `json:"gpu"`       // -2 auto, -1 cpu, >=0 index
	BalancerID  int64     `json:"balancer_id"`
	Transcode   Transcode `json:"transcode"`
	Outputs     []Output  `json:"outputs"`
	Restart     Restart   `json:"restart"`
	SpeedMin    float64   `json:"speed_min"`
	MemMaxMB    int       `json:"mem_max_mb"`
	GPUMemMaxMB int       `json:"gpu_mem_max_mb"`
	Watchdog    bool      `json:"watchdog"`
	Note        string    `json:"note"`
	Order       int       `json:"order"`
}

type Profile struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	ServerID  int64     `json:"server_id"`
	GPU       *int      `json:"gpu,omitempty"` // nil = stream decides, -2 auto, -1 cpu, >=0 index
	Transcode Transcode `json:"transcode"`
}

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // live, movie, series
}

type Adapter struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	ServerID   int64  `json:"server_id"`
	Device     int    `json:"device"`
	System     string `json:"system"`
	Modulation string `json:"modulation"`
	Frequency  int    `json:"frequency"`
	SymbolRate int    `json:"symbol_rate"`
	Voltage    int    `json:"voltage"`
	DiSEqC     string `json:"diseqc"`
	FEC        string `json:"fec"`
	Pilot      string `json:"pilot"`
	Rolloff    string `json:"rolloff"`
	Inversion  string `json:"inversion"`
	Budget     bool   `json:"budget"`
	PLSMode    string `json:"pls_mode"`
	PLSCode    int    `json:"pls_code"`
	StreamID   int    `json:"stream_id"`
	Charset    string `json:"charset"`
	SoftcamID  int64  `json:"softcam_id"`
	UDPAddr    string `json:"udp_addr"`
	Enabled    bool   `json:"enabled"`
}

type Softcam struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	CAID     string `json:"caid"`
	DESKey   string `json:"des_key"`
}

type Movie struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Category  int64   `json:"category"`
	ServerID  int64   `json:"server_id"`
	Source    string  `json:"source"`
	Poster    string  `json:"poster"`
	Plot      string  `json:"plot"`
	Year      int     `json:"year"`
	Rating    float64 `json:"rating"`
	TMDB      int64   `json:"tmdb"`
	ProfileID int64   `json:"profile_id"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	File      string  `json:"file"`
	Duration  float64 `json:"duration"`
	SubFile   string  `json:"sub_file"`
}

type Series struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category int64  `json:"category"`
	Poster   string `json:"poster"`
	Plot     string `json:"plot"`
	Year     int    `json:"year"`
	TMDB     int64  `json:"tmdb"`
}

type Episode struct {
	ID        int64   `json:"id"`
	SeriesID  int64   `json:"series_id"`
	Season    int     `json:"season"`
	Number    int     `json:"number"`
	Name      string  `json:"name"`
	ServerID  int64   `json:"server_id"`
	Source    string  `json:"source"`
	ProfileID int64   `json:"profile_id"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	File      string  `json:"file"`
}

type User struct {
	ID             int64   `json:"id"`
	Username       string  `json:"username"`
	Password       string  `json:"password"`
	Expire         int64   `json:"expire"` // unix seconds, 0 = never
	Created        int64   `json:"created"`
	LastSeen       int64   `json:"last_seen"`
	Owner          int64   `json:"owner"`
	MaxConnections int     `json:"max_connections"`
	PackageID      int64   `json:"package_id"`
	Bouquets       []int64 `json:"bouquets"`
	MaxResolution  int     `json:"max_resolution"`
	AllowedIPs     string  `json:"allowed_ips"`
	Enabled        bool    `json:"enabled"`
	Trial          bool    `json:"trial"`
	Note           string  `json:"note"`
}

type Bouquet struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Streams []int64 `json:"streams"`
	Movies  []int64 `json:"movies"`
	Series  []int64 `json:"series"`
}

type Package struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Credits        int     `json:"credits"`
	Duration       int     `json:"duration"`
	Unit           string  `json:"unit"` // hours, days, months, years
	Bouquets       []int64 `json:"bouquets"`
	MaxConnections int     `json:"max_connections"`
	MaxResolution  int     `json:"max_resolution"`
	Trial          bool    `json:"trial"`
}

type Account struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	Email      string `json:"email"`
	Role       string `json:"role"` // admin, reseller, subreseller
	Owner      int64  `json:"owner"`
	Credits    int    `json:"credits"`
	Token      string `json:"token"`
	Theme      string `json:"theme"`
	Lang       string `json:"lang"`
	Note       string `json:"note"`
	LastLogin  int64  `json:"last_login"`
	LastIP     string `json:"last_ip"`
	Disabled   bool   `json:"disabled"`
	MustChange bool   `json:"must_change"`
}

// Balancer groups servers/GPUs for automatic placement of streams and viewers.
type Balancer struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`     // gpu, viewer
	Strategy  string  `json:"strategy"` // least_load, round_robin, fill_first, least_conn, weighted, geo
	Servers   []int64 `json:"servers"`
	MaxEnc    int     `json:"max_enc"`
	MaxMem    int     `json:"max_mem"`
	MaxTemp   int     `json:"max_temp"`
	MaxPerGPU int     `json:"max_per_gpu"`
	CPUFallbk bool    `json:"cpu_fallback"`
	AutoMove  bool    `json:"auto_move"`
	Enabled   bool    `json:"enabled"`
}

type IPRule struct {
	ID     int64  `json:"id"`
	CIDR   string `json:"cidr"`
	Action string `json:"action"` // allow, block
	Scope  string `json:"scope"`  // panel, stream, all
	Note   string `json:"note"`
}

// DRM describes a protected DASH/HLS source handled by the internal remuxer.
type DRM struct {
	Enabled       bool              `json:"enabled"`
	Mode          string            `json:"mode"`   // keys, keyserver, script
	Vendor        string            `json:"vendor"` // clearkey, widevine, playready
	LicenseURL    string            `json:"license_url"`
	KeyServer     string            `json:"key_server"` // URL returning KID:KEY lines/JSON ({kid},{pssh},{id})
	Script        string            `json:"script"`
	Headers       map[string]string `json:"headers"`
	Video         string            `json:"video"`     // best, worst, representation id
	Audio         string            `json:"audio"`     // all, first, lang code, id
	Subtitles     string            `json:"subtitles"` // all, none, lang code, id
	SubBurn       bool              `json:"sub_burn"`
	SubTranslate  string            `json:"sub_translate"` // target language
	LiveDelay     int               `json:"live_delay"`    // segments behind live edge
	Heartbeat     string            `json:"heartbeat"`
	HeartbeatSec  int               `json:"heartbeat_sec"`
	StallSec      int               `json:"stall_sec"`
	ProxyManifest string            `json:"proxy_manifest"`
	ProxyMedia    string            `json:"proxy_media"`
	ProxyKeys     string            `json:"proxy_keys"`
}

// Proxy is a reusable upstream proxy (HTTP/HTTPS/SOCKS5) selectable per stream or stage.
type Proxy struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // http, https, socks5
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	BindIP   string `json:"bind_ip"`
	Pool     string `json:"pool"` // group name for rotation
	Disabled bool   `json:"disabled"`
	Note     string `json:"note"`
}

// DRMKey is a stored KID:KEY pair (content you are licensed to decrypt).
type DRMKey struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	KID       string `json:"kid"`
	Key       string `json:"key"`
	ContentID string `json:"content_id"`
	PSSH      string `json:"pssh"`
	Note      string `json:"note"`
}
