package derive

import (
	"sort"
	"time"

	"sonde/internal/domain"
)

// Assemble performs the full offline assembly of one sounding.
func Assemble(in Input) Result {
	groups := groupCandidates(in.Rows)
	overrides := indexOverrides(in.Overrides)
	applyResolveOverrides(groups, overrides)

	primary, alt := buildTracks(groups)
	classify(primary, overrides)
	markGPSGaps(primary)
	markIcing(primary, overrides)
	attachFlags(primary, groups)
	attachFlagsAlt(alt, primary)

	branches, transitions := buildBranches(primary)
	levels := deriveLevels(primary, branches)

	return Result{
		Points:      primary,
		AltPoints:   alt,
		Branches:    branches,
		Transitions: transitions,
		Levels:      levels,
		Conflicts:   groups,
	}
}

// grouped payloads for a single sequence number
type seqGroup struct {
	seq   int64
	ts    time.Time
	rows  []domain.Packet // distinct payloads, arrival order
	hash  []string
	first []time.Time
	late  bool
}

func groupCandidates(rows []domain.Packet) []ConflictGroup {
	bySeq := map[int64]*seqGroup{}
	order := []int64{}
	for _, r := range rows {
		r.Payload = PayloadHash(r)
		g, ok := bySeq[r.Seq]
		if !ok {
			g = &seqGroup{seq: r.Seq, ts: r.ObservedAt}
			bySeq[r.Seq] = g
			order = append(order, r.Seq)
		}
		idx := -1
		for i, hh := range g.hash {
			if hh == r.Payload {
				idx = i
			}
		}
		if idx < 0 {
			g.hash = append(g.hash, r.Payload)
			g.rows = append(g.rows, r)
			g.first = append(g.first, r.ReceivedAt)
		}
		if r.Late {
			g.late = true
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]ConflictGroup, 0, len(order))
	for _, s := range order {
		g := bySeq[s]
		// stable candidate ordering: first arrival, tie broken by hash
		sort.Slice(g.rows, func(a, b int) bool {
			if !g.first[a].Equal(g.first[b]) {
				return g.first[a].Before(g.first[b])
			}
			return g.hash[a] < g.hash[b]
		})
		cg := ConflictGroup{Seq: g.seq, Time: g.ts}
		for i, pk := range g.rows {
			cg.Candidates = append(cg.Candidates, domain.Candidate{
				ID:        int64(i + 1),
				Seq:       g.seq,
				FirstSeen: g.first[i],
				Packet:    pk,
			})
		}
		if len(cg.Candidates) > 1 {
			chosen := cg.Candidates[0].ID
			cg.ChosenID = &chosen
		}
		out = append(out, cg)
	}
	return out
}

func buildTracks(groups []ConflictGroup) (primary, alt []domain.Point) {
	for _, cg := range groups {
		rows := make([]domain.Packet, len(cg.Candidates))
		for i, c := range cg.Candidates {
			rows[i] = c.Packet
		}
		chosen := 0
		keepBoth := cg.KeepBoth
		if cg.ChosenID != nil {
			keepBoth = false
			for i, c := range cg.Candidates {
				if c.ID == *cg.ChosenID {
					chosen = i
				}
			}
		}
		conflict := len(cg.Candidates) > 1

		pk := rows[chosen]
		p := domain.Point{
			Seq:         cg.Seq,
			TrackID:     0,
			Time:        pk.ObservedAt,
			Pressure:    pk.Pressure,
			Temp:        pk.Temp,
			RH:          pk.RH,
			Lat:         pk.Lat,
			Lon:         pk.Lon,
			Alt:         pk.Alt,
			Status:      pk.Status,
			Conflict:    conflict,
			CandidateID: cg.Candidates[chosen].ID,
			KeptBoth:    keepBoth && conflict,
			Late:        pk.Late,
			Flags:       []domain.Flag{},
		}
		primary = append(primary, p)

		if conflict && keepBoth {
			for i, c := range cg.Candidates {
				if i == chosen {
					continue
				}
				a := domain.Point{
					Seq:         cg.Seq,
					TrackID:     c.ID, // alt track identity = candidate id
					Time:        c.Packet.ObservedAt,
					Pressure:    c.Packet.Pressure,
					Temp:        c.Packet.Temp,
					RH:          c.Packet.RH,
					Lat:         c.Packet.Lat,
					Lon:         c.Packet.Lon,
					Alt:         c.Packet.Alt,
					Status:      c.Packet.Status,
					Conflict:    true,
					CandidateID: c.ID,
					KeptBoth:    true,
					Late:        c.Packet.Late,
					Flags:       []domain.Flag{},
				}
				alt = append(alt, a)
			}
		}
	}
	sort.Slice(primary, func(i, j int) bool { return primary[i].Seq < primary[j].Seq })
	sort.Slice(alt, func(i, j int) bool {
		if alt[i].Seq != alt[j].Seq {
			return alt[i].Seq < alt[j].Seq
		}
		return alt[i].CandidateID < alt[j].CandidateID
	})
	return primary, alt
}
