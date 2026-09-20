// Package sim generates a synthetic radiosonde flight and a deliberately
// messy delivery stream: duplicate retries, conflicting payloads, late
// packets, pressure reversals, iced humidity and GPS breakpoints.
package sim

import (
	"math"
	"time"

	"sonde/internal/domain"
)

type Config struct {
	DeviceID     string
	Start        time.Time
	AscentSteps  int   // samples up to burst
	FloatSteps   int   // plateau samples near 300 hPa
	DescentSteps int   // samples after burst
	ConflictSeq  int64 // seq delivered twice with different payloads (-1 none)
	RetrySeqs    []int64
	LateSeq      int64 // seq withheld until after later packets (-1 none)
	GPSGapSeqLo  int64
	GPSGapSeqHi  int64
	IcingLo      int64
	IcingHi      int64
	ReversalSeq  int64
	Seed         int64
}

// Delivery is one wire packet at one receive instant.
type Delivery struct {
	Packet     domain.Packet
	ReceivedAt time.Time
	Note       string // retry | conflict | late
}

func DefaultConfig() Config {
	return Config{
		DeviceID:     "RSN-DEMO-01",
		Start:        time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		AscentSteps:  120,
		FloatSteps:   10,
		DescentSteps: 90,
		ConflictSeq:  140,
		RetrySeqs:    []int64{42, 87},
		LateSeq:      60,
		GPSGapSeqLo:  95,
		GPSGapSeqHi:  100,
		IcingLo:      108,
		IcingHi:      116,
		ReversalSeq:  73,
		Seed:         42,
	}
}

// Generate builds the "true" flight, then the disordered delivery list.
func Generate(cfg Config) []Delivery {
	pkts := flight(cfg)
	bySeq := map[int64]domain.Packet{}
	for _, p := range pkts {
		bySeq[p.Seq] = p
	}

	var dels []Delivery
	recv := cfg.Start.Add(-5 * time.Second)
	nextRecv := func(step int64) time.Time {
		recv = recv.Add(2 * time.Second)
		return recv
	}

	lateHeld := domain.Packet{}
	haveLate := false

	for _, p := range pkts {
		step := p.Seq

		if cfg.LateSeq >= 0 && step == cfg.LateSeq {
			lateHeld = p
			haveLate = true
			continue
		}

		// inject pressure reversal blip on the ascent
		if step == cfg.ReversalSeq {
			blip := p
			v := *blip.Pressure + 3.5
			blip.Pressure = &v
			dels = append(dels, Delivery{Packet: blip, ReceivedAt: nextRecv(step), Note: "reversal"})
			continue
		}

		// conflict: first deliver the variant, later the canonical payload
		if cfg.ConflictSeq >= 0 && step == cfg.ConflictSeq {
			variant := p
			va := *variant.Alt + 120
			vt := *variant.Temp + 0.8
			variant.Alt, variant.Temp = &va, &vt
			variant.Status = "CONFLICT-VARIANT"
			dels = append(dels, Delivery{Packet: variant, ReceivedAt: nextRecv(step), Note: "conflict"})
			continue
		}
		if cfg.ConflictSeq >= 0 && step == cfg.ConflictSeq+12 {
			canon := bySeq[cfg.ConflictSeq]
			dels = append(dels, Delivery{Packet: canon, ReceivedAt: nextRecv(step), Note: "conflict"})
		}

		// pure retries: identical payload, later arrival
		isRetry := false
		for _, r := range cfg.RetrySeqs {
			if r == step {
				isRetry = true
			}
		}
		dels = append(dels, Delivery{Packet: p, ReceivedAt: nextRecv(step)})
		if isRetry {
			dels = append(dels, Delivery{Packet: p, ReceivedAt: nextRecv(step), Note: "retry"})
		}
	}

	// late packet arrives well after later sequences
	if haveLate {
		dels = append(dels, Delivery{Packet: lateHeld, ReceivedAt: recv.Add(20 * time.Second), Note: "late"})
	}
	return dels
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// altForPressure is the inverse of the ICAO standard atmosphere used to
// give the synthetic flight self-consistent geometry.
func altForPressure(p float64) float64 {
	const (
		p0 = 1013.25
		t0 = 288.15
		lr = 0.0065
		g  = 9.80665
		mr = 0.0289644
		rr = 8.31432
	)
	if p >= 226.32 {
		return t0 / lr * (1 - math.Pow(p/p0, rr*lr/(mr*g)))
	}
	// stratosphere, isothermal at 216.65 K from 11 km
	const t1 = 216.65
	const h1 = 11000.0
	p1 := p0 * math.Pow(t1/t0, mr*g/(rr*lr))
	return h1 + (rr*t1)/(mr*g)*math.Log(p1/p)
}
