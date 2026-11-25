package engine

import (
	"io"
	"sync"
)

const gopMax = 24 << 20

// Hub fans an MPEG-TS byte stream out to many HTTP viewers. Slow viewers are
// dropped instead of slowing the encoder. It caches PAT/PMT plus the packets
// since the last video keyframe so new viewers start instantly and cleanly.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	pat    []byte
	pmt    []byte
	pmtPID int
	vidPID int
	gop    []byte
	gopOK  bool
}

func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}, pmtPID: -1, vidPID: -1} }

func (h *Hub) Subscribe() (chan []byte, func()) { return h.SubscribeN(256) }

// SubscribeN subscribes with a buffer of n chunks (~64KB each).
func (h *Hub) SubscribeN(n int) (chan []byte, func()) {
	c := make(chan []byte, max(n, 8))
	h.mu.Lock()
	h.subs[c] = struct{}{}
	if h.gopOK && len(h.gop) > 0 {
		c <- append([]byte(nil), h.gop...)
	}
	h.mu.Unlock()
	return c, func() {
		h.mu.Lock()
		if _, ok := h.subs[c]; ok {
			delete(h.subs, c)
			close(c)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) Pump(r io.Reader) {
	buf := make([]byte, 188*350)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			h.mu.Lock()
			h.track(chunk)
			for c := range h.subs {
				select {
				case c <- chunk:
				default:
					delete(h.subs, c)
					close(c)
				}
			}
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (h *Hub) track(b []byte) {
	for i := 0; i+188 <= len(b); i += 188 {
		p := b[i : i+188]
		if p[0] != 0x47 {
			h.gopOK, h.gop = false, h.gop[:0]
			return
		}
		pid := int(p[1]&0x1f)<<8 | int(p[2])
		pusi := p[1]&0x40 != 0
		switch {
		case pid == 0 && pusi:
			h.pat = append(h.pat[:0], p...)
			if pm := patPMT(p); pm > 0 {
				h.pmtPID = pm
			}
		case pid == h.pmtPID && pusi:
			h.pmt = append(h.pmt[:0], p...)
			if v := pmtVideo(p); v > 0 {
				h.vidPID = v
			}
		}
		if pid == h.vidPID && pusi && p[3]&0x20 != 0 && p[4] > 0 && p[5]&0x40 != 0 && h.pat != nil && h.pmt != nil {
			h.gop = append(append(h.gop[:0], h.pat...), h.pmt...)
			h.gopOK = true
		}
		if h.gopOK {
			if len(h.gop)+188 > gopMax {
				h.gopOK, h.gop = false, h.gop[:0]
				continue
			}
			h.gop = append(h.gop, p...)
		}
	}
}

// psi returns the section of a PSI packet (after the pointer field).
func psi(p []byte) []byte {
	o := 4
	if p[3]&0x20 != 0 {
		o += 1 + int(p[4])
	}
	if o >= 188 {
		return nil
	}
	o += 1 + int(p[o])
	if o+8 > 188 {
		return nil
	}
	s := p[o:]
	l := int(s[1]&0x0f)<<8 | int(s[2])
	if 3+l > len(s) || l < 9 {
		return nil
	}
	return s[:3+l-4]
}

func patPMT(p []byte) int {
	s := psi(p)
	if s == nil || s[0] != 0 {
		return -1
	}
	for e := s[8:]; len(e) >= 4; e = e[4:] {
		if prog := int(e[0])<<8 | int(e[1]); prog != 0 {
			return int(e[2]&0x1f)<<8 | int(e[3])
		}
	}
	return -1
}

func pmtVideo(p []byte) int {
	s := psi(p)
	if s == nil || s[0] != 2 || len(s) < 12 {
		return -1
	}
	il := int(s[10]&0x0f)<<8 | int(s[11])
	if 12+il > len(s) {
		return -1
	}
	for e := s[12+il:]; len(e) >= 5; {
		pid := int(e[1]&0x1f)<<8 | int(e[2])
		switch e[0] {
		case 0x01, 0x02, 0x10, 0x1b, 0x24, 0x33, 0x42, 0xea:
			return pid
		}
		el := int(e[3]&0x0f)<<8 | int(e[4])
		if 5+el > len(e) {
			break
		}
		e = e[5+el:]
	}
	return -1
}

func (h *Hub) Reset() {
	h.mu.Lock()
	h.pat, h.pmt, h.gop, h.gopOK, h.pmtPID, h.vidPID = nil, nil, nil, false, -1, -1
	h.mu.Unlock()
}
