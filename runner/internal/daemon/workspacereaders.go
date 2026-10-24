package daemon

import (
	"context"
	"errors"
)

// Suspend preserves the pinned registration while rejecting new reads. Every
// operation, including its bounded delivery, is canceled and joined. Repeating
// suspend is safe; a failed join leaves the registration suspended.
func (r *WorkspaceExplorerRouter) Suspend(ctx context.Context, id string) error {
	r.mu.Lock()
	if entry := r.runtimes[id]; entry != nil {
		entry.suspended = true
	}
	r.mu.Unlock()
	return r.CancelSessionContext(ctx, id)
}

func (r *WorkspaceExplorerRouter) Resume(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("workspace readers closed")
	}
	if entry := r.runtimes[id]; entry != nil {
		entry.suspended = false
	}
	return nil
}

// Release keeps the registration fenced on a failed close, so storage release
// can be retried without losing the reader/helper that still needs joining.
func (r *WorkspaceExplorerRouter) Release(ctx context.Context, id string) error {
	if err := r.Suspend(ctx, id); err != nil {
		return err
	}
	r.mu.Lock()
	entry := r.runtimes[id]
	r.mu.Unlock()
	if entry == nil {
		return nil
	}
	var err error
	switch closer := entry.runtime.(type) {
	case interface{ CloseForLifecycle(context.Context) error }:
		err = closer.CloseForLifecycle(ctx)
	case interface{ Close(context.Context) error }:
		err = closer.Close(ctx)
	case interface{ Close() error }:
		err = closer.Close()
	default:
		return errors.New("workspace reader cleanup unavailable")
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimes[id] != entry {
		return errors.New("workspace reader replaced during cleanup")
	}
	delete(r.runtimes, id)
	return nil
}

func (r *WorkspaceExplorerRouter) registered(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runtimes[id] != nil
}
