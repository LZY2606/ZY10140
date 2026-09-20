package derive

import (
	"sort"
	"time"

	"sonde/internal/domain"
)

type overrideIndex struct {
	phase   []domain.Override // sorted by created time, ascending
	icing   []domain.Override
	resolve map[int64]domain.Override
}

func indexOverrides(all []domain.Override) overrideIndex {
	idx := overrideIndex{resolve: map[int64]domain.Override{}}
	for _, o := range all {
		switch o.Kind {
		case domain.OverridePhase:
			idx.phase = append(idx.phase, o)
		case domain.OverrideIcing:
			idx.icing = append(idx.icing, o)
		case domain.OverrideResolve:
			if o.Seq != nil {
				idx.resolve[*o.Seq] = o
			}
		}
	}
	sort.Slice(idx.phase, func(i, j int) bool { return idx.phase[i].CreatedAt.Before(idx.phase[j].CreatedAt) })
	sort.Slice(idx.icing, func(i, j int) bool { return idx.icing[i].CreatedAt.Before(idx.icing[j].CreatedAt) })
	return idx
}

func applyResolveOverrides(groups []ConflictGroup, idx overrideIndex) {
	for i := range groups {
		o, ok := idx.resolve[groups[i].Seq]
		if !ok {
			continue
		}
		if o.Candidate == nil {
			continue
		}
		if *o.Candidate == -1 {
			groups[i].KeepBoth = true
			groups[i].ChosenID = nil
			continue
		}
		for _, c := range groups[i].Candidates {
			if c.ID == *o.Candidate {
				id := c.ID
				groups[i].ChosenID = &id
				groups[i].KeepBoth = false
			}
		}
	}
}

// phaseAt returns the latest manual phase covering t, or "" when none.
func (idx overrideIndex) phaseAt(t time.Time) (domain.Phase, bool) {
	var ph domain.Phase
	found := false
	// slices are ascending by creation time so the last match wins
	for _, o := range idx.phase {
		if !t.Before(o.Start) && (o.End == nil || t.Before(*o.End) || t.Equal(*o.End)) {
			if o.Phase != nil {
				ph = *o.Phase
				found = true
			}
		}
	}
	return ph, found
}

// icingAt returns the latest manual icing decision covering t.
func (idx overrideIndex) icingAt(t time.Time) (bool, bool) {
	var v bool
	found := false
	for _, o := range idx.icing {
		if !t.Before(o.Start) && (o.End == nil || t.Before(*o.End) || t.Equal(*o.End)) {
			if o.Icing != nil {
				v = *o.Icing
				found = true
			}
		}
	}
	return v, found
}
