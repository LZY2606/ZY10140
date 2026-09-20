// Package service is the transactional use-case layer: batch reception is
// kept separate from profile publication, and every draft is a pure
// function of accepted packets plus signed overrides.
package service

import (
	"context"

	"sonde/internal/derive"
	"sonde/internal/domain"
	"sonde/internal/store"
)

type Service struct{ st *store.Store }

func New(st *store.Store) *Service     { return &Service{st: st} }
func (s *Service) Store() *store.Store { return s.st }

func (s *Service) CreateSounding(ctx context.Context, deviceID, name string) (store.Sounding, error) {
	return s.st.CreateSounding(ctx, deviceID, name)
}

func (s *Service) ListSoundings(ctx context.Context) ([]store.Sounding, error) {
	return s.st.ListSoundings(ctx)
}

// Ingest accepts a batch as an all-or-nothing unit. The returned draft is
// already reassembled from the new packet set.
func (s *Service) Ingest(ctx context.Context, id int64, packets []domain.Packet) (store.IngestResult, *store.Draft, error) {
	res, rows, err := s.st.IngestBatch(ctx, id, packets)
	if err != nil {
		return res, nil, err
	}
	draft, err := s.Draft(ctx, id, rows)
	return res, draft, err
}

func (s *Service) Draft(ctx context.Context, id int64, rows []domain.Packet) (*store.Draft, error) {
	rev, err := s.st.Revision(ctx, id)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows, err = s.st.Packets(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	ovs, err := s.st.ListOverrides(ctx, id)
	if err != nil {
		return nil, err
	}
	pub, err := s.st.LatestPublished(ctx, id)
	if err != nil {
		return nil, err
	}
	// Candidate ids are 1-based ordinals per seq by first arrival, which
	// matches derive's stable assignment, so overrides pass through.
	res := derive.Assemble(derive.Input{Rows: rows, Overrides: ovs})
	d := &store.Draft{
		SoundingID: id, Revision: rev,
		Result: res, Overrides: ovs,
	}
	if pub != nil {
		d.PublishedRev = pub.Revision
	}
	return d, nil
}

// AddDecision stores a signed override and returns the reassembled draft.
// conflicts lists prior signed decisions overlapping the same time window.
func (s *Service) AddDecision(ctx context.Context, o domain.Override, expectedRev int64) (*store.Draft, []domain.Override, int64, error) {
	var start, end = o.Start, o.Start
	if o.End != nil {
		end = *o.End
	}
	conflicts, err := s.st.OverlappingOverrides(ctx, o.SoundingID, start, end)
	if err != nil {
		return nil, nil, 0, err
	}
	newRev, err := s.st.AddOverride(ctx, o, expectedRev)
	if err != nil {
		return nil, conflicts, 0, err
	}
	draft, derr := s.Draft(ctx, o.SoundingID, nil)
	return draft, conflicts, newRev, derr
}

func (s *Service) Publish(ctx context.Context, id int64, author, note string) (*store.PublishedProfile, *store.Draft, error) {
	draft, err := s.Draft(ctx, id, nil)
	if err != nil {
		return nil, nil, err
	}
	pub, err := s.st.Publish(ctx, id, author, note, *draft)
	if err != nil {
		return nil, draft, err
	}
	draft.PublishedRev = pub.Revision
	return &pub, draft, nil
}

func (s *Service) LatestPublished(ctx context.Context, id int64) (*store.PublishedProfile, error) {
	return s.st.LatestPublished(ctx, id)
}
