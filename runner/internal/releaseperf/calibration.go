package releaseperf

import "math"

// CalibrationRuns is the total number of same-commit repetitions for a
// scenario: the primary run plus two calibration repeats.
// docs/design/release-performance.md, "Noise calibration (H4)".
const CalibrationRuns = 3

// Noise computes a scenario's noise floor from its calibration runs: the
// maximum absolute difference between any two runs' medians. A simple,
// auditable spread measure rather than a modeled distribution, matching the
// approved design. Fewer than two runs has no spread to measure.
func Noise(runs [][]float64) float64 {
	if len(runs) < 2 {
		return 0
	}
	medians := make([]float64, len(runs))
	for i, r := range runs {
		medians[i] = Median(r)
	}
	spread := 0.0
	for i := range medians {
		for j := i + 1; j < len(medians); j++ {
			if d := math.Abs(medians[i] - medians[j]); d > spread {
				spread = d
			}
		}
	}
	return spread
}
