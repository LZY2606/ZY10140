package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sonde/internal/domain"
)

// ErrConflict indicates an optimistic-concurrency failure.
var ErrConflict = errors.New("revision conflict")

// ErrValidation indicates an invalid request (bad range, unknown candidate).
var ErrValidation = errors.New("validation error")

func (s *Store) Revision(ctx context.Context, soundingID int64) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM soundings WHERE id=?`, soundingID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: sounding %d", ErrValidation, soundingID)
	}
	return rev, err
}

// AddOverride stores a signed manual decision and bumps the draft revision.
// expectedRev implements optimistic concurrency: two analysts judging the
// same time range cannot silently overwrite each other.
func (s *Store) AddOverride(ctx context.Context, o domain.Override, expectedRev int64) (int64, error) {
	if o.Author == "" {
		return 0, fmt.Errorf("%w: author required", ErrValidation)
	}
	if err := s.validateOverride(ctx, o); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM soundings WHERE id=?`, o.SoundingID).Scan(&rev); err != nil {
		return 0, err
	}
	if rev != expectedRev {
		return 0, fmt.Errorf("%w: expected revision %d but draft is at %d", ErrConflict, expectedRev, rev)
	}
	created := o.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO overrides(sounding_id,kind,start_time,end_time,phase,icing,seq,candidate_id,author,note,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		o.SoundingID, string(o.Kind), ts(o.Start), endTS(o.End), phaseStr(o.Phase), icingInt(o.Icing),
		seqInt(o.Seq), candInt(o.Candidate), o.Author, o.Note, ts(created)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE soundings SET revision=revision+1 WHERE id=?`, o.SoundingID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rev + 1, nil
}

func (s *Store) validateOverride(ctx context.Context, o domain.Override) error {
	if o.End != nil && o.End.Before(o.Start) {
		return fmt.Errorf("%w: end before start", ErrValidation)
	}
	switch o.Kind {
	case domain.OverridePhase:
		if o.Phase == nil {
			return fmt.Errorf("%w: phase override needs phase", ErrValidation)
		}
		switch *o.Phase {
		case domain.PhaseAscent, domain.PhaseFloat, domain.PhaseDescent, domain.PhaseTerminated:
		default:
			return fmt.Errorf("%w: unknown phase %q", ErrValidation, *o.Phase)
		}
	case domain.OverrideIcing:
		if o.Icing == nil {
			return fmt.Errorf("%w: icing override needs bool", ErrValidation)
		}
	case domain.OverrideResolve:
		if o.Seq == nil || o.Candidate == nil {
			return fmt.Errorf("%w: resolve override needs seq and candidate_id (-1 = both)", ErrValidation)
		}
		if *o.Candidate != -1 {
			hashes, err := s.candidateHashes(ctx, o.SoundingID, *o.Seq)
			if err != nil {
				return err
			}
			if *o.Candidate < 1 || int(*o.Candidate) > len(hashes) {
				return fmt.Errorf("%w: candidate %d not found for seq %d", ErrValidation, *o.Candidate, *o.Seq)
			}
		}
	}
	return nil
}

// candidateHashes returns distinct payload hashes for a seq ordered by
// first arrival; index+1 is the candidate number used by clients.
func (s *Store) candidateHashes(ctx context.Context, soundingID, seq int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload_hash FROM packets WHERE sounding_id=? AND seq=? ORDER BY received_at, payload_hash`,
		soundingID, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// OverlappingOverrides returns signed decisions overlapping [start,end],
// used to show two analysts where their judgments collide.
func (s *Store) OverlappingOverrides(ctx context.Context, soundingID int64, start, end time.Time) ([]domain.Override, error) {
	all, err := s.ListOverrides(ctx, soundingID)
	if err != nil {
		return nil, err
	}
	var out []domain.Override
	for _, o := range all {
		if o.Kind == domain.OverrideResolve {
			continue // duplicate decisions attach to a sequence, not a time span
		}
		oEnd := end
		if o.End != nil {
			oEnd = *o.End
		}
		if !oEnd.Before(start) && !o.Start.After(end) {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *Store) ListOverrides(ctx context.Context, soundingID int64) ([]domain.Override, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,sounding_id,kind,start_time,end_time,phase,icing,seq,candidate_id,author,note,created_at
		 FROM overrides WHERE sounding_id=? ORDER BY id`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Override
	for rows.Next() {
		var (
			id, sid              int64
			kind, startS, author string
			endS, phaseS, note   sql.NullString
			icing, cand, seq     sql.NullInt64
			createdS             string
		)
		if err := rows.Scan(&id, &sid, &kind, &startS, &endS, &phaseS, &icing, &seq, &cand, &author, &note, &createdS); err != nil {
			return nil, err
		}
		o := domain.Override{
			ID: id, SoundingID: sid, Kind: domain.OverrideKind(kind),
			Start: parseTS(startS), Author: author, Note: note.String,
			CreatedAt: parseTS(createdS),
		}
		if endS.Valid {
			t := parseTS(endS.String)
			o.End = &t
		}
		if phaseS.Valid && phaseS.String != "" {
			ph := domain.Phase(phaseS.String)
			o.Phase = &ph
		}
		if icing.Valid {
			v := icing.Int64 == 1
			o.Icing = &v
		}
		if seq.Valid {
			v := seq.Int64
			o.Seq = &v
		}
		if cand.Valid {
			v := cand.Int64
			o.Candidate = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func endTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func phaseStr(p *domain.Phase) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

func icingInt(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func seqInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func candInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
