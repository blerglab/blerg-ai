package db_test

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func TestBlackboardRound6FullBudgetEventReplay(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(map[bool]string{false: "actor", true: "workspace"}[workspace], func(t *testing.T) {
			store, s, _, mid := partitionFixture(t)
			ctx := context.Background()
			actor := blackboard.Actor{ID: "retry", Kind: "agent"}
			var err error
			if workspace {
				_, err = store.Pool.Exec(ctx, `INSERT INTO blackboard_workspace_budgets(workspace_id,scope_id,event_count,event_bytes) VALUES($1,$2,$3,0)`, s.BlackboardID, mid, blackboard.MaxWorkspaceEvents-1)
			} else {
				_, err = store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id,event_count,event_bytes) VALUES($1,$2,'event',$3,$4,0)`, s.BlackboardID, mid, actor.ID, blackboard.MaxAutomaticEvents-1)
			}
			if err != nil {
				t.Fatal(err)
			}
			input := blackboard.EventInput{ID: blackboard.NewID(), MilestoneID: mid, Kind: "decision"}
			first, err := s.RecordEvent(ctx, actor, input)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := s.RecordEvent(ctx, actor, input)
			if err != nil || retry.ID != first.ID || !retry.RecordedAt.Equal(first.RecordedAt) {
				t.Fatalf("budget-filling replay refused: %v", err)
			}
			input.Payload = []byte(`{"changed":true}`)
			if _, err = s.RecordEvent(ctx, actor, input); !errors.Is(err, blackboard.ErrConflict) {
				t.Errorf("changed replay must conflict: %v", err)
			}
			input.ID = blackboard.NewID()
			if _, err = s.RecordEvent(ctx, actor, input); !errors.Is(err, blackboard.ErrInvalid) {
				t.Errorf("new event bypassed full budget: %v", err)
			}
		})
	}
}

func TestBlackboardRound6ObservationDoesNotQueue(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorID: "busy", ActorKind: "agent"}, func(*blackboard.Record) error {
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
		t.Fatal("writer failed to enter")
	}
	defer func() { close(release); <-done }()
	if err := store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionObservation, MilestoneID: mid}, func(*blackboard.Record) error { t.Error("observation passed a busy queue"); return nil }); !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("busy observation: %v", err)
	}
}

func TestBlackboardRound6TryOnlyDoesNotWaitForPool(t *testing.T) {
	base, s, _, mid := partitionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cfg := base.Pool.Config()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := db.BlackboardStore{Pool: pool}
	a, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	if err := store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionObservation, MilestoneID: mid}, func(*blackboard.Record) error { t.Error("observation found a full pool connection"); return nil }); !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("pool acquisition waited: %v", err)
	}
}
