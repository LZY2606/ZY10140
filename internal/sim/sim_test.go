package sim

import (
	"testing"

	"sonde/internal/derive"
)

func TestGenerateStreamHasAllAnomalies(t *testing.T) {
	cfg := DefaultConfig()
	dels := Generate(cfg)
	if len(dels) < cfg.AscentSteps+cfg.DescentSteps {
		t.Fatalf("delivery stream too short: %d", len(dels))
	}
	notes := map[string]int{}
	seqs := map[int64]int{}
	payloadsAtConflict := map[string]bool{}
	for _, d := range dels {
		if d.Note != "" {
			notes[d.Note]++
		}
		seqs[d.Packet.Seq]++
		if d.Packet.Seq == cfg.ConflictSeq {
			payloadsAtConflict[derive.PayloadHash(d.Packet)] = true
		}
	}
	for _, n := range []string{"retry", "conflict", "late", "reversal"} {
		if notes[n] == 0 {
			t.Fatalf("expected anomaly %q in stream, got %+v", n, notes)
		}
	}
	if len(payloadsAtConflict) < 2 {
		t.Fatalf("conflict seq %d should deliver 2 distinct payloads, got %d", cfg.ConflictSeq, len(payloadsAtConflict))
	}
	// late packet must arrive AFTER a strictly later sequence
	var lateIdx int
	var maxBefore int64 = -1
	for i, d := range dels {
		if d.Note == "late" {
			lateIdx = i
		}
	}
	for i := 0; i < lateIdx; i++ {
		if dels[i].Packet.Seq > maxBefore {
			maxBefore = dels[i].Packet.Seq
		}
	}
	if maxBefore <= cfg.LateSeq {
		t.Fatalf("late seq %d did not arrive after later data (max before=%d)", cfg.LateSeq, maxBefore)
	}
	// replay: generating twice is deterministic
	dels2 := Generate(cfg)
	if len(dels2) != len(dels) {
		t.Fatal("replay changed stream length")
	}
	for i := range dels {
		if dels[i].Packet.Seq != dels2[i].Packet.Seq || dels[i].Note != dels2[i].Note {
			t.Fatalf("replay mismatch at %d", i)
		}
	}
}
