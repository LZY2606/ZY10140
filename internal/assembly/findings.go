package assembly

import (
	"fmt"
	"time"

	"soundingapp/internal/domain"
	"soundingapp/internal/store"
)

// buildFindings records the algorithm's evidence with per-finding detail so
// the UI can show the basis for every quality flag.
func buildFindings(sps []store.StoredPacket, autoIcing, gaps []interval,
	reversals map[int]string, branches []store.BranchRow,
	terminatedFrom int) []domain.Finding {

	var fs []domain.Finding

	for _, sp := range sps {
		switch sp.DupKind {
		case domain.DupRetry:
			fs = append(fs, domain.Finding{
				Kind: "dup", Start: sp.ObsTime, End: sp.ObsTime,
				Detail:   fmt.Sprintf("device %s seq %d repeated with identical payload", sp.Device, sp.Seq),
				Evidence: fmt.Sprintf("retry hash=%s", sp.Hash),
			})
		case domain.DupConflict:
			fs = append(fs, domain.Finding{
				Kind: "conflict", Start: sp.ObsTime, End: sp.ObsTime,
				Detail:   fmt.Sprintf("device %s seq %d has conflicting payload", sp.Device, sp.Seq),
				Evidence: fmt.Sprintf("candidate hash=%s differs from first=%s", sp.Hash, hashOrEmpty(sp)),
			})
		}
		if sp.Late {
			fs = append(fs, domain.Finding{
				Kind: "late", Start: sp.ObsTime, End: sp.ObsTime,
				Detail:   fmt.Sprintf("device %s seq %d arrived after a greater seq", sp.Device, sp.Seq),
				Evidence: fmt.Sprintf("received_at=%s", sp.ReceivedAt.Format(time.RFC3339Nano)),
			})
		}
	}

	for _, b := range branches {
		fs = append(fs, domain.Finding{
			Kind:   string(b.Phase),
			Start:  b.Start,
			End:    b.End,
			Detail: fmt.Sprintf("%s branch #%d (%s)", b.Phase, b.BranchID, b.Source),
		})
	}
	for _, iv := range autoIcing {
		if iv.start < len(sps) && iv.end < len(sps) {
			fs = append(fs, domain.Finding{
				Kind:  "icing",
				Start: sps[iv.start].ObsTime, End: sps[iv.end].ObsTime,
				Detail: fmt.Sprintf("sustained RH>=%.1f%% at T<=%.1fC over %d points",
					DefaultParams().IcingMinRH, DefaultParams().IcingMaxTempC, iv.end-iv.start+1),
			})
		}
	}
	for _, g := range gaps {
		missing := g.end - g.start + 1
		fs = append(fs, domain.Finding{
			Kind:  "gps_gap",
			Start: sps[g.start].ObsTime, End: sps[g.end].ObsTime,
			Detail: fmt.Sprintf("%d sample(s) without GPS fix", missing),
		})
	}
	for i, why := range reversals {
		if i < len(sps) {
			fs = append(fs, domain.Finding{
				Kind: "pressure_reversal", Start: sps[i].ObsTime, End: sps[i].ObsTime,
				Detail: why,
			})
		}
	}
	if terminatedFrom >= 0 && terminatedFrom < len(sps) {
		fs = append(fs, domain.Finding{
			Kind:  "terminate",
			Start: sps[terminatedFrom].ObsTime, End: sps[len(sps)-1].ObsTime,
			Detail: "termination status word received; later points excluded from layers",
		})
	}
	return fs
}

func hashOrEmpty(sp store.StoredPacket) string {
	if sp.DupOfHash.Valid {
		return sp.DupOfHash.String
	}
	return ""
}
