// Package derive turns raw, possibly duplicated packets into one assembled
// sounding: a de-duplicated timeline, motion branches, standard-pressure
// levels and quality flags with machine-readable evidence.
package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"sonde/internal/domain"
)

// StandardLevels are the mandatory standard pressure surfaces (hPa),
// ordered surface to ceiling.
var StandardLevels = []float64{
	1000, 925, 850, 700, 500, 400, 300, 250, 200, 150, 100, 70, 50, 30, 20, 10, 7, 5, 3, 2, 1,
}

const (
	exactTol   = 0.05 // hPa tolerance for "pressure equals standard level"
	gpsGapSecs = 30.0 // seconds without a GPS fix counts as a breakpoint
)

// Input bundles everything the assembler needs.
type Input struct {
	Rows      []domain.Packet
	Overrides []domain.Override
}

// Result is the assembled sounding; draft responses are built from it.
type Result struct {
	Points      []domain.Point      `json:"points"`
	AltPoints   []domain.Point      `json:"alt_points"`
	Branches    []domain.Branch     `json:"branches"`
	Transitions []domain.Transition `json:"transitions"`
	Levels      []domain.Level      `json:"levels"`
	Conflicts   []ConflictGroup     `json:"conflicts"`
}

// ConflictGroup describes the distinct payloads seen for one sequence.
type ConflictGroup struct {
	Seq        int64              `json:"seq"`
	Time       time.Time          `json:"time"`
	Candidates []domain.Candidate `json:"candidates"`
	ChosenID   *int64             `json:"chosen_candidate_id,omitempty"`
	KeepBoth   bool               `json:"keep_both"`
}

// PayloadHash is the canonical fingerprint of a packet payload. The
// receive timestamp is deliberately excluded: identical telegrams retried
// on the wire must hash identically.
func PayloadHash(p domain.Packet) string {
	if p.Payload != "" {
		return p.Payload
	}
	h := sha256.New()
	write := func(b []byte) { h.Write(b) }
	write([]byte(itoa(p.Seq)))
	write([]byte("|"))
	write([]byte(p.ObservedAt.UTC().Format(time.RFC3339Nano)))
	write([]byte("|"))
	writeFloat(h, p.Pressure)
	writeFloat(h, p.Temp)
	writeFloat(h, p.RH)
	writeFloat(h, p.Lat)
	writeFloat(h, p.Lon)
	writeFloat(h, p.Alt)
	write([]byte("|" + p.Status))
	return hex.EncodeToString(h.Sum(nil))
}
