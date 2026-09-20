package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/simulator"
	"soundingapp/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st)
	clock := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return s, st
}

func TestEndToEndSimulatorReplayAndPublishImmutability(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()

	cfg := simulator.DefaultConfig()
	stream := simulator.NewStream(cfg)
	pkts := stream.All()

	// First half arrives out of order.
	half := len(pkts) / 2
	r1, err := s.Ingest(ctx, "flight-1", pkts[:half])
	if err != nil {
		t.Fatal(err)
	}
	if r1.Conflicts == 0 {
		t.Fatalf("expected a conflict in first half (got 0); result=%+v", r1)
	}
	if r1.Late == 0 {
		t.Fatalf("expected a late packet in first half")
	}

	// Second half completes the flight (post-burst descent included).
	if _, err := s.Ingest(ctx, "flight-1", pkts[half:]); err != nil {
		t.Fatal(err)
	}

	vers, err := st.ListVersions(ctx, "flight-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) < 2 {
		t.Fatalf("expected draft + conflict candidates, got %d versions", len(vers))
	}

	// Pick candidate_a (earliest payload) and publish it.
	var publishID int64
	for _, v := range vers {
		if v.Kind == "candidate_a" && !v.PublishedAt.Valid {
			publishID = v.ID
		}
	}
	if publishID == 0 {
		// fallback: any draft
		for _, v := range vers {
			if !v.PublishedAt.Valid {
				publishID = v.ID
				break
			}
		}
	}
	rec, err := s.Publish(ctx, "flight-1", publishID, "alice", "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if rec.LayerCount == 0 {
		t.Fatal("published zero layers")
	}
	_, frozenBefore, err := st.PublishedLayers(ctx, "flight-1")
	if err != nil || len(frozenBefore) != rec.LayerCount {
		t.Fatalf("frozen layers=%d record=%d", len(frozenBefore), rec.LayerCount)
	}

	// Replay the exact stream: retries collapse, drafts rebuild, but the
	// signed profile stays byte-identical.
	stream.Reset()
	if _, err := s.Ingest(ctx, "flight-1", stream.All()); err != nil {
		t.Fatal(err)
	}
	_, frozenAfter, err := st.PublishedLayers(ctx, "flight-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(frozenAfter) != len(frozenBefore) {
		t.Fatalf("published layers changed after replay: %d -> %d", len(frozenBefore), len(frozenAfter))
	}
	for i := range frozenBefore {
		a, b := frozenBefore[i], frozenAfter[i]
		if a.Pressure != b.Pressure || a.BranchID != b.BranchID ||
			(a.Temp == nil) != (b.Temp == nil) ||
			(a.Temp != nil && *a.Temp != *b.Temp) {
			t.Fatalf("frozen layer %d changed on replay", i)
		}
	}

	// Republishing the same version is rejected (immutable).
	if _, err := s.Publish(ctx, "flight-1", publishID, "bob", "again"); err == nil {
		t.Fatal("republish of signed version should fail")
	}

	// New evidence after publication creates new drafts but must not touch
	// the frozen profile.
	late := pkts[0]
	late.Seq = 200
	late.ObsTime = late.ObsTime.Add(400 * time.Second)
	late.PayloadID = "A"
	if _, err := s.Ingest(ctx, "flight-1", []domain.Packet{late}); err != nil {
		t.Fatal(err)
	}
	pub, ok, err := st.LatestPublished(ctx, "flight-1")
	if err != nil || !ok || pub.VersionID != rec.VersionID {
		t.Fatalf("latest publish changed: %+v ok=%v", pub, ok)
	}
}

func TestTwoAnalystsConflictShowsRange(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	_, err := s.Ingest(ctx, "f", simulator.NewStream(simulator.DefaultConfig()).All())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 21, 0, 0, 40, 0, time.UTC)
	end := start.Add(20 * time.Second)
	mk := func(by string) domain.Judgment {
		return domain.Judgment{SoundingID: "f", Kind: "icing",
			Start: &start, End: &end, SignedBy: by}
	}
	if _, err := s.Store.AddJudgment(ctx, mk("alice"), s.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.AddJudgment(ctx, mk("bob"), s.Now())
	var cf *store.JudgmentConflict
	if err == nil {
		t.Fatal("expected conflict between alice and bob")
	}
	asConflict := false
	if e, ok := err.(*store.JudgmentConflict); ok {
		cf = e
	}
	if cf != nil && len(cf.Ranges) > 0 {
		r := cf.Ranges[0]
		asConflict = r.OtherSignedBy == "alice" && !r.Start.IsZero() && !r.End.IsZero()
	}
	if !asConflict {
		t.Fatalf("conflict must carry the affected interval: %v", err)
	}
}
