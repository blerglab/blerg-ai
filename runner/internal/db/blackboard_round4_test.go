package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBlackboardRound4ActorAdmission(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	a := blackboard.Actor{ID: "a", Kind: "agent"}
	b := blackboard.Actor{ID: "b", Kind: "agent"}
	for i := 0; i < blackboard.MaxPendingAgentProposals; i++ {
		if _, err := s.Propose(ctx, a, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "idea"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Propose(ctx, a, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "overflow"}}}); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatalf("count budget: %v", err)
	}
	if _, err := s.Propose(ctx, b, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "other agent"}}}); err != nil {
		t.Fatalf("one actor exhausted another: %v", err)
	}
	// Event admission must independently reject both SQL bounds for a and leave b free.
	for _, bound := range []string{"event_count=9000", "event_count=0,event_bytes=1048576"} {
		if _, err := store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id) VALUES($1,$2,'event','a') ON CONFLICT DO NOTHING`, s.BlackboardID, mid); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `UPDATE blackboard_partition_budgets SET `+bound+` WHERE blackboard_id=$1 AND scope_id=$2 AND kind='event' AND actor_id='a'`, s.BlackboardID, mid); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordEvent(ctx, a, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); !errors.Is(err, blackboard.ErrInvalid) {
			t.Fatalf("%s: %v", bound, err)
		}
		if err := store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorKind: "agent", ActorID: a.ID}, func(r *blackboard.Record) error {
			r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: mid, Kind: "decision", Source: "agent", AuthenticatedActor: a, RecordedAt: time.Now(), Payload: json.RawMessage(`{}`)})
			return nil
		}); !errors.Is(err, blackboard.ErrInvalid) {
			t.Fatalf("SQL provider %s: %v", bound, err)
		}
		if _, err := s.RecordEvent(ctx, b, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); err != nil {
			t.Fatalf("event quota leaked between actors: %v", err)
		}
	}
}

func TestBlackboardRound4HumanEventSkipsAdmission(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	q, err := s.Propose(ctx, blackboard.Actor{ID: "a", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "idea"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id) VALUES($1,$2,'event','a') ON CONFLICT DO NOTHING`, s.BlackboardID, mid); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id) VALUES($1,$2,'event','owner') ON CONFLICT DO NOTHING`, s.BlackboardID, mid); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT kind FROM blackboard_partition_budgets WHERE blackboard_id=$1 FOR UPDATE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = s.RecordEvent(c, p.Actor, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); err != nil {
		t.Fatalf("human joined admission: %v", err)
	}
	if err = s.Decide(c, p, mid, q, 0, nil); err != nil {
		t.Fatalf("human event blocked reject: %v", err)
	}
}

// Final must capture the live slice without an observation write: that budget
// can be held indefinitely by an unrelated reader recording evidence.
type round4Board struct{ partitionBoard }

func (b round4Board) Call(c context.Context, tool string, args map[string]any, out any) error {
	switch tool {
	case "blerg_release_get":
		// The fixture's cards are still open, so Final needs an open next release.
		data := []byte(`{"release":{"id":"release","board_id":"board","status":"released"}}`)
		if args["release_id"] == "next-release" {
			data = []byte(`{"release":{"id":"next-release","board_id":"board","status":"planned"}}`)
		}
		return json.Unmarshal(data, out)
	case "blerg_release_close":
		return nil
	default:
		return b.partitionBoard.Call(c, tool, args, out)
	}
}
func TestBlackboardRound4DerivedSnapshotSkipsObservations(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Milestones[0].BoardID = "board"
		r.Milestones[0].ReleaseID = "release"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Board = round4Board{}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind) VALUES($1,$2,'observation')`, s.BlackboardID, mid); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT kind FROM blackboard_partition_budgets WHERE blackboard_id=$1 FOR UPDATE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = seedDBLegacyCut(c, s, mid, false); err != nil {
		t.Fatalf("Final joined observation queue: %v", err)
	}
	var n int
	if err = store.Pool.QueryRow(c, `SELECT count(*) FROM plan_events WHERE blackboard_id=$1 AND source='board'`, s.BlackboardID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Final wrote observations: %d %v", n, err)
	}
}

func TestBlackboardRound4SemaphoreReservesPersonConnections(t *testing.T) {
	base, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	cfg := base.Pool.Config()
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := db.BlackboardStore{Pool: pool}
	s.Store = store
	// Hold every permitted agent slot, then prove an excess writer is refused
	// before it acquires a connection, while person Apply succeeds.
	releases := []func(){}
	for i := 0; i < 2; i++ {
		_, release, e := store.AdmitAgentWrite(ctx)
		if e != nil {
			t.Fatal(e)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	if _, _, err = store.AdmitAgentWrite(ctx); !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("admission overflow: %v", err)
	}
	if _, err = s.Propose(ctx, blackboard.Actor{ID: "a", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "excess"}}}); !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("store bypassed semaphore: %v", err)
	}
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("admission acquired pool connection")
	}
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = s.Apply(c, p, mid, 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestBlackboardRound4MilestoneShareFence(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM milestones WHERE blackboard_id=$1 FOR NO KEY UPDATE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = s.Propose(c, blackboard.Actor{ID: "a", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "fenced"}}})
	if !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("agent bypassed nowait milestone fence: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Propose(c, blackboard.Actor{ID: "a", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "after person"}}}); err != nil {
		t.Fatal(err)
	}

}

func TestBlackboardRound4HumanEventLocksHeader(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM blackboards WHERE id=$1 FOR NO KEY UPDATE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := s.RecordEvent(c, p.Actor, blackboard.EventInput{MilestoneID: mid, Kind: "decision"})
		done <- e
	}()
	for {
		select {
		case e := <-done:
			t.Fatalf("human event bypassed person header lock: %v", e)
		default:
		}
		var blocked bool
		err = store.Pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT record FROM blackboards%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBlackboardRound4PersonHeaderCompatibleWithChildren(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM blackboards WHERE id=$1 FOR KEY SHARE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = s.RecordEvent(c, p.Actor, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); err != nil {
		t.Fatalf("person header lock conflicts with child foreign keys: %v", err)
	}
	if err = s.Apply(c, p, mid, 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}}); err != nil {
		t.Fatal(err)
	}
}
func TestBlackboardRound4EventCountersBelongToActor(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		a := blackboard.Actor{ID: id, Kind: "agent"}
		if _, e := s.RecordEvent(ctx, a, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"a", "b"} {
		r, e := store.LoadPartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorKind: "agent", ActorID: id})
		if e != nil || r.Usage.Events != 1 || r.Usage.EventBytes == 0 {
			t.Fatalf("actor %s counters: %+v %v", id, r.Usage, e)
		}
	}
}

func TestBlackboardRound4NumberedEventsAcrossActors(t *testing.T) {
	base, s, _, mid := partitionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := base.Pool.Config()
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.Store = db.BlackboardStore{Pool: pool}
	start := make(chan struct{})
	type answer struct {
		event blackboard.Event
		err   error
	}
	answers := make(chan answer, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) {
			<-start
			e, err := s.RecordEvent(ctx, blackboard.Actor{ID: id, Kind: "agent"}, blackboard.EventInput{MilestoneID: mid, Kind: "alpha", Payload: json.RawMessage(`{"commit":"abc123"}`)})
			answers <- answer{e, err}
		}(id)
	}
	close(start)
	values := []blackboard.Event{}
	retry := 0
	for i := 0; i < 2; i++ {
		a := <-answers
		if errors.Is(a.err, blackboard.ErrConflict) {
			retry++
			continue
		}
		if a.err != nil {
			t.Fatal(a.err)
		}
		values = append(values, a.event)
	}
	for i := 0; i < retry; i++ {
		e, err := s.RecordEvent(ctx, blackboard.Actor{ID: "retry", Kind: "agent"}, blackboard.EventInput{MilestoneID: mid, Kind: "alpha", Payload: json.RawMessage(`{"commit":"abc123"}`)})
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, e)
	}
	numbers := map[float64]bool{}
	for _, e := range values {
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		n, ok := payload["number"].(float64)
		if !ok {
			t.Fatal("missing event number")
		}
		numbers[n] = true
	}

	if !numbers[1] || !numbers[2] {
		t.Fatalf("cross-actor numbering lost serialization: %v", numbers)
	}
}
