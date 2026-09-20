// Package store persists raw packets, assembled versions, profiles and
// human judgments in SQLite. Ingestion (new drafts) and publication
// (freezing a profile) are separate operations; published rows are copied
// into immutable tables.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"soundingapp/internal/domain"
)

// ErrConflict indicates an optimistic-lock conflict between two human
// judgments overlapping the same time interval.
var ErrConflict = errors.New("judgment conflict: overlapping interval changed")

// ErrPublishedImmutable is returned when code attempts to mutate a version
// whose layers were already signed/published.
var ErrPublishedImmutable = errors.New("published version is immutable")

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A small pool: every query closes its rows, so connections are returned
	// promptly. WAL + busy_timeout serialize writers without SQLITE_BUSY.
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, fmt.Errorf("wal: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(Schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// Tx runs fn inside a transaction.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// EnsureSounding returns the sounding id, creating the row on first use.
func (s *Store) EnsureSounding(ctx context.Context, id, device string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO soundings(id, device, created_at) VALUES(?,?,?)
		 ON CONFLICT(id) DO NOTHING`, id, device, now.UTC().Format(time.RFC3339Nano))
	return err
}

// StoredPacket is one raw packet row plus its dedup classification.
type StoredPacket struct {
	domain.Packet
	ID         int64          `json:"id"`
	BatchID    int64          `json:"batch_id"`
	Hash       string         `json:"hash"`
	DupKind    domain.DupKind `json:"dup_kind"`
	DupOfHash  sql.NullString `json:"dup_of_hash"`
	Late       bool           `json:"late"`
	ReceivedAt time.Time      `json:"received_at"`
}
