package assembly

import (
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/store"
)

// Plan builds the versions to persist for one sounding.
//
// Conflict policy (same device+seq, different payload):
//   - without a human choice, two candidate versions are produced:
//     candidate_a uses the earliest-arriving payload (default), candidate_b
//     the alternative. Non-conflicting points are shared; nothing is averaged.
//   - a variant_choice judgment selects one hash and collapses to one draft.
//   - a retain_both judgment keeps both candidates and marks them official.
func Plan(soundingID string, packets []store.StoredPacket, judgments []domain.Judgment,
	p Params, now time.Time) []store.AssembledVersion {

	groups := conflictGroups(packets)
	choice := chosenHashes(judgments)
	retain := hasRetainBoth(judgments)

	if len(groups) == 0 {
		in := Input{SoundingID: soundingID, Kind: "draft", Packets: packets, Judgments: judgments}
		v := Assemble(in, p, now)
		return []store.AssembledVersion{v}
	}

	// Build the variant packet sets.
	var variants [][]store.StoredPacket
	variantHashes := variantHashSets(groups)
	variantKinds := []string{"candidate_a", "candidate_b"}
	for vi, want := range variantHashes {
		set := applyChoice(packets, groups, want, choice)
		_ = vi
		variants = append(variants, set)
	}

	// If every conflicting seq has a matching variant_choice, the rejected
	// candidate collapses away (only one distinct set remains).
	if !retain {
		a, b := variants[0], variants[1]
		if sameSet(a, b) {
			in := Input{SoundingID: soundingID, Kind: "draft", Packets: a, Judgments: judgments}
			return []store.AssembledVersion{Assemble(in, p, now)}
		}
	}

	out := make([]store.AssembledVersion, 0, len(variants))
	for i, set := range variants {
		kind := "draft"
		if i < len(variantKinds) {
			kind = variantKinds[i]
		}
		in := Input{SoundingID: soundingID, Kind: kind, Packets: set, Judgments: judgments}
		out = append(out, Assemble(in, p, now))
	}
	return out
}

// conflictGroups returns one entry per conflicting (device,seq): the
// candidate hashes present for that key, in arrival order.
func conflictGroups(packets []store.StoredPacket) map[[2]any][]store.StoredPacket {
	byKey := map[[2]any][]store.StoredPacket{}
	for _, sp := range packets {
		if sp.DupKind == domain.DupConflict || hasConflictSibling(sp, packets) {
			key := [2]any{sp.Device, sp.Seq}
			byKey[key] = append(byKey[key], sp)
		}
	}
	for k, v := range byKey {
		if len(v) < 2 {
			delete(byKey, k)
		}
	}
	return byKey
}

func hasConflictSibling(sp store.StoredPacket, all []store.StoredPacket) bool {
	for _, o := range all {
		if o.Device == sp.Device && o.Seq == sp.Seq && o.Hash != sp.Hash {
			return true
		}
	}
	return false
}

func chosenHashes(js []domain.Judgment) map[[2]any]string {
	m := map[[2]any]string{}
	for _, j := range js {
		if j.Kind == "variant_choice" && j.Seq != nil && j.ChosenHash != "" {
			m[[2]any{j.Device, *j.Seq}] = j.ChosenHash
		}
	}
	return m
}

func hasRetainBoth(js []domain.Judgment) bool {
	for _, j := range js {
		if j.Kind == "retain_both" {
			return true
		}
	}
	return false
}

// variantHashSets gives the preferred hash per variant index: [0] earliest
// arrival, [1] the alternative.
func variantHashSets(groups map[[2]any][]store.StoredPacket) []map[[2]any]string {
	a := map[[2]any]string{}
	b := map[[2]any]string{}
	for key, sps := range groups {
		// already arrival-ordered by caller grouping; ensure order by id.
		ordered := append([]store.StoredPacket(nil), sps...)
		// insertion order from packets slice is arrival order, but sort to
		// be safe by ID (StoredPacket.ID set by store).
		for i := 1; i < len(ordered); i++ {
			for j := i; j > 0 && ordered[j-1].ID > ordered[j].ID; j-- {
				ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
			}
		}
		a[key] = ordered[0].Hash
		b[key] = ordered[len(ordered)-1].Hash
	}
	return []map[[2]any]string{a, b}
}

// applyChoice produces one trajectory: variant preferred hashes win unless
// a human choice overrides that specific seq.
func applyChoice(packets []store.StoredPacket, groups map[[2]any][]store.StoredPacket,
	preferred, choice map[[2]any]string) []store.StoredPacket {

	want := map[[2]any]string{}
	for k, h := range preferred {
		want[k] = h
	}
	for k, h := range choice {
		if _, ok := groups[k]; ok {
			want[k] = h
		}
	}
	out := make([]store.StoredPacket, 0, len(packets))
	for _, sp := range packets {
		key := [2]any{sp.Device, sp.Seq}
		if h, ok := want[key]; ok {
			if sp.Hash != h {
				continue
			}
		}
		out = append(out, sp)
	}
	return out
}

func sameSet(a, b []store.StoredPacket) bool {
	if len(a) != len(b) {
		return false
	}
	ha := map[string]bool{}
	for _, sp := range a {
		ha[sp.Hash] = true
	}
	for _, sp := range b {
		if !ha[sp.Hash] {
			return false
		}
	}
	return true
}
