package checklist

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeltaLongSession(t *testing.T) {
	p, _ := Apply(nil, Request{Op: "set", Items: []string{"First", strings.Repeat("x", 200), "Last"}})
	cur := p.Items
	for i := 0; i < 5000; i++ {
		op := "tick"
		if i%2 == 1 {
			op = "untick"
		}
		next, err := Apply(cur, Request{Op: op, Index: 1})
		if err != nil {
			t.Fatal(err)
		}
		delta := Compact(cur, next)
		raw, _ := json.Marshal(delta)
		if strings.Contains(string(raw), strings.Repeat("x", 200)) {
			t.Fatal("untouched item stored")
		}
		got := Replay(cur, delta)
		a, _ := json.Marshal(got.Items)
		b, _ := json.Marshal(next.Items)
		if string(a) != string(b) {
			t.Fatalf("replay %d: %s != %s", i, a, b)
		}
		cur = next.Items
	}
}

func TestDeltaWorkmapReplay(t *testing.T) {
	p, _ := Apply(nil, Request{Op: "set", Items: []string{"One", "Two", "Three"}})
	cur := p.Items
	worker := Assignee{Kind: "subagent", ID: "worker"}
	requests := []Request{{Op: "assign", Index: 1, Assignee: &worker}, {Op: "doing", Index: 2, Assignee: &worker}, {Op: "move", Index: 2, Position: 3}, {Op: "rename", Index: 3, Item: "Renamed"}, {Op: "remove", Index: 2}, {Op: "add", Item: "Added"}, {Op: "clear"}}
	for _, req := range requests {
		next, err := Apply(cur, req)
		if err != nil {
			t.Fatal(err)
		}
		got := Replay(cur, Compact(cur, next))
		a, _ := json.Marshal(got.Items)
		b, _ := json.Marshal(next.Items)
		if string(a) != string(b) {
			t.Fatalf("%s: %s != %s", req.Op, a, b)
		}
		cur = next.Items
	}
}
