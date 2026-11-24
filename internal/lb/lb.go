// Package lb decides where streams run (GPU balancer) and where viewers go (viewer balancer).
package lb

import (
	"sort"
	"sync"
	"sync/atomic"

	"gpupanel/internal/gpu"
)

type Limits struct {
	Strategy    string // least_load, fill_first, round_robin
	MaxEnc      float64
	MaxMem      float64
	MaxTemp     float64
	MaxPerGPU   int
	CPUFallback bool
}

type Candidate struct {
	Server int64
	GPU    int // -1 = cpu
	Score  float64
}

var rr uint64

// PickGPU returns the best card index for a new stream, -1 for CPU, or -2 when
// nothing fits and CPU fallback is disabled. assigned holds streams already placed per card.
func PickGPU(cards []gpu.Card, assigned map[int]int, l Limits) int {
	if l.MaxEnc == 0 {
		l.MaxEnc = 90
	}
	if l.MaxMem == 0 {
		l.MaxMem = 92
	}
	if l.MaxTemp == 0 {
		l.MaxTemp = 87
	}
	type c struct {
		idx   int
		score float64
	}
	var ok []c
	for _, k := range cards {
		mem := 0.0
		if k.MemTotal > 0 {
			mem = 100 * k.MemUsed / k.MemTotal
		}
		n := assigned[k.Index]
		if k.Enc >= l.MaxEnc || mem >= l.MaxMem || (k.Temp > 0 && k.Temp >= l.MaxTemp) {
			continue
		}
		if l.MaxPerGPU > 0 && n >= l.MaxPerGPU {
			continue
		}
		if k.SessLimit > 0 && n >= k.SessLimit {
			continue
		}
		// weighted score: encoder is the real bottleneck for transcoding.
		score := k.Enc*0.45 + k.Dec*0.2 + mem*0.25 + k.Util*0.1 + float64(n)*2
		ok = append(ok, c{k.Index, score})
	}
	if len(ok) == 0 {
		if l.CPUFallback {
			return -1
		}
		return -2
	}
	switch l.Strategy {
	case "fill_first":
		sort.Slice(ok, func(i, j int) bool { return ok[i].idx < ok[j].idx })
		return ok[0].idx
	case "round_robin":
		return ok[int(atomic.AddUint64(&rr, 1))%len(ok)].idx
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].score < ok[j].score })
	return ok[0].idx
}

// ViewerTarget is one server that can serve viewers.
type ViewerTarget struct {
	ID      int64
	Base    string // http://host:port
	Weight  int
	Conns   int
	Max     int
	Healthy bool
	BWUsed  float64
	BWMax   float64
}

type ViewerBalancer struct {
	mu sync.Mutex
	n  uint64
}

// Pick chooses a server for one viewer. Returns nil when no healthy server has room.
func (v *ViewerBalancer) Pick(ts []ViewerTarget, strategy string) *ViewerTarget {
	var ok []ViewerTarget
	for _, t := range ts {
		if !t.Healthy || (t.Max > 0 && t.Conns >= t.Max) || (t.BWMax > 0 && t.BWUsed >= t.BWMax*0.95) {
			continue
		}
		if t.Weight <= 0 {
			t.Weight = 1
		}
		ok = append(ok, t)
	}
	if len(ok) == 0 {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.n++
	switch strategy {
	case "round_robin":
		t := ok[int(v.n)%len(ok)]
		return &t
	case "weighted":
		total := 0
		for _, t := range ok {
			total += t.Weight
		}
		x := int(v.n) % total
		for _, t := range ok {
			if x < t.Weight {
				return &t
			}
			x -= t.Weight
		}
	}
	sort.Slice(ok, func(i, j int) bool {
		return float64(ok[i].Conns)/float64(ok[i].Weight) < float64(ok[j].Conns)/float64(ok[j].Weight)
	})
	return &ok[0]
}
