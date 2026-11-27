package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// hlsLive reports whether an HLS source is currently live: its (first) media
// playlist has no #EXT-X-ENDLIST and is not PLAYLIST-TYPE:VOD. Non-HLS inputs and
// transient network errors count as live so normal restart handling applies.
func hlsLive(ctx context.Context, in, ua, referer string) (bool, string) {
	if !strings.Contains(strings.ToLower(path.Ext(strings.SplitN(in, "?", 2)[0])), "m3u8") {
		return true, ""
	}
	get := func(u string) (string, int, error) {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rq, err := http.NewRequestWithContext(c, "GET", u, nil)
		if err != nil {
			return "", 0, err
		}
		if ua == "" {
			ua = "Mozilla/5.0"
		}
		rq.Header.Set("User-Agent", ua)
		if referer != "" {
			rq.Header.Set("Referer", referer)
		}
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			return "", 0, err
		}
		defer r.Body.Close()
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		return string(b), r.StatusCode, err
	}
	body, code, err := get(in)
	if err != nil {
		return true, ""
	}
	if code >= 400 {
		return false, fmt.Sprintf("source HTTP %d", code)
	}
	if strings.Contains(body, "#EXT-X-STREAM-INF") {
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			if !strings.HasPrefix(l, "#EXT-X-STREAM-INF") {
				continue
			}
			for _, v := range lines[i+1:] {
				if v = strings.TrimSpace(v); v != "" && !strings.HasPrefix(v, "#") {
					base, _ := url.Parse(in)
					ref, err := url.Parse(v)
					if base == nil || err != nil {
						return true, ""
					}
					if body, code, err = get(base.ResolveReference(ref).String()); err != nil {
						return true, ""
					}
					if code >= 400 {
						return false, fmt.Sprintf("source HTTP %d", code)
					}
					break
				}
			}
			break
		}
	}
	if strings.Contains(body, "#EXT-X-ENDLIST") || strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:VOD") {
		return false, "source not live (VOD / ended)"
	}
	return true, ""
}
