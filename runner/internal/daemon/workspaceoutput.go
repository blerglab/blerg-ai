package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

type workspaceOutputJob struct {
	ctx   context.Context
	write func() error
	done  chan error
}

// A bounded single writer with separate reserved viewer capacity. Once a frame
// starts it cannot be preempted; a queued viewer always precedes the next tree
// frame. Callers retain their request until delivery, cancellation or refusal.
type workspaceOutputQueue struct {
	mu               sync.Mutex
	viewer, explorer []*workspaceOutputJob
	running          bool
}

func (q *workspaceOutputQueue) send(ctx context.Context, viewer bool, write func() error) error {
	job := &workspaceOutputJob{ctx: ctx, write: write, done: make(chan error, 1)}
	q.mu.Lock()
	queue, limit := &q.explorer, 4
	if viewer {
		queue, limit = &q.viewer, 4
	}
	if len(*queue) >= limit {
		q.mu.Unlock()
		return errors.New("workspace output queue full")
	}
	*queue = append(*queue, job)
	if !q.running {
		q.running = true
		go q.run()
	}
	q.mu.Unlock()
	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		q.mu.Lock()
		for _, pending := range []*[]*workspaceOutputJob{&q.viewer, &q.explorer} {
			for i, item := range *pending {
				if item == job {
					*pending = append((*pending)[:i], (*pending)[i+1:]...)
					q.mu.Unlock()
					return ctx.Err()
				}
			}
		}
		q.mu.Unlock()
		return <-job.done
	}
}
func (q *workspaceOutputQueue) run() {
	for {
		q.mu.Lock()
		queue := &q.viewer
		if len(*queue) == 0 {
			queue = &q.explorer
		}
		if len(*queue) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		job := (*queue)[0]
		*queue = (*queue)[1:]
		q.mu.Unlock()
		err := job.ctx.Err()
		if err == nil {
			err = job.write()
		}
		job.done <- err
	}
}
func workspaceOutputDeadline(ctx context.Context, until time.Time) time.Time {
	if deadline, ok := ctx.Deadline(); ok && (until.IsZero() || deadline.Before(until)) {
		until = deadline
	}
	if until.IsZero() {
		until = time.Now().Add(2 * time.Second)
	}
	return until
}
