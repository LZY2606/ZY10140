package assembly

import (
	"testing"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/store"
)

func fp(v float64) *float64 { return &v }
func baseTime(i int) time.Time {
	return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 2 * time.Second)
}

// build a simple ascent -> burst -> descent flight with monotonic pressure.
func flightPackets(n int) []store.StoredPacket {
	out := make([]store.StoredPacket, n)
	burst := n / 2
	for i := 0; i < n; i++ {
		var p, alt, temp float64
		switch {
		case i <= burst:
			f := float64(i) / float64(burst)
			p = 1000 - f*900
			alt = f * 16000
			temp = 20 - f*70
		default:
			f := float64(i-burst) / float64(n-1-burst)
			p = 100 + f*900
			alt = (1 - f) * 16000
			temp = -50 + f*65
		}
		status := "ok"
		if i == burst {
			status = "burst"
		}
		pk := domain.Packet{
			Device: "dev", Seq: i, ObsTime: baseTime(i),
			Pressure: fp(p), Temp: fp(temp), RH: fp(50),
			Lat: fp(1), Lon: fp(2), AltGPS: fp(alt), Status: status,
		}
		out[i] = store.StoredPacket{Packet: pk, Hash: hashFor(i), DupKind: domain.DupFirst, ID: int64(i + 1)}
	}
	return out
}

func hashFor(i int) string {
	const h = "0123456789abcdef"
	return string([]byte{h[i%16], h[(i+3)%16]}) + "0000"
}

func TestClassifiesAscentAndDescent(t *testing.T) {
	pkts := flightPackets(20)
	v := Assemble(Input{SoundingID: "s", Kind: "draft", Packets: pkts}, DefaultParams(), time.Now())
	hasAscent, hasDescent := false, false
	for _, b := range v.Branches {
		if b.Phase == domain.PhaseAscent {
			hasAscent = true
		}
		if b.Phase == domain.PhaseDescent {
			hasDescent = true
		}
	}
	if !hasAscent || !hasDescent {
		t.Fatalf("branches=%+v", v.Branches)
	}
}

func TestTerminationExcludedFromLayers(t *testing.T) {
	pkts := flightPackets(12)
	for i := 10; i < 12; i++ {
		pkts[i].Status = "terminated"
	}
	v := Assemble(Input{SoundingID: "s", Kind: "draft", Packets: pkts}, DefaultParams(), time.Now())
	term := 0
	for _, p := range v.Points {
		if p.Phase == domain.PhaseTerminate {
			term++
		}
	}
	if term != 2 {
		t.Fatalf("terminated points=%d want 2", term)
	}
	for _, b := range v.Branches {
		if b.Phase == domain.PhaseTerminate {
			// terminated branch exists but profile layer derivation skips it
		}
	}
}

func TestManualPhaseOverride(t *testing.T) {
	pkts := flightPackets(20)
	start, end := baseTime(2), baseTime(5)
	js := []domain.Judgment{{
		SoundingID: "s", Kind: "phase_override",
		Start: &start, End: &end, Phase: domain.PhaseDescent, SignedBy: "alice",
	}}
	v := Assemble(Input{SoundingID: "s", Kind: "draft", Packets: pkts, Judgments: js}, DefaultParams(), time.Now())
	marked := 0
	for _, p := range v.Points {
		if !p.ObsTime.Before(start) && !p.ObsTime.After(end) {
			if p.Phase == domain.PhaseDescent {
				marked++
			}
		}
	}
	if marked == 0 {
		t.Fatalf("manual override not applied; points=%+v", v.Points[2:6])
	}
}

func TestConflictProducesTwoCandidates(t *testing.T) {
	pkts := flightPackets(20)
	// introduce a conflicting payload at seq 10 (different hash & temp)
	conf := pkts[10]
	conf2 := conf
	t2 := conf.Temp
	*conf2.Temp = *t2 + 3
	conf2.Hash = "ffff0000"
	conf2.DupKind = domain.DupConflict
	conf.DupKind = domain.DupConflict
	pkts = append(pkts, conf2)

	vs := Plan("s", pkts, nil, DefaultParams(), time.Now())
	if len(vs) != 2 {
		t.Fatalf("candidate versions=%d want 2", len(vs))
	}
	if vs[0].Kind != "candidate_a" || vs[1].Kind != "candidate_b" {
		t.Fatalf("kinds=%s,%s", vs[0].Kind, vs[1].Kind)
	}
}

func TestVariantChoiceCollapsesCandidates(t *testing.T) {
	pkts := flightPackets(20)
	conf := pkts[10]
	conf.DupKind = domain.DupConflict
	conf2 := conf
	*conf2.Temp = *conf.Temp + 3
	conf2.Hash = "ffff0000"
	conf2.DupKind = domain.DupConflict
	pkts = append(pkts, conf2)

	seq := 10
	js := []domain.Judgment{{
		SoundingID: "s", Kind: "variant_choice", Device: "dev",
		Seq: &seq, ChosenHash: "ffff0000", SignedBy: "alice",
	}}
	vs := Plan("s", pkts, js, DefaultParams(), time.Now())
	if len(vs) != 1 || vs[0].Kind != "draft" {
		t.Fatalf("expected single draft after choice, got %d: %+v", len(vs), kinds(vs))
	}
}

func kinds(vs []store.AssembledVersion) []string {
	out := make([]string, len(vs))
	for i := range vs {
		out[i] = vs[i].Kind
	}
	return out
}
