package shellruntime

import (
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// retainedOutput quarantines private helper output until a matching access
// transaction commits. Positions always belong to the helper, never this queue.
// It contains no durable terminal bytes and grants no process lifetime.
type retainedOutput struct {
	mu                                      sync.Mutex
	history                                 *ownershell.History
	epoch, pending, receivedEpoch, received uint64
	transaction                             string
	active                                  bool
	changed                                 chan struct{}
}

func newRetainedOutput(epoch uint64) *retainedOutput {
	return &retainedOutput{history: ownershell.NewHistory(ownershell.ScrollbackBytes), epoch: epoch, active: true, changed: make(chan struct{}, 1)}
}
func (*retainedOutput) String() string   { return "<private shell output>" }
func (*retainedOutput) GoString() string { return "<private shell output>" }
func (s *retainedOutput) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *retainedOutput) suspend() { s.mu.Lock(); s.active = false; s.mu.Unlock(); s.notify() }
func (s *retainedOutput) prepared(transaction string, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transaction != "" && epoch > s.epoch && (s.pending == 0 || epoch >= s.pending) {
		s.transaction = transaction
		s.pending = epoch
		s.active = false
	}
}
func (s *retainedOutput) committed(transaction string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transaction == s.transaction && s.pending > s.epoch {
		s.epoch = s.pending
		s.pending = 0
		s.active = true
		s.notify()
	}
}
func (s *retainedOutput) receive(f HelperFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Epoch == 0 || f.Epoch != s.epoch && s.pending == 0 || s.pending != 0 && f.Epoch != s.pending {
		return ownershell.ErrDenied
	}
	if _, err := s.history.Receive(ownershell.HistoryFrame{Offset: f.Offset, End: f.End, Gap: f.Gap, Data: f.Data}); err != nil {
		s.active = false
		s.notify()
		return err
	}
	// A replay of an early frame can only acknowledge that frame's actual end.
	// End advertises the source high-water mark; it is not receipt of those bytes.
	if s.receivedEpoch != f.Epoch {
		s.receivedEpoch = f.Epoch
		s.received = 0
	}
	s.received = max(s.received, f.Offset+uint64(len(f.Data)))
	s.notify()
	return nil
}
func (s *retainedOutput) acknowledgment() (uint64, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receivedEpoch, s.received
}
func (s *retainedOutput) read(epoch, offset uint64) (ownershell.HistoryFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || epoch != s.epoch {
		return ownershell.HistoryFrame{}, ownershell.ErrDenied
	}
	return s.history.Read(offset, ownershell.MaxFrame)
}

func (s *retainedOutput) permits(epoch uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || epoch != s.epoch {
		return ownershell.ErrDenied
	}
	return nil
}
