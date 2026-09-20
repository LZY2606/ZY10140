package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"soundingapp/internal/domain"
)

// VersionMeta describes a draft, candidate or published snapshot.
type VersionMeta struct {
	ID          int64         `json:"id"`
	SoundingID  string        `json:"sounding_id"`
	Kind        string        `json:"kind"`
	ParentID    sql.NullInt64 `json:"parent_id"`
	BatchID     int64         `json:"batch_id"`
	CreatedAt   time.Time     `json:"created_at"`
	PublishedAt sql.NullTime  `json:"published_at"`
	PublishedBy string        `json:"published_by"`
	Note        string        `json:"note"`
}

func (s *Store) ListVersions(ctx context.Context, soundingID string) ([]VersionMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, sounding_id, kind, parent_id, batch_id, created_at,
		        published_at, COALESCE(published_by,''), COALESCE(note,'')
		 FROM versions WHERE sounding_id=? ORDER BY id`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionMeta
	for rows.Next() {
		var vm VersionMeta
		var created string
		var pubAt sql.NullString
		if err := rows.Scan(&vm.ID, &vm.SoundingID, &vm.Kind, &vm.ParentID,
			&vm.BatchID, &created, &pubAt, &vm.PublishedBy, &vm.Note); err != nil {
			return nil, err
		}
		vm.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if pubAt.Valid && pubAt.String != "" {
			if tt, err := time.Parse(time.RFC3339Nano, pubAt.String); err == nil {
				vm.PublishedAt = sql.NullTime{Time: tt, Valid: true}
			}
		}
		out = append(out, vm)
	}
	return out, rows.Err()
}

// LatestPublished returns the most recent publish for a sounding.
func (s *Store) LatestPublished(ctx context.Context, soundingID string) (domain.PublishRecord, bool, error) {
	var rec domain.PublishRecord
	var at string
	err := s.db.QueryRowContext(ctx,
		`SELECT version_id, published_at, COALESCE(published_by,''), layer_count, COALESCE(note,'')
		 FROM publishes WHERE sounding_id=? ORDER BY id DESC LIMIT 1`,
		soundingID).Scan(&rec.VersionID, &at, &rec.PublishedBy, &rec.LayerCount, &rec.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	rec.SoundingID = soundingID
	rec.PublishedAt, _ = time.Parse(time.RFC3339Nano, at)
	return rec, true, nil
}

// AssembledVersion is everything the assembly layer produces for one
// candidate trajectory, persisted together as a single version.
type AssembledVersion struct {
	Kind     string // draft | candidate_a | candidate_b
	ParentID int64  // 0 if none
	Points   []domain.Point
	Branches []BranchRow
	Findings []domain.Finding
	Layers   []domain.Layer
}

// BranchRow is one motion branch segment.
type BranchRow struct {
	BranchID int          `json:"branch_id"`
	Phase    domain.Phase `json:"phase"`
	Start    time.Time    `json:"start_time"`
	End      time.Time    `json:"end_time"`
	Source   string       `json:"source"`
}

// ReplaceDrafts atomically removes non-published versions of a sounding and
// writes freshly assembled versions derived from the given batch. Published
// versions and the frozen published_layers table are never touched.
func (s *Store) ReplaceDrafts(ctx context.Context, soundingID string, batchID int64, now time.Time, vs []AssembledVersion) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		// Find draft/version rows that are not published.
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM versions WHERE sounding_id=? AND published_at IS NULL`,
			soundingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var stale []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			stale = append(stale, id)
		}
		rows.Close()
		for _, id := range stale {
			for _, tbl := range []string{"version_points", "findings", "layers", "branches"} {
				if _, err := tx.ExecContext(ctx,
					fmt.Sprintf(`DELETE FROM %s WHERE version_id=?`, tbl), id); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM versions WHERE id=?`, id); err != nil {
				return err
			}
		}

		for _, v := range vs {
			parent := sql.NullInt64{}
			if v.ParentID > 0 {
				parent = sql.NullInt64{Int64: v.ParentID, Valid: true}
			}
			r, err := tx.ExecContext(ctx,
				`INSERT INTO versions(sounding_id, kind, parent_id, created_at, batch_id, note)
				 VALUES(?,?,?,?,?,?)`,
				soundingID, v.Kind, parent, now.UTC().Format(time.RFC3339Nano), batchID, "")
			if err != nil {
				return err
			}
			vid, _ := r.LastInsertId()
			if err := writeVersion(ctx, tx, vid, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeVersion(ctx context.Context, tx *sql.Tx, vid int64, v AssembledVersion) error {
	for i := range v.Points {
		p := &v.Points[i]
		p.VersionID = vid
		flags, _ := json.Marshal(p.Flags)
		data, _ := json.Marshal(p)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO version_points
			 (version_id, device, seq, obs_time, order_idx, branch_id, phase, hash, data_json, flags_json)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			vid, p.Device, p.Seq, p.ObsTime.UTC().Format(time.RFC3339Nano),
			p.OrderIdx, p.BranchID, string(p.Phase), p.Hash, string(data), string(flags)); err != nil {
			return err
		}
	}
	for _, b := range v.Branches {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO branches(version_id, branch_id, phase, start_time, end_time, source)
			 VALUES(?,?,?,?,?,?)`,
			vid, b.BranchID, string(b.Phase),
			b.Start.UTC().Format(time.RFC3339Nano),
			b.End.UTC().Format(time.RFC3339Nano), b.Source); err != nil {
			return err
		}
	}
	for _, f := range v.Findings {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO findings(version_id, kind, start_time, end_time, detail, evidence)
			 VALUES(?,?,?,?,?,?)`,
			vid, f.Kind, f.Start.UTC().Format(time.RFC3339Nano),
			f.End.UTC().Format(time.RFC3339Nano), f.Detail, f.Evidence); err != nil {
			return err
		}
	}
	for _, l := range v.Layers {
		if err := insertLayer(ctx, tx, "layers", vid, 0, l); err != nil {
			return err
		}
	}
	return nil
}
