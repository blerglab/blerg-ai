package db

import (
	"context"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admission is shared by all stores and HTTP requests using a pool. Never wait
// for admission while owning a connection: at least half the pool stays outside
// agent write traffic, including queued writers blocked on database locks.
var blackboardAgentGates sync.Map

const MaxBlackboardAutomaticWaiters = 32
const BlackboardAutomaticWait = 250 * time.Millisecond

type blackboardQueueKey struct {
	pool *pgxpool.Pool
	id   string
}
type blackboardQueue struct {
	token      chan struct{}
	references int
}

var blackboardAutomaticQueues = struct {
	sync.Mutex
	rows map[blackboardQueueKey]*blackboardQueue
}{rows: make(map[blackboardQueueKey]*blackboardQueue)}

// Backpressure precedes connection admission. Refusing a busy SQL try-lock must
// not turn many writers of one blackboard into CPU-consuming retry loops.
// Entries are removed when the last active/waiting request leaves.
func (s BlackboardStore) waitAutomaticWrite(ctx context.Context, id string) (func(), error) {
	return s.admitAutomaticWrite(ctx, id, false)
}

func (s BlackboardStore) admitAutomaticWrite(ctx context.Context, id string, tryOnly bool) (func(), error) {
	return s.automaticWriteTimer(ctx, id, tryOnly, func() (<-chan time.Time, func()) {
		timer := time.NewTimer(BlackboardAutomaticWait)
		return timer.C, func() { timer.Stop() }
	})
}

func (s BlackboardStore) automaticWriteTimer(ctx context.Context, id string, tryOnly bool, timer func() (<-chan time.Time, func())) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := blackboardQueueKey{s.Pool, id}
	blackboardAutomaticQueues.Lock()
	queue := blackboardAutomaticQueues.rows[key]
	if queue == nil {
		queue = &blackboardQueue{token: make(chan struct{}, 1)}
		blackboardAutomaticQueues.rows[key] = queue
	}
	if queue.references >= MaxBlackboardAutomaticWaiters+1 {
		blackboardAutomaticQueues.Unlock()
		return nil, blackboard.ErrConflict
	}
	if tryOnly && queue.references != 0 {
		blackboardAutomaticQueues.Unlock()
		return nil, blackboard.ErrConflict
	}
	queue.references++
	blackboardAutomaticQueues.Unlock()
	unregister := func() {
		blackboardAutomaticQueues.Lock()
		defer blackboardAutomaticQueues.Unlock()
		queue.references--
		if queue.references == 0 {
			delete(blackboardAutomaticQueues.rows, key)
		}
	}
	release := func() { <-queue.token; unregister() }
	select {
	case queue.token <- struct{}{}:
		return release, nil
	default:
	}
	if tryOnly {
		unregister()
		return nil, blackboard.ErrConflict
	}
	expired, stop := timer()
	defer stop()
	select {
	case queue.token <- struct{}{}:
		return release, nil
	case <-expired:
		unregister()
		return nil, blackboard.ErrConflict
	case <-ctx.Done():
		unregister()
		return nil, ctx.Err()
	}
}

// AutomaticWriteWaiters observes admission without acquiring a connection.
func (s BlackboardStore) AutomaticWriteWaiters(id string) int {
	blackboardAutomaticQueues.Lock()
	defer blackboardAutomaticQueues.Unlock()
	if queue := blackboardAutomaticQueues.rows[blackboardQueueKey{s.Pool, id}]; queue != nil {
		return queue.references - len(queue.token)
	}
	return 0
}

type blackboardAdmissionKey struct{}
type blackboardAdmission struct{ pool *pgxpool.Pool }

func (s BlackboardStore) AdmitAgentWrite(ctx context.Context) (context.Context, func(), error) {
	if held, ok := ctx.Value(blackboardAdmissionKey{}).(blackboardAdmission); ok && held.pool == s.Pool {
		return ctx, func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	gate, ok := blackboardAgentGates.Load(s.Pool)
	if !ok {
		capacity := int(s.Pool.Config().MaxConns) / 2
		if capacity == 0 {
			return ctx, nil, blackboard.ErrConflict
		}
		gate, _ = blackboardAgentGates.LoadOrStore(s.Pool, make(chan struct{}, capacity))
	}
	sem, ok := gate.(chan struct{})
	if !ok {
		return ctx, nil, blackboard.ErrInvalid
	}
	select {
	case sem <- struct{}{}:
		return context.WithValue(ctx, blackboardAdmissionKey{}, blackboardAdmission{s.Pool}), func() { <-sem }, nil
	default:
		return ctx, nil, blackboard.ErrConflict
	}
}
