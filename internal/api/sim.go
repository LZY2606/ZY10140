package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"soundingapp/internal/domain"
	"soundingapp/internal/service"
	"soundingapp/internal/simulator"
)

// SimServer runs a single controllable simulated flight and can replay the
// identical out-of-order packet stream.
type SimServer struct {
	mu        sync.Mutex
	svc       *service.Service
	cfg       simulator.Config
	stream    *simulator.Stream
	delivered int
	sounding  string
	batches   [][]domain.Packet
}

func NewSimServer(svc *service.Service, sounding string) *SimServer {
	cfg := simulator.DefaultConfig()
	return &SimServer{svc: svc, cfg: cfg, sounding: sounding}
}

func (s *SimServer) Mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /start", s.start)
	m.HandleFunc("GET /status", s.status)
	m.HandleFunc("POST /next", s.next)
	m.HandleFunc("POST /batch", s.batch)
	m.HandleFunc("POST /all", s.all)
	m.HandleFunc("POST /replay", s.replay)
	m.HandleFunc("GET /queue", s.queue)
	return m
}

type simStartReq struct {
	Sounding string            `json:"sounding_id"`
	Config   *simulator.Config `json:"config,omitempty"`
}

func (s *SimServer) start(w http.ResponseWriter, r *http.Request) {
	var req simStartReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Sounding != "" {
		s.sounding = req.Sounding
	}
	if req.Config != nil {
		s.cfg = *req.Config
	} else {
		s.cfg = simulator.DefaultConfig()
	}
	s.stream = simulator.NewStream(s.cfg)
	s.delivered = 0
	s.batches = nil
	writeJSON(w, 200, map[string]any{
		"sounding_id": s.sounding,
		"queued":      len(s.stream.All()),
		"config":      s.cfg,
	})
}

func (s *SimServer) ensure() *simulator.Stream {
	if s.stream == nil {
		s.stream = simulator.NewStream(s.cfg)
	}
	return s.stream
}

func (s *SimServer) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	total := len(st.All())
	writeJSON(w, 200, map[string]any{
		"sounding_id": s.sounding,
		"delivered":   s.delivered,
		"total":       total,
		"remaining":   total - s.delivered,
	})
}

func (s *SimServer) next(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	pk, ok := st.Next()
	if !ok {
		writeJSON(w, 200, map[string]any{"done": true})
		return
	}
	res, err := s.svc.Ingest(r.Context(), s.sounding, []domain.Packet{pk})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.delivered++
	writeJSON(w, 200, map[string]any{"delivered": s.delivered, "packet": pk, "result": res})
}

type simBatchReq struct {
	Count int `json:"count"`
}

func (s *SimServer) batch(w http.ResponseWriter, r *http.Request) {
	var req simBatchReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Count <= 0 {
		req.Count = 8
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	var batch []domain.Packet
	for i := 0; i < req.Count; i++ {
		pk, ok := st.Next()
		if !ok {
			break
		}
		batch = append(batch, pk)
	}
	if len(batch) == 0 {
		writeJSON(w, 200, map[string]any{"done": true})
		return
	}
	res, err := s.svc.Ingest(r.Context(), s.sounding, batch)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.delivered += len(batch)
	writeJSON(w, 200, map[string]any{"delivered": s.delivered, "batch_size": len(batch), "result": res})
}

func (s *SimServer) all(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	pkts := st.All()
	res, err := s.svc.Ingest(r.Context(), s.sounding, pkts)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.delivered = len(pkts)
	writeJSON(w, 200, map[string]any{"delivered": s.delivered, "result": res})
}

// replay rewinds the stream to its start and re-delivers every packet in the
// identical order into the same sounding, exercising late/retry/conflict
// idempotence.
func (s *SimServer) replay(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	st.Reset()
	res, err := s.svc.Ingest(r.Context(), s.sounding, st.All())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.delivered = len(st.All())
	writeJSON(w, 200, map[string]any{"replayed": s.delivered, "result": res})
}

func (s *SimServer) queue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure()
	writeJSON(w, 200, map[string]any{
		"queue":      st.All(),
		"order_note": "queue is delivery order (out-of-order); obs_time is onboard time",
	})
}
