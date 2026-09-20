package store

import (
	"database/sql"
	"time"

	"sonde/internal/derive"
	"sonde/internal/domain"
)

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPackets(rows *sql.Rows, soundingID int64) ([]domain.Packet, error) {
	var out []domain.Packet
	for rows.Next() {
		var (
			p                             domain.Packet
			obs, recv, hash, status       string
			pres, temp, rh, lat, lon, alt sql.NullFloat64
			late                          int
		)
		if err := rows.Scan(&p.Seq, &hash, &obs, &pres, &temp, &rh, &lat, &lon, &alt, &status, &recv, &late); err != nil {
			return nil, err
		}
		p.SoundingID = soundingID
		p.Payload = hash
		p.ObservedAt = parseTS(obs)
		p.ReceivedAt = parseTS(recv)
		p.Status = status
		p.Late = late == 1
		p.Pressure = f(pres)
		p.Temp = f(temp)
		p.RH = f(rh)
		p.Lat = f(lat)
		p.Lon = f(lon)
		p.Alt = f(alt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func f(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}

func sqlFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func b(v bool) int {
	if v {
		return 1
	}
	return 0
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// payloadHashOf avoids a derive import cycle risk; derive is leaf-only.
func payloadHashOf(p domain.Packet) string { return derive.PayloadHash(p) }
