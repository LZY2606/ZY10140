// Package profile derives standard-pressure layers from assembled points.
//
// Rules (see README for the full specification):
//   - Each motion branch is profiled independently. Ascent and descent at
//     the same pressure are never averaged or interpolated across.
//   - A level whose pressure exactly equals an observation (within
//     ExactTolerance) uses the observation itself (with its flags).
//   - Otherwise interpolate only between two VALID points that bracket the
//     level AND belong to the same branch. Interpolation is linear in ln(p).
//   - RH inside an icing interval is invalid. Pressure-reversal points are
//     not valid endpoints. Terminated points do not yield layers.
//   - Layers that cannot be derived are kept with an explicit missing reason.
package profile

import (
	"math"
	"time"

	"soundingapp/internal/domain"
)

// Derive produces the full standard-pressure layer set for one version.
func Derive(pts []domain.Point, branches BranchLister) []domain.Layer {
	byBranch := map[int][]domain.Point{}
	for _, pt := range pts {
		byBranch[pt.BranchID] = append(byBranch[pt.BranchID], pt)
	}
	var out []domain.Layer
	for _, b := range branches.List() {
		if b.Phase == domain.PhaseTerminate {
			// Still emit a fully-missing terminated record? We emit layers
			// only for active phases; termination is documented as a flag on
			// points. Descent is the branch that normally carries data after
			// burst, so keep terminate separate.
			continue
		}
		bpts := byBranch[b.BranchID]
		out = append(out, deriveBranch(b.BranchID, b.Phase, bpts)...)
	}
	return out
}

// BranchInfo is the minimal branch metadata Derive needs.
type BranchInfo struct {
	BranchID int
	Phase    domain.Phase
}

type BranchLister interface {
	List() []BranchInfo
}

// BranchList adapts store branch rows.
type BranchList []BranchInfo

func (l BranchList) List() []BranchInfo { return l }

func deriveBranch(bid int, phase domain.Phase, pts []domain.Point) []domain.Layer {
	// Valid, pressure-bearing points in chronological order. Terminated and
	// reversal points are not endpoints.
	valid := make([]domain.Point, 0, len(pts))
	for _, pt := range pts {
		if pt.Phase == domain.PhaseTerminate {
			continue
		}
		if pt.Pressure == nil {
			continue
		}
		if has(pt.Flags, domain.FlagPressureReversal) {
			continue
		}
		valid = append(valid, pt)
	}

	var layers []domain.Layer
	for _, sp := range domain.StandardPressures {
		layers = append(layers, deriveLevel(bid, phase, sp, pts, valid))
	}
	return layers
}

func deriveLevel(bid int, phase domain.Phase, level float64,
	all, valid []domain.Point) domain.Layer {

	l := domain.Layer{BranchID: bid, Phase: phase, Pressure: level}

	// 1) Exact observation anywhere in this branch (within tolerance), even
	//    if it is a reversal point — exact observation is preferred per the
	//    spec ("气压恰好等于标准层时使用观测值").
	if pt, ok := findExact(all, level); ok {
		l.Exact = true
		l.ObsTime = &pt.ObsTime
		l.Flags = append(l.Flags, domain.FlagExactObs)
		l.Flags = append(l.Flags, pt.Flags...)
		switch {
		case pt.Phase == domain.PhaseTerminate:
			l.Missing = domain.MissingTerminated
		case rhInvalid(pt) && pt.Temp == nil:
			l.Missing = domain.MissingExactInvalid
		default:
			l.Temp = pt.Temp
			if rhInvalid(pt) {
				l.RH = nil
				l.Flags = append(l.Flags, domain.FlagIcing)
			} else {
				l.RH = pt.RH
			}
			l.AltGPS = pt.AltGPS
			l.Lat = pt.Lat
			l.Lon = pt.Lon
			// If exact point's temp is itself invalid, mark that variable
			// missing rather than the whole layer.
			if l.Temp == nil {
				l.Flags = append(l.Flags, domain.FlagTempMissing)
			}
		}
		return l
	}

	// 2) Bracketing pair within the same branch.
	lo, hi, ok := bracket(valid, level)
	if !ok {
		if outsideRange(all, level) {
			l.Missing = domain.MissingOutOfRange
		} else {
			l.Missing = domain.MissingNoValidPair
		}
		return l
	}

	// Fraction in ln(p).
	x1, x2 := math.Log(*lo.Pressure), math.Log(*hi.Pressure)
	x := math.Log(level)
	f := 0.0
	if x2 != x1 {
		f = (x - x1) / (x2 - x1)
	}
	t := lo.ObsTime.Add(time.Duration(float64(hi.ObsTime.Sub(lo.ObsTime)) * f))
	l.ObsTime = &t
	l.Interpolated = true
	l.Flags = append(l.Flags, domain.FlagInterp)

	// Pressure trend sanity: endpoints must move monotonically across the
	// level; bracket() already guarantees lo.Pressure >= level >= hi.Pressure.

	// Temp: needs both endpoints valid.
	if lo.Temp != nil && hi.Temp != nil {
		v := interp(*lo.Temp, *hi.Temp, f)
		l.Temp = &v
	} else {
		l.Flags = append(l.Flags, domain.FlagTempMissing)
	}

	// RH: endpoints must be non-iced and present.
	if rhUsable(lo) && rhUsable(hi) {
		v := interp(*lo.RH, *hi.RH, f)
		l.RH = &v
	} else {
		l.Missing = domain.MissingVariableGap
	}

	// GPS altitude/position: usable endpoints (interpolated short gaps are
	// usable; long-gap missing values are nil).
	switch {
	case lo.AltGPS != nil && hi.AltGPS != nil:
		alt := interp(*lo.AltGPS, *hi.AltGPS, f)
		l.AltGPS = &alt
		if lo.Lat != nil && hi.Lat != nil {
			lat := interp(*lo.Lat, *hi.Lat, f)
			lon := interp(*lo.Lon, *hi.Lon, f)
			l.Lat, l.Lon = &lat, &lon
		}
		if has(lo.Flags, domain.FlagGPSGapMissing) || has(hi.Flags, domain.FlagGPSGapMissing) {
			l.AltGPS, l.Lat, l.Lon = nil, nil, nil
			l.Missing = domain.MissingGPSLongGap
		} else if has(lo.Flags, domain.FlagGPSGap) || has(hi.Flags, domain.FlagGPSGap) {
			l.Flags = append(l.Flags, domain.FlagGPSGap)
		}
	default:
		if l.Missing == "" {
			l.Missing = domain.MissingGPSLongGap
		}
	}

	if l.Temp == nil && l.RH == nil && l.AltGPS == nil {
		if l.Missing == "" {
			l.Missing = domain.MissingNoValidPair
		}
	}
	return l
}

func findExact(pts []domain.Point, level float64) (domain.Point, bool) {
	for _, pt := range pts {
		if pt.Pressure != nil && math.Abs(*pt.Pressure-level) <= domain.ExactTolerance {
			return pt, true
		}
	}
	return domain.Point{}, false
}

// bracket returns two same-branch valid points with p(lo) >= level >= p(hi).
// Points are chronological; pressure may decrease (ascent) or increase
// (descent), so we scan each adjacent valid pair in time order.
func bracket(valid []domain.Point, level float64) (domain.Point, domain.Point, bool) {
	for i := 0; i+1 < len(valid); i++ {
		a, b := valid[i], valid[i+1]
		pa, pb := *a.Pressure, *b.Pressure
		if pa >= level && pb <= level || pa <= level && pb >= level {
			// Order so that lo is the higher-pressure endpoint.
			if pa >= pb {
				return a, b, true
			}
			return b, a, true
		}
	}
	return domain.Point{}, domain.Point{}, false
}

func outsideRange(all []domain.Point, level float64) bool {
	minP, maxP := math.Inf(1), math.Inf(-1)
	seen := false
	for _, pt := range all {
		if pt.Pressure == nil {
			continue
		}
		seen = true
		if *pt.Pressure < minP {
			minP = *pt.Pressure
		}
		if *pt.Pressure > maxP {
			maxP = *pt.Pressure
		}
	}
	if !seen {
		return true
	}
	return level < minP || level > maxP
}

func rhInvalid(pt domain.Point) bool {
	return has(pt.Flags, domain.FlagIcing) || has(pt.Flags, domain.FlagIcingManual)
}

func rhUsable(pt domain.Point) bool {
	return pt.RH != nil && !rhInvalid(pt)
}

func interp(a, b, f float64) float64 { return a + (b-a)*f }

func has(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
