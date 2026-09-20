package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"soundingapp/internal/service"
	"soundingapp/internal/simulator"
	"soundingapp/internal/store"
)

func newHarness(t *testing.T) (http.Handler, *service.Service) {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st)
	sim := NewSimServer(svc, "demo")
	return New(svc, sim).Handler(), svc
}

func do(t *testing.T, h http.Handler, method, path string, body any, analyst string) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewBuffer(b)
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if analyst != "" {
		req.Header.Set("X-Analyst", analyst)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Code != 204 && rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestHTTPSimulatorPublishAndConflict(t *testing.T) {
	h, _ := newHarness(t)

	code, body := do(t, h, "POST", "/api/simulator/start", map[string]string{"sounding_id": "demo"}, "")
	if code != 200 {
		t.Fatalf("start: %d %v", code, body)
	}
	code, body = do(t, h, "POST", "/api/simulator/all", nil, "")
	if code != 200 {
		t.Fatalf("all: %d %v", code, body)
	}
	res := body["result"].(map[string]any)
	if f, _ := res["conflicts"].(float64); f < 1 {
		t.Fatalf("expected conflict via HTTP, got %v", res)
	}

	code, view := do(t, h, "GET", "/api/soundings/demo", nil, "")
	if code != 200 {
		t.Fatalf("get sounding: %d", code)
	}
	versions := view["versions"].([]any)
	if len(versions) < 2 {
		t.Fatalf("expected >=2 versions (candidates), got %d", len(versions))
	}
	first := versions[0].(map[string]any)
	vid := int64(first["id"].(float64))

	// Publish must atomically sign all layers.
	code, pub := do(t, h, "POST", "/api/soundings/demo/publish",
		map[string]any{"version_id": vid, "published_by": "alice"}, "")
	if code != 200 {
		t.Fatalf("publish: %d %v", code, pub)
	}
	n, _ := pub["layer_count"].(float64)
	if n == 0 {
		t.Fatalf("published zero layers body=%v", pub)
	}

	code, frozen := do(t, h, "GET", "/api/soundings/demo/published", nil, "")
	if code != 200 || frozen["published"] != true {
		t.Fatalf("published view: %d %v", code, frozen)
	}

	// Second publish of same version rejected (no partial state change).
	code, _ = do(t, h, "POST", "/api/soundings/demo/publish",
		map[string]any{"version_id": vid, "published_by": "bob"}, "")
	if code != 409 {
		t.Fatalf("republish expected 409, got %d", code)
	}

	// Concurrent icing judgments by two analysts -> 409 with ranges.
	j := map[string]any{
		"kind":  "icing",
		"start": "2026-09-21T00:00:40Z",
		"end":   "2026-09-21T00:01:00Z",
	}
	if code, _ := do(t, h, "POST", "/api/soundings/demo/judgments", j, "alice"); code != 200 {
		t.Fatalf("alice judgment: %d", code)
	}
	code, conflict := do(t, h, "POST", "/api/soundings/demo/judgments", j, "bob")
	if code != 409 {
		t.Fatalf("bob overlapping judgment expected 409, got %d", code)
	}
	ranges, ok := conflict["ranges"].([]any)
	if !ok || len(ranges) == 0 {
		t.Fatalf("409 must include affected ranges: %v", conflict)
	}

	// Static UI is served.
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("探空")) {
		t.Fatalf("index page not served: %d", rec.Code)
	}
}

func TestHTTPFlagsDocs(t *testing.T) {
	h, _ := newHarness(t)
	code, body := do(t, h, "GET", "/api/flags", nil, "")
	if code != 200 || body["flags"] == nil {
		t.Fatalf("flags: %d %v", code, body)
	}
}

func TestSimulatorPackageSelfTest(t *testing.T) {
	// sanity: default config generates the expected anomalies
	cfg := simulator.DefaultConfig()
	if len(cfg.ConflictSeqs) == 0 {
		t.Fatal("default config lacks conflicts")
	}
}
