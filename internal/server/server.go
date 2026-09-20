// Package server exposes the sounding API and the embedded browser UI.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"sonde/internal/domain"
	"sonde/internal/service"
	"sonde/internal/sim"
	"sonde/internal/store"
)

//go:embed all:web
var webFS embed.FS

type Server struct {
	svc *service.Service
	mux *http.ServeMux
}

func New(svc *service.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/soundings", s.listSoundings)
	m.HandleFunc("POST /api/soundings", s.createSounding)
	m.HandleFunc("GET /api/soundings/{id}/draft", s.getDraft)
	m.HandleFunc("POST /api/soundings/{id}/packets", s.ingest)
	m.HandleFunc("POST /api/soundings/{id}/overrides", s.addOverride)
	m.HandleFunc("POST /api/soundings/{id}/publish", s.publish)
	m.HandleFunc("GET /api/soundings/{id}/published", s.getPublished)
	m.HandleFunc("POST /api/soundings/{id}/simulate", s.simulate)

	sub, _ := fs.Sub(webFS, "web")
	m.Handle("/", http.FileServer(http.FS(sub)))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) listSoundings(w http.ResponseWriter, r *http.Request) {
	out, err := s.svc.ListSoundings(r.Context())
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"soundings": out})
}

type createReq struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

func (s *Server) createSounding(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	if req.DeviceID == "" {
		errJSON(w, 400, "device_id required")
		return
	}
	if req.Name == "" {
		req.Name = req.DeviceID
	}
	out, err := s.svc.CreateSounding(r.Context(), req.DeviceID, req.Name)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := s.svc.Draft(r.Context(), id, nil)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

type ingestReq struct {
	Packets []domain.Packet `json:"packets"`
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req ingestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	if len(req.Packets) == 0 {
		errJSON(w, 400, "empty batch")
		return
	}
	for i := range req.Packets {
		req.Packets[i].SoundingID = id
	}
	res, draft, err := s.svc.Ingest(r.Context(), id, req.Packets)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"batch": res, "draft": draft})
}

type overrideReq struct {
	ExpectedRev int64      `json:"expected_revision"`
	Kind        string     `json:"kind"`
	StartTime   time.Time  `json:"start_time"`
	EndTime     *time.Time `json:"end_time"`
	Phase       string     `json:"phase"`
	Icing       *bool      `json:"icing"`
	Seq         *int64     `json:"seq"`
	Candidate   *int64     `json:"candidate_id"`
	Author      string     `json:"author"`
	Note        string     `json:"note"`
}

func (s *Server) addOverride(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req overrideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	o := domain.Override{
		SoundingID: id,
		Kind:       domain.OverrideKind(req.Kind),
		Start:      req.StartTime,
		End:        req.EndTime,
		Icing:      req.Icing,
		Seq:        req.Seq,
		Candidate:  req.Candidate,
		Author:     req.Author,
		Note:       req.Note,
	}
	if req.Phase != "" {
		ph := domain.Phase(req.Phase)
		o.Phase = &ph
	}
	draft, conflicts, newRev, err := s.svc.AddDecision(r.Context(), o, req.ExpectedRev)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeJSON(w, 409, map[string]any{
				"error":               err.Error(),
				"affected_overrides":  conflicts,
				"affected_time_range": timeRange(conflicts),
			})
			return
		}
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"revision": newRev, "draft": draft,
		"affected_overrides":  conflicts,
		"affected_time_range": timeRange(conflicts),
	})
}

type publishReq struct {
	Author string `json:"author"`
	Note   string `json:"note"`
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req publishReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	pub, draft, err := s.svc.Publish(r.Context(), id, req.Author, req.Note)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			errJSON(w, 409, err.Error())
			return
		}
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"published": pub, "draft": draft})
}

func (s *Server) getPublished(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	pub, err := s.svc.LatestPublished(r.Context(), id)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if pub == nil {
		errJSON(w, 404, "no published profile")
		return
	}
	writeJSON(w, 200, pub)
}

type simulateReq struct {
	BatchSize int `json:"batch_size"`
}

func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req simulateReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.BatchSize <= 0 {
		req.BatchSize = 25
	}
	cfg := sim.DefaultConfig()
	dels := sim.Generate(cfg)
	var report []store.IngestResult
	ctx := r.Context()
	for i := 0; i < len(dels); i += req.BatchSize {
		end := i + req.BatchSize
		if end > len(dels) {
			end = len(dels)
		}
		pkts := make([]domain.Packet, 0, end-i)
		for _, d := range dels[i:end] {
			p := d.Packet
			p.ReceivedAt = d.ReceivedAt
			pkts = append(pkts, p)
		}
		res, _, err := s.svc.Ingest(ctx, id, pkts)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		report = append(report, res)
	}
	draft, _ := s.svc.Draft(ctx, id, nil)
	writeJSON(w, 200, map[string]any{"deliveries": len(dels), "batches": report, "draft": draft})
}

func (s *Server) writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrValidation):
		errJSON(w, 400, err.Error())
	case errors.Is(err, store.ErrConflict):
		errJSON(w, 409, err.Error())
	default:
		errJSON(w, 500, err.Error())
	}
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := r.PathValue("id")
	var id int64
	for _, c := range v {
		if c < '0' || c > '9' {
			errJSON(w, 400, "bad sounding id")
			return 0, false
		}
		id = id*10 + int64(c-'0')
	}
	if v == "" || id == 0 {
		errJSON(w, 400, "bad sounding id")
		return 0, false
	}
	return id, true
}

func timeRange(os []domain.Override) map[string]any {
	if len(os) == 0 {
		return nil
	}
	var start, end = os[0].Start, os[0].Start
	for _, o := range os {
		if o.Start.Before(start) {
			start = o.Start
		}
		e := o.Start
		if o.End != nil {
			e = *o.End
		}
		if e.After(end) {
			end = e
		}
	}
	return map[string]any{"start": start, "end": end}
}

// ensure embed is used
var _ context.Context
var _ = strings.TrimSpace
