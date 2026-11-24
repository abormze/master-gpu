package gpu

import "strings"

// Classify fills architecture, NVENC generation, supported encode codecs and the
// default consumer session limit from the card name.
func Classify(c *Card) {
	n := strings.ToUpper(c.Name)
	type rule struct {
		keys        []string
		arch, nvenc string
		codecs      []string
	}
	h264 := []string{"h264"}
	hevc := []string{"h264", "hevc"}
	av1 := []string{"h264", "hevc", "av1"}
	rules := []rule{
		{[]string{"RTX 50", "B200", "B100", "RTX PRO 6000", "BLACKWELL"}, "Blackwell", "9th", av1},
		{[]string{"RTX 40", "L4", "L40", "RTX 2000 ADA", "RTX 4000 ADA", "RTX 4500 ADA", "RTX 5000 ADA", "RTX 6000 ADA", "ADA"}, "Ada Lovelace", "8th", av1},
		{[]string{"H100", "H200", "HOPPER"}, "Hopper", "-", nil},
		{[]string{"RTX 30", "A2000", "A4000", "A4500", "A5000", "A6000", "A10", "A16", "A40", "A2", "RTX A", "MX5"}, "Ampere", "7th", hevc},
		{[]string{"A100", "A30"}, "Ampere", "-", nil},
		{[]string{"RTX 20", "GTX 16", "T4", "T400", "T600", "T1000", "QUADRO RTX", "TITAN RTX", "T2000"}, "Turing", "7th", hevc},
		{[]string{"V100", "TITAN V", "GV100"}, "Volta", "6th", hevc},
		{[]string{"GTX 10", "P400", "P600", "P620", "P1000", "P2000", "P2200", "P4000", "P5000", "P6000", "P4", "P40", "P100", "TITAN X", "GT 1030", "MX"}, "Pascal", "6th", hevc},
		{[]string{"GTX 9", "M2000", "M4000", "M5000", "M6000", "M60", "M40", "M10", "M4", "GTX 750"}, "Maxwell", "5th", hevc},
		{[]string{"GTX 7", "GTX 6", "K", "GRID"}, "Kepler", "4th", h264},
	}
	c.Arch, c.NVENCGen, c.Codecs = "Unknown", "?", hevc
	for _, r := range rules {
		for _, k := range r.keys {
			if strings.Contains(n, k) {
				c.Arch, c.NVENCGen, c.Codecs = r.arch, r.nvenc, r.codecs
				goto done
			}
		}
	}
done:
	consumer := strings.Contains(n, "GEFORCE") || strings.Contains(n, "GTX") || strings.Contains(n, "RTX 20") || strings.Contains(n, "RTX 30") || strings.Contains(n, "RTX 40") || strings.Contains(n, "RTX 50") || strings.Contains(n, "TITAN")
	if consumer {
		c.SessLimit = 8
	}
}
