package netx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRelayChain(t *testing.T) {
	bypassLoopback = false
	defer func() { bypassLoopback = true }()
	hits := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; io.WriteString(w, "ok:"+r.URL.Path) })
	plain := httptest.NewServer(h)
	defer plain.Close()
	secure := httptest.NewTLSServer(h)
	defer secure.Close()

	a, err := Start(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Start(Spec{Upstream: a.Addr})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	pu, _ := url.Parse(b.Addr)
	tr := secure.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	for _, u := range []string{plain.URL + "/a", secure.URL + "/b"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) < 4 {
			t.Fatalf("%s: %d %q", u, resp.StatusCode, body)
		}
	}
	if hits != 2 {
		t.Fatalf("hits=%d", hits)
	}
	if _, err := Dialer(Spec{Upstream: "ftp://x:1"}); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}
