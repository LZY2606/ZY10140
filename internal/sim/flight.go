package sim

import (
	"math"
	"time"

	"sonde/internal/domain"
)

// flight builds the true flight as ordered records: ascent up to ~300 hPa,
// a float plateau, continued ascent to the burst, then descent to
// termination. Icing and GPS gaps are marked on the returned samples.
func flight(cfg Config) []domain.Packet {
	type spl struct {
		pres, temp, rh, alt float64
		status              string
		gap                 bool
	}
	var s []spl
	add := func(pres, temp, rh, alt float64, status string, gap bool) {
		s = append(s, spl{pres, temp, rh, alt, status, gap})
	}

	// segment 1: surface (1010) -> 300 hPa
	const pSurface, pPlateau, pTop = 1010.0, 300.0, 12.0
	n1 := 70
	for i := 0; i < n1; i++ {
		fr := float64(i) / float64(n1-1)
		pres := pSurface * math.Exp(math.Log(pPlateau/pSurface)*fr)
		alt := altForPressure(pres)
		temp := stdTemp(alt) + 0.8*math.Sin(float64(i)*0.4)
		rh := 55 + 25*math.Sin(float64(i)*0.2)
		add(pres, temp, clamp(rh, 5, 96), alt, "", false)
	}
	// float plateau at 300 hPa (flat pressure)
	for i := 0; i < cfg.FloatSteps; i++ {
		pres := pPlateau + 0.07*math.Sin(float64(i))
		add(pres, -44+0.2*math.Sin(float64(i)), 42+3*math.Sin(float64(i)),
			altForPressure(pres), "", false)
	}
	// segment 2: 300 -> burst top (12 hPa)
	n2 := cfg.AscentSteps - n1
	if n2 < 20 {
		n2 = 50
	}
	for i := 1; i <= n2; i++ {
		fr := float64(i) / float64(n2)
		pres := pPlateau * math.Exp(math.Log(pTop/pPlateau)*fr)
		alt := altForPressure(pres)
		add(pres, stdTemp(alt), 60-20*fr, alt, "", false)
	}
	// burst marker at the top
	add(pTop, stdTemp(altForPressure(pTop)), 40, altForPressure(pTop), "BURST", false)
	// descent: 12 -> 1005 hPa
	dn := cfg.DescentSteps
	for i := 1; i <= dn; i++ {
		fr := float64(i) / float64(dn)
		pres := pTop + (pSurface-5-pTop)*(1-math.Exp(-3.2*fr))
		alt := altForPressure(pres)
		temp := stdTemp(alt) + 3*math.Pow(fr, 1.3)
		rh := 40 + 55*math.Pow(fr, 1.2)
		status := ""
		if i == dn {
			status = "TERMINATED"
		}
		add(pres, temp, clamp(rh, 5, 99), alt, status, false)
	}

	out := make([]domain.Packet, 0, len(s))
	lat0, lon0 := 35.6895, 139.6917
	for i, q := range s {
		seq := int64(i)
		t := cfg.Start.Add(time.Duration(seq) * 10 * time.Second)
		p := domain.Packet{
			Seq:        seq,
			ObservedAt: t,
			Pressure:   &q.pres,
			Temp:       &q.temp,
			RH:         &q.rh,
			Alt:        &q.alt,
			Status:     q.status,
		}
		lat := lat0 + float64(seq)*0.0008
		lon := lon0 + float64(seq)*0.0006
		p.Lat, p.Lon = &lat, &lon
		out = append(out, p)
	}

	// icing signature on the upper ascent segment
	for i := range out {
		if out[i].Seq >= cfg.IcingLo && out[i].Seq <= cfg.IcingHi {
			rh := 99.4
			t := -31.5
			out[i].RH, out[i].Temp = &rh, &t
		}
		if out[i].Seq >= cfg.GPSGapSeqLo && out[i].Seq <= cfg.GPSGapSeqHi {
			out[i].Lat, out[i].Lon = nil, nil
		}
	}
	return out
}

// stdTemp is a rough standard-atmosphere temperature by geopotential height.
func stdTemp(alt float64) float64 {
	if alt <= 11000 {
		return 22.0 - 6.4*alt/1000.0
	}
	return -53.0 + 1.2*(alt-11000)/1000.0
}
