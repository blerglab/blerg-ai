//go:build !linux

package processtelemetry

import "time"

type SampleRoot struct {
	PID      int
	Identity string
}
type Sampler struct{}

func NewSampler() *Sampler                            { return &Sampler{} }
func ProcessIdentity(int) string                      { return "" }
func (*Sampler) Sample([]SampleRoot, time.Time) Batch { return Batch{} }
