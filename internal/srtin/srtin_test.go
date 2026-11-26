package srtin

import "testing"

func TestParse(t *testing.T) {
	l := Parse(`/usr/local/bin/srt-live-transmit srt://@10005?latency=1000 udp://239.200.0.5:1234 > /dev/null 2>&1 &
# srt-live-transmit srt://@10006 udp://239.200.0.6:1234
/usr/local/bin/srt-live-transmit "srt://:10001?latency=200&passphrase=abcdefghij" "udp://239.200.0.1:1234"
/usr/local/bin/srt-live-transmit srt://@10005?latency=1000 udp://239.200.0.5:1234`, "hot")
	if len(l) != 2 || l[0].Port != 10001 || l[0].Latency != 200 || l[0].Passphrase != "abcdefghij" || l[0].Output != "udp://239.200.0.1:1234" ||
		l[1].Port != 10005 || l[1].Latency != 1000 || l[1].Output != "udp://239.200.0.5:1234" || l[1].Name != "hot10005" {
		t.Fatalf("%+v", l)
	}
}
