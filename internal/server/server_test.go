package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"sonde/internal/domain"
	"sonde/internal/service"
	"sonde/internal/sim"
	"sonde/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *service.Service, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st)
	sd, err := svc.CreateSounding(context.Background(), "dev-e2e", "e2e")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(svc).Handler())
	t.Cleanup(srv.Close)
	return srv, svc, sd.ID
}

func doJSON(t *testing.T, method, url string, body any, into any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if into != nil {
		_ = json.NewDecoder(res.Body).Decode(into)
	}
	return res
}

func TestE2ESimulateAndPublish(t *testing.T) {
	srv, _, id := newTestServer(t)

	var simj map[string]any
	res := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/simulate", map[string]int{"batch_size": 25}, &simj)
	if res.StatusCode != 200 {
		t.Fatalf("simulate status %d", res.StatusCode)
	}
	if simj["deliveries"].(float64) < 200 {
		t.Fatalf("expected full delivery stream, got %v", simj["deliveries"])
	}

	var draft struct {
		Revision int64 `json:"revision"`
		Assembly struct {
			Points []struct {
				Seq   int64  `json:"seq"`
				Phase string `json:"phase"`
			} `json:"points"`
			Levels []struct {
				Pressure float64 `json:"pressure_hpa"`
				Primary  bool    `json:"primary"`
				Kind     string  `json:"kind"`
			} `json:"levels"`
			Conflicts []struct {
				Seq        int `json:"seq"`
				Candidates []struct {
					ID int `json:"candidate_id"`
				} `json:"candidates"`
			} `json:"conflicts"`
		} `json:"assembly"`
	}
	if r := doJSON(t, "GET", srv.URL+"/api/soundings/"+itoa(id)+"/draft", nil, &draft); r.StatusCode != 200 {
		t.Fatal("draft fetch failed")
	}
	phases := map[string]bool{}
	for _, p := range draft.Assembly.Points {
		phases[p.Phase] = true
	}
	for _, want := range []string{"ascent", "descent", "float"} {
		if !phases[want] {
			t.Fatalf("draft missing phase %s: %+v", want, phases)
		}
	}
	conflictOK := false
	for _, c := range draft.Assembly.Conflicts {
		if len(c.Candidates) > 1 {
			conflictOK = true
		}
	}
	if !conflictOK {
		t.Fatal("simulated duplicate conflict not visible in draft")
	}
	if len(draft.Assembly.Levels) == 0 {
		t.Fatal("no standard levels")
	}

	// publish
	var pubj map[string]any
	pr := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/publish",
		map[string]string{"author": "signer", "note": "first release"}, &pubj)
	if pr.StatusCode != 201 {
		t.Fatalf("publish status %d body=%v", pr.StatusCode, pubj)
	}

	// second publish of the same draft revision must 409 (immutable)
	pr2 := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/publish",
		map[string]string{"author": "signer2"}, &map[string]any{})
	if pr2.StatusCode != 409 {
		t.Fatalf("double publish expected 409, got %d", pr2.StatusCode)
	}
}

func TestE2ELatePacketDoesNotChangePublished(t *testing.T) {
	srv, svc, id := newTestServer(t)
	base := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	lat := 35.0
	mk := func(seq int64, p float64) domain.Packet {
		return domain.Packet{
			Seq: seq, ObservedAt: base.Add(time.Duration(seq) * time.Second),
			Pressure: &p, Temp: &p, RH: &p, Lat: &lat, Lon: &lat,
			Alt: &p, ReceivedAt: base.Add(time.Duration(seq) * time.Second),
		}
	}
	var ing map[string]any
	r := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/packets",
		map[string]any{"packets": []domain.Packet{mk(1, 1000), mk(5, 950)}}, &ing)
	if r.StatusCode != 200 {
		t.Fatalf("ingest %d %v", r.StatusCode, ing)
	}
	// publish whatever exists
	pub := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/publish",
		map[string]string{"author": "x"}, &map[string]any{})
	if pub.StatusCode != 201 {
		t.Fatalf("publish %d", pub.StatusCode)
	}
	signed, err := svc.LatestPublished(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	before := len(signed.Levels)

	// late packet arrives -> new draft revision, but signed profile untouched
	var ing2 map[string]any
	r2 := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/packets",
		map[string]any{"packets": []domain.Packet{mk(3, 970)}}, &ing2)
	if r2.StatusCode != 200 {
		t.Fatalf("late ingest %d", r2.StatusCode)
	}
	signed2, _ := svc.LatestPublished(context.Background(), id)
	if signed2.Revision != signed.Revision || len(signed2.Levels) != before {
		t.Fatal("late packet mutated the signed profile")
	}
}

func TestE2EConcurrentOverrideShowsTimeRange(t *testing.T) {
	srv, _, id := newTestServer(t)
	base := time.Now().UTC()
	lat := 35.0
	p := domain.Packet{Seq: 1, ObservedAt: base, Pressure: &lat, Lat: &lat, Lon: &lat,
		Alt: &lat, Temp: &lat, RH: &lat, ReceivedAt: base}
	doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/packets",
		map[string]any{"packets": []domain.Packet{p}}, &map[string]any{})

	var draft struct {
		Revision int64 `json:"revision"`
	}
	doJSON(t, "GET", srv.URL+"/api/soundings/"+itoa(id)+"/draft", nil, &draft)

	ov := map[string]any{
		"expected_revision": draft.Revision,
		"kind":              "phase",
		"phase":             "descent",
		"start_time":        base,
		"author":            "analyst-a",
	}
	var first map[string]any
	r1 := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/overrides", ov, &first)
	if r1.StatusCode != 200 {
		t.Fatalf("first override %d", r1.StatusCode)
	}
	// second analyst uses the stale revision AND overlaps the time window
	var second map[string]any
	r2 := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/overrides", ov, &second)
	if r2.StatusCode != 409 {
		t.Fatalf("expected 409, got %d", r2.StatusCode)
	}
	if second["affected_time_range"] == nil {
		t.Fatalf("conflict must report affected time range: %v", second)
	}
}

func TestE2EInvalidBatchRejected(t *testing.T) {
	srv, _, id := newTestServer(t)
	r := doJSON(t, "POST", srv.URL+"/api/soundings/"+itoa(id)+"/packets",
		map[string]any{"packets": []domain.Packet{}}, &map[string]any{})
	if r.StatusCode != 400 {
		t.Fatalf("empty batch expected 400, got %d", r.StatusCode)
	}
}

func TestSimulatorConfigDeterministic(t *testing.T) {
	a := sim.Generate(sim.DefaultConfig())
	b := sim.Generate(sim.DefaultConfig())
	if len(a) != len(b) {
		t.Fatal("replay mismatch")
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	out := []byte{}
	for v > 0 {
		out = append([]byte{byte('0' + v%10)}, out...)
		v /= 10
	}
	return string(out)
}
