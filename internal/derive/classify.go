package derive

import (
	"time"

	"sonde/internal/domain"
)

// classify labels every primary-track point with a motion phase. Manual
// overrides take precedence; automatic labels come from the sign of the
// rolling-median pressure tendency with hysteresis smoothing.
func classify(points []domain.Point, idx overrideIndex) {
	auto := autoPhases(points)
	for i := range points {
		if ph, ok := idx.phaseAt(points[i].Time); ok {
			points[i].Phase = ph
			points[i].PhaseSource = "manual"
			continue
		}
		points[i].Phase = auto[i]
		points[i].PhaseSource = "auto"
	}

	// Pressure short-term reversal: the local tendency points against the
	// surrounding branch motion. Reversals stay usable for interpolation
	// only when bracketed by same-branch points (handled in levels).
	for i := range points {
		prev, next := nearestValidPressure(points, i, -1), nearestValidPressure(points, i, 1)
		cur := points[i].Pressure
		if cur == nil || prev == nil || next == nil {
			continue
		}
		switch points[i].Phase {
		case domain.PhaseAscent:
			if *cur > *prev && *cur > *next {
				points[i].Reversal = true
			}
		case domain.PhaseDescent:
			if *cur < *prev && *cur < *next {
				points[i].Reversal = true
			}
		}
	}
}

// autoPhases produces per-point labels without manual overrides.
func autoPhases(points []domain.Point) []domain.Phase {
	n := len(points)
	labels := make([]domain.Phase, n)
	if n == 0 {
		return labels
	}

	// terminated points: once the instrument reports TERMINATED, the rest
	// of the record is terminal.
	terminatedFrom := n
	for i, p := range points {
		if p.Status == "TERMINATED" {
			terminatedFrom = i
			break
		}
	}
	for i := terminatedFrom; i < n; i++ {
		labels[i] = domain.PhaseTerminated
	}

	type win struct {
		i int
		d float64
	}
	raw := make([]float64, terminatedFrom)
	for i := 0; i < terminatedFrom; i++ {
		lo, hi := i-2, i+2
		if lo < 0 {
			lo = 0
		}
		if hi >= terminatedFrom {
			hi = terminatedFrom - 1
		}
		vals := []float64{}
		for j := lo; j <= hi; j++ {
			if points[j].Pressure != nil {
				vals = append(vals, *points[j].Pressure)
			}
		}
		raw[i] = median(vals)
	}
	// tendency d = pressure change to the next median; ascent => d<0
	d := make([]float64, terminatedFrom)
	for i := 0; i < terminatedFrom; i++ {
		if i+1 < terminatedFrom {
			d[i] = raw[i+1] - raw[i]
		} else if i > 0 {
			d[i] = raw[i] - raw[i-1]
		}
	}

	const floatTol = 0.2 // hPa per sample, after median smoothing
	for i := 0; i < terminatedFrom; i++ {
		switch {
		case d[i] < -floatTol:
			labels[i] = domain.PhaseAscent
		case d[i] > floatTol:
			labels[i] = domain.PhaseDescent
		default:
			labels[i] = domain.PhaseFloat
		}
	}

	// float run smoothing: short float runs (median-level plateaus) shorter
	// than 3 samples inherit the surrounding dominant phase.
	labels = smoothRuns(labels, terminatedFrom, 3)

	// A BURST status anchors the first descent sample.
	for i, p := range points[:terminatedFrom] {
		if p.Status == "BURST" && i+1 < terminatedFrom {
			for j := i + 1; j < terminatedFrom; j++ {
				if labels[j] == domain.PhaseFloat || labels[j] == domain.PhaseAscent {
					labels[j] = domain.PhaseDescent
				}
			}
		}
	}
	return labels
}

// smoothRuns replaces float runs shorter than minLen with the neighbor
// phase, preferring the phase that dominates the run's surroundings.
func smoothRuns(labels []domain.Phase, n, minLen int) []domain.Phase {
	i := 0
	for i < n {
		if labels[i] != domain.PhaseFloat {
			i++
			continue
		}
		j := i
		for j < n && labels[j] == domain.PhaseFloat {
			j++
		}
		runLen := j - i
		if runLen < minLen {
			before, after := domain.Phase(""), domain.Phase("")
			if i > 0 {
				before = labels[i-1]
			}
			if j < n {
				after = labels[j]
			}
			repl := domain.PhaseAscent
			switch {
			case before != "" && after != "":
				// merge into descent side once descent began
				if before == domain.PhaseDescent || after == domain.PhaseDescent {
					repl = domain.PhaseDescent
				} else if before == after {
					repl = before
				} else {
					repl = after
				}
			case before != "":
				repl = before
			case after != "":
				repl = after
			}
			for k := i; k < j; k++ {
				labels[k] = repl
			}
		}
		i = j
	}
	return labels
}

func nearestValidPressure(points []domain.Point, i, dir int) *float64 {
	for j := i + dir; j >= 0 && j < len(points); j += dir {
		if points[j].Pressure != nil {
			return points[j].Pressure
		}
	}
	return nil
}

// markGPSGaps flags points without a fix or more than 30 s after the last fix.
func markGPSGaps(points []domain.Point) {
	var lastFix time.Time
	for i := range points {
		if points[i].Lat == nil || points[i].Lon == nil {
			points[i].GPSGap = true
			continue
		}
		if !lastFix.IsZero() && points[i].Time.Sub(lastFix).Seconds() > gpsGapSecs {
			points[i].GPSGap = true
		}
		lastFix = points[i].Time
	}
}
