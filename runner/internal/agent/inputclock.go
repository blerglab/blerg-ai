package agent

import (
	"sync"
	"sync/atomic"

	"github.com/blerglab/blerg-ai/runner/internal/persontime"
)

// PersonClock belongs to a hosted session, shared by every input source and its driver.
// OnChange persists server updates for restart recovery; metadata never enters transcripts.
type PersonClock struct {
	mu       sync.Mutex
	current  atomic.Pointer[persontime.Account]
	OnChange func(*persontime.Account)
}

func (c *PersonClock) Apply(in Input) Input {
	c.mu.Lock()
	defer c.mu.Unlock()
	if in.PersonTime != nil {
		previous := c.current.Swap(in.PersonTime)
		if previous != in.PersonTime && c.OnChange != nil {
			c.OnChange(in.PersonTime)
		}
	} else {
		in.PersonTime = c.current.Load()
	}
	return in
}

// InputClockSource gives all engine queues the same fallback, including daemon-made inputs.
type InputClockSource struct{ clock atomic.Pointer[PersonClock] }

func (s *InputClockSource) SetInputClock(c *PersonClock) { s.clock.Store(c) }
func (s *InputClockSource) WithPersonTime(in Input) Input {
	c := s.clock.Load()
	if c == nil {
		return in
	}
	if in.PersonTime == nil {
		in.PersonTime = c.current.Load()
	}
	return in
}
