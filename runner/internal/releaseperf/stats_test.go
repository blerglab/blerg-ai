package releaseperf

import "testing"

func TestMedianOdd(t *testing.T) {
	if got := Median([]float64{3, 1, 2}); got != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestMedianEven(t *testing.T) {
	if got := Median([]float64{1, 2, 3, 4}); got != 2.5 {
		t.Fatalf("got %v", got)
	}
}

func TestP95ElevenSamples(t *testing.T) {
	samples := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 100}
	// ceil(0.95*11)-1 = ceil(10.45)-1 = 11-1 = 10 (0-indexed) -> sorted[10] = 100
	if got := P95(samples); got != 100 {
		t.Fatalf("got %v, want 100", got)
	}
}

func TestP95ThirtySamples(t *testing.T) {
	samples := make([]float64, 30)
	for i := range samples {
		samples[i] = float64(i + 1) // 1..30
	}
	// ceil(0.95*30)-1 = ceil(28.5)-1 = 29-1 = 28 (0-indexed) -> sorted[28] = 29
	if got := P95(samples); got != 29 {
		t.Fatalf("got %v, want 29", got)
	}
}
