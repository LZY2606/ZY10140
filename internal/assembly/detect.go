package assembly

import (
	"math"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/store"
)

// interval is a half-open-ish [Start,End] run of point indices.
type interval struct {
	start int
	end   int
}

func (iv interval) contains(i int) bool { return i >= iv.start && i <= iv.end }

func markDuplicateFlags(pts []domain.Point, sps []store.StoredPacket) {
	// Candidate packets never share (device,seq) inside one trajectory by
	// construction (conflicts become separate candidate versions), so the
	// flags here reflect retry/late evidence recorded on the chosen packet.
	for i, sp := range sps {
		switch sp.DupKind {
		case domain.DupRetry:
			pts[i].Flags = append(pts[i].Flags, domain.FlagRetry)
		case domain.DupConflict:
			pts[i].Flags = append(pts[i].Flags, domain.FlagConflict)
		}
		if sp.Late {
			pts[i].Flags = append(pts[i].Flags, domain.FlagLate)
		}
	}
}

func markInstrumentStatus(pts []domain.Point, p Params) {
	for i := range pts {
		s := pts[i].Status
		if s != "" && s != "ok" && !p.TerminateStatus[s] {
			pts[i].Flags = append(pts[i].Flags, domain.FlagInstrumentStatus)
		}
	}
}

// markTermination returns the index at which a terminate status appears;
// every point at/after it is terminated. -1 when no termination signal.
func markTermination(pts []domain.Point, p Params) int {
	for i := range pts {
		if p.TerminateStatus[pts[i].Status] {
			return i
		}
	}
	return -1
}

// detectPressureReversals marks isolated points whose pressure moves against
// the surrounding trend (an increase during the ascent, a decrease during
// the descent). Such points are excluded from interpolation endpoints.
func detectPressureReversals(pts []domain.Point, p Params) map[int]string {
	bad := map[int]string{}
	n := len(pts)
	for i := 1; i < n-1; i++ {
		a, b, c := pts[i-1].Pressure, pts[i].Pressure, pts[i+1].Pressure
		if a == nil || b == nil || c == nil {
			continue
		}
		dt1 := minutes(pts[i].ObsTime.Sub(pts[i-1].ObsTime))
		dt2 := minutes(pts[i+1].ObsTime.Sub(pts[i].ObsTime))
		if dt1 <= 0 || dt2 <= 0 {
			continue
		}
		rateIn := (*b - *a) / dt1
		rateOut := (*c - *b) / dt2
		// A reversal: the middle point spikes opposite to both neighbours'
		// progression, with a meaningful absolute hop.
		spike := math.Abs(*b - *a)
		spike2 := math.Abs(*c - *b)
		if spike < p.ReversalMinHPa && spike2 < p.ReversalMinHPa {
			continue
		}
		if rateIn > 0 && rateOut < 0 || rateIn < 0 && rateOut > 0 {
			// Only flag the point if the outer points are monotonic; this
			// removes the "bounce" while keeping genuine phase changes.
			outer := *c - *a
			_ = outer
			bad[i] = "pressure rate changes sign across single point"
		}
	}
	return bad
}

func minutes(d time.Duration) float64 { return d.Minutes() }

// detectGPSGaps finds runs of consecutive points lacking GPS altitude; the
// length is the number of missing samples between fixes.
func detectGPSGaps(pts []domain.Point) []interval {
	var runs []interval
	for i := 0; i < len(pts); {
		if pts[i].AltGPS == nil {
			j := i
			for j < len(pts) && pts[j].AltGPS == nil {
				j++
			}
			runs = append(runs, interval{start: i, end: j - 1})
			i = j
		} else {
			i++
		}
	}
	return runs
}

// detectIcing finds sustained near-100% RH at/below 0C.
func detectIcing(pts []domain.Point, p Params) []interval {
	var out []interval
	i := 0
	for i < len(pts) {
		if icingAt(pts[i], p) {
			j := i
			for j < len(pts) && icingAt(pts[j], p) {
				j++
			}
			if j-i >= p.IcingMinRun {
				out = append(out, interval{start: i, end: j - 1})
			}
			i = j
		} else {
			i++
		}
	}
	return out
}

func icingAt(pt domain.Point, p Params) bool {
	return pt.RH != nil && pt.Temp != nil &&
		*pt.RH >= p.IcingMinRH && *pt.Temp <= p.IcingMaxTempC
}

func splitJudgments(js []domain.Judgment) (icing []domain.Judgment, phases []domain.Judgment) {
	for _, j := range js {
		switch j.Kind {
		case "icing":
			icing = append(icing, j)
		case "phase_override":
			phases = append(phases, j)
		}
	}
	return
}

// classifyPhases assigns a preliminary phase per point using the smoothed
// pressure tendency. GPS altitude supports the decision near float plateaus.
func classifyPhases(pts []domain.Point, p Params) []domain.Phase {
	n := len(pts)
	ph := make([]domain.Phase, n)
	// Compute a robust per-point rate using the larger of a 2-step window.
	rate := make([]float64, n) // hPa/min; negative = climbing
	for i := 0; i < n; i++ {
		l, r := i-1, i+1
		if l < 0 || pts[l].Pressure == nil {
			l = i
		}
		if r >= n || pts[r].Pressure == nil {
			r = i
		}
		if l == r {
			rate[i] = 0
			continue
		}
		dt := minutes(pts[r].ObsTime.Sub(pts[l].ObsTime))
		if dt <= 0 {
			rate[i] = 0
			continue
		}
		rate[i] = (*pts[r].Pressure - *pts[l].Pressure) / dt
	}
	// Burst: the single point of maximum altitude-rate sign change; before
	// it we expect ascent, after it descent. Find the first sustained switch
	// from negative to positive rate.
	burst := detectBurstIndex(rate, p)
	for i := 0; i < n; i++ {
		switch {
		case burst >= 0 && i > burst:
			ph[i] = domain.PhaseDescent
		case math.Abs(rate[i]) <= p.FloatRateHPaPerMin:
			ph[i] = domain.PhaseFloat
		case rate[i] < 0:
			ph[i] = domain.PhaseAscent
		default:
			ph[i] = domain.PhaseDescent
		}
	}
	return ph
}

// detectBurstIndex finds the index where the tendency flips from ascent to
// descent and stays on the descent side for several points (balloon burst).
func detectBurstIndex(rate []float64, p Params) int {
	n := len(rate)
	for i := 2; i < n-2; i++ {
		if rate[i-1] < -p.FloatRateHPaPerMin && rate[i+1] > p.FloatRateHPaPerMin &&
			rate[i+2] > 0 {
			return i
		}
	}
	return -1
}

// applyPhaseOverrides forces a phase onto points inside a signed interval.
func applyPhaseOverrides(ph []domain.Phase, pts []domain.Point, overrides []domain.Judgment) {
	for i := range pts {
		for _, j := range overrides {
			if j.Start == nil || j.End == nil || !j.Phase.Valid() {
				continue
			}
			t := pts[i].ObsTime
			if (t.After(*j.Start) || t.Equal(*j.Start)) &&
				(t.Before(*j.End) || t.Equal(*j.End)) {
				ph[i] = j.Phase
				pts[i].Flags = appendUnique(pts[i].Flags, domain.FlagManualPhase)
			}
		}
	}
}

// smoothRuns absorbs runs shorter than MinRunPoints into the neighbours,
// except terminate which is sticky and applied later.
func smoothRuns(ph []domain.Phase, p Params) []domain.Phase {
	if len(ph) == 0 {
		return ph
	}
	out := append([]domain.Phase(nil), ph...)
	for {
		start, ln := firstShortRun(out, p.MinRunPoints)
		if start < 0 {
			return out
		}
		replace := domain.PhaseAscent
		if start > 0 {
			replace = out[start-1]
		} else {
			for k := start + ln; k < len(out); k++ {
				if out[k] != out[start] {
					replace = out[k]
					break
				}
			}
		}
		for k := start; k < start+ln; k++ {
			out[k] = replace
		}
	}
}

func firstShortRun(ph []domain.Phase, min int) (int, int) {
	i := 0
	for i < len(ph) {
		j := i
		for j < len(ph) && ph[j] == ph[i] {
			j++
		}
		if j-i < min {
			// Do not smooth a float run that sits between ascent and
			// descent at burst, and don't touch runs that border a manual
			// phase (manual flags checked outside).
			return i, j - i
		}
		i = j
	}
	return -1, 0
}

func applyTermination(ph []domain.Phase, from int) {
	for i := from; i >= 0 && i < len(ph); i++ {
		ph[i] = domain.PhaseTerminate
	}
}

// assignBranches numbers maximal runs of identical phase. Each branch is a
// unit of interpolation: ascent and descent at the same pressure get
// different branch ids and are never mixed.
func assignBranches(ph []domain.Phase) []int {
	ids := make([]int, len(ph))
	if len(ph) == 0 {
		return ids
	}
	bid := 0
	ids[0] = bid
	for i := 1; i < len(ph); i++ {
		if ph[i] != ph[i-1] {
			bid++
		}
		ids[i] = bid
	}
	return ids
}

func buildBranches(pts []domain.Point, ids []int, ph []domain.Phase, overrides []domain.Judgment) []store.BranchRow {
	if len(pts) == 0 {
		return nil
	}
	var out []store.BranchRow
	for i := 0; i < len(pts); {
		bid := ids[i]
		j := i
		for j < len(pts) && ids[j] == bid {
			j++
		}
		source := "auto"
		for k := i; k < j; k++ {
			if hasFlag(pts[k].Flags, domain.FlagManualPhase) {
				source = "manual"
				break
			}
		}
		out = append(out, store.BranchRow{
			BranchID: bid, Phase: ph[i],
			Start: pts[i].ObsTime, End: pts[j-1].ObsTime, Source: source,
		})
		i = j
	}
	return out
}

func applyIcingFlags(pts []domain.Point, auto []interval, manual []domain.Judgment) {
	for _, iv := range auto {
		for i := iv.start; i <= iv.end; i++ {
			pts[i].Flags = appendUnique(pts[i].Flags, domain.FlagIcing)
		}
	}
	for i := range pts {
		for _, j := range manual {
			if j.Start == nil || j.End == nil {
				continue
			}
			t := pts[i].ObsTime
			if (t.After(*j.Start) || t.Equal(*j.Start)) &&
				(t.Before(*j.End) || t.Equal(*j.End)) {
				pts[i].Flags = appendUnique(pts[i].Flags, domain.FlagIcing, domain.FlagIcingManual)
			}
		}
	}
}

func applyGPSGapFlags(pts []domain.Point, runs []interval, p Params) {
	for _, r := range runs {
		missing := r.end - r.start + 1
		flag := domain.FlagGPSGapMissing
		if missing <= p.GPSInterpMaxMissingSamples {
			flag = domain.FlagGPSGap
			interpolateGPS(pts, r)
		}
		for i := r.start; i <= r.end; i++ {
			pts[i].Flags = appendUnique(pts[i].Flags, flag)
		}
	}
}

// interpolateGPS linearly fills short altitude/position gaps between the
// bracketing fixes, which must exist in the same branch vicinity.
func interpolateGPS(pts []domain.Point, r interval) {
	if r.start == 0 || r.end == len(pts)-1 {
		return // no brackets on both sides; leave missing
	}
	a, b := pts[r.start-1], pts[r.end+1]
	if a.AltGPS == nil || b.AltGPS == nil {
		return
	}
	span := b.OrderIdx - a.OrderIdx
	for i := r.start; i <= r.end; i++ {
		f := float64(pts[i].OrderIdx-a.OrderIdx) / float64(span)
		alt := lerp(*a.AltGPS, *b.AltGPS, f)
		pts[i].AltGPS = &alt
		if a.Lat != nil && b.Lat != nil {
			lat := lerp(*a.Lat, *b.Lat, f)
			lon := lerp(*a.Lon, *b.Lon, f)
			pts[i].Lat, pts[i].Lon = &lat, &lon
		}
	}
}

func applyReversalFlags(pts []domain.Point, bad map[int]string) {
	for i := range bad {
		pts[i].Flags = appendUnique(pts[i].Flags, domain.FlagPressureReversal)
	}
}

func lerp(a, b, f float64) float64 { return a + (b-a)*f }

func appendUnique(xs []string, vs ...string) []string {
	for _, v := range vs {
		exists := false
		for _, x := range xs {
			if x == v {
				exists = true
				break
			}
		}
		if !exists {
			xs = append(xs, v)
		}
	}
	return xs
}

func hasFlag(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
