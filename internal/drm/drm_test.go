package drm

import (
	"strings"
	"testing"
)

const mpd = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013" type="dynamic">
<Period id="1">
<AdaptationSet contentType="video" mimeType="video/mp4">
<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc" cenc:default_KID="10000000-1000-1000-1000-100000000001"/>
<ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"><cenc:pssh>AAAAPHBzc2g=</cenc:pssh></ContentProtection>
<Representation id="v1" bandwidth="3000000"/>
</AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4" lang="en">
<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" cenc:default_KID="20000000-2000-2000-2000-200000000002"/>
</AdaptationSet>
<AdaptationSet contentType="text" mimeType="application/mp4" codecs="wvtt" lang="ar"/>
</Period>
</MPD>`

func TestParseMPD(t *testing.T) {
	in := ParseMPD([]byte(mpd))
	if in.KID("video") != "10000000100010001000100000000001" || in.KID("audio") != "20000000200020002000200000000002" {
		t.Fatalf("kids %+v", in.Sets)
	}
	if !in.HasText || len(in.PSSH) != 1 || len(in.KIDs()) != 2 {
		t.Fatalf("info %+v", in)
	}
}

func TestRebase(t *testing.T) {
	out := string(Rebase([]byte(mpd), "https://cdn.example.com/live/ch1/manifest.mpd?tok=1"))
	if !strings.Contains(out, "<BaseURL>https://cdn.example.com/live/ch1/</BaseURL>") {
		t.Fatal(out[:200])
	}
	rel := strings.Replace(mpd, `<Period id="1">`, `<BaseURL>../seg/</BaseURL><Period id="1">`, 1)
	out = string(Rebase([]byte(rel), "https://cdn.example.com/live/ch1/manifest.mpd"))
	if !strings.Contains(out, "<BaseURL>https://cdn.example.com/live/seg/</BaseURL>") || strings.Count(out, "<BaseURL>") != 1 {
		t.Fatal(out)
	}
}

func TestParseKeys(t *testing.T) {
	ck := `{"keys":[{"kty":"oct","kid":"EAAAABAAEAAQABAAAAAAAQ","k":"ESIzRFVmd4iZqrvM3e7_AA"}],"type":"temporary"}`
	k := ParseKeys([]byte(ck))
	if k["10000000100010001000100000000001"] != "112233445566778899aabbccddeeff00" {
		t.Fatalf("clearkey %v", k)
	}
	k = ParseKeys([]byte("10000000-1000-1000-1000-100000000001:112233445566778899AABBCCDDEEFF00\n--key 20000000200020002000200000000002:00112233445566778899aabbccddeeff"))
	if len(k) != 2 || k["10000000100010001000100000000001"] != "112233445566778899aabbccddeeff00" {
		t.Fatalf("text %v", k)
	}
	k = ParseKeys([]byte(`{"10000000100010001000100000000001":"112233445566778899aabbccddeeff00"}`))
	if len(k) != 1 {
		t.Fatalf("map %v", k)
	}
	if pick(map[string]string{"aa": "k1"}, "bb") != "k1" || pick(map[string]string{"aa": "k1", "cc": "k2"}, "bb") != "" {
		t.Fatal("pick")
	}
}
