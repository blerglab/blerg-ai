package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"
)

type delegationLifecycle struct {
	mu      sync.Mutex
	pending map[string]*delegationLifecycleWork
	// Zero selects production timing; tests exercise lock retries in milliseconds.
	lockWait, retryEvery time.Duration
}

type delegationLifecycleWork struct {
	terminal, again bool
	cutoff          time.Time
}

func (a *API) onParentTerminalEnd(id string) { a.queueDelegationLifecycle(id, true) }
func (a *API) onParentPaused(id string)      { a.queueDelegationLifecycle(id, false) }

func (a *API) onDelegationDaemonHello(ctx context.Context, daemonID string) error {
	ids, err := db.PendingDelegationParentsForDaemon(ctx, a.dbPool, daemonID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := a.expireParentDelegations(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) queueDelegationLifecycle(id string, terminal bool) {
	if a.dbPool == nil {
		return
	}
	life := &a.delegationLife
	life.mu.Lock()
	if work := life.pending[id]; work != nil {
		work.terminal = work.terminal || terminal
		if !terminal {
			work.cutoff = time.Now()
		}
		work.again = true
		life.mu.Unlock()
		return
	}
	if life.pending == nil {
		life.pending = make(map[string]*delegationLifecycleWork)
	}
	work := &delegationLifecycleWork{terminal: terminal}
	if !terminal {
		work.cutoff = time.Now()
	}
	life.pending[id] = work
	lockWait, retry := life.lockWait, life.retryEvery
	life.mu.Unlock()
	if lockWait <= 0 {
		lockWait = delegationLockWait
	}
	if retry <= 0 {
		retry = time.Second
	}
	go func() {
		// The parent frame may carry a runtime token scoped only to the parent;
		// cascading server policy must not reuse that token for a different child.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		defer func() {
			life.mu.Lock()
			if life.pending[id] == work {
				delete(life.pending, id)
			}
			life.mu.Unlock()
		}()
		for {
			life.mu.Lock()
			terminal := work.terminal
			cutoff := work.cutoff
			work.again = false
			life.mu.Unlock()
			err := a.repairDelegationLifecycle(ctx, id, terminal, cutoff, lockWait)
			life.mu.Lock()
			again := work.again
			if err == nil && !again {
				delete(life.pending, id)
			}
			life.mu.Unlock()
			if err == nil {
				if again {
					continue
				}
				return
			}
			if errors.Is(err, puddle.ErrClosedPool) {
				return
			}
			timer := time.NewTimer(retry)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Printf("delegation lifecycle %s: repair did not finish", id)
				return
			case <-timer.C:
			}
		}
	}()
}

func (a *API) expireParentDelegations(ctx context.Context, id string) error {
	return a.expireParentDelegationsThrough(ctx, id, time.Now())
}

func (a *API) expireParentDelegationsThrough(ctx context.Context, id string, cutoff time.Time) error {
	requests, err := db.ExpireParentDelegationsThrough(ctx, a.dbPool, id, cutoff)
	if err != nil {
		return err
	}
	for _, request := range requests {
		a.recordDelegationDecided(ctx, request)
		a.delegationWaiters.signal(request.ID)
	}
	return nil
}

func (a *API) repairDelegationLifecycle(ctx context.Context, id string, terminal bool, cutoff time.Time, lockWait time.Duration) error {
	if !terminal {
		return a.expireParentDelegationsThrough(ctx, id, cutoff)
	}
	parent, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil || parent == nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	account := firstNonEmpty(derefOrEmpty(parent.SpawningAccountID), "parent:"+id)
	return a.withDelegationLock(lockCtx, account, func() error {
		// A worker that read the live parent before its end may have inserted a
		// child. Holding the same account fence makes enumeration come after it.
		boundary, err := db.DelegationEndBoundary(ctx, a.dbPool, id)
		if err != nil || boundary == nil {
			return err
		}
		var problems []error
		if err := db.RevokeParentDelegationGrantsThrough(ctx, a.dbPool, id, *boundary); err != nil {
			problems = append(problems, err)
		}
		requestCutoff := *boundary
		if cutoff.After(requestCutoff) {
			requestCutoff = cutoff
		}
		if err := a.expireParentDelegationsThrough(ctx, id, requestCutoff); err != nil {
			problems = append(problems, err)
		}
		children, err := db.ChildrenToStopThrough(ctx, a.dbPool, id, *boundary)
		if err != nil {
			return errors.Join(append(problems, err)...)
		}
		for _, child := range children {
			if err := a.stopSession(ctx, child, systemEnd(db.EndReasonParentEnded)); err != nil {
				problems = append(problems, err)
				continue
			}
			row, err := db.GetSession(ctx, a.dbPool, child)
			if err != nil {
				problems = append(problems, err)
			} else if row != nil && !db.TerminallyEnded(row) {
				problems = append(problems, fmt.Errorf("child stop not recorded"))
			}
		}
		return errors.Join(problems...)
	})
}

// parentResumeProblem is shared by every path which can admit another turn.
// It fails closed on a lookup error, rather than treating a missing policy read
// as permission to revive an orphan. The normal caller still authorizes access.
func parentResumeProblem(ctx context.Context, pool *pgxpool.Pool, id string) *APIError {
	if pool == nil {
		return nil
	}
	blocked, err := db.DelegationResumeBlocked(ctx, pool, id)
	if err != nil {
		return apiErrorf(http.StatusServiceUnavailable, "session recovery permissions could not be verified")
	}
	if blocked {
		return apiErrorf(http.StatusConflict, "the session's parent ended; this child cannot resume")
	}
	return nil
}

func withParentInputGuard(ctx context.Context, pool *pgxpool.Pool, id string, enqueue func() error) error {
	if pool == nil {
		return enqueue()
	}
	// The action-lock connection must remain outside this transaction. Its
	// cancellation writes can take FK key-share locks on the child, while a
	// cluster resume must independently lock that same child to rotate tokens.
	return db.WithDelegationInputGuardForPool(ctx, pool, id, enqueue)
}

// endOrphanedSession is used before native upsert/token healing or input replay.
// It persists policy first, so a later hello can repeat a dropped kill.
func endOrphanedSession(ctx context.Context, h *Hub, pool *pgxpool.Pool, id string, dc *DaemonConn) error {
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id, "ended", &now, systemEnd(db.EndReasonParentEnded)); err != nil {
		return err
	}
	revokeSessionTokens(ctx, pool, id, "parent ended")
	if dc != nil {
		killStoppedSessions(dc, []string{id})
	}
	h.SessionTerminallyEnded(id)
	return nil
}
