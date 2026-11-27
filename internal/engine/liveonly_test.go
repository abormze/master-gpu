package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHLSLive(t *testing.T) {
	media := map[string]string{
		"/live/v.m3u8":  "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:4,\na.mp4\n",
		"/ended/v.m3u8": "#EXTM3U\n#EXTINF:4,\na.mp4\n#EXT-X-ENDLIST\n",
		"/vod/v.m3u8":   "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:4,\na.mp4\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := media[r.URL.Path]; ok {
			w.Write([]byte(b))
			return
		}
		if len(r.URL.Path) > 11 && r.URL.Path[len(r.URL.Path)-11:] == "/index.m3u8" {
			w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	for in, want := range map[string]bool{
		srv.URL + "/live/index.m3u8": true, srv.URL + "/ended/index.m3u8": false,
		srv.URL + "/vod/index.m3u8": false, srv.URL + "/gone/index.m3u8": false,
		srv.URL + "/live/v.m3u8": true, "udp://239.0.0.1:1234": true,
	} {
		if got, why := hlsLive(context.Background(), in, "", ""); got != want {
			t.Errorf("%s: live=%v (%s), want %v", in, got, why, want)
		}
	}
}
