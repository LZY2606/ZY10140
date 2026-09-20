package store

import (
	"context"
	"testing"
	"time"

	"soundingapp/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open("file:" + t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fp(v float64) *float64 { return &v }
func t0() time.Time         { return time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) }

func pkt(seq int, p float64, t time.Time) domain.Packet {
	return domain.Packet{
		Device: "dev1", Seq: seq, ObsTime: t,
		Pressure: fp(p), Temp: fp(10), RH: fp(50),
		Lat: fp(1), Lon: fp(2), AltGPS: fp(100), Status: "ok",
	}
}

func TestInsertDedupRetryAndConflict(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	base := pkt(1, 1000, t0())

	r1, err := st.InsertBatch(ctx, "s1", []domain.Packet{base}, t0())
	if err != nil || r1.First != 1 {
		t.Fatalf("first: %+v err=%v", r1, err)
	}
	// identical payload -> retry
	r2, err := st.InsertBatch(ctx, "s1", []domain.Packet{base}, t0().Add(time.Second))
	if err != nil || r2.Retries != 1 || r2.First != 0 {
		t.Fatalf("retry: %+v err=%v", r2, err)
	}
	// same seq, changed payload -> conflict
	conf := base
	conf.Temp = fp(99.9)
	r3, err := st.InsertBatch(ctx, "s1", []domain.Packet{conf}, t0().Add(2*time.Second))
	if err != nil || r3.Conflicts != 1 {
		t.Fatalf("conflict: %+v err=%v", r3, err)
	}
	cands, err := st.CandidatePackets(ctx, "s1")
	if err != nil || len(cands) != 2 {
		t.Fatalf("candidates=%d err=%v", len(cands), err)
	}
}

func TestLatePacket(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	_, err := st.InsertBatch(ctx, "s1", []domain.Packet{pkt(5, 900, t0().Add(10*time.Second))}, t0())
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.InsertBatch(ctx, "s1", []domain.Packet{pkt(2, 950, t0().Add(4*time.Second))}, t0().Add(time.Second))
	if err != nil || r.Late != 1 {
		t.Fatalf("late=%+v err=%v", r, err)
	}
}

func TestJudgmentConflict(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.EnsureSounding(ctx, "s1", "dev1", t0()); err != nil {
		t.Fatal(err)
	}
	start, end := t0(), t0().Add(time.Minute)
	mk := func(by string) domain.Judgment {
		return domain.Judgment{SoundingID: "s1", Kind: "icing",
			Start: &start, End: &end, SignedBy: by}
	}
	if _, err := st.AddJudgment(ctx, mk("alice"), t0()); err != nil {
		t.Fatal(err)
	}
	// same person may re-sign (treated as a new judgment), a different
	// person overlapping the same interval must conflict.
	_, err := st.AddJudgment(ctx, mk("bob"), t0())
	var cf *JudgmentConflict
	if err == nil {
		t.Fatal("expected conflict")
	}
	if e, ok := err.(*JudgmentConflict); ok {
		cf = e
	}
	if cf == nil || len(cf.Ranges) != 1 {
		t.Fatalf("conflict ranges: %v", err)
	}
}
