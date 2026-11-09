package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// An explicit one-minute concurrent load measurement, not a timing-based policy
// test. Agents continue writing throughout every person action. No sleeps.
func TestBlackboardAgentFloodOneMinutePersonProgress(t *testing.T) {
	base, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	cfg := base.Pool.Config()
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := db.BlackboardStore{Pool: pool}
	s.Store = store
	abortMid := blackboard.NewID()
	if err = store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Milestones = append(r.Milestones, blackboard.Milestone{ID: abortMid, Owner: p.Actor, Version: 1, Stage: "Finalizing", Frozen: json.RawMessage(`{"complete":true,"cards":[]}`)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agent := blackboard.Actor{ID: "agent", Kind: "agent"}
	commit, err := s.Propose(ctx, agent, mid, []blackboard.Change{{ID: "commit", Operation: "add", Item: blackboard.Item{Title: "commit me"}}})
	if err != nil {
		t.Fatal(err)
	}
	reject, err := s.Propose(ctx, agent, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "reject me"}}})
	if err != nil {
		t.Fatal(err)
	}
	writeCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stop(); wg.Wait() }()
	warmed := make(chan struct{})
	var once sync.Once
	done := make(chan error, 32)
	var attempts, accepted, refused atomic.Int64
	start := time.Now()
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			actor := blackboard.Actor{ID: fmt.Sprintf("writer-%d", n), Kind: "agent"}
			bulk := make([]blackboard.Change, 80)
			for i := range bulk {
				bulk[i] = blackboard.Change{Operation: "add", Item: blackboard.Item{Title: strings.Repeat("t", 200), Reason: strings.Repeat("r", 1000)}}
			}
			record := func(e error) bool {
				attempts.Add(1)
				switch {
				case e == nil:
					accepted.Add(1)
				case errors.Is(e, blackboard.ErrInvalid) || errors.Is(e, blackboard.ErrFrozen) || errors.Is(e, blackboard.ErrConflict):
					refused.Add(1)
				case writeCtx.Err() == nil:
					done <- e
					return false
				}
				return true
			}
			for writeCtx.Err() == nil {
				_, e := s.Propose(writeCtx, actor, mid, append([]blackboard.Change(nil), bulk...))
				if !record(e) {
					return
				}
				_, e = s.RecordEvent(writeCtx, actor, blackboard.EventInput{MilestoneID: mid, Kind: "decision", Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", 4000) + `"}`)})
				if !record(e) {
					return
				}
				if time.Since(start) >= time.Minute {
					once.Do(func() { close(warmed) })
				}
			}
		}(n)
	}
	guard, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	select {
	case <-warmed:
	case e := <-done:
		t.Fatalf("writer ended: %v", e)
	case <-guard.Done():
		t.Fatal("load did not complete minute")
	}
	for _, op := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"commit", func(c context.Context) error { return s.Decide(c, p, mid, commit, 0, []blackboard.ItemID{"commit"}) }},
		{"reject", func(c context.Context) error { return s.Decide(c, p, mid, reject, 0, nil) }},
		{"Apply", func(c context.Context) error {
			return s.Apply(c, p, mid, 1, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}})
		}},
		{"legacy lock clear", func(c context.Context) error { return seedDBLegacyCut(c, s, abortMid, true) }},
		{"legacy snapshot", func(c context.Context) error { return seedDBLegacyCut(c, s, mid, false) }},
	} {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		start := time.Now()
		e := op.fn(c)
		cancel()
		t.Logf("person %s: %s", op.name, time.Since(start))
		if e != nil {
			t.Fatalf("person %s: %v", op.name, e)
		}
	}
	stop()
	wg.Wait()
	select {
	case e := <-done:
		t.Fatal(e)
	default:
	}
	if accepted.Load() == 0 || refused.Load() == 0 {
		t.Fatal("load not exercised")
	}
	t.Logf("one-minute writers=32 pool=32 attempts=%d accepted=%d refused=%d", attempts.Load(), accepted.Load(), refused.Load())
	var history bool
	if err = pool.QueryRow(ctx, `SELECT record ?| ARRAY['events','proposals'] FROM blackboards WHERE id=$1`, s.BlackboardID).Scan(&history); err != nil || history {
		t.Fatalf("history in header: %v", err)
	}
}
