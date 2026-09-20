package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"soundingapp/internal/domain"
)

func insertLayer(ctx context.Context, tx *sql.Tx, table string, vid, publishID int64, l domain.Layer) error {
	flags, _ := json.Marshal(l.Flags)
	q := `INSERT INTO ` + table + `
		(version_id, branch_id, phase, pressure, obs_time, temp, rh,
		 alt_gps, lat, lon, exact, interpolated, flags_json, missing)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	args := []any{
		vid, l.BranchID, string(l.Phase), l.Pressure, timeArg(l.ObsTime),
		floatArg(l.Temp), floatArg(l.RH), floatArg(l.AltGPS),
		floatArg(l.Lat), floatArg(l.Lon),
		boolInt(l.Exact), boolInt(l.Interpolated), string(flags), l.Missing,
	}
	if table == "published_layers" {
		args = append([]any{publishID}, args[1:]...)
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

func floatArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func timeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func scanLayer(row interface {
	Scan(dest ...any) error
}, frozen bool) (domain.Layer, error) {
	var l domain.Layer
	var phase, flagsJSON, missing string
	var obsTime sql.NullString
	var temp, rh, alt, lat, lon sql.NullFloat64
	var exact, interp int
	var vid int64
	var pid int64
	if frozen {
		if err := row.Scan(&pid, &vid, &l.BranchID, &phase, &l.Pressure, &obsTime,
			&temp, &rh, &alt, &lat, &lon, &exact, &interp, &flagsJSON, &missing); err != nil {
			return l, err
		}
	} else {
		if err := row.Scan(&vid, &l.BranchID, &phase, &l.Pressure, &obsTime,
			&temp, &rh, &alt, &lat, &lon, &exact, &interp, &flagsJSON, &missing); err != nil {
			return l, err
		}
	}
	l.VersionID = vid
	l.Phase = domain.Phase(phase)
	if obsTime.Valid {
		t, _ := time.Parse(time.RFC3339Nano, obsTime.String)
		l.ObsTime = &t
	}
	l.Temp = nullFloat(temp)
	l.RH = nullFloat(rh)
	l.AltGPS = nullFloat(alt)
	l.Lat = nullFloat(lat)
	l.Lon = nullFloat(lon)
	l.Exact = exact == 1
	l.Interpolated = interp == 1
	_ = json.Unmarshal([]byte(flagsJSON), &l.Flags)
	l.Missing = missing
	return l, nil
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

// Layers returns the derived layers of one version.
func (s *Store) Layers(ctx context.Context, versionID int64) ([]domain.Layer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version_id, branch_id, phase, pressure, obs_time, temp, rh,
		        alt_gps, lat, lon, exact, interpolated, flags_json, missing
		 FROM layers WHERE version_id=? ORDER BY branch_id, pressure DESC`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Layer
	for rows.Next() {
		l, err := scanLayer(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Points returns the assembled points of one version.
func (s *Store) Points(ctx context.Context, versionID int64) ([]domain.Point, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data_json FROM version_points WHERE version_id=? ORDER BY order_idx`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Point
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p domain.Point
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Branches returns motion branches of a version.
func (s *Store) Branches(ctx context.Context, versionID int64) ([]BranchRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT branch_id, phase, start_time, end_time, source
		 FROM branches WHERE version_id=? ORDER BY branch_id`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BranchRow
	for rows.Next() {
		var b BranchRow
		var phase, st, et string
		if err := rows.Scan(&b.BranchID, &phase, &st, &et, &b.Source); err != nil {
			return nil, err
		}
		b.Phase = domain.Phase(phase)
		b.Start, _ = time.Parse(time.RFC3339Nano, st)
		b.End, _ = time.Parse(time.RFC3339Nano, et)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Findings returns auto findings of a version.
func (s *Store) Findings(ctx context.Context, versionID int64) ([]domain.Finding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, version_id, kind, start_time, end_time, detail, evidence, created_at
		 FROM findings WHERE version_id=? ORDER BY start_time`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Finding
	for rows.Next() {
		var f domain.Finding
		var st, et, ct string
		if err := rows.Scan(&f.ID, &f.VersionID, &f.Kind, &st, &et,
			&f.Detail, &f.Evidence, &ct); err != nil {
			return nil, err
		}
		f.Start, _ = time.Parse(time.RFC3339Nano, st)
		f.End, _ = time.Parse(time.RFC3339Nano, et)
		f.CreatedAt, _ = time.Parse(time.RFC3339Nano, ct)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Publish atomically signs a profile. It copies every layer of the chosen
// version into published_layers and marks the version published. Any error
// rolls the whole transaction back, so a failure can never leave a partial
// set of standard layers marked published.
func (s *Store) Publish(ctx context.Context, soundingID string, versionID int64, by, note string, now time.Time) (domain.PublishRecord, error) {
	rec := domain.PublishRecord{SoundingID: soundingID, VersionID: versionID,
		PublishedBy: by, PublishedAt: now, Note: note}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var already sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT published_at FROM versions WHERE id=? AND sounding_id=?`,
			versionID, soundingID).Scan(&already)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("version not found for sounding")
		}
		if err != nil {
			return err
		}
		if already.Valid {
			return ErrPublishedImmutable
		}

		pr, err := tx.ExecContext(ctx,
			`INSERT INTO publishes(sounding_id, version_id, published_at, published_by, layer_count, note)
			 VALUES(?,?,?,?,0,?)`,
			soundingID, versionID, now.UTC().Format(time.RFC3339Nano), by, note)
		if err != nil {
			return err
		}
		pid, _ := pr.LastInsertId()

		rows, err := tx.QueryContext(ctx,
			`SELECT version_id, branch_id, phase, pressure, obs_time, temp, rh,
			        alt_gps, lat, lon, exact, interpolated, flags_json, missing
			 FROM layers WHERE version_id=? ORDER BY branch_id, pressure DESC`, versionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			l, err := scanLayer(rows, false)
			if err != nil {
				rows.Close()
				return err
			}
			if err := insertFrozen(ctx, tx, pid, soundingID, l); err != nil {
				rows.Close()
				return err
			}
			count++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if count == 0 {
			return errors.New("refusing to publish a profile with zero layers")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE publishes SET layer_count=? WHERE id=?`, count, pid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE versions SET published_at=?, published_by=? WHERE id=?`,
			now.UTC().Format(time.RFC3339Nano), by, versionID); err != nil {
			return err
		}
		rec.LayerCount = count
		return nil
	})
	return rec, err
}

func insertFrozen(ctx context.Context, tx *sql.Tx, pid int64, soundingID string, l domain.Layer) error {
	flags, _ := json.Marshal(l.Flags)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO published_layers
		 (publish_id, version_id, sounding_id, branch_id, phase, pressure, obs_time, temp, rh,
		  alt_gps, lat, lon, exact, interpolated, flags_json, missing)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, l.VersionID, soundingID, l.BranchID, string(l.Phase), l.Pressure,
		timeArg(l.ObsTime), floatArg(l.Temp), floatArg(l.RH),
		floatArg(l.AltGPS), floatArg(l.Lat), floatArg(l.Lon),
		boolInt(l.Exact), boolInt(l.Interpolated), string(flags), l.Missing)
	return err
}

// PublishedLayers returns the frozen layers of the latest publish.
func (s *Store) PublishedLayers(ctx context.Context, soundingID string) (int64, []domain.Layer, error) {
	var pid int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM publishes WHERE sounding_id=? ORDER BY id DESC LIMIT 1`,
		soundingID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT publish_id, version_id, branch_id, phase, pressure, obs_time, temp, rh,
		        alt_gps, lat, lon, exact, interpolated, flags_json, missing
		 FROM published_layers WHERE publish_id=? ORDER BY branch_id, pressure DESC`, pid)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	defer rows.Close()
	var out []domain.Layer
	for rows.Next() {
		l, err := scanLayer(rows, true)
		if err != nil {
			return 0, nil, err
		}
		out = append(out, l)
	}
	return pid, out, rows.Err()
}
