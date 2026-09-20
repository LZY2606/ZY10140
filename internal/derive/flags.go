package derive

import "sonde/internal/domain"

// attachFlags records the machine-readable evidence behind every quality
// marker so the UI can show why each flag exists.
func attachFlags(points []domain.Point, groups []ConflictGroup) {
	lateInfo := map[int64]bool{}
	for _, g := range groups {
		for _, c := range g.Candidates {
			if c.Packet.Late {
				lateInfo[g.Seq] = true
			}
		}
	}
	for i := range points {
		p := &points[i]
		add := func(f domain.Flag) {
			for _, e := range p.Flags {
				if e.Code == f.Code {
					return
				}
			}
			p.Flags = append(p.Flags, f)
		}
		if p.Conflict {
			sev := "warning"
			if p.KeptBoth {
				sev = "error"
			}
			reason := "same sequence number delivered with different payloads"
			if p.KeptBoth {
				reason += "; both candidate trajectories retained, no signed interpolation"
			}
			add(domain.Flag{Code: "duplicate_conflict", Label: "重复序号冲突", Severity: sev, Source: "auto", Reason: reason})
		}
		if p.Late || lateInfo[p.Seq] {
			add(domain.Flag{Code: "late_packet", Label: "迟到包", Severity: "info", Source: "auto", Reason: "packet arrived after a later sequence; creates a new draft but cannot change a published profile"})
		}
		if p.Reversal {
			add(domain.Flag{Code: "pressure_reversal", Label: "气压短时反向", Severity: "warning", Source: "auto", Reason: "local pressure tendency opposes branch motion; excluded from bracketing choice conservatively"})
		}
		if p.Icing {
			add(domain.Flag{Code: "sensor_icing", Label: "湿度传感器结冰", Severity: "warning", Source: p.IcingSource, Reason: "RH pinned near saturation with sensor temperature below -20C; RH withheld on affected standard levels"})
		}
		if p.GPSGap {
			add(domain.Flag{Code: "gps_gap", Label: "GPS 断点", Severity: "warning", Source: "auto", Reason: "missing position fix or >30s gap since previous fix"})
		}
		if p.Status == "BURST" {
			add(domain.Flag{Code: "burst", Label: "气球爆裂", Severity: "info", Source: "auto", Reason: "instrument reported burst status"})
		}
		if p.Phase == domain.PhaseTerminated {
			add(domain.Flag{Code: "terminated", Label: "终止", Severity: "info", Source: p.PhaseSource, Reason: "instrument reported termination"})
		}
		if p.PhaseSource == "manual" {
			add(domain.Flag{Code: "manual_phase", Label: "人工运动判定", Severity: "info", Source: "manual", Reason: "phase set by signed analyst override"})
		}
	}
}

// attachFlagsAlt mirrors evidence onto the retained candidate trajectory.
func attachFlagsAlt(alt, primary []domain.Point) {
	bySeq := map[int64]*domain.Point{}
	for i := range primary {
		bySeq[primary[i].Seq] = &primary[i]
	}
	for i := range alt {
		a := &alt[i]
		a.Phase = domain.PhaseFloat // alternate tracks are not phase-signed
		a.Flags = append(a.Flags, domain.Flag{
			Code:     "candidate_trajectory",
			Label:    "候选轨迹",
			Severity: "warning",
			Source:   "manual",
			Reason:   "retained alternate payload; excluded from standard-level derivation until resolved",
		})
		if a.Late {
			a.Flags = append(a.Flags, domain.Flag{Code: "late_packet", Label: "迟到包", Severity: "info", Source: "auto", Reason: "candidate arrived late"})
		}
	}
}
