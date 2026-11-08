package blackboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRound2ObservationFencesAndDedupe(t *testing.T) {
	for _, race := range []string{"frozen", "finalizing", "revision", "none"} {
		t.Run(race, func(t *testing.T) {
			ctx := context.Background()
			s, _ := planFixture(t)
			if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil }); err != nil {
				t.Fatal(err)
			}
			b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
			changed := false
			s.Board = boardFunc(func(c context.Context, tool string, a map[string]any, o any) error {
				if tool == "blerg_card_search" && !changed {
					changed = true
					if err := s.Store.Update(ctx, "b", func(r *Record) error {
						switch race {
						case "frozen":
							now := time.Now()
							r.Milestones[0].FrozenAt = &now
						case "finalizing":
							r.Milestones[0].Stage = "Finalizing"
						case "revision":
							r.Milestones[0].Revision++
						}
						return nil
					}); err != nil {
						return err
					}
				}
				return b.Call(c, tool, a, o)
			})
			v, err := s.Slice(ctx, "m")
			if err != nil || !v.Complete {
				t.Fatalf("read: %+v %v", v, err)
			}
			if race == "none" {
				if _, err = s.Slice(ctx, "m"); err != nil {
					t.Fatal(err)
				}
			}
			r, _ := s.Store.Load(ctx, "b")
			want := 0
			if race == "none" {
				want = 1
			}
			if len(r.Events) != want {
				t.Fatalf("observation fence/dedupe %s: %d", race, len(r.Events))
			}
		})
	}
}

func TestRound2SnapshotRepairCannotRewriteIdentity(t *testing.T) {
	for _, field := range []string{"complete", "already_complete", "milestone", "day", "time"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			s, _ := planFixture(t)
			now := time.Now().UTC()
			if err := s.Store.Update(ctx, "b", func(r *Record) error {
				r.Snapshots = []Snapshot{{MilestoneID: "m", Day: "2026-01-01", ObservedAt: now}}
				r.Snapshots[0].Complete = field == "already_complete"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err := s.Store.Update(ctx, "b", func(r *Record) error {
				v := &r.Snapshots[0]
				v.Complete = true
				v.Counts = map[string]int{"done": 1}
				switch field {
				case "complete":
					v.Complete = false
				case "milestone":
					v.MilestoneID = "other"
				case "day":
					v.Day = "2026-01-02"
				case "time":
					v.ObservedAt = now.Add(-time.Second)
				}
				return nil
			})
			if !errors.Is(err, ErrImmutable) {
				t.Fatalf("snapshot %s: %v", field, err)
			}
		})
	}
}

func TestRound2ObservationBudget(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		r.Milestones[0].ReleaseID = "release"
		r.Events = make([]Event, MaxAutomaticEvents)
		for i := range r.Events {
			r.Events[i].Source = "board"
			r.Events[i].MilestoneID = "m"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
	v, err := s.Slice(ctx, "m")
	if err != nil || !v.Complete {
		t.Fatalf("budget read %+v %v", v, err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Events) != MaxAutomaticEvents {
		t.Fatal("observations consumed lifecycle reserve")
	}
}
