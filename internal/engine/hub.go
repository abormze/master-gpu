package engine

import (
	"io"
	"sync"
)

// Hub fans an MPEG-TS byte stream out to many HTTP viewers. Slow viewers are
// dropped instead of slowing the encoder.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
	head []byte // last PAT/PMT-bearing chunk so new viewers start cleanly
}

func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

func (h *Hub) Subscribe() (chan []byte, func()) {
	c := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	if h.head != nil {
		c <- h.head
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
			if h.head == nil && n >= 188 {
				h.head = chunk
			}
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

func (h *Hub) Reset() {
	h.mu.Lock()
	h.head = nil
	h.mu.Unlock()
}
