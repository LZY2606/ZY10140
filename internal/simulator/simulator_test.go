package simulator

import "testing"

func TestOutOfOrderAndReplayDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	s1 := NewStream(cfg)
	s2 := NewStream(cfg)
	a, b := s1.All(), s2.All()
	if len(a) != len(b) {
		t.Fatalf("length %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Seq != b[i].Seq || a[i].PayloadID != b[i].PayloadID {
			t.Fatalf("non-deterministic delivery at %d", i)
		}
	}
	// The stream must actually be out of order at some point.
	inOrder := true
	for i, p := range a {
		// Allow duplicates (retry/conflict) but seq must decrease somewhere.
		if i > 0 && p.Seq < a[i-1].Seq {
			inOrder = false
			break
		}
	}
	if inOrder {
		t.Fatal("expected out-of-order delivery")
	}
	// A retry produces a duplicate and a conflict two payloads.
	counts := map[int]int{}
	payloads := map[int]map[string]bool{}
	for _, p := range a {
		counts[p.Seq]++
		if payloads[p.Seq] == nil {
			payloads[p.Seq] = map[string]bool{}
		}
		payloads[p.Seq][p.PayloadID] = true
	}
	if counts[cfg.RetrySeqs[0]] < 2 {
		t.Fatalf("retry seq not duplicated: %d", counts[cfg.RetrySeqs[0]])
	}
	if len(payloads[cfg.ConflictSeqs[0]]) != 2 {
		t.Fatalf("conflict seq payloads=%v", payloads[cfg.ConflictSeqs[0]])
	}
	// Reset/replay returns the same order.
	s1.Reset()
	c := s1.All()
	for i := range c {
		if c[i].Seq != a[i].Seq {
			t.Fatalf("replay order differs at %d", i)
		}
	}
	// Late seq 12 must appear after seq 15 in delivery order.
	pos12, pos15 := -1, -1
	for i, p := range a {
		if p.Seq == 12 && p.PayloadID == "A" {
			pos12 = i
		}
		if p.Seq == 15 {
			pos15 = i
		}
	}
	if pos12 < 0 || pos15 < 0 || pos12 <= pos15 {
		t.Fatalf("late packet not delayed: pos12=%d pos15=%d", pos12, pos15)
	}
}

func TestAnomaliesPresent(t *testing.T) {
	env := Generate(DefaultConfig())
	var iced, gap int
	for _, e := range env {
		if e.conflict != nil {
			// ok
		}
		p := e.primary
		if p.RH != nil && *p.RH == 100 && p.Temp != nil && *p.Temp <= 0 {
			iced++
		}
		if p.AltGPS == nil {
			gap++
		}
	}
	if iced == 0 {
		t.Fatal("no icing samples generated")
	}
	if gap == 0 {
		t.Fatal("no GPS gap samples generated")
	}
}
