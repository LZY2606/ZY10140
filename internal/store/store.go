// Package store is the SQLite persistence layer. Batches and profile
// publication are separate transactions and each is atomic: a failure can
// never leave a subset of standard levels marked published.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"sonde/internal/domain"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex // serialise writers (single sqlite file)
}

const schema = `
CREATE TABLE IF NOT EXISTS soundings (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS packets (
  sounding_id  INTEGER NOT NULL REFERENCES soundings(id),
  seq          INTEGER NOT NULL,
  payload_hash TEXT NOT NULL,
  observed_at  TEXT NOT NULL,
  pressure     REAL,
  temp         REAL,
  rh           REAL,
  lat          REAL,
  lon          REAL,
  alt          REAL,
  status       TEXT NOT NULL DEFAULT '',
  received_at  TEXT NOT NULL,
  late         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (sounding_id, seq, payload_hash)
);
CREATE TABLE IF NOT EXISTS arrivals (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  sounding_id INTEGER NOT NULL,
  seq         INTEGER NOT NULL,
  payload_hash TEXT NOT NULL,
  received_at TEXT NOT NULL,
  late        INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS overrides (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  sounding_id INTEGER NOT NULL REFERENCES soundings(id),
  kind        TEXT NOT NULL,
  start_time  TEXT NOT NULL,
  end_time    TEXT,
  phase       TEXT,
  icing       INTEGER,
  seq         INTEGER,
  candidate_id INTEGER,
  author      TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS published_profiles (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  sounding_id  INTEGER NOT NULL REFERENCES soundings(id),
  revision     INTEGER NOT NULL,
  published_at TEXT NOT NULL,
  author       TEXT NOT NULL,
  note         TEXT NOT NULL DEFAULT '',
  snapshot     TEXT NOT NULL,
  UNIQUE(sounding_id, revision)
);
CREATE TABLE IF NOT EXISTS published_levels (
  sounding_id  INTEGER NOT NULL,
  revision     INTEGER NOT NULL,
  pressure     REAL NOT NULL,
  branch_id    INTEGER NOT NULL,
  kind         TEXT NOT NULL,
  alt          REAL,
  temp         REAL,
  rh           REAL,
  lat          REAL,
  lon          REAL,
  observed_at  TEXT,
  exact        INTEGER NOT NULL,
  ambiguous    INTEGER NOT NULL,
  missing_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (sounding_id, revision, pressure, branch_id)
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_busy_timeout=10000&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Sounding struct {
	ID           int64      `json:"id"`
	DeviceID     string     `json:"device_id"`
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"created_at"`
	Revision     int64      `json:"revision"`
	PublishedRev int64      `json:"published_revision"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
}

func (s *Store) CreateSounding(ctx context.Context, deviceID, name string) (Sounding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO soundings(device_id,name,created_at,revision) VALUES(?,?,?,0)`,
		deviceID, name, ts(now))
	if err != nil {
		return Sounding{}, err
	}
	id, _ := res.LastInsertId()
	return Sounding{ID: id, DeviceID: deviceID, Name: name, CreatedAt: now}, nil
}

func (s *Store) ListSoundings(ctx context.Context) ([]Sounding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.device_id, s.name, s.created_at, s.revision,
		       COALESCE(pp.revision, 0), pp.published_at
		FROM soundings s
		LEFT JOIN (
		  SELECT sounding_id, revision, published_at
		  FROM published_profiles p1
		  WHERE id = (SELECT MAX(id) FROM published_profiles p2 WHERE p2.sounding_id = p1.sounding_id)
		) pp ON pp.sounding_id = s.id
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sounding
	for rows.Next() {
		var sd Sounding
		var created string
		var pubAt sql.NullString
		if err := rows.Scan(&sd.ID, &sd.DeviceID, &sd.Name, &created, &sd.Revision, &sd.PublishedRev, &pubAt); err != nil {
			return nil, err
		}
		sd.CreatedAt = parseTS(created)
		if pubAt.Valid {
			t := parseTS(pubAt.String)
			sd.PublishedAt = &t
		}
		out = append(out, sd)
	}
	return out, rows.Err()
}

// IngestResult reports what a batch did.
type IngestResult struct {
	SoundingID   int64 `json:"sounding_id"`
	Revision     int64 `json:"revision"`
	Received     int   `json:"received"`
	Retries      int   `json:"retries"`
	Conflicts    int   `json:"conflicts"` // sequences that now have >1 distinct payload
	Late         int   `json:"late"`
	NewSequences int   `json:"new_sequences"`
}

// IngestBatch accepts a whole packet batch atomically. The returned rows
// are the complete accepted packet set for the sounding (with late flags).
func (s *Store) IngestBatch(ctx context.Context, soundingID int64, packets []domain.Packet) (IngestResult, []domain.Packet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestResult{}, nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM soundings WHERE id=?`, soundingID).Scan(&exists); err != nil {
		return IngestResult{}, nil, err
	}
	if exists == 0 {
		return IngestResult{}, nil, fmt.Errorf("sounding %d not found", soundingID)
	}

	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM packets WHERE sounding_id=?`, soundingID).Scan(&maxSeq); err != nil {
		return IngestResult{}, nil, err
	}

	res := IngestResult{SoundingID: soundingID, Received: len(packets)}
	now := time.Now().UTC()
	conflictSeqs := map[int64]bool{}
	inserted := false

	for _, p := range packets {
		if p.ReceivedAt.IsZero() {
			p.ReceivedAt = now
		}
		if p.Payload == "" {
			p.Payload = payloadHashOf(p)
		}
		late := maxSeq.Valid && p.Seq < maxSeq.Int64

		// arrival log: every wire delivery, including pure retries
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO arrivals(sounding_id,seq,payload_hash,received_at,late) VALUES(?,?,?,?,?)`,
			soundingID, p.Seq, p.Payload, ts(p.ReceivedAt), b(late)); err != nil {
			return IngestResult{}, nil, err
		}

		var dup int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM packets WHERE sounding_id=? AND seq=? AND payload_hash=?`,
			soundingID, p.Seq, p.Payload).Scan(&dup); err != nil {
			return IngestResult{}, nil, err
		}
		if dup > 0 {
			res.Retries++
			continue
		}
		var other int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM packets WHERE sounding_id=? AND seq=? AND payload_hash<>?`,
			soundingID, p.Seq, p.Payload).Scan(&other); err != nil {
			return IngestResult{}, nil, err
		}
		if other > 0 {
			conflictSeqs[p.Seq] = true
		} else if maxSeq.Valid {
			if p.Seq > maxSeq.Int64 {
				res.NewSequences++
			}
		} else {
			res.NewSequences++
		}
		if late {
			res.Late++
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO packets(sounding_id,seq,payload_hash,observed_at,pressure,temp,rh,lat,lon,alt,status,received_at,late)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			soundingID, p.Seq, p.Payload, ts(p.ObservedAt),
			sqlFloat(p.Pressure), sqlFloat(p.Temp), sqlFloat(p.RH),
			sqlFloat(p.Lat), sqlFloat(p.Lon), sqlFloat(p.Alt),
			p.Status, ts(p.ReceivedAt), b(late)); err != nil {
			return IngestResult{}, nil, err
		}
		inserted = true
	}
	res.Conflicts = len(conflictSeqs)
	if inserted {
		if _, err := tx.ExecContext(ctx, `UPDATE soundings SET revision=revision+1 WHERE id=?`, soundingID); err != nil {
			return IngestResult{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IngestResult{}, nil, err
	}
	all, err := s.Packets(ctx, soundingID)
	if err != nil {
		return IngestResult{}, nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT revision FROM soundings WHERE id=?`, soundingID).Scan(&res.Revision); err != nil {
		return IngestResult{}, nil, err
	}
	return res, all, nil
}

func (s *Store) Packets(ctx context.Context, soundingID int64) ([]domain.Packet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq,payload_hash,observed_at,pressure,temp,rh,lat,lon,alt,status,received_at,late
		 FROM packets WHERE sounding_id=? ORDER BY seq, payload_hash`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPackets(rows, soundingID)
}
