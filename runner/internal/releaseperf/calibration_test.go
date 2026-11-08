package releaseperf

import "testing"

func TestNoiseFromCalibrationRunsIsMaxPairwiseMedianSpread(t *testing.T) {
	// Three calibration runs' samples for the same scenario, on one commit.
	runs := [][]float64{
		{10, 10, 10}, // median 10
		{12, 12, 12}, // median 12
		{9, 9, 9},    // median 9
	}
	// max pairwise diff among {10,12,9} is |12-9| = 3
	if got := Noise(runs); got != 3 {
		t.Fatalf("got %v, want 3", got)
	}
}

func TestNoiseWithIdenticalRunsIsZero(t *testing.T) {
	runs := [][]float64{{5, 5, 5}, {5, 5, 5}, {5, 5, 5}}
	if got := Noise(runs); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}

func TestNoiseWithOneRunIsZero(t *testing.T) {
	runs := [][]float64{{5, 6, 7}}
	if got := Noise(runs); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}
