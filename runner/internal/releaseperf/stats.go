package releaseperf

import (
	"math"
	"sort"
)

// Median returns the sample median. It does not mutate its argument.
func Median(samples []float64) float64 {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// P95 returns the sample's 95th percentile, computed as
// sorted[ceil(0.95*N)-1] — a named helper instead of a hardcoded sample
// index, so it doesn't drift as N changes per scenario class.
func P95(samples []float64) float64 {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	idx := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
