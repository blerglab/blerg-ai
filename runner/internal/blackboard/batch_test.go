package blackboard

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type batchBoard struct{ calls int }

func (b *batchBoard) Call(_ context.Context, tool string, args map[string]any, out any) error {
	switch tool {
	case "blerg_column_list":
		*out.(*[]Column) = []Column{{ID: "work", Name: "In progress"}}
	case "blerg_card_search":
		b.calls++
		numbers, ok := args["numbers"].([]int)
		if !ok || len(numbers) > 100 {
			return ErrInvalid
		}
		for i := len(numbers) - 1; i >= 0; i-- {
			n := numbers[i]
			if n != 17 {
				out.(*CardPage).Cards = append(out.(*CardPage).Cards, boardCard(n, "work"))
			}
		}
	case "blerg_card_get":
		return ErrInvalid
	default:
		return ErrInvalid
	}
	return nil
}
func TestSliceBatches250Cards(t *testing.T) {
	s, _ := planFixture(t)
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error {
		for n := 1; n <= 250; n++ {
			r.Items = append(r.Items, Item{ID: ItemID(fmt.Sprint(n)), MilestoneID: "m", BoardID: "board", CardNumber: n, State: "committed"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b := &batchBoard{}
	s.Board = b
	v, err := s.Slice(t.Context(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if b.calls != 3 || len(v.Cards) != 250 || !v.Complete {
		t.Fatalf("calls=%d cards=%d complete=%v", b.calls, len(v.Cards), v.Complete)
	}
	for i, c := range v.Cards {
		if c.Card.Number != i+1 {
			t.Fatalf("order at %d: %d", i, c.Card.Number)
		}
	}
	if !v.Cards[16].Deleted || v.Cards[16].Stage != "moved_out" {
		t.Fatal("missing card lost tombstone")
	}
}

func TestSliceBatchFailureDoesNotInventDeletion(t *testing.T) {
	s, _ := planFixture(t)
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error {
		r.Items = []Item{{ID: "one", MilestoneID: "m", BoardID: "board", CardNumber: 1, State: "planned"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Board = boardFunc(func(context.Context, string, map[string]any, any) error { return ErrForbidden })
	v, err := s.Slice(t.Context(), "m")
	if !errors.Is(err, ErrForbidden) || v.Complete || len(v.Cards) != 0 {
		t.Fatalf("failure became missing: %+v %v", v, err)
	}
}
