package derive

import (
	"hash"
	"math"
	"sort"
	"strconv"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func writeFloat(h hash.Hash, f *float64) {
	if f == nil {
		h.Write([]byte("n"))
		return
	}
	h.Write([]byte(strconv.FormatFloat(*f, 'g', -1, 64)))
}

func ptr(f float64) *float64 { return &f }

func valid(f *float64) bool {
	return f != nil && !math.IsNaN(*f)
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// median returns the median of vals, which are copied and sorted.
func median(vals []float64) float64 {
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	n := len(cp)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}
