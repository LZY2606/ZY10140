package derive

import (
	"math"
	"time"

	"sonde/internal/domain"
)

// deriveLevels produces standard-pressure levels per MOTION GROUP. A motion
// group is one branch, except that ascent segments separated by a float
// plateau share a group (the plateau itself stays a visible branch).
// Ascent and descent groups are never averaged together; interpolation only
// happens between two valid points of the same group, and every failure is
// retained as a missing reason.
func deriveLevels(points []domain.Point, branches []domain.Branch) []domain.Level {
	groups := motionGroups(branches)
	primaryGroup := pickPrimaryGroup(groups)
	var out []domain.Level
	for _, g := range groups {
		if g.kind == domain.PhaseTerminated {
			continue
		}
		gp := groupPoints(points, branches, g)
		if len(gp) == 0 {
			continue
		}
		pmin, pmax := pressureRange(gp)
		for _, L := range StandardLevels {
			lvl := domain.Level{
				Pressure: L,
				BranchID: g.firstBranch,
				Kind:     g.kind,
				Primary:  g.id == primaryGroup,
				Missing:  []domain.MissingField{},
			}
			switch {
			case L > pmax+exactTol:
				lvl.Missing = allMissing("below_branch_range")
				if g.id == primaryGroup {
					out = append(out, lvl)
				}
			case L < pmin-exactTol:
				lvl.Missing = allMissing("above_branch_range")
				if g.id == primaryGroup {
					out = append(out, lvl)
				}
			default:
				fillLevel(&lvl, gp)
				out = append(out, lvl)
			}
		}
	}
	return out
}

type motionGroup struct {
	id          int
	kind        domain.Phase
	firstBranch int
	branchIDs   []int
}

func motionGroups(branches []domain.Branch) []motionGroup {
	var out []motionGroup
	seen := map[int]bool{}
	// emit in branch order
	for _, b := range branches {
		if b.Kind == domain.PhaseTerminated {
			out = append(out, motionGroup{id: b.ID, kind: b.Kind, firstBranch: b.ID, branchIDs: []int{b.ID}})
			continue
		}
		if seen[b.GroupID] {
			continue
		}
		seen[b.GroupID] = true
		mg := motionGroup{id: b.GroupID, kind: b.Kind, firstBranch: b.ID}
		for _, bb := range branches {
			if bb.GroupID == b.GroupID {
				mg.branchIDs = append(mg.branchIDs, bb.ID)
			}
		}
		out = append(out, mg)
	}
	return out
}

func pickPrimaryGroup(groups []motionGroup) int {
	for _, g := range groups {
		if g.kind == domain.PhaseAscent {
			return g.id
		}
	}
	if len(groups) > 0 {
		return groups[0].id
	}
	return 0
}

func groupPoints(points []domain.Point, branches []domain.Branch, g motionGroup) []domain.Point {
	inGroup := map[int]bool{}
	for _, id := range g.branchIDs {
		inGroup[id] = true
	}
	var out []domain.Point
	for _, p := range points {
		if p.TrackID == 0 && inGroup[p.BranchID] {
			out = append(out, p)
		}
	}
	return out
}

func pressureRange(ps []domain.Point) (min, max float64) {
	min, max = math.Inf(1), math.Inf(-1)
	for _, p := range ps {
		if p.Pressure == nil {
			continue
		}
		if *p.Pressure < min {
			min = *p.Pressure
		}
		if *p.Pressure > max {
			max = *p.Pressure
		}
	}
	return min, max
}

func allMissing(reason string) []domain.MissingField {
	var ms []domain.MissingField
	for _, f := range []string{"alt_m", "temp_c", "rh_pct", "lat", "lon"} {
		ms = append(ms, domain.MissingField{Field: f, Reason: reason})
	}
	return ms
}

func fillLevel(lvl *domain.Level, gp []domain.Point) {
	target := lvl.Pressure

	// exact observation wins: observed pressure (within tolerance) equals
	// the standard level.
	for i := range gp {
		p := gp[i]
		if p.Pressure != nil && near(*p.Pressure, target, exactTol) {
			lvl.Exact = true
			t := p.Time
			lvl.Time = &t
			if p.KeptBoth {
				lvl.Ambiguous = true
				lvl.Missing = allMissing("duplicate_conflict_unresolved")
				return
			}
			lvl.Alt = withField(lvl, p, "alt_m", p.Alt)
			lvl.Temp = withField(lvl, p, "temp_c", p.Temp)
			lvl.RH = withRH(lvl, p, p.RH)
			lvl.Lat = withField(lvl, p, "lat", p.Lat)
			lvl.Lon = withField(lvl, p, "lon", p.Lon)
			if p.Conflict {
				lvl.Ambiguous = true
			}
			return
		}
	}

	// bracketing pair within this same motion group
	lo, hi := bracket(lvl, gp, target)
	if lo < 0 || hi < 0 {
		lvl.Missing = allMissing("no_same_branch_bracket")
		return
	}
	a, b := gp[lo], gp[hi]
	if a.KeptBoth || b.KeptBoth {
		lvl.Ambiguous = true
		lvl.Missing = allMissing("duplicate_conflict_unresolved")
		return
	}
	if a.Conflict || b.Conflict {
		lvl.Ambiguous = true
	}
	w := weight(*a.Pressure, *b.Pressure, target)
	t := interpTime(a.Time, b.Time, w)
	lvl.Time = &t
	lvl.Alt = interpField(lvl, "alt_m", a, b, w)
	lvl.Temp = interpField(lvl, "temp_c", a, b, w)
	lvl.RH = interpRH(lvl, a, b, w)
	lvl.Lat = interpField(lvl, "lat", a, b, w)
	lvl.Lon = interpField(lvl, "lon", a, b, w)
}

func bracket(lvl *domain.Level, gp []domain.Point, target float64) (lo, hi int) {
	// points are sequence-ordered; pressure may fall (ascent) or rise
	// (descent), so take the two consecutive valid-pressure points straddling
	// the target. Samples belonging to float branches are skipped when an
	// ascent group bridges a plateau (the plateau emits its own levels), so
	// interpolation never crosses a motion boundary implicitly. Kept-both
	// points remain visible so the conflict reason can be emitted.
	var usable []int
	for i := range gp {
		if lvl.Kind != domain.PhaseFloat && gp[i].Phase == domain.PhaseFloat {
			continue
		}
		usable = append(usable, i)
	}
	for k := 0; k+1 < len(usable); k++ {
		i, j := usable[k], usable[k+1]
		p0, p1 := gp[i].Pressure, gp[j].Pressure
		if p0 == nil || p1 == nil {
			continue
		}
		if (target >= *p0 && target <= *p1) || (target <= *p0 && target >= *p1) {
			return i, j
		}
	}
	return -1, -1
}

// weight is the log-pressure interpolation fraction at target between p0,p1.
func weight(p0, p1, target float64) float64 {
	l0, l1, lt := math.Log(p0), math.Log(p1), math.Log(target)
	if l1 == l0 {
		return 0
	}
	return (lt - l0) / (l1 - l0)
}

func lerp(a, b, w float64) float64 { return a + (b-a)*w }

func interpTime(a, b time.Time, w float64) time.Time {
	return a.Add(time.Duration(float64(b.Sub(a)) * w))
}

func interpField(lvl *domain.Level, name string, a, b domain.Point, w float64) *float64 {
	va, vb := fieldValue(name, a), fieldValue(name, b)
	if va == nil || vb == nil {
		addMissing(lvl, name, "observation_missing")
		return nil
	}
	if a.GPSGap || b.GPSGap {
		// a GPS breakpoint only invalidates position. Barometric altitude
		// remains interpolable between the pressure-anchored endpoints.
		if name == "lat" || name == "lon" {
			addMissing(lvl, name, "gps_breakpoint")
			return nil
		}
	}
	v := lerp(*va, *vb, w)
	return &v
}

func interpRH(lvl *domain.Level, a, b domain.Point, w float64) *float64 {
	if a.Icing || b.Icing {
		addMissing(lvl, "rh_pct", "humidity_sensor_iced")
		return nil
	}
	if a.RH == nil || b.RH == nil {
		addMissing(lvl, "rh_pct", "observation_missing")
		return nil
	}
	v := lerp(*a.RH, *b.RH, w)
	return &v
}

func withField(lvl *domain.Level, p domain.Point, name string, v *float64) *float64 {
	if v == nil {
		addMissing(lvl, name, "observation_missing")
		return nil
	}
	if p.GPSGap && (name == "lat" || name == "lon") {
		addMissing(lvl, name, "gps_breakpoint")
		return nil
	}
	cp := *v
	return &cp
}

func withRH(lvl *domain.Level, p domain.Point, v *float64) *float64 {
	if p.Icing {
		addMissing(lvl, "rh_pct", "humidity_sensor_iced")
		return nil
	}
	return withField(lvl, p, "rh_pct", v)
}

func addMissing(lvl *domain.Level, field, reason string) {
	for _, m := range lvl.Missing {
		if m.Field == field {
			return
		}
	}
	lvl.Missing = append(lvl.Missing, domain.MissingField{Field: field, Reason: reason})
}

func fieldValue(name string, p domain.Point) *float64 {
	switch name {
	case "alt_m":
		return p.Alt
	case "temp_c":
		return p.Temp
	case "rh_pct":
		return p.RH
	case "lat":
		return p.Lat
	case "lon":
		return p.Lon
	}
	return nil
}
