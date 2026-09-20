package profile

import (
	"testing"
	"time"

	"soundingapp/internal/domain"
)

func fp(v float64) *float64 { return &v }
func tm(sec int) time.Time {
	return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

func pt(seq int, sec int, p, tC, rh, alt float64, phase domain.Phase, branch int, flags ...string) domain.Point {
	return domain.Point{
		Device: "d", Seq: seq, ObsTime: tm(sec), OrderIdx: seq,
		Pressure: fp(p), Temp: fp(tC), RH: fp(rh), AltGPS: fp(alt),
		Lat: fp(1), Lon: fp(2), Phase: phase, BranchID: branch, Flags: flags,
	}
}

func layerFor(ls []domain.Layer, branch int, p float64) (domain.Layer, bool) {
	for _, l := range ls {
		if l.BranchID == branch && l.Pressure == p {
			return l, true
		}
	}
	return domain.Layer{}, false
}

func TestInterpolationSameBranchOnly(t *testing.T) {
	// Ascent crosses 850 and 700; descent crosses them too. Values must be
	// computed per branch and never averaged across branches.
	pts := []domain.Point{
		pt(0, 0, 900, 10, 60, 900, domain.PhaseAscent, 0),
		pt(1, 2, 800, 5, 55, 1900, domain.PhaseAscent, 0),
		pt(2, 4, 700, -5, 50, 3000, domain.PhaseAscent, 0),
		pt(3, 6, 800, 7, 57, 1900, domain.PhaseDescent, 1),
		pt(4, 8, 900, 12, 62, 900, domain.PhaseDescent, 1),
	}
	bl := BranchList{{BranchID: 0, Phase: domain.PhaseAscent}, {BranchID: 1, Phase: domain.PhaseDescent}}
	ls := Derive(pts, bl)

	a850, ok := layerFor(ls, 0, 850)
	if !ok || !a850.Interpolated || a850.RH == nil || a850.Temp == nil {
		t.Fatalf("ascent 850 not interpolated: %+v ok=%v", a850, ok)
	}
	// ln(p) interpolation fraction: between 900 and 800, at 850
	wantT := 10 + (5-10)*fraction(900, 800, 850)
	if a850.Temp == nil || abs(*a850.Temp-wantT) > 1e-6 {
		t.Fatalf("ascent 850 temp=%v want %v", a850.Temp, wantT)
	}
	d850, ok := layerFor(ls, 1, 850)
	if !ok || d850.Temp == nil {
		t.Fatalf("descent 850 missing: %+v", d850)
	}
	// Distinct temperatures on the two branches prove no mixing.
	wantD := 7 + (12-7)*fraction(800, 900, 850)
	if abs(*d850.Temp-wantD) > 1e-6 || *d850.Temp == *a850.Temp {
		t.Fatalf("descent temp=%v want %v (ascent=%v)", *d850.Temp, wantD, *a850.Temp)
	}
}

func TestExactObservation(t *testing.T) {
	pts := []domain.Point{
		pt(0, 0, 900, 10, 60, 900, domain.PhaseAscent, 0),
		pt(1, 2, 850, 6.5, 58, 1450, domain.PhaseAscent, 0),
		pt(2, 4, 700, -5, 50, 3000, domain.PhaseAscent, 0),
	}
	ls := Derive(pts, BranchList{{BranchID: 0, Phase: domain.PhaseAscent}})
	l, ok := layerFor(ls, 0, 850)
	if !ok || !l.Exact || l.Temp == nil || *l.Temp != 6.5 {
		t.Fatalf("exact layer wrong: %+v", l)
	}
}

func TestOutOfRangeAndNoPair(t *testing.T) {
	// All points between 800-900 hPa; 100 hPa is out of range.
	pts := []domain.Point{
		pt(0, 0, 900, 10, 60, 900, domain.PhaseAscent, 0),
		pt(1, 2, 800, 5, 55, 1900, domain.PhaseAscent, 0),
	}
	ls := Derive(pts, BranchList{{BranchID: 0, Phase: domain.PhaseAscent}})
	l, _ := layerFor(ls, 0, 100)
	if l.Missing != domain.MissingOutOfRange {
		t.Fatalf("100hPa missing=%q want OUT_OF_RANGE", l.Missing)
	}
}

func TestIcingInvalidatesRH(t *testing.T) {
	pts := []domain.Point{
		pt(0, 0, 900, -10, 100, 900, domain.PhaseAscent, 0, domain.FlagIcing),
		pt(1, 2, 800, -15, 100, 1900, domain.PhaseAscent, 0, domain.FlagIcing),
	}
	ls := Derive(pts, BranchList{{BranchID: 0, Phase: domain.PhaseAscent}})
	l, _ := layerFor(ls, 0, 850)
	if l.RH != nil || l.Missing != domain.MissingVariableGap {
		t.Fatalf("iced RH should be missing: %+v", l)
	}
	if l.Temp == nil {
		t.Fatalf("temp should still interpolate under icing")
	}
}

func TestReversalNotAnEndpoint(t *testing.T) {
	// 850 hPa is the reversal point: it must not anchor interpolation.
	pts := []domain.Point{
		pt(0, 0, 900, 10, 60, 900, domain.PhaseAscent, 0),
		pt(1, 1, 850, 20, 99, 1400, domain.PhaseAscent, 0, domain.FlagPressureReversal),
		pt(2, 2, 800, 5, 55, 1900, domain.PhaseAscent, 0),
	}
	ls := Derive(pts, BranchList{{BranchID: 0, Phase: domain.PhaseAscent}})
	l, _ := layerFor(ls, 0, 850)
	// Exact observation is preferred per spec, but it carries the reversal
	// flag so the UI shows the evidence; temp remains the observed value.
	if !l.Exact {
		t.Fatalf("exact-pressure observation must be used even with reversal: %+v", l)
	}
	found := false
	for _, f := range l.Flags {
		if f == domain.FlagPressureReversal {
			found = true
		}
	}
	if !found {
		t.Fatalf("exact layer should inherit reversal flag: %+v", l.Flags)
	}
}

func fraction(p1, p2, level float64) float64 {
	// replicates ln(p) interpolation
	x1, x2, x := ln(p1), ln(p2), ln(level)
	return (x - x1) / (x2 - x1)
}

func ln(v float64) float64 {
	// avoid importing math only for one call; use math directly instead
	return mathLn(v)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
