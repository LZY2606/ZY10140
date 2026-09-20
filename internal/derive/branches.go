package derive

import "sonde/internal/domain"

// buildBranches groups adjacent same-phase primary points into branches and
// records the evidence for every transition (burst, manual override, ...).
func buildBranches(points []domain.Point) ([]domain.Branch, []domain.Transition) {
	if len(points) == 0 {
		return nil, nil
	}
	var branches []domain.Branch
	var transitions []domain.Transition

	start := 0
	flush := func(end int) {
		id := len(branches) + 1
		branches = append(branches, domain.Branch{
			ID:        id,
			Kind:      points[start].Phase,
			StartSeq:  points[start].Seq,
			EndSeq:    points[end].Seq,
			StartTime: points[start].Time,
			EndTime:   points[end].Time,
			Source:    points[start].PhaseSource,
		})
	}
	for i := 1; i <= len(points); i++ {
		if i == len(points) || points[i].Phase != points[start].Phase {
			flush(i - 1)
			if i < len(points) {
				reason, source := transitionReason(points, start, i)
				transitions = append(transitions, domain.Transition{
					FromSeq: points[i-1].Seq,
					ToSeq:   points[i].Seq,
					From:    points[i-1].Phase,
					To:      points[i].Phase,
					Reason:  reason,
					Source:  source,
					Time:    points[i].Time,
				})
			}
			start = i
		}
	}
	for i := range points {
		points[i].BranchID = branchOf(branches, points[i])
	}
	// Motion groups: ascent segments separated by a float plateau are one
	// ascent "motion branch" for standard-level interpolation (the plateau
	// stays a visible branch and still emits transitions).
	groupMotion(branches)
	return branches, transitions
}

// groupMotion assigns shared GroupIDs to ascent runs linked across an
// intervening float plateau. Group 0 means "use the branch id".
func groupMotion(branches []domain.Branch) {
	nextGroup := 1000
	groupOf := map[int]int{}
	for i := 0; i < len(branches); i++ {
		if branches[i].Kind != domain.PhaseAscent {
			continue
		}
		if _, done := groupOf[branches[i].ID]; done {
			continue
		}
		gid := nextGroup
		nextGroup++
		groupOf[branches[i].ID] = gid
		// absorb A -> F -> A chains
		j := i
		for j+2 < len(branches) &&
			branches[j+1].Kind == domain.PhaseFloat &&
			branches[j+2].Kind == domain.PhaseAscent {
			groupOf[branches[j+2].ID] = gid
			j += 2
		}
	}
	for i := range branches {
		if g, ok := groupOf[branches[i].ID]; ok {
			branches[i].GroupID = g
		} else {
			branches[i].GroupID = branches[i].ID
		}
	}
}

func branchOf(branches []domain.Branch, p domain.Point) int {
	for _, b := range branches {
		if p.Seq >= b.StartSeq && p.Seq <= b.EndSeq && p.Phase == b.Kind {
			return b.ID
		}
	}
	return 0
}

func transitionReason(points []domain.Point, prevStart, i int) (reason, source string) {
	cur, prev := points[i], points[i-1]
	if cur.PhaseSource == "manual" || prev.PhaseSource == "manual" {
		return "analyst_phase_override", "manual"
	}
	// the BURST sample is the last ascent point; the next point begins the
	// descent branch (allow a few samples of distance at the boundary).
	for j := i; j >= 0 && j >= i-3; j-- {
		if points[j].Status == "BURST" {
			return "burst_status", "auto"
		}
	}
	if cur.Phase == domain.PhaseDescent {
		for j := i - 1; j >= 0 && i-j <= 3; j-- {
			if points[j].Status == "BURST" {
				return "burst_status", "auto"
			}
		}
	}
	if cur.Status == "BURST" || prev.Status == "BURST" {
		return "burst_status", "auto"
	}
	if cur.Phase == domain.PhaseDescent && prev.Phase != domain.PhaseDescent {
		return "pressure_tendency_reversal", "auto"
	}
	if cur.Phase == domain.PhaseFloat || prev.Phase == domain.PhaseFloat {
		return "pressure_plateau", "auto"
	}
	return "pressure_tendency_change", "auto"
}
