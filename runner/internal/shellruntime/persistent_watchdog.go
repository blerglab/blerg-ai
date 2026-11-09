package shellruntime

import (
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"sync"
	"time"
)

// NewPersistentWatchdog runs in the unfrozen runtime owner. Its deadline is
// independent of access grants and counts host sleep even if wall time falls.
func NewPersistentWatchdog(until time.Time) (func() bool, error) {
	clock, err := persistentClock()
	if err != nil {
		return nil, err
	}
	return newPersistentWatchdog(until, clock)
}
func newPersistentWatchdog(until time.Time, clock ownershell.LeaseClock) (func() bool, error) {
	wall, elapsed, err := clock()
	if err != nil || elapsed < 0 {
		return nil, ErrClosed
	}
	duration := until.Sub(wall)
	if duration < 0 {
		duration = 0
	}
	if duration > time.Duration(1<<63-1)-elapsed {
		return nil, ErrClosed
	}
	deadline := elapsed + duration
	last := elapsed
	ended := false
	var mu sync.Mutex
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		wall, elapsed, err := clock()
		if err != nil || elapsed < last || elapsed >= deadline || !wall.Before(until) {
			ended = true
		}
		last = elapsed
		return ended
	}, nil
}
func (s *Service) HasPersistentCustody(session string) bool {
	if s.bridged(session) {
		var result bool
		if s.bridgeBackground("persistent", session, &result) != nil {
			return true
		}
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.launches[session]
	return entry != nil && entry.retained != nil
}
