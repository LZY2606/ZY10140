package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"soundingapp/internal/domain"
)

// InsertBatchResult reports how a batch of packets was classified.
type InsertBatchResult struct {
	BatchID   int64
	Accepted  []StoredPacket
	First     int
	Retries   int
	Conflicts int
	Late      int
}

// InsertBatch stores raw packets as evidence and classifies duplicates by
// device serial + sequence:
//   - same (device, seq), same payload hash  -> retry
//   - same (device, seq), different hash      -> conflict
//
// It never mutates previously stored bytes. "late" means a packet arrives
// after a packet with a larger seq of the same device had already arrived.
func (s *Store) InsertBatch(ctx context.Context, soundingID string, pkts []domain.Packet, now time.Time) (InsertBatchResult, error) {
	res := InsertBatchResult{}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO soundings(id, device, created_at) VALUES(?,?,?)
			 ON CONFLICT(id) DO NOTHING`,
			soundingID, firstDevice(pkts), now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx,
			`INSERT INTO batches(sounding_id, accepted_at, packet_count) VALUES(?,?,?)`,
			soundingID, now.UTC().Format(time.RFC3339Nano), len(pkts))
		if err != nil {
			return err
		}
		batchID, _ := r.LastInsertId()
		res.BatchID = batchID

		for _, p := range pkts {
			hash := fmt.Sprintf("%x", sha256sum(p.CanonicalPayload()))
			data, _ := json.Marshal(p)

			// Classify against existing distinct hashes for this key.
			var existingCount int
			var firstHash sql.NullString
			row := tx.QueryRowContext(ctx,
				`SELECT COUNT(DISTINCT payload_hash), MIN(payload_hash)
				 FROM raw_packets WHERE sounding_id=? AND device=? AND seq=?`,
				soundingID, p.Device, p.Seq)
			if err := row.Scan(&existingCount, &firstHash); err != nil {
				return err
			}

			var sameHash int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(1) FROM raw_packets
				 WHERE sounding_id=? AND device=? AND seq=? AND payload_hash=?`,
				soundingID, p.Device, p.Seq, hash).Scan(&sameHash); err != nil {
				return err
			}

			kind := domain.DupFirst
			var dupOf sql.NullString
			switch {
			case sameHash > 0:
				kind = domain.DupRetry
				dupOf = sql.NullString{String: hash, Valid: true}
				res.Retries++
			case existingCount > 0:
				kind = domain.DupConflict
				dupOf = firstHash
				res.Conflicts++
			default:
				res.First++
			}

			// Late: a strictly greater seq for this device arrived earlier.
			var greaterSeen int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(1) FROM raw_packets
				 WHERE sounding_id=? AND device=? AND seq>?`,
				soundingID, p.Device, p.Seq).Scan(&greaterSeen); err != nil {
				return err
			}
			late := greaterSeen > 0
			if late {
				res.Late++
			}

			_, err = tx.ExecContext(ctx,
				`INSERT INTO raw_packets
				 (sounding_id, device, seq, obs_time, received_at, batch_id,
				  payload_hash, data_json, dup_of_hash, dup_kind, late)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?)
				 ON CONFLICT(sounding_id, device, seq, payload_hash) DO NOTHING`,
				soundingID, p.Device, p.Seq,
				p.ObsTime.UTC().Format(time.RFC3339Nano),
				now.UTC().Format(time.RFC3339Nano), batchID, hash, string(data),
				dupOf, string(kind), boolInt(late))
			if err != nil {
				return err
			}
			if kind == domain.DupRetry {
				continue // retries add no new candidate observation
			}
			res.Accepted = append(res.Accepted, StoredPacket{
				Packet: p, Hash: hash, DupKind: kind, DupOfHash: dupOf,
				Late: late, ReceivedAt: now, BatchID: batchID,
			})
		}
		return nil
	})
	if err != nil {
		return InsertBatchResult{}, err
	}
	// When a conflict hash existed before this batch, an earlier insert in
	// the same batch could already have stored the same hash once; dedupe
	// accepted list by hash so assembly sees unique candidates.
	seen := map[string]bool{}
	uniq := res.Accepted[:0]
	for _, sp := range res.Accepted {
		if seen[sp.Hash] {
			continue
		}
		seen[sp.Hash] = true
		uniq = append(uniq, sp)
	}
	res.Accepted = uniq
	return res, nil
}

// AllPackets returns the full append-only packet evidence for a sounding.
func (s *Store) AllPackets(ctx context.Context, soundingID string) ([]StoredPacket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, batch_id, payload_hash, dup_kind, COALESCE(dup_of_hash,''),
		        late, received_at, data_json
		 FROM raw_packets WHERE sounding_id=? ORDER BY received_at, id`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredPacket
	for rows.Next() {
		var sp StoredPacket
		var kind, hash, dupHash, recvAt, dataJSON string
		var late int
		if err := rows.Scan(&sp.ID, &sp.BatchID, &hash, &kind, &dupHash,
			&late, &recvAt, &dataJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dataJSON), &sp.Packet); err != nil {
			return nil, err
		}
		sp.Hash = hash
		sp.DupKind = domain.DupKind(kind)
		if dupHash != "" {
			sp.DupOfHash = sql.NullString{String: dupHash, Valid: true}
		}
		sp.Late = late == 1
		sp.ReceivedAt, _ = time.Parse(time.RFC3339Nano, recvAt)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// CandidatePackets returns one StoredPacket per distinct (device,seq,hash):
// retries collapsed, conflicts preserved as distinct candidates.
func (s *Store) CandidatePackets(ctx context.Context, soundingID string) ([]StoredPacket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT MIN(id), MIN(batch_id), payload_hash, MIN(dup_kind),
		        COALESCE(MIN(dup_of_hash),''), MAX(late), MIN(received_at),
		        data_json
		 FROM raw_packets WHERE sounding_id=?
		 GROUP BY device, seq, payload_hash
		 ORDER BY MIN(id)`, soundingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredPacket
	for rows.Next() {
		var sp StoredPacket
		var kind, hash, dupHash, recvAt, dataJSON string
		var late int
		if err := rows.Scan(&sp.ID, &sp.BatchID, &hash, &kind, &dupHash,
			&late, &recvAt, &dataJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dataJSON), &sp.Packet); err != nil {
			return nil, err
		}
		sp.Hash = hash
		sp.DupKind = domain.DupKind(kind)
		if dupHash != "" {
			sp.DupOfHash = sql.NullString{String: dupHash, Valid: true}
		}
		sp.Late = late == 1
		sp.ReceivedAt, _ = time.Parse(time.RFC3339Nano, recvAt)
		out = append(out, sp)
	}
	return out, rows.Err()
}

func firstDevice(pkts []domain.Packet) string {
	for _, p := range pkts {
		if p.Device != "" {
			return p.Device
		}
	}
	return ""
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
