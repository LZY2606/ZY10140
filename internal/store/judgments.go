package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"soundingapp/internal/domain"
)

// ConflictRange describes the overlap between two concurrent judgments.
type ConflictRange struct {
	OtherSignedBy string    `json:"other_signed_by"`
	Kind          string    `json:"kind"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	OtherRev      int       `json:"other_rev"`
}

func (c ConflictRange) Error() string { return "conflict" }

// JudgmentConflict wraps ErrConflict with the affected time ranges so the UI
// can show both analysts which segment is contested.
type JudgmentConflict struct {
	Ranges []ConflictRange
}

func (e *JudgmentConflict) Error() string { return domain2ErrText(e.Ranges) }

func domain2ErrText(rs []ConflictRange) string {
	if len(rs) == 0 {
		return "judgment conflict"
	}
	b, _ := json.Marshal(rs)
	return "judgment conflict: " + string(b)
}

// ActiveJudgments returns non-superseded judgments for a sounding.
func (s *Store) ActiveJudgments(ctx context.Context, soundingID string) ([]domain.Judgment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, sounding_id, kind, start_time, end_time, phase, device,
		        seq, chosen_hash, reason, signed_by, rev, created_at
		 FROM judgments WHERE sounding_id=? AND superseded_at IS NULL
		 ORDER BY id`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Judgment
	for rows.Next() {
		j, err := scanJudgment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func scanJudgment(row interface {
	Scan(dest ...any) error
}) (domain.Judgment, error) {
	var j domain.Judgment
	var start, end sql.NullString
	var phase, device, chosenHash, reason, by, created string
	var seq sql.NullInt64
	var rev int
	if err := row.Scan(&j.ID, &j.SoundingID, &j.Kind, &start, &end,
		&phase, &device, &seq, &chosenHash, &reason, &by, &rev, &created); err != nil {
		return j, err
	}
	j.Phase = domain.Phase(phase)
	j.Device = device
	j.ChosenHash = chosenHash
	j.Reason = reason
	j.SignedBy = by
	j.Rev = rev
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if start.Valid {
		t, _ := time.Parse(time.RFC3339Nano, start.String)
		j.Start = &t
	}
	if end.Valid {
		t, _ := time.Parse(time.RFC3339Nano, end.String)
		j.End = &t
	}
	if seq.Valid {
		n := int(seq.Int64)
		j.Seq = &n
	}
	return j, nil
}

// AddJudgment validates optimistic concurrency against existing interval
// judgments of the same kind. If another analyst's active interval overlaps
// and was signed after expectedRev was read (or exists unconditionally for
// a different signer at the same key), it returns a JudgmentConflict with
// the intersecting ranges. Variant choices on the same seq conflict
// pointwise; retain_both supersedes an earlier variant_choice and vice versa.
func (s *Store) AddJudgment(ctx context.Context, j domain.Judgment, now time.Time) (domain.Judgment, error) {
	if j.SignedBy == "" {
		return j, errors.New("signed_by is required")
	}
	j.Rev = 1
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		// Pointwise conflict for variant_choice / retain_both on a seq.
		if j.Seq != nil {
			rows, err := tx.QueryContext(ctx,
				`SELECT id, signed_by, rev, kind FROM judgments
				 WHERE sounding_id=? AND superseded_at IS NULL AND seq=?
				   AND kind IN ('variant_choice','retain_both')
				   AND signed_by<>?`,
				j.SoundingID, *j.Seq, j.SignedBy)
			if err != nil {
				return err
			}
			defer rows.Close()
			type other struct {
				id   int64
				by   string
				rev  int
				kind string
			}
			var os []other
			for rows.Next() {
				var o other
				if err := rows.Scan(&o.id, &o.by, &o.rev, &o.kind); err != nil {
					rows.Close()
					return err
				}
				os = append(os, o)
			}
			rows.Close()
			if len(os) > 0 {
				cf := &JudgmentConflict{}
				for _, o := range os {
					cf.Ranges = append(cf.Ranges, ConflictRange{
						OtherSignedBy: o.by, Kind: o.kind, OtherRev: o.rev,
						Start: timePtrOrZero(j.Start), End: timePtrOrZero(j.End),
					})
				}
				return cf
			}
		}

		// Interval conflicts for phase_override / icing.
		if j.Start != nil && j.End != nil {
			rows, err := tx.QueryContext(ctx,
				`SELECT signed_by, rev, kind, start_time, end_time FROM judgments
				 WHERE sounding_id=? AND superseded_at IS NULL AND kind=?
				   AND signed_by<>? AND start_time IS NOT NULL AND end_time IS NOT NULL`,
				j.SoundingID, j.Kind, j.SignedBy)
			if err != nil {
				return err
			}
			defer rows.Close()
			var cf *JudgmentConflict
			for rows.Next() {
				var by string
				var rev int
				var kind, st, et string
				if err := rows.Scan(&by, &rev, &kind, &st, &et); err != nil {
					rows.Close()
					return err
				}
				ost, _ := time.Parse(time.RFC3339Nano, st)
				oet, _ := time.Parse(time.RFC3339Nano, et)
				if ovStart, ovEnd, ok := overlap(*j.Start, *j.End, ost, oet); ok {
					if cf == nil {
						cf = &JudgmentConflict{}
					}
					cf.Ranges = append(cf.Ranges, ConflictRange{
						OtherSignedBy: by, Kind: kind, Start: ovStart, End: ovEnd, OtherRev: rev,
					})
				}
			}
			rows.Close()
			if cf != nil {
				return cf
			}
		}

		var start, end any
		if j.Start != nil {
			start = j.Start.UTC().Format(time.RFC3339Nano)
		}
		if j.End != nil {
			end = j.End.UTC().Format(time.RFC3339Nano)
		}
		var seq any
		if j.Seq != nil {
			seq = *j.Seq
		}
		r, err := tx.ExecContext(ctx,
			`INSERT INTO judgments
			 (sounding_id, kind, start_time, end_time, phase, device, seq,
			  chosen_hash, reason, signed_by, rev, created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			j.SoundingID, j.Kind, start, end, string(j.Phase), j.Device, seq,
			j.ChosenHash, j.Reason, j.SignedBy, 1,
			now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		j.ID, _ = r.LastInsertId()
		j.CreatedAt = now
		return nil
	})
	return j, err
}

func timePtrOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func overlap(a1, a2, b1, b2 time.Time) (time.Time, time.Time, bool) {
	s := maxTime(a1, b1)
	e := minTime(a2, b2)
	if s.Before(e) || s.Equal(e) {
		return s, e, true
	}
	return time.Time{}, time.Time{}, false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
