// Package api exposes the sounding service over HTTP and serves the browser
// profile UI.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/service"
	"soundingapp/internal/store"
)

type Server struct {
	svc *service.Service
	mux *http.ServeMux
	sim *SimServer
}

func New(svc *service.Service, sim *SimServer) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux(), sim: sim}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("GET /api/soundings", s.listSoundings)
	m.HandleFunc("GET /api/soundings/{id}", s.getSounding)
	m.HandleFunc("POST /api/soundings/{id}/packets", s.ingest)
	m.HandleFunc("POST /api/soundings/{id}/rebuild", s.rebuild)

	m.HandleFunc("GET /api/soundings/{id}/versions", s.listVersions)
	m.HandleFunc("GET /api/versions/{vid}", s.getVersion)
	m.HandleFunc("GET /api/versions/{vid}/points", s.getPoints)
	m.HandleFunc("GET /api/versions/{vid}/layers", s.getLayers)
	m.HandleFunc("GET /api/versions/{vid}/branches", s.getBranches)
	m.HandleFunc("GET /api/versions/{vid}/findings", s.getFindings)

	m.HandleFunc("GET /api/soundings/{id}/published", s.getPublished)
	m.HandleFunc("POST /api/soundings/{id}/publish", s.publish)

	m.HandleFunc("GET /api/soundings/{id}/judgments", s.listJudgments)
	m.HandleFunc("POST /api/soundings/{id}/judgments", s.addJudgment)

	m.HandleFunc("GET /api/flags", s.flags)

	if s.sim != nil {
		m.HandleFunc("POST /api/simulator/start", s.simStart)
		m.HandleFunc("GET /api/simulator/status", s.simStatus)
		m.HandleFunc("POST /api/simulator/next", s.simNext)
		m.HandleFunc("POST /api/simulator/batch", s.simBatch)
		m.HandleFunc("POST /api/simulator/all", s.simAll)
		m.HandleFunc("POST /api/simulator/replay", s.simReplay)
		m.HandleFunc("GET /api/simulator/queue", s.simQueue)
	}
	m.HandleFunc("GET /", s.index)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339Nano)})
}

func (s *Server) listSoundings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.svc.Store.DB().QueryContext(r.Context(),
		`SELECT id, device, created_at FROM soundings ORDER BY id`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type item struct {
		ID        string `json:"id"`
		Device    string `json:"device"`
		CreatedAt string `json:"created_at"`
	}
	var out []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Device, &it.CreatedAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, it)
	}
	writeJSON(w, 200, out)
}

// soundingView is the aggregate payload consumed by the UI.
type soundingView struct {
	ID         string                `json:"id"`
	Versions   []store.VersionMeta   `json:"versions"`
	Published  *domain.PublishRecord `json:"published,omitempty"`
	Judgments  []domain.Judgment     `json:"judgments"`
	RawPackets []store.StoredPacket  `json:"raw_packets"`
}

func (s *Server) getSounding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view := soundingView{ID: id}
	vers, err := s.svc.Store.ListVersions(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	view.Versions = vers
	if pub, ok, err := s.svc.Store.LatestPublished(r.Context(), id); err == nil && ok {
		view.Published = &pub
	}
	js, err := s.svc.Store.ActiveJudgments(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	view.Judgments = js
	raw, err := s.svc.Store.AllPackets(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	view.RawPackets = raw
	writeJSON(w, 200, view)
}

type ingestReq struct {
	Packets []domain.Packet `json:"packets"`
	Packet  *domain.Packet  `json:"packet"`
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req ingestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Packet != nil {
		req.Packets = append(req.Packets, *req.Packet)
	}
	if len(req.Packets) == 0 {
		writeErr(w, 400, "no packets")
		return
	}
	for i := range req.Packets {
		if req.Packets[i].Device == "" {
			writeErr(w, 400, "packet device required")
			return
		}
	}
	res, err := s.svc.Ingest(r.Context(), id, req.Packets)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) rebuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ids, err := s.svc.RebuildDrafts(r.Context(), id, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"version_ids": ids})
}

func parseVid(r *http.Request) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vers, err := s.svc.Store.ListVersions(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, vers)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	vid, ok := parseVid(r)
	if !ok {
		writeErr(w, 400, "bad version id")
		return
	}
	pts, _ := s.svc.Store.Points(r.Context(), vid)
	br, _ := s.svc.Store.Branches(r.Context(), vid)
	ls, _ := s.svc.Store.Layers(r.Context(), vid)
	fs, _ := s.svc.Store.Findings(r.Context(), vid)
	writeJSON(w, 200, map[string]any{
		"points": pts, "branches": br, "layers": ls, "findings": fs,
	})
}

func (s *Server) getPoints(w http.ResponseWriter, r *http.Request) {
	vid, ok := parseVid(r)
	if !ok {
		writeErr(w, 400, "bad version id")
		return
	}
	pts, err := s.svc.Store.Points(r.Context(), vid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, pts)
}

func (s *Server) getLayers(w http.ResponseWriter, r *http.Request) {
	vid, ok := parseVid(r)
	if !ok {
		writeErr(w, 400, "bad version id")
		return
	}
	ls, err := s.svc.Store.Layers(r.Context(), vid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ls)
}

func (s *Server) getBranches(w http.ResponseWriter, r *http.Request) {
	vid, ok := parseVid(r)
	if !ok {
		writeErr(w, 400, "bad version id")
		return
	}
	br, err := s.svc.Store.Branches(r.Context(), vid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, br)
}

func (s *Server) getFindings(w http.ResponseWriter, r *http.Request) {
	vid, ok := parseVid(r)
	if !ok {
		writeErr(w, 400, "bad version id")
		return
	}
	fs, err := s.svc.Store.Findings(r.Context(), vid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, fs)
}

type publishReq struct {
	VersionID   int64  `json:"version_id"`
	PublishedBy string `json:"published_by"`
	Note        string `json:"note"`
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req publishReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.VersionID == 0 {
		writeErr(w, 400, "version_id required")
		return
	}
	rec, err := s.svc.Publish(r.Context(), id, req.VersionID, defaultName(req.PublishedBy), req.Note)
	if err != nil {
		if errors.Is(err, store.ErrPublishedImmutable) {
			writeErr(w, 409, "version already published (immutable)")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) getPublished(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid, ls, err := s.svc.Store.PublishedLayers(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pid == 0 {
		writeJSON(w, 200, map[string]any{"published": false, "layers": []domain.Layer{}})
		return
	}
	pub, _, _ := s.svc.Store.LatestPublished(r.Context(), id)
	writeJSON(w, 200, map[string]any{"published": true, "record": pub, "layers": ls})
}

func (s *Server) listJudgments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	js, err := s.svc.Store.ActiveJudgments(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, js)
}

func (s *Server) addJudgment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var j domain.Judgment
	if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	j.SoundingID = id
	if j.SignedBy == "" {
		j.SignedBy = defaultName(r.Header.Get("X-Analyst"))
	}
	if !validJudgment(j) {
		writeErr(w, 400, "invalid judgment")
		return
	}
	saved, err := s.svc.Store.AddJudgment(r.Context(), j, time.Now().UTC())
	if err != nil {
		var cf *store.JudgmentConflict
		if errors.As(err, &cf) {
			writeJSON(w, 409, map[string]any{
				"error":  "overlapping judgment from another analyst",
				"ranges": cf.Ranges,
			})
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	// Manual judgments change the assembled result: rebuild drafts, but
	// never touch the published profile.
	if _, err := s.svc.RebuildDrafts(r.Context(), id, 0); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func validJudgment(j domain.Judgment) bool {
	switch j.Kind {
	case "phase_override":
		return j.Start != nil && j.End != nil && j.Phase.Valid()
	case "icing":
		return j.Start != nil && j.End != nil
	case "variant_choice":
		return j.Seq != nil && j.ChosenHash != ""
	case "retain_both":
		return true
	}
	return false
}

func (s *Server) flags(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"flags":              domain.FlagDoc,
		"missing":            domain.MissingDoc,
		"standard_pressures": domain.StandardPressures,
	})
}

func defaultName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "analyst"
	}
	return v
}

// Simulator route forwards (the simulator owns its own state/mutex).
func (s *Server) simStart(w http.ResponseWriter, r *http.Request)  { s.sim.start(w, r) }
func (s *Server) simStatus(w http.ResponseWriter, r *http.Request) { s.sim.status(w, r) }
func (s *Server) simNext(w http.ResponseWriter, r *http.Request)   { s.sim.next(w, r) }
func (s *Server) simBatch(w http.ResponseWriter, r *http.Request)  { s.sim.batch(w, r) }
func (s *Server) simAll(w http.ResponseWriter, r *http.Request)    { s.sim.all(w, r) }
func (s *Server) simReplay(w http.ResponseWriter, r *http.Request) { s.sim.replay(w, r) }
func (s *Server) simQueue(w http.ResponseWriter, r *http.Request)  { s.sim.queue(w, r) }
