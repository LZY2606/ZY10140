package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sonde/internal/derive"
	"sonde/internal/domain"
)

// Draft is the complete, signed-agnostic assembly served to the UI.
type Draft struct {
	SoundingID   int64             `json:"sounding_id"`
	Revision     int64             `json:"revision"`
	PublishedRev int64             `json:"published_revision"`
	Result       derive.Result     `json:"assembly"`
	Overrides    []domain.Override `json:"overrides"`
}

// PublishedProfile is one immutable signed release.
type PublishedProfile struct {
	ID          int64           `json:"id"`
	SoundingID  int64           `json:"sounding_id"`
	Revision    int64           `json:"revision"`
	PublishedAt string          `json:"published_at"`
	Author      string          `json:"author"`
	Note        string          `json:"note"`
	Levels      []domain.Level  `json:"levels"`
	Snapshot    json.RawMessage `json:"snapshot,omitempty"`
}

// Publish atomically signs the current draft. Either every standard level
// of the chosen assembly and the profile header are written together, or
// nothing is. A profile already signed at this revision is rejected.
func (s *Store) Publish(ctx context.Context, soundingID int64, author, note string, draft Draft) (PublishedProfile, error) {
	if author == "" {
		return PublishedProfile{}, fmt.Errorf("%w: author required", ErrValidation)
	}
	if len(draft.Result.Levels) == 0 {
		return PublishedProfile{}, fmt.Errorf("%w: no levels to publish", ErrValidation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublishedProfile{}, err
	}
	defer tx.Rollback()

	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM soundings WHERE id=?`, soundingID).Scan(&rev); err != nil {
		return PublishedProfile{}, err
	}
	if rev != draft.Revision {
		return PublishedProfile{}, fmt.Errorf("%w: draft revision %d stale, current %d", ErrConflict, draft.Revision, rev)
	}
	var already int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM published_profiles WHERE sounding_id=? AND revision=?`,
		soundingID, rev).Scan(&already); err != nil {
		return PublishedProfile{}, err
	}
	if already > 0 {
		return PublishedProfile{}, fmt.Errorf("%w: revision %d already published", ErrConflict, rev)
	}

	snapshot, err := json.Marshal(draft)
	if err != nil {
		return PublishedProfile{}, err
	}
	now := tsNow()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO published_profiles(sounding_id,revision,published_at,author,note,snapshot)
		 VALUES(?,?,?,?,?,?)`, soundingID, rev, now, author, note, string(snapshot))
	if err != nil {
		return PublishedProfile{}, err
	}
	pid, _ := res.LastInsertId()

	// all levels in the SAME transaction: no partial published state
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO published_levels
		(sounding_id,revision,pressure,branch_id,kind,alt,temp,rh,lat,lon,observed_at,exact,ambiguous,missing_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return PublishedProfile{}, err
	}
	defer stmt.Close()
	for _, l := range draft.Result.Levels {
		mj, _ := json.Marshal(l.Missing)
		if _, err := stmt.ExecContext(ctx, soundingID, rev, l.Pressure, l.BranchID, string(l.Kind),
			sqlFloat(l.Alt), sqlFloat(l.Temp), sqlFloat(l.RH), sqlFloat(l.Lat), sqlFloat(l.Lon),
			timeTS(l.Time), b(l.Exact), b(l.Ambiguous), string(mj)); err != nil {
			return PublishedProfile{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PublishedProfile{}, err
	}
	return PublishedProfile{
		ID: pid, SoundingID: soundingID, Revision: rev,
		PublishedAt: now, Author: author, Note: note,
		Levels: draft.Result.Levels, Snapshot: snapshot,
	}, nil
}

// LatestPublished returns the most recently signed profile, or nil.
func (s *Store) LatestPublished(ctx context.Context, soundingID int64) (*PublishedProfile, error) {
	var (
		id, rev                       int64
		pubAt, author, note, snapshot string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id,revision,published_at,author,note,snapshot FROM published_profiles
		 WHERE sounding_id=? ORDER BY revision DESC, id DESC LIMIT 1`, soundingID).
		Scan(&id, &rev, &pubAt, &author, &note, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	levels, err := s.publishedLevels(ctx, soundingID, rev)
	if err != nil {
		return nil, err
	}
	return &PublishedProfile{
		ID: id, SoundingID: soundingID, Revision: rev, PublishedAt: pubAt,
		Author: author, Note: note, Levels: levels, Snapshot: json.RawMessage(snapshot),
	}, nil
}

func (s *Store) publishedLevels(ctx context.Context, soundingID, rev int64) ([]domain.Level, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pressure,branch_id,kind,alt,temp,rh,lat,lon,observed_at,exact,ambiguous,missing_json
		 FROM published_levels WHERE sounding_id=? AND revision=? ORDER BY branch_id, pressure DESC`,
		soundingID, rev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Level
	for rows.Next() {
		var (
			pres          float64
			bid           int
			kind          string
			alt, temp, rh sql.NullFloat64
			lat, lon      sql.NullFloat64
			obs           sql.NullString
			exact, amb    int
			missingJSON   string
		)
		if err := rows.Scan(&pres, &bid, &kind, &alt, &temp, &rh, &lat, &lon, &obs, &exact, &amb, &missingJSON); err != nil {
			return nil, err
		}
		l := domain.Level{
			Pressure: pres, BranchID: bid, Kind: domain.Phase(kind),
			Alt: f(alt), Temp: f(temp), RH: f(rh), Lat: f(lat), Lon: f(lon),
			Exact: exact == 1, Ambiguous: amb == 1,
		}
		if obs.Valid {
			t := parseTS(obs.String)
			l.Time = &t
		}
		_ = json.Unmarshal([]byte(missingJSON), &l.Missing)
		out = append(out, l)
	}
	return out, rows.Err()
}
