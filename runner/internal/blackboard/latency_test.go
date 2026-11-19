package blackboard

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkSlice250Latency models an explicitly requested 100 ms board round
// trip. This is a wall-clock measurement, not a timing assertion in a unit test.
func BenchmarkSlice250Latency(b *testing.B) {
	store := NewMemoryStore()
	r := Record{ID: "b", Version: 1, Owner: Actor{ID: "owner", Kind: "human"}, Milestones: []Milestone{{ID: "m", BoardID: "board", Version: 1}}}
	board := &fakeBoard{cards: map[int]Card{}}
	for n := 1; n <= 250; n++ {
		r.Items = append(r.Items, Item{ID: ItemID(fmt.Sprint(n)), MilestoneID: "m", BoardID: "board", CardNumber: n, State: "planned"})
		board.cards[n] = boardCard(n, "work")
	}
	if err := store.Create(context.Background(), r); err != nil {
		b.Fatal(err)
	}
	calls := 0
	s := Service{Store: store, BlackboardID: "b", Board: boardFunc(func(ctx context.Context, tool string, args map[string]any, out any) error {
		calls++
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		return board.Call(ctx, tool, args, out)
	})}
	b.ResetTimer()
	for range b.N {
		v, err := s.Slice(context.Background(), "m")
		if err != nil || len(v.Cards) != 250 {
			b.Fatalf("cards=%d err=%v", len(v.Cards), err)
		}
	}
	b.ReportMetric(float64(calls)/float64(b.N), "board_calls/op")
}
