// Package netx provides a local HTTP forward proxy that chains to HTTP/HTTPS/SOCKS5
// upstreams with an optional bind IP and DNS server, so ffmpeg and the DRM fetcher
// can use any proxy type through a plain -http_proxy.
package netx

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Spec describes how outbound connections are made.
type Spec struct {
	Upstream string // "", http://u:p@h:p, https://..., socks5://u:p@h:p
	BindIP   string
	DNS      string // ip[:port] of a DNS server
}

func (s Spec) key() string { return s.Upstream + "|" + s.BindIP + "|" + s.DNS }

// Empty reports whether the spec changes nothing compared to a direct connection.
func (s Spec) Empty() bool { return s.Upstream == "" && s.BindIP == "" && s.DNS == "" }

type dialFn func(ctx context.Context, network, addr string) (net.Conn, error)

func baseDialer(s Spec) *net.Dialer {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if s.BindIP != "" {
		if ip := net.ParseIP(s.BindIP); ip != nil {
			d.LocalAddr = &net.TCPAddr{IP: ip}
		}
	}
	if s.DNS != "" {
		srv := s.DNS
		if _, _, err := net.SplitHostPort(srv); err != nil {
			srv = net.JoinHostPort(srv, "53")
		}
		d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, srv)
		}}
	}
	return d
}

var bypassLoopback = true

func isLoopback(addr string) bool {
	if !bypassLoopback {
		return false
	}
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Dialer returns a dial function that connects to addr through the spec.
func Dialer(s Spec) (dialFn, error) {
	d := baseDialer(s)
	direct := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	if s.Upstream == "" {
		return direct, nil
	}
	u, err := url.Parse(s.Upstream)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad proxy %q", s.Upstream)
	}
	var via dialFn
	switch u.Scheme {
	case "socks5", "socks5h", "socks":
		var auth *proxy.Auth
		if u.User != nil {
			p, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: p}
		}
		sd, err := proxy.SOCKS5("tcp", u.Host, auth, d)
		if err != nil {
			return nil, err
		}
		cd := sd.(proxy.ContextDialer)
		via = cd.DialContext
	case "http", "https":
		via = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, "tcp", u.Host)
			if err != nil {
				return nil, err
			}
			if u.Scheme == "https" {
				tc := tls.Client(c, &tls.Config{ServerName: u.Hostname()})
				if err := tc.HandshakeContext(ctx); err != nil {
					c.Close()
					return nil, err
				}
				c = tc
			}
			req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
			if u.User != nil {
				p, _ := u.User.Password()
				req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+p)) + "\r\n"
			}
			c.SetDeadline(time.Now().Add(15 * time.Second))
			if _, err := io.WriteString(c, req+"\r\n"); err != nil {
				c.Close()
				return nil, err
			}
			br := bufio.NewReader(c)
			resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
			if err != nil {
				c.Close()
				return nil, err
			}
			if resp.StatusCode != 200 {
				c.Close()
				return nil, fmt.Errorf("proxy CONNECT %s: %s", addr, resp.Status)
			}
			c.SetDeadline(time.Time{})
			if br.Buffered() > 0 {
				return &bufConn{Conn: c, r: br}, nil
			}
			return c, nil
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if isLoopback(addr) {
			return direct(ctx, network, addr)
		}
		return via(ctx, network, addr)
	}, nil
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// Client returns an HTTP client that connects through the spec.
func Client(s Spec, timeout time.Duration) (*http.Client, error) {
	dial, err := Dialer(s)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{DialContext: dial, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ForceAttemptHTTP2: true}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// Relay is a local forward proxy bound to 127.0.0.1.
type Relay struct {
	Addr string
	ln   net.Listener
}

var (
	mu     sync.Mutex
	relays = map[string]*Relay{}
)

// Get returns a shared local relay for the spec, starting it on first use.
func Get(s Spec) (*Relay, error) {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := relays[s.key()]; ok {
		return r, nil
	}
	r, err := Start(s)
	if err != nil {
		return nil, err
	}
	relays[s.key()] = r
	return r, nil
}

// Start launches a new relay for the spec.
func Start(s Spec) (*Relay, error) {
	dial, err := Dialer(s)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{DialContext: dial, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second, DisableCompression: true}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			tunnel(w, r, dial)
			return
		}
		if !r.URL.IsAbs() {
			http.Error(w, "absolute URL required", http.StatusBadRequest)
			return
		}
		r.RequestURI = ""
		r.Header.Del("Proxy-Connection")
		r.Header.Del("Proxy-Authorization")
		resp, err := tr.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	go (&http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second}).Serve(ln)
	return &Relay{Addr: "http://" + ln.Addr().String(), ln: ln}, nil
}

func tunnel(w http.ResponseWriter, r *http.Request, dial dialFn) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	up, err := dial(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	go func() {
		if n := brw.Reader.Buffered(); n > 0 {
			b, _ := brw.Reader.Peek(n)
			up.Write(b)
		}
		io.Copy(up, c)
		up.Close()
	}()
	io.Copy(c, up)
	c.Close()
}

// Close stops the relay.
func (r *Relay) Close() error { return r.ln.Close() }

// Normalize turns user proxy strings such as "host:port" or "socks5://..." into URLs.
func Normalize(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "://") {
		return p
	}
	return "http://" + p
}
