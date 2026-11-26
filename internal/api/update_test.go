package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchGitHubRelease(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.2","body":"notes","assets":[{"name":"other","url":"x"},{"name":"mastergpu-linux-amd64","url":"https://dl/bin","digest":"sha256:abc"}]}`))
	}))
	defer ts.Close()
	rel, err := fetchRelease(ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.2" || rel.URL != "https://dl/bin" || rel.SHA256 != "abc" || rel.Notes != "notes" {
		t.Fatalf("%+v", rel)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		w    bool
	}{{"1.1", "1.0", true}, {"1.0", "1.0", false}, {"1.0.1", "1.0", true}, {"1.0", "1.10", false}, {"2", "1.9", true}} {
		if newer(c.a, c.b) != c.w {
			t.Errorf("newer(%s,%s)", c.a, c.b)
		}
	}
}
