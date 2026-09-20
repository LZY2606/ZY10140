// Package assembly turns deduplicated packet candidates into ordered
// trajectories, classifies motion branches (ascent / float / descent /
// terminate), detects pressure reversals, humidity-sensor icing and GPS
// breaks, and applies human judgments as signed overrides.
package assembly

import (
	"sort"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/store"
)

// Params controls the heuristics. Defaults are chosen for ~2 s sondes
// reporting cadence.
type Params struct {
	// Pressure rate (hPa per minute) below which a run is considered
	// floating (near-zero vertical motion).
	FloatRateHPaPerMin float64
	// Minimum run length (points) for a phase to survive smoothing; shorter
	// runs are absorbed into the surrounding branch.
	MinRunPoints int
	// Icing detection: relative humidity at/above this value together with
	// temperature at/below IcingMaxTempC sustained for IcingMinRun points.
	IcingMinRH    float64
	IcingMaxTempC float64
	IcingMinRun   int
	// GPS gaps: <= this many missing samples interpolate and flag; longer
	// gaps leave altitude/position missing with a reason.
	GPSInterpMaxMissingSamples int
	// Pressure reversals: a single-point inversion larger than this fraction
	// of the local trend is treated as a short-term reversal (bad endpoint).
	ReversalMinHPa float64
	// Terminate status words stop layer derivation.
	TerminateStatus map[string]bool
}

func DefaultParams() Params {
	return Params{
		FloatRateHPaPerMin:         2.0,
		MinRunPoints:               3,
		IcingMinRH:                 99.5,
		IcingMaxTempC:              0.0,
		IcingMinRun:                4,
		GPSInterpMaxMissingSamples: 2,
		ReversalMinHPa:             0.5,
		TerminateStatus: map[string]bool{
			"terminated": true, "term": true, "end_of_sounding": true,
		},
	}
}

// Input bundles one candidate trajectory's evidence.
type Input struct {
	SoundingID string
	Kind       string // draft | candidate_a | candidate_b
	ParentID   int64
	Packets    []store.StoredPacket
	Judgments  []domain.Judgment
}

// Assemble builds one store.AssembledVersion from one Input.
func Assemble(in Input, p Params, now time.Time) store.AssembledVersion {
	obs := orderObservations(in.Packets)
	pts := make([]domain.Point, len(obs))
	for i, sp := range obs {
		pts[i] = toPoint(sp, i)
	}

	markDuplicateFlags(pts, obs)
	markInstrumentStatus(pts, p)
	terminatedFrom := markTermination(pts, p)
	reversals := detectPressureReversals(pts, p)
	gapRuns := detectGPSGaps(pts)
	autoIcing := detectIcing(pts, p)

	manualIcing, phaseOverrides := splitJudgments(in.Judgments)
	phases := classifyPhases(pts, p)
	applyPhaseOverrides(phases, pts, phaseOverrides)
	phases = smoothRuns(phases, p)
	applyTermination(phases, terminatedFrom)

	branchIDs := assignBranches(phases)
	branches := buildBranches(pts, branchIDs, phases, phaseOverrides)
	applyIcingFlags(pts, autoIcing, manualIcing)
	applyGPSGapFlags(pts, gapRuns, p)
	applyReversalFlags(pts, reversals)
	for i := range pts {
		pts[i].BranchID = branchIDs[i]
		pts[i].Phase = phases[i]
	}

	findings := buildFindings(obs, autoIcing, gapRuns, reversals, branches, terminatedFrom)

	out := store.AssembledVersion{
		Kind:     in.Kind,
		ParentID: in.ParentID,
		Points:   pts,
		Branches: branches,
		Findings: findings,
	}
	return out
}

// orderObservations sorts by onboard observation time, then seq, then
// arrival/hash for deterministic tie-breaking.
func orderObservations(sps []store.StoredPacket) []store.StoredPacket {
	out := make([]store.StoredPacket, 0, len(sps))
	out = append(out, sps...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Packet.ObsTime, out[j].Packet.ObsTime
		if !a.Equal(b) {
			return a.Before(b)
		}
		if a2, b2 := out[i].Seq, out[j].Seq; a2 != b2 {
			return a2 < b2
		}
		if out[i].ReceivedAt != out[j].ReceivedAt {
			return out[i].ReceivedAt.Before(out[j].ReceivedAt)
		}
		return out[i].Hash < out[j].Hash
	})
	return out
}

func toPoint(sp store.StoredPacket, idx int) domain.Point {
	return domain.Point{
		Device:   sp.Device,
		Seq:      sp.Seq,
		ObsTime:  sp.ObsTime,
		Pressure: sp.Pressure,
		Temp:     sp.Temp,
		RH:       sp.RH,
		Lat:      sp.Lat,
		Lon:      sp.Lon,
		AltGPS:   sp.AltGPS,
		Status:   sp.Status,
		Hash:     sp.Hash,
		OrderIdx: idx,
	}
}
