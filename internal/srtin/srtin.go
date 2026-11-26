// Package srtin is a built-in SRT ingest gateway: each entry listens on a UDP port
// for an SRT publisher (caller) and relays the MPEG-TS payload to a UDP/multicast
// address, replacing per-channel srt-live-transmit + monit setups.
package srtin

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	srt "github.com/datarhei/gosrt"

	"gpupanel/internal/models"
	"gpupanel/internal/store"
)

type Status struct {
	ID        int64   `json:"id"`
	State     string  `json:"state"` // listening, connected, error, disabled
	Error     string  `json:"error,omitempty"`
	Peer      string  `json:"peer,omitempty"`
	Since     int64   `json:"since,omitempty"`
	Mbps      float64 `json:"mbps"`
	Bytes     uint64  `json:"bytes"`
	Lost      uint64  `json:"lost"`
	Reconnect int     `json:"reconnects"`
}

type relay struct {
	cfg  models.SRTIngest
	key  string
	ln   srt.Listener
	stop chan struct{}

	mu        sync.Mutex
	state     string
	err       string
	peer      string
	since     time.Time
	conn      srt.Conn
	reconnect int
	bytes     atomic.Uint64
	rate      float64
	lastBytes uint64
}

type Manager struct {
	St     *store.Store
	mu     sync.Mutex
	relays map[int64]*relay
	routes map[string]bool
}

func New(st *store.Store) *Manager {
	return &Manager{St: st, relays: map[int64]*relay{}, routes: map[string]bool{}}
}

func cfgKey(c models.SRTIngest) string {
	return fmt.Sprintf("%d|%d|%s|%s|%s|%v", c.Port, c.Latency, c.Passphrase, c.Output, c.Bind, c.Disabled)
}

// Run syncs listeners with the stored entries until stop closes.
func (m *Manager) Run(stop chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	m.Sync()
	for {
		select {
		case <-stop:
			m.closeAll()
			return
		case <-t.C:
			m.Sync()
			m.tickRates(2)
		}
	}
}

func (m *Manager) Sync() {
	want := map[int64]models.SRTIngest{}
	for _, c := range store.List[models.SRTIngest](m.St, models.KSRTIn) {
		want[c.ID] = c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.relays {
		if c, ok := want[id]; !ok || cfgKey(c) != r.key {
			r.close()
			delete(m.relays, id)
		}
	}
	for id, c := range want {
		if _, ok := m.relays[id]; ok {
			continue
		}
		r := &relay{cfg: c, key: cfgKey(c), stop: make(chan struct{}), state: "disabled"}
		m.relays[id] = r
		if !c.Disabled {
			m.ensureRoute(c.Output)
			go r.run()
		}
	}
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.relays {
		r.close()
		delete(m.relays, id)
	}
}

func (m *Manager) tickRates(sec float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.relays {
		b := r.bytes.Load()
		r.mu.Lock()
		r.rate = float64(b-r.lastBytes) * 8 / sec / 1e6
		r.lastBytes = b
		r.mu.Unlock()
	}
}

// ensureRoute keeps multicast outputs on the loopback interface (the classic
// `ip route add 239.200.0.0/24 dev lo`), so local readers get the packets and
// nothing leaks to the LAN. Existing routes are left untouched.
func (m *Manager) ensureRoute(out string) {
	u, err := url.Parse(out)
	if err != nil {
		return
	}
	ip := net.ParseIP(u.Hostname()).To4()
	if ip == nil || !ip.IsMulticast() {
		return
	}
	cidr := fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
	if m.routes[cidr] {
		return
	}
	m.routes[cidr] = true
	if o, _ := exec.Command("ip", "route", "show", cidr).Output(); len(strings.TrimSpace(string(o))) > 0 {
		return
	}
	if err := exec.Command("ip", "route", "add", cidr, "dev", "lo").Run(); err == nil {
		m.St.Log("srt", 0, "info", "added route "+cidr+" dev lo")
	}
}

func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.relays))
	for id, r := range m.relays {
		r.mu.Lock()
		s := Status{ID: id, State: r.state, Error: r.err, Peer: r.peer, Mbps: r.rate, Bytes: r.bytes.Load(), Reconnect: r.reconnect}
		if !r.since.IsZero() {
			s.Since = r.since.Unix()
		}
		if r.conn != nil {
			var st srt.Statistics
			r.conn.Stats(&st)
			s.Lost = st.Accumulated.PktRecvLoss
		}
		r.mu.Unlock()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *relay) set(state, err string) {
	r.mu.Lock()
	r.state, r.err = state, err
	r.mu.Unlock()
}

func (r *relay) close() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	r.mu.Lock()
	ln, c := r.ln, r.conn
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if ln != nil {
		ln.Close()
	}
}

func (r *relay) stopped() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

func (r *relay) run() {
	for !r.stopped() {
		if err := r.listen(); err != nil && !r.stopped() {
			r.set("error", err.Error())
			select {
			case <-r.stop:
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (r *relay) listen() error {
	c := r.cfg
	out, err := dialOut(c.Output)
	if err != nil {
		return err
	}
	defer out.Close()
	sc := srt.DefaultConfig()
	lat := time.Duration(c.Latency) * time.Millisecond
	if lat <= 0 {
		lat = 120 * time.Millisecond
	}
	sc.ReceiverLatency, sc.PeerLatency = lat, lat
	if c.Passphrase != "" {
		sc.Passphrase = c.Passphrase
	}
	ln, err := srt.Listen("srt", net.JoinHostPort(c.Bind, strconv.Itoa(c.Port)), sc)
	if err != nil {
		return fmt.Errorf("listen :%d: %v", c.Port, err)
	}
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()
	defer ln.Close()
	r.set("listening", "")
	for !r.stopped() {
		conn, mode, err := ln.Accept(func(req srt.ConnRequest) srt.ConnType {
			r.mu.Lock()
			busy := r.conn != nil
			r.mu.Unlock()
			if busy {
				return srt.REJECT
			}
			if req.IsEncrypted() {
				if c.Passphrase == "" || req.SetPassphrase(c.Passphrase) != nil {
					req.SetRejectionReason(srt.REJ_BADSECRET)
					return srt.REJECT
				}
			} else if c.Passphrase != "" {
				req.SetRejectionReason(srt.REJ_UNSECURE)
				return srt.REJECT
			}
			return srt.PUBLISH
		})
		if err != nil {
			if r.stopped() {
				return nil
			}
			return err
		}
		if conn == nil || mode == srt.REJECT {
			continue
		}
		r.serve(conn, out)
	}
	return nil
}

func (r *relay) serve(conn srt.Conn, out net.Conn) {
	r.mu.Lock()
	r.conn, r.peer, r.since, r.state, r.err = conn, conn.RemoteAddr().String(), time.Now(), "connected", ""
	r.mu.Unlock()
	buf := make([]byte, 1500)
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		if n > 0 {
			out.Write(buf[:n])
			r.bytes.Add(uint64(n))
		}
	}
	conn.Close()
	r.mu.Lock()
	r.conn, r.peer, r.since = nil, "", time.Time{}
	r.reconnect++
	if r.state == "connected" {
		r.state = "listening"
	}
	r.mu.Unlock()
}

func dialOut(u string) (net.Conn, error) {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "udp" || p.Port() == "" {
		return nil, fmt.Errorf("output must be udp://host:port, got %q", u)
	}
	return net.Dial("udp", p.Host)
}

var (
	reSRT = regexp.MustCompile(`srt://[^\s"'?]*?[:@](\d+)([^\s"']*)`)
	reUDP = regexp.MustCompile(`udp://[^\s"']+`)
	reLat = regexp.MustCompile(`latency=(\d+)`)
	reKey = regexp.MustCompile(`passphrase=([^&\s"']+)`)
)

// Parse extracts ingest entries from srt-live-transmit command lines
// (`srt-live-transmit srt://@10005?latency=1000 udp://239.200.0.5:1234`).
func Parse(text, namePrefix string) []models.SRTIngest {
	var out []models.SRTIngest
	seen := map[int]bool{}
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		s, d := reSRT.FindStringSubmatch(ln), reUDP.FindString(ln)
		if s == nil || d == "" {
			continue
		}
		port, _ := strconv.Atoi(s[1])
		if port == 0 || seen[port] {
			continue
		}
		seen[port] = true
		e := models.SRTIngest{Port: port, Output: d, Latency: 120}
		if m := reLat.FindStringSubmatch(s[2]); m != nil {
			e.Latency, _ = strconv.Atoi(m[1])
		}
		if m := reKey.FindStringSubmatch(s[2]); m != nil {
			e.Passphrase, _ = url.QueryUnescape(m[1])
		}
		e.Name = fmt.Sprintf("%s%d", namePrefix, port)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// ScanInitScripts reads the classic /etc/init.d/iptv/* channel scripts.
func ScanInitScripts(dir string) (string, map[int]string) {
	names := map[int]string{}
	var b strings.Builder
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	sort.Strings(files)
	for _, f := range files {
		if strings.Contains(filepath.Base(f), ".") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.Contains(ln, "srt://") && strings.Contains(ln, "udp://") {
				b.WriteString(ln + "\n")
				if m := reSRT.FindStringSubmatch(ln); m != nil {
					p, _ := strconv.Atoi(m[1])
					names[p] = filepath.Base(f)
				}
			}
		}
	}
	return b.String(), names
}
