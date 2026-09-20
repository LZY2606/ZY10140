// Package service orchestrates storage, assembly and profiling.
package service

import (
	"context"
	"time"

	"soundingapp/internal/assembly"
	"soundingapp/internal/domain"
	"soundingapp/internal/profile"
	"soundingapp/internal/store"
)

type Service struct {
	Store  *store.Store
	Params assembly.Params
	Now    func() time.Time
}

func New(st *store.Store) *Service {
	return &Service{Store: st, Params: assembly.DefaultParams(), Now: time.Now}
}

// IngestResult is what the API returns after one batch is accepted.
type IngestResult struct {
	SoundingID string                `json:"sounding_id"`
	BatchID    int64                 `json:"batch_id"`
	First      int                   `json:"first"`
	Retries    int                   `json:"retries"`
	Conflicts  int                   `json:"conflicts"`
	Late       int                   `json:"late"`
	Versions   []int64               `json:"version_ids"`
	Published  *domain.PublishRecord `json:"published,omitempty"`
}

// Ingest stores a batch (batch acceptance) and rebuilds working drafts from
// all evidence plus current human judgments. Re-running the algorithm only
// replaces drafts; the signed profile is untouched.
func (s *Service) Ingest(ctx context.Context, soundingID string, pkts []domain.Packet) (IngestResult, error) {
	now := s.Now().UTC()
	res, err := s.Store.InsertBatch(ctx, soundingID, pkts, now)
	if err != nil {
		return IngestResult{}, err
	}
	versionIDs, err := s.RebuildDrafts(ctx, soundingID, res.BatchID)
	if err != nil {
		return IngestResult{}, err
	}
	out := IngestResult{
		SoundingID: soundingID, BatchID: res.BatchID,
		First: res.First, Retries: res.Retries, Conflicts: res.Conflicts,
		Late: res.Late, Versions: versionIDs,
	}
	if pub, ok, err := s.Store.LatestPublished(ctx, soundingID); err == nil && ok {
		p := pub
		out.Published = &p
	}
	return out, nil
}

// RebuildDrafts regenerates every non-published working version from the
// full append-only evidence and the active human judgments.
func (s *Service) RebuildDrafts(ctx context.Context, soundingID string, batchID int64) ([]int64, error) {
	cands, err := s.Store.CandidatePackets(ctx, soundingID)
	if err != nil {
		return nil, err
	}
	judgments, err := s.Store.ActiveJudgments(ctx, soundingID)
	if err != nil {
		return nil, err
	}
	planned := assembly.Plan(soundingID, cands, judgments, s.Params, s.Now().UTC())
	for i := range planned {
		bl := make(profile.BranchList, 0, len(planned[i].Branches))
		for _, b := range planned[i].Branches {
			bl = append(bl, profile.BranchInfo{BranchID: b.BranchID, Phase: b.Phase})
		}
		planned[i].Layers = profile.Derive(planned[i].Points, bl)
	}
	if err := s.Store.ReplaceDrafts(ctx, soundingID, batchID, s.Now().UTC(), planned); err != nil {
		return nil, err
	}
	metas, err := s.Store.ListVersions(ctx, soundingID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, m := range metas {
		if !m.PublishedAt.Valid {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// Publish signs a version's layers atomically.
func (s *Service) Publish(ctx context.Context, soundingID string, versionID int64, by, note string) (domain.PublishRecord, error) {
	return s.Store.Publish(ctx, soundingID, versionID, by, note, s.Now().UTC())
}
