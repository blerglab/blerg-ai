package activity

import (
	"context"
	"sync"
	"time"
)

type workKey struct{}

// Work records lifecycle durations without retaining paths or errors.
type Work struct {
	mu      sync.Mutex
	seconds map[string]float64
}

func NewWork(ctx context.Context) (context.Context, *Work) {
	w := &Work{seconds: map[string]float64{}}
	return context.WithValue(ctx, workKey{}, w), w
}
func Measure(ctx context.Context, phase string, fn func() error) error {
	start := time.Now()
	err := fn()
	if w, ok := ctx.Value(workKey{}).(*Work); ok {
		w.mu.Lock()
		w.seconds[phase] += time.Since(start).Seconds()
		w.mu.Unlock()
	}
	return err
}
func (w *Work) Seconds(total time.Duration) map[string]float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := map[string]float64{"sync": max(0, total.Seconds()-w.seconds["freeze"]-w.seconds["thaw"])}
	for k, v := range w.seconds {
		result[k] = v
	}
	return result
}
