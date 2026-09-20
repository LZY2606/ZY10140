package derive

import "sonde/internal/domain"

// markIcing sets the icing attribute per point. Manual icing intervals win;
// the automatic heuristic flags a contiguous humidity freeze signature:
// RH pinned near 100% while the sensor temperature is well below zero.
func markIcing(points []domain.Point, idx overrideIndex) {
	auto := make([]bool, len(points))
	runStart := -1
	for i := range points {
		suspect := points[i].RH != nil && points[i].Temp != nil &&
			*points[i].RH >= 98.0 && *points[i].Temp <= -20.0
		if suspect {
			if runStart < 0 {
				runStart = i
			}
		} else if runStart >= 0 {
			if i-runStart >= 3 {
				for k := runStart; k < i; k++ {
					auto[k] = true
				}
			}
			runStart = -1
		}
	}
	if runStart >= 0 && len(points)-runStart >= 3 {
		for k := runStart; k < len(points); k++ {
			auto[k] = true
		}
	}

	for i := range points {
		if v, ok := idx.icingAt(points[i].Time); ok {
			points[i].Icing = v
			points[i].IcingSource = "manual"
			continue
		}
		points[i].Icing = auto[i]
		if auto[i] {
			points[i].IcingSource = "auto"
		}
	}
}
