package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type round5OverlapStore struct {
	db.BlackboardStore
	entered chan<- error
	release <-chan struct{}
}

func (s round5OverlapStore) UpdatePartition(ctx context.Context, id string, p blackboard.Partition, fn func(*blackboard.Record) error) error {
	entered := false
	err := s.BlackboardStore.UpdatePartition(ctx, id, p, func(r *blackboard.Record) error {
		if err := fn(r); err != nil {
			return err
		}
		entered = true
		s.entered <- nil
		select {
		case <-s.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if !entered {
		s.entered <- err
	}
	return err
}

// With 32 connections the old code admits twelve overlapping share lockers,
// then four more while a person waits. The additional lockers model a sustained
// overlap (one generation handing off to another), rather than relying on sleeps
// or probabilistic scheduler timing. They are released when the person finishes.
func TestBlackboardRound5PersonWinsOverlappingAgents(t *testing.T) {
	base, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	cfg := base.Pool.Config()
	cfg.MaxConns = 32
	cfg.ConnConfig.RuntimeParams["application_name"] = "blackboard-round5-fairness"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := db.BlackboardStore{Pool: pool}
	s.Store = store
	q, err := s.Propose(ctx, blackboard.Actor{ID: "seed", Kind: "agent"}, mid, []blackboard.Change{{ID: "commit", Operation: "add", Item: blackboard.Item{Title: "person commit"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		name string
		op   func(context.Context) error
	}{
		{"Commit", func(c context.Context) error { return s.Decide(c, p, mid, q, 0, []blackboard.ItemID{"commit"}) }},
		{"legacy snapshot", func(c context.Context) error { return seedDBLegacyCut(c, s, mid, false) }},
	} {
		t.Run(phase.name, func(t *testing.T) {
			agentCtx, stop := context.WithCancel(ctx)
			var wg sync.WaitGroup
			initial, probes := make(chan struct{}), make(chan struct{})
			defer func() { stop(); wg.Wait() }()
			reports := make(chan error, 32)
			start := func(n int, release <-chan struct{}) {
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						writer := blackboard.Service{Store: round5OverlapStore{store, reports, release}, BlackboardID: s.BlackboardID}
						_, _ = writer.Propose(agentCtx, blackboard.Actor{ID: fmt.Sprintf("%s-%p-%d", phase.name, release, i), Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "load"}}})
					}(i)
				}
			}
			gather := func(n, previousWaiters, minimumActive int) int {
				held := 0
				finished := 0
				guard, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				for held+finished+store.AutomaticWriteWaiters(s.BlackboardID)-previousWaiters < n || held < minimumActive && finished < n {
					select {
					case e := <-reports:
						switch {
						case e == nil:
							held++
						case !errors.Is(e, blackboard.ErrConflict):
							t.Fatalf("agent admission: %v", e)
						default:
							finished++
						}
					case <-guard.Done():
						t.Fatal("agents failed to enter or refuse")
					default:
						runtime.Gosched()
					}
				}
				return held
			}
			start(12, initial)
			overlap := gather(12, 0, 1)
			if overlap == 0 {
				t.Fatal("no active agent")
			}
			personCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- phase.op(personCtx) }()
			intent := false
			for {
				var waiting bool
				e := pool.QueryRow(personCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='blackboard-round5-fairness' AND wait_event_type='Lock' AND (query LIKE 'SELECT id FROM milestones%' OR query LIKE 'SELECT pg_advisory_xact_lock%')),EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name='blackboard-round5-fairness' AND wait_event_type='Lock' AND query LIKE 'SELECT pg_advisory_xact_lock%')`).Scan(&waiting, &intent)
				if e != nil {
					t.Fatal(e)
				}
				if waiting {
					break
				}
			}
			previousWaiters := store.AutomaticWriteWaiters(s.BlackboardID)
			start(4, probes)
			barged := gather(4, previousWaiters, 0)
			close(initial)
			startTime := time.Now()
			err := <-done
			close(probes)
			stop()
			wg.Wait()
			t.Logf("pool=32 initial=%d new lockers after person queued=%d person %s=%s", overlap, barged, phase.name, time.Since(startTime))
			if err != nil {
				t.Fatalf("person %s starved by overlapping agents: %v", phase.name, err)
			}
			if !intent {
				t.Fatal("person did not register exclusive write intent")
			}
		})
		if t.Failed() {
			break
		}
	}
}

func TestBlackboardRound5WorkspaceAdmissionSurvivesNewWorkers(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	// A trusted import models multiple exhausted workers. A fresh authenticated
	// session must not reset the workspace's aggregate event allowance.
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.WorkspaceID = "workspace"
		for i := 0; i < 5; i++ {
			r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: mid, Source: "agent", Kind: "decision", AuthenticatedActor: blackboard.Actor{ID: fmt.Sprintf("worker-%d", i), Kind: "agent"}, Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", blackboard.MaxWorkspaceEventBytes/5+1024) + `"}`)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, err := s.RecordEvent(ctx, blackboard.Actor{ID: fmt.Sprintf("new-worker-%d", i), Kind: "agent"}, blackboard.EventInput{MilestoneID: mid, Kind: "decision"})
		if !errors.Is(err, blackboard.ErrInvalid) {
			t.Fatalf("worker churn reset aggregate cap: %v", err)
		}
	}
	if _, err := s.RecordEvent(ctx, p.Actor, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); err != nil {
		t.Fatalf("person charged aggregate quota: %v", err)
	}
	if err := s.Apply(ctx, p, mid, 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}}); err != nil {
		t.Fatal(err)
	}
	if err := seedDBLegacyCut(ctx, s, mid, false); err != nil {
		t.Fatal(err)
	}
}

func TestBlackboardRound5WorkspacePendingOnly(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		for i := 0; i < blackboard.MaxWorkspacePendingProposals; i++ {
			r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), MilestoneID: mid, Status: "pending", Actor: blackboard.Actor{ID: fmt.Sprint(i), Kind: "agent"}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a := blackboard.Actor{ID: "fresh-worker", Kind: "agent"}
	change := []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "new"}}}
	if _, err := s.Propose(ctx, a, mid, change); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatalf("new worker bypassed aggregate pending cap: %v", err)
	}
	r, err := store.Load(ctx, s.BlackboardID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, mid, r.Proposals[0].ID, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Propose(ctx, a, mid, change); err != nil {
		t.Fatalf("rejected proposal still charged aggregate: %v", err)
	}
}

func TestBlackboardRound5SQLReadPagesBoundBytesAndRows(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	payload := json.RawMessage(`{"text":"` + strings.Repeat("x", 70<<10) + `"}`)
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		for i := 0; i < 140; i++ {
			r.Events = append(r.Events, blackboard.Event{ID: fmt.Sprintf("event-%03d", i), MilestoneID: mid, Source: "agent", Kind: "decision", AuthenticatedActor: blackboard.Actor{ID: "legacy", Kind: "agent"}, Payload: payload})
		}
		r.Milestones[0].Frozen = json.RawMessage(`{"evidence":"` + strings.Repeat("x", 2<<20) + `"}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, total, _, err := store.LoadReadPage(ctx, s.BlackboardID, 0, 100, true)
	if err != nil || total != 140 || len(page.Events) != 100 || len(page.Milestones[0].Frozen) != 0 {
		t.Fatalf("unbounded/lost record page: total=%d rows=%d err=%v", total, len(page.Events), err)
	}
	b, _ := json.Marshal(page)
	if len(b) > BlackboardPageTestBound || !page.Events[99].MetadataOmitted {
		t.Fatalf("read byte cap: bytes=%d omitted=%v", len(b), page.Events[99].MetadataOmitted)
	}
	next, _, _, err := store.LoadReadPage(ctx, s.BlackboardID, 100, 1, true)
	if err != nil || len(next.Events) != 1 || next.Events[0].ID != "event-100" {
		t.Fatalf("SQL offset lost: %v", err)
	}
	history, err := store.LoadHistoryPage(ctx, s.BlackboardID, mid, "", 0, 100, 1)
	if err != nil || history.Total != 140 || len(history.Events) != 1 || history.Events[0].ID != "event-100" {
		t.Fatalf("history pagination: %+v %v", history, err)
	}
	if _, _, _, err = store.LoadReadPage(ctx, s.BlackboardID, 0, 101, true); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatalf("unbounded page accepted: %v", err)
	}
}

const BlackboardPageTestBound = 1 << 20

func TestBlackboardRound5CardHistoryFiltersBeforePaging(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Links = []blackboard.Link{{WorkspaceID: "workspace", BoardID: "b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q, err := s.Propose(ctx, blackboard.Actor{ID: "worker", Kind: "agent"}, mid, []blackboard.Change{{ID: "card", Operation: "add", Item: blackboard.Item{BoardID: "b", CardNumber: 1, Reason: "selected"}}, {ID: "other", Operation: "add", Item: blackboard.Item{BoardID: "b", CardNumber: 2, Reason: "not selected"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, mid, q, 0, []blackboard.ItemID{"card"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Events = append(r.Events, blackboard.Event{ID: "observation", MilestoneID: mid, Source: "board", Kind: "planned", Payload: json.RawMessage(`{"board_id":"b","card_number":1}`)}, blackboard.Event{ID: "unrelated", MilestoneID: mid, Source: "board", Kind: "planned", Payload: json.RawMessage(`{"board_id":"other-board","card_number":1}`)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.LoadHistoryPage(ctx, s.BlackboardID, mid, "b", 1, 0, 1)
	if err != nil || first.Total != 2 || len(first.Events) != 1 {
		t.Fatalf("filtered first page: %+v %v", first, err)
	}
	second, err := store.LoadHistoryPage(ctx, s.BlackboardID, mid, "b", 1, 1, 1)
	if err != nil || second.Total != 2 || len(second.Events) != 1 || second.Events[0].ID == first.Events[0].ID {
		t.Fatalf("filtered second page: %+v %v", second, err)
	}
	hidden, err := store.LoadHistoryPage(ctx, s.BlackboardID, mid, "b", 2, 0, 100)
	if err != nil || hidden.Total != 0 || len(hidden.Events) != 0 {
		t.Fatalf("unselected change escaped: %+v %v", hidden, err)
	}
	foreign, err := store.LoadHistoryPage(ctx, s.BlackboardID, mid, "missing-board", 1, 0, 100)
	if err != nil || foreign.Total != 0 {
		t.Fatalf("foreign board escaped filter: %+v %v", foreign, err)
	}
}

func TestBlackboardRound5AggregateSQLBothBounds(t *testing.T) {
	for _, bound := range []string{"event_count=31999,event_bytes=0", "event_count=0,event_bytes=4194294"} {
		t.Run(bound, func(t *testing.T) {
			store, s, _, mid := partitionFixture(t)
			ctx := context.Background()
			if _, err := store.Pool.Exec(ctx, `INSERT INTO blackboard_workspace_budgets(workspace_id,scope_id) VALUES($1,$2)`, s.BlackboardID, mid); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Pool.Exec(ctx, `UPDATE blackboard_workspace_budgets SET `+bound+` WHERE workspace_id=$1`, s.BlackboardID); err != nil {
				t.Fatal(err)
			}
			err := store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorKind: "agent", ActorID: "new"}, func(r *blackboard.Record) error {
				for i := 0; i < 2; i++ {
					r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: mid, Kind: "decision", Source: "agent", AuthenticatedActor: blackboard.Actor{ID: "new", Kind: "agent"}, Payload: json.RawMessage(`{}`)})
				}
				return nil
			})
			if !errors.Is(err, blackboard.ErrInvalid) {
				t.Fatalf("SQL aggregate bound %s: %v", bound, err)
			}
		})
	}
}

type round5MilestoneTrace struct{ locks atomic.Int64 }

func (t *round5MilestoneTrace) TraceQueryStart(c context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "SELECT id FROM milestones") {
		t.locks.Add(1)
	}
	return c
}
func (*round5MilestoneTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestBlackboardRound5FullQuotaDoesNotLockMilestone(t *testing.T) {
	base, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	a := blackboard.Actor{ID: "full", Kind: "agent"}
	if err := base.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		for i := 0; i < blackboard.MaxPendingAgentProposals; i++ {
			r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), MilestoneID: mid, Status: "pending", Actor: a})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	trace := &round5MilestoneTrace{}
	cfg := base.Pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.Store = db.BlackboardStore{Pool: pool}
	if _, err := s.Propose(ctx, a, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "refused"}}}); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatal(err)
	}
	if trace.locks.Load() != 0 {
		t.Fatal("known-full quota acquired milestone fence")
	}
}

func TestBlackboardRound5AggregateProposalSQLBothBounds(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			store, s, _, mid := partitionFixture(t)
			ctx := context.Background()
			if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
				n := 79
				size := 0
				if kind == "bytes" {
					n = 1
					size = blackboard.MaxWorkspaceProposalBytes - 1024
				}
				for i := 0; i < n; i++ {
					r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), MilestoneID: mid, Status: "pending", Actor: blackboard.Actor{ID: fmt.Sprint(i), Kind: "agent"}, Changes: []blackboard.Change{{Item: blackboard.Item{Title: strings.Repeat("x", size)}}}})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err := store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionProposal, MilestoneID: mid, ActorKind: "agent", ActorID: "new"}, func(r *blackboard.Record) error {
				for i := 0; i < 2; i++ {
					r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), MilestoneID: mid, Status: "pending", Actor: blackboard.Actor{ID: "new", Kind: "agent"}, Changes: []blackboard.Change{{Item: blackboard.Item{Title: strings.Repeat("x", 2000)}}}})
				}
				return nil
			})
			if !errors.Is(err, blackboard.ErrInvalid) {
				t.Fatalf("aggregate proposal %s: %v", kind, err)
			}
		})
	}
}

func TestBlackboardRound5AgentYieldsToIntent(t *testing.T) {
	for _, kind := range []string{"blackboard", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			store, s, _, mid := partitionFixture(t)
			ctx := context.Background()
			tx, err := store.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			key := "blackboard-write:" + s.BlackboardID
			if kind == "workspace" {
				key = "blackboard-workspace:" + s.BlackboardID + ":" + mid
			}
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
				t.Fatal(err)
			}
			c, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err = s.Propose(c, blackboard.Actor{ID: "agent", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "yield"}}}); !errors.Is(err, blackboard.ErrConflict) {
				t.Fatalf("agent ignored %s intent before row locks: %v", kind, err)
			}
		})
	}
}

func TestBlackboardRound5BackpressureBeforePool(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{ActorID: "first", ActorKind: "agent", Kind: blackboard.PartitionProposal, MilestoneID: mid}, func(*blackboard.Record) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first agent never entered")
	}
	queuedCtx, stop := context.WithCancel(ctx)
	defer stop()
	second := make(chan error, 1)
	go func() {
		second <- store.UpdatePartition(queuedCtx, s.BlackboardID, blackboard.Partition{ActorID: "second", ActorKind: "agent", Kind: blackboard.PartitionProposal, MilestoneID: mid}, func(*blackboard.Record) error { return nil })
	}()
	for store.AutomaticWriteWaiters(s.BlackboardID) != 1 {
		select {
		case err := <-second:
			t.Fatalf("agent refused instead of applying backpressure: %v", err)
		case <-ctx.Done():
			t.Fatal("agent failed to queue")
		default:
			runtime.Gosched()
		}
	}
	if n := store.Pool.Stat().AcquiredConns(); n != 1 {
		t.Errorf("queued agent owns a connection: acquired=%d", n)
	}
	stop()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Errorf("queued cancellation: %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if n := store.AutomaticWriteWaiters(s.BlackboardID); n != 0 {
		t.Errorf("leaked queue waiters: %d", n)
	}
}
