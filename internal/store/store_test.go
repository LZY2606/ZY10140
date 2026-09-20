package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sonde/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func mkPkt(seq int64, p float64, recv time.Time) domain.Packet {
	t0 := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	return domain.Packet{
		Seq: seq, ObservedAt: t0.Add(time.Duration(seq) * time.Second),
		Pressure: &p, ReceivedAt: recv,
	}
}

func TestRetryVersusConflictCounters(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, err := st.CreateSounding(ctx, "dev", "n")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	res, _, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{mkPkt(1, 1000, base), mkPkt(2, 990, base)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Retries != 0 || res.NewSequences != 2 {
		t.Fatalf("first batch counters wrong: %+v", res)
	}

	// identical payload = retry
	retry := mkPkt(1, 1000, base.Add(time.Second))
	res2, _, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{retry})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Retries != 1 || res2.Conflicts != 0 {
		t.Fatalf("retry not recognized: %+v", res2)
	}

	// different payload at same seq = conflict
	conf := mkPkt(1, 999.5, base.Add(2*time.Second))
	res3, rows, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{conf})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Conflicts != 1 {
		t.Fatalf("conflict not counted: %+v", res3)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 distinct rows (2 retry-dedup + variant), got %d", len(rows))
	}
}

func TestLatePacketFlagged(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	if _, _, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{mkPkt(1, 1000, base), mkPkt(5, 950, base)}); err != nil {
		t.Fatal(err)
	}
	res, rows, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{mkPkt(2, 990, base.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	if res.Late != 1 {
		t.Fatalf("late counter: %+v", res)
	}
	for _, r := range rows {
		if r.Seq == 2 && !r.Late {
			t.Fatal("seq 2 must be flagged late")
		}
	}
}

func TestBatchAtomicity(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	good := []domain.Packet{mkPkt(1, 1000, base), mkPkt(2, 990, base)}
	if _, _, err := st.IngestBatch(ctx, sd.ID, good); err != nil {
		t.Fatal(err)
	}
	// second batch references a missing sounding -> whole batch rejected
	missingID := sd.ID + 999
	if _, _, err := st.IngestBatch(ctx, missingID, []domain.Packet{mkPkt(3, 980, base)}); err == nil {
		t.Fatal("expected failure for missing sounding")
	}
	rows, err := st.Packets(ctx, sd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("failed batch must not persist any packet, got %d", len(rows))
	}
}

func TestPublishAtomicAndImmutable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	pkts := []domain.Packet{}
	seq := int64(0)
	for _, p := range []float64{1000, 950, 900, 850, 700, 500, 300, 200, 100} {
		lat, lon := 35.0, 139.0
		pkts = append(pkts, domain.Packet{
			Seq: seq, ObservedAt: base.Add(time.Duration(seq) * time.Second),
			Pressure: &p, Temp: &p, RH: &p, Lat: &lat, Lon: &lon,
			Alt: &p, ReceivedAt: base,
		})
		seq++
	}
	if _, _, err := st.IngestBatch(ctx, sd.ID, pkts); err != nil {
		t.Fatal(err)
	}
	rev, _ := st.Revision(ctx, sd.ID)

	// build a draft via the real assembler at service level is avoided here;
	// construct minimal Draft with enough levels
	draft := assembleDraft(t, st, sd.ID, rev)
	if len(draft.Result.Levels) == 0 {
		t.Fatal("no levels")
	}
	pub, err := st.Publish(ctx, sd.ID, "signer-a", "ok", draft)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(pub.Levels) == 0 {
		t.Fatal("published levels empty")
	}

	// every mandatory level is stored or explicitly missing, count matches
	stored, err := st.LatestPublished(ctx, sd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Levels) != len(draft.Result.Levels) {
		t.Fatalf("partial publish: %d vs %d", len(stored.Levels), len(draft.Result.Levels))
	}

	// re-publish at the SAME revision must be rejected (no overwrite)
	if _, err := st.Publish(ctx, sd.ID, "signer-b", "again", draft); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict on double publish, got %v", err)
	}
}

func TestConcurrentOverrideConflict(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	if _, _, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{mkPkt(1, 1000, base)}); err != nil {
		t.Fatal(err)
	}
	rev, _ := st.Revision(ctx, sd.ID)
	ph := domain.PhaseDescent
	o1 := domain.Override{
		SoundingID: sd.ID, Kind: domain.OverridePhase, Phase: &ph,
		Start: base, End: nil, Author: "analyst-a",
	}
	if _, err := st.AddOverride(ctx, o1, rev); err != nil {
		t.Fatal(err)
	}
	// second analyst still holds the old revision
	if _, err := st.AddOverride(ctx, o1, rev); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected optimistic conflict, got %v", err)
	}
}

func TestConcurrentPublishSingleWinner(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	lat := 35.0
	pkts := []domain.Packet{{
		Seq: 0, ObservedAt: base, Pressure: latp(1000), Lat: &lat, Lon: &lat,
		Alt: latp(0), Temp: latp(10), RH: latp(50), ReceivedAt: base,
	}, {
		Seq: 1, ObservedAt: base.Add(time.Second), Pressure: latp(900), Lat: &lat, Lon: &lat,
		Alt: latp(1000), Temp: latp(8), RH: latp(50), ReceivedAt: base,
	}}
	if _, _, err := st.IngestBatch(ctx, sd.ID, pkts); err != nil {
		t.Fatal(err)
	}
	rev, _ := st.Revision(ctx, sd.ID)
	draft := assembleDraft(t, st, sd.ID, rev)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.Publish(ctx, sd.ID, "u", "", draft)
		}(i)
	}
	wg.Wait()
	wins, conflicts := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			wins++
		case errors.Is(e, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected publish error %v", e)
		}
	}
	if wins != 1 || conflicts != 3 {
		t.Fatalf("want exactly one winner, got wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestOverlappingOverridesReport(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	sd, _ := st.CreateSounding(ctx, "dev", "n")
	base := time.Now().UTC()
	if _, _, err := st.IngestBatch(ctx, sd.ID, []domain.Packet{mkPkt(1, 1000, base)}); err != nil {
		t.Fatal(err)
	}
	rev, _ := st.Revision(ctx, sd.ID)
	ph := domain.PhaseDescent
	o := domain.Override{SoundingID: sd.ID, Kind: domain.OverridePhase, Phase: &ph,
		Start: base.Add(10 * time.Second), End: nil, Author: "a"}
	if _, err := st.AddOverride(ctx, o, rev); err != nil {
		t.Fatal(err)
	}
	found, err := st.OverlappingOverrides(ctx, sd.ID, base.Add(20*time.Second), base.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("expected to find 1 overlapping override, got %d", len(found))
	}
}
