package server

import "sync"

// delegationWaiters registers the current callers, not the lifetime of a
// request. A finished/canceled poll releases only its own generation/reference;
// it cannot detach another poll from a later decision signal.
type delegationWaiters struct {
	mu sync.Mutex
	m  map[string]*delegationWaitRegistration
}

type delegationWaitRegistration struct {
	ch   chan struct{}
	refs int
}

func newDelegationWaiters() *delegationWaiters {
	return &delegationWaiters{m: make(map[string]*delegationWaitRegistration)}
}

// acquire must precede the database read and stay registered through the wait,
// so a signal between the read and select is observed. Release on every exit
// and before registering the next iteration. The release function is idempotent.
func (w *delegationWaiters) acquire(id string) (<-chan struct{}, func()) {
	w.mu.Lock()
	registration := w.m[id]
	if registration == nil {
		registration = &delegationWaitRegistration{ch: make(chan struct{})}
		w.m[id] = registration
	}
	registration.refs++
	w.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			registration.refs--
			if registration.refs == 0 && w.m[id] == registration {
				delete(w.m, id)
			}
		})
	}
	return registration.ch, release
}

// signal retires this generation immediately. A new poll can register a new
// channel; outstanding releases from the old one cannot remove the new one.
func (w *delegationWaiters) signal(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if registration := w.m[id]; registration != nil {
		close(registration.ch)
		delete(w.m, id)
	}
}
