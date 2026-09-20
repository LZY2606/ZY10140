// Package simulator generates a synthetic radiosonde flight with retries,
// payload conflicts, late packets, short pressure reversals, an icing
// interval and GPS breaks. Packets can be delivered out of order and the
// exact stream can be replayed for deterministic tests.
package simulator

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"soundingapp/internal/domain"
)

// Config controls a simulated flight.
type Config struct {
	Device       string
	Start        time.Time
	SamplePeriod time.Duration
	Samples      int

	// Pressure (hPa) profile endpoints.
	SurfaceP float64
	BurstP   float64

	// Anomalies
	RetrySeqs    []int // retransmitted byte-identically
	ConflictSeqs []int // second, different payload
	ConflictTemp map[int]float64
	LateSeqs     []int  // held back and delivered after later seqs
	ReverseSeqs  []int  // short pressure blip
	IcingRange   [2]int // inclusive seq range, 0 => disabled
	GPSGapRange  [2]int // inclusive seq range with no GPS
	LongGapRange [2]int // inclusive seq range long GPS break (no interp)

	TerminateAt int // seq of termination status; -1 disables

	// Delivery
	ShuffleWindow int // max displacement for out-of-order delivery
	Seed          int64
}

func DefaultConfig() Config {
	t0 := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	return Config{
		Device: "RS-0001", Start: t0, SamplePeriod: 2 * time.Second, Samples: 60,
		SurfaceP: 1000, BurstP: 100,
		RetrySeqs:     []int{7},
		ConflictSeqs:  []int{23},
		ConflictTemp:  map[int]float64{23: -33.7},
		LateSeqs:      []int{12},
		ReverseSeqs:   []int{31},
		IcingRange:    [2]int{18, 25},
		GPSGapRange:   [2]int{40, 41},
		LongGapRange:  [2]int{52, 55},
		TerminateAt:   59,
		ShuffleWindow: 3,
		Seed:          42,
	}
}

// Envelope is one generated sample before anomaly handling.
type envelope struct {
	seq      int
	primary  domain.Packet
	conflict *domain.Packet
	retry    bool
	late     bool
}

// Generate builds the logical stream (observation-time order) of envelopes.
func Generate(cfg Config) []envelope {
	if cfg.TerminateAt == 0 {
		cfg.TerminateAt = -1
	}
	burst := cfg.Samples / 2
	env := make([]envelope, 0, cfg.Samples)
	for i := 0; i < cfg.Samples; i++ {
		f := float64(i) / float64(cfg.Samples-1)
		ascF := clamp(float64(i)/float64(burst), 0, 1)
		p := cfg.SurfaceP
		alt := 0.0
		temp := 20.0 - f*75.0
		switch {
		case i <= burst:
			// exponential pressure decrease on ascent
			p = cfg.SurfaceP * math.Exp(math.Log(cfg.BurstP/cfg.SurfaceP)*ascF)
			alt = altFromPressure(p)
			temp = 20.0 - ascF*75.0
		default:
			// descend back toward surface pressure
			df := float64(i-burst) / float64(cfg.Samples-1-burst)
			p = cfg.BurstP * math.Exp(math.Log(cfg.SurfaceP/cfg.BurstP)*df)
			alt = altFromPressure(p)
			temp = -55.0 + df*70.0
		}
		rh := 55.0
		lat := 35.6812 + 0.001*float64(i)
		lon := 139.7671 + 0.0008*float64(i)
		status := "ok"
		if cfg.TerminateAt >= 0 && i >= cfg.TerminateAt {
			status = "terminated"
		} else if i == burst {
			status = "burst"
		}

		pk := domain.Packet{
			Device: cfg.Device, Seq: i,
			ObsTime:  cfg.Start.Add(time.Duration(i) * cfg.SamplePeriod),
			Pressure: ptr(p), Temp: ptr(temp), RH: ptr(rh),
			Lat: ptr(lat), Lon: ptr(lon), AltGPS: ptr(alt), Status: status,
			PayloadID: "A",
		}

		// Icing: pin RH to 100 across the interval, temp already <=0 aloft.
		if inRange(i, cfg.IcingRange) {
			pk.RH = ptr(100.0)
			pk.Status = mergeStatus(status, "iced")
		}
		// GPS gaps.
		if inRange(i, cfg.GPSGapRange) || inRange(i, cfg.LongGapRange) {
			pk.Lat, pk.Lon, pk.AltGPS = nil, nil, nil
		}
		// Pressure reversal blip: nudge pressure opposite to trend.
		if contains(cfg.ReverseSeqs, i) {
			blip := 3.0
			if i <= burst {
				p = *pk.Pressure + blip // rising pressure during ascent
			} else {
				p = *pk.Pressure - blip
			}
			pk.Pressure = ptr(p)
		}

		e := envelope{seq: i, primary: pk}
		for _, s := range cfg.RetrySeqs {
			if s == i {
				e.retry = true
			}
		}
		for _, s := range cfg.LateSeqs {
			if s == i {
				e.late = true
			}
		}
		for _, s := range cfg.ConflictSeqs {
			if s == i {
				c := pk
				if t, ok := cfg.ConflictTemp[i]; ok {
					c.Temp = ptr(t)
				} else {
					c.Temp = ptr(*c.Temp + 0.5)
				}
				c.PayloadID = "B"
				e.conflict = &c
			}
		}
		env = append(env, e)
	}
	return env
}

// DeliveryPlan turns envelopes into an out-of-order delivery queue. Late
// seqs are moved after the next few later seqs; each retry duplicates its
// primary; conflicts emit both payloads.
func DeliveryPlan(env []envelope, shuffleWindow int, seed int64) []domain.Packet {
	type item struct {
		pk      domain.Packet
		logical int
	}
	var items []item
	logical := 0
	for _, e := range env {
		main := item{pk: e.primary, logical: logical}
		logical++
		items = append(items, main)
		if e.retry {
			items = append(items, item{pk: e.primary, logical: logical})
			logical++
		}
		if e.conflict != nil {
			items = append(items, item{pk: *e.conflict, logical: logical})
			logical++
		}
	}
	// Move late items later in queue.
	lateBySeq := map[int]bool{}
	for _, e := range env {
		lateBySeq[e.seq] = e.late
	}
	var normal, late []item
	for _, it := range items {
		if lateBySeq[it.pk.Seq] {
			late = append(late, it)
		} else {
			normal = append(normal, it)
		}
	}
	// Splice each late item after seq+3 position in normal stream.
	ordered := append([]item{}, normal...)
	for _, li := range late {
		insertAfter := -1
		for idx, it := range ordered {
			if it.pk.Seq >= li.pk.Seq+3 {
				insertAfter = idx
				break
			}
		}
		if insertAfter < 0 {
			ordered = append(ordered, li)
		} else {
			ordered = append(ordered[:insertAfter+1],
				append([]item{li}, ordered[insertAfter+1:]...)...)
		}
	}

	// Bounded shuffle: each item may move at most shuffleWindow slots.
	if shuffleWindow > 0 {
		r := rand.New(rand.NewSource(seed))
		idx := make([]int, len(ordered))
		for i := range idx {
			idx[i] = i
		}
		for i := len(idx) - 1; i > 0; i-- {
			j := i - r.Intn(shuffleWindow+1)
			if j < 0 {
				j = 0
			}
			idx[i], idx[j] = idx[j], idx[i]
		}
		// Restore bounded ordering: sort by constrained key so displacement
		// never exceeds the window.
		type keyed struct {
			it  item
			key int
		}
		ks := make([]keyed, len(ordered))
		for pos, origPos := range idx {
			ks[pos] = keyed{it: ordered[origPos], key: origPos}
		}
		sort.SliceStable(ks, func(a, b int) bool { return ks[a].key < ks[b].key })
		for i := range ks {
			ordered[i] = ks[i].it
		}
	}

	out := make([]domain.Packet, len(ordered))
	for i, it := range ordered {
		out[i] = it.pk
	}
	return out
}

// Stream is a controllable, replayable packet source.
type Stream struct {
	Cfg    Config
	queue  []domain.Packet
	pos    int
	groups [][]domain.Packet // batched delivery groups
	gpos   int
}

func NewStream(cfg Config) *Stream {
	env := Generate(cfg)
	q := DeliveryPlan(env, cfg.ShuffleWindow, cfg.Seed)
	return &Stream{Cfg: cfg, queue: q}
}

// All returns the full out-of-order queue (for one-shot ingest).
func (s *Stream) All() []domain.Packet {
	out := make([]domain.Packet, len(s.queue))
	copy(out, s.queue)
	return out
}

// Reset rewinds the stream so the identical sequence can be replayed.
func (s *Stream) Reset() { s.pos = 0; s.gpos = 0 }

// Next returns the next packet in delivery order, or false at end.
func (s *Stream) Next() (domain.Packet, bool) {
	if s.pos >= len(s.queue) {
		return domain.Packet{}, false
	}
	pk := s.queue[s.pos]
	s.pos++
	return pk, true
}

// Batches splits the queue into n roughly equal batches for step delivery.
func (s *Stream) Batches(n int) [][]domain.Packet {
	if n <= 0 {
		n = 1
	}
	groups := make([][]domain.Packet, n)
	for i, pk := range s.queue {
		groups[i%n] = append(groups[i%n], pk)
	}
	s.groups = groups
	return groups
}

// NextBatch delivers one pre-split batch.
func (s *Stream) NextBatch() ([]domain.Packet, bool) {
	if s.gpos >= len(s.groups) {
		return nil, false
	}
	b := s.groups[s.gpos]
	s.gpos++
	return b, true
}

func ptr(v float64) *float64 { return &v }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func inRange(i int, r [2]int) bool {
	if r[1] < r[0] {
		return false
	}
	return i >= r[0] && i <= r[1]
}

func mergeStatus(a, b string) string {
	if a == "ok" || a == "" {
		return b
	}
	return fmt.Sprintf("%s,%s", a, b)
}

// altFromPressure uses a simple barometric estimate for visualisation and
// simulated GPS altitude (m): h = 44330*(1-(p/p0)^(1/5.255)).
func altFromPressure(p float64) float64 {
	const p0 = 1013.25
	return 44330.0 * (1.0 - math.Pow(p/p0, 1.0/5.255))
}
