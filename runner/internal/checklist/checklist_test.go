package checklist

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func items(texts ...string) []Item {
	out := make([]Item, len(texts))
	for i, t := range texts {
		out[i] = Item{Text: t}
	}
	return out
}

func TestSetReplacesTheListAndCleansItems(t *testing.T) {
	p, err := Apply(items("old"), Request{Op: "set", Items: []string{" Write\ttests ", "Ship\u202e it", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.Items[0].Text != "Write tests" || p.Items[1].Text != "Ship it" || p.Items[0].Done {
		t.Fatalf("items = %+v", p.Items)
	}
	if p.Op != "set" || p.Changed != nil {
		t.Fatalf("payload = %+v", p)
	}
}

func TestSetRefusesTooManyAndCutsLongItems(t *testing.T) {
	many := make([]string, MaxItems+1)
	for i := range many {
		many[i] = "x"
	}
	if _, err := Apply(nil, Request{Op: "set", Items: many}); err == nil {
		t.Fatal("51 items accepted")
	}
	p, err := Apply(nil, Request{Op: "set", Items: []string{strings.Repeat("a", 300)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Items[0].Text)); n != MaxText {
		t.Fatalf("len = %d", n)
	}
	if _, err := Apply(nil, Request{Op: "set", Items: []string{"  "}}); err == nil {
		t.Fatal("an empty list from set accepted")
	}
}

func TestTickByIndexAndText(t *testing.T) {
	cur := items("a", "b", "2")
	p, err := Apply(cur, Request{Op: "tick", Index: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Items[1].Done || p.Changed == nil || p.Changed.Index != 2 || p.Changed.Text != "b" || !p.Changed.Done {
		t.Fatalf("payload = %+v changed=%+v", p, p.Changed)
	}
	if cur[1].Done {
		t.Fatal("Apply changed the list it was given")
	}
	p, err = Apply(cur, Request{Op: "tick", Text: "2"})
	if err != nil || !p.Items[2].Done || p.Changed.Index != 3 {
		t.Fatalf("by text: %+v %v", p, err)
	}
}

func TestTickPicksTheFirstNotDoneOfSeveralMatches(t *testing.T) {
	cur := []Item{{Text: "x", Done: true}, {Text: "x"}, {Text: "x"}}
	p, err := Apply(cur, Request{Op: "tick", Text: "x"})
	if err != nil || p.Changed.Index != 2 {
		t.Fatalf("tick: %+v %v", p.Changed, err)
	}
	p, err = Apply([]Item{{Text: "x"}, {Text: "x", Done: true}}, Request{Op: "untick", Text: "x"})
	if err != nil || p.Changed.Index != 2 || p.Items[1].Done {
		t.Fatalf("untick: %+v %v", p.Changed, err)
	}
	// Every match already ticked: the first one, no change.
	p, err = Apply([]Item{{Text: "x", Done: true}}, Request{Op: "tick", Text: "x"})
	if err != nil || p.Changed.Index != 1 || !p.Items[0].Done {
		t.Fatalf("again: %+v %v", p.Changed, err)
	}
}

func TestTickRefusals(t *testing.T) {
	cur := items("a")
	for name, req := range map[string]Request{
		"no such index": {Op: "tick", Index: 2},
		"no such text":  {Op: "tick", Text: "A"},
		"neither":       {Op: "untick"},
		"both":          {Op: "tick", Index: 1, Text: "a"},
		"bad op":        {Op: "toggle"},
	} {
		if _, err := Apply(cur, req); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.As(err, new(*Error)) {
			t.Errorf("%s: %T is not a refusal", name, err)
		}
	}
}

func TestAddAndClear(t *testing.T) {
	p, err := Apply(items("a"), Request{Op: "add", Item: " b "})
	if err != nil || len(p.Items) != 2 || p.Changed.Index != 2 || p.Changed.Text != "b" || p.Changed.Done {
		t.Fatalf("add: %+v %v", p, err)
	}
	full := make([]Item, MaxItems)
	if _, err := Apply(full, Request{Op: "add", Item: "c"}); err == nil {
		t.Fatal("a 51st item accepted")
	}
	if _, err := Apply(nil, Request{Op: "add", Item: " "}); err == nil {
		t.Fatal("an empty item accepted")
	}
	p, err = Apply(items("a"), Request{Op: "clear"})
	if err != nil || len(p.Items) != 0 || p.Items == nil {
		t.Fatalf("clear: %+v %v", p, err)
	}
}

func TestParse(t *testing.T) {
	if _, err := Parse([]byte(`{"op":"set","items":["a",2]}`)); err == nil || !strings.Contains(err.Error(), "items") {
		t.Fatalf("a number in items: %v", err)
	}
	if _, err := Parse([]byte(`{"op":"tick","index":"2"}`)); err == nil {
		t.Fatal("a string index accepted")
	}
	r, err := Parse([]byte(`{"op":"tick","index":3,"extra":{"deep":[1]}}`))
	if err != nil || r.Index != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("not json accepted")
	}
}

func TestDone(t *testing.T) {
	if n := Done([]Item{{Done: true}, {}, {Done: true}}); n != 2 {
		t.Fatal(n)
	}
}

func TestTickWithCommitsStoresThemAndReTickingAddsDeduped(t *testing.T) {
	p, err := Apply(items("a", "b"), Request{Op: "tick", Index: 1, Commits: []string{"ABCDEF1", "abcdef1", "1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Items[0].Commits; len(got) != 2 || got[0] != "abcdef1" || got[1] != "1234567890" {
		t.Fatalf("commits = %v", got)
	}
	if len(p.Items[1].Commits) != 0 || len(p.Changed.Commits) != 2 {
		t.Fatalf("payload = %+v changed = %+v", p, p.Changed)
	}
	p2, err := Apply(p.Items, Request{Op: "tick", Index: 1, Commits: []string{"abcdef1", "fedcba9"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p2.Items[0].Commits; len(got) != 3 || got[2] != "fedcba9" {
		t.Fatalf("re-tick commits = %v", got)
	}
}

func TestAddWithCommitsAndTheRefusals(t *testing.T) {
	p, err := Apply(nil, Request{Op: "add", Item: "x", Commits: []string{"abcdef1"}})
	if err != nil || len(p.Items[0].Commits) != 1 {
		t.Fatalf("add: %v %+v", err, p)
	}
	if _, err := Apply(items("a"), Request{Op: "tick", Index: 1, Commits: []string{"nothex!"}}); err == nil {
		t.Fatal("a bad sha accepted")
	}
	if _, err := Apply(items("a"), Request{Op: "untick", Index: 1, Commits: []string{"abcdef1"}}); err == nil {
		t.Fatal("commits on untick accepted")
	}
	many := make([]string, MaxCommits+1)
	for i := range many {
		many[i] = fmt.Sprintf("%07x", i+1)
	}
	if _, err := Apply(items("a"), Request{Op: "tick", Index: 1, Commits: many}); err == nil {
		t.Fatal("too many commits accepted")
	}
	cur := items("a")
	cur[0].Commits = many[:MaxCommits]
	if _, err := Apply(cur, Request{Op: "tick", Index: 1, Commits: []string{"fffffff"}}); err == nil {
		t.Fatal("more than the cap on one item accepted")
	}
}

func TestReadKeepsCommitsAndOldPayloadsHaveNone(t *testing.T) {
	p, ok := Read([]byte(`{"items":[{"text":"a","done":true,"commits":["abcdef1"]},{"text":"b","done":false}],"op":"tick"}`))
	if !ok || len(p.Items[0].Commits) != 1 || p.Items[1].Commits != nil {
		t.Fatalf("%v %+v", ok, p)
	}
}

func TestADiffGoesWithCommitsAndTheLatestOneIsKept(t *testing.T) {
	d1, d2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	p, err := Apply(items("a"), Request{Op: "tick", Index: 1, Commits: []string{"abcdef1"}, Diff: d1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Diff != d1 || p.Changed.Diff != d1 {
		t.Fatalf("diff not stored: %+v %+v", p.Items[0], p.Changed)
	}
	// A later tick without a diff keeps the one the item has; with one, the newer replaces it.
	p2, err := Apply(p.Items, Request{Op: "tick", Index: 1, Commits: []string{"fedcba9"}})
	if err != nil || p2.Items[0].Diff != d1 {
		t.Fatalf("diff lost: %v %+v", err, p2.Items[0])
	}
	p3, err := Apply(p2.Items, Request{Op: "tick", Index: 1, Commits: []string{"1234567"}, Diff: d2})
	if err != nil || p3.Items[0].Diff != d2 {
		t.Fatalf("diff not replaced: %v %+v", err, p3.Items[0])
	}
	a, err := Apply(nil, Request{Op: "add", Item: "x", Commits: []string{"abcdef1"}, Diff: d1})
	if err != nil || a.Items[0].Diff != d1 {
		t.Fatalf("add: %v %+v", err, a)
	}
	for _, bad := range []Request{
		{Op: "tick", Index: 1, Diff: d1},                                   // a diff without commits
		{Op: "tick", Index: 1, Commits: []string{"abcdef1"}, Diff: "../x"}, // not an artifact id
		{Op: "tick", Index: 1, Commits: []string{"abcdef1"}, Diff: strings.ToUpper(d1)},
		{Op: "set", Items: []string{"a"}, Diff: d1},
	} {
		if _, err := Apply(items("a"), bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestReadDropsAMalformedDiff(t *testing.T) {
	raw := `{"items":[{"text":"a","done":true,"commits":["abcdef1"],"diff":"javascript:x"},` +
		`{"text":"b","done":true,"commits":["abcdef1"],"diff":"` + strings.Repeat("c", 32) + `"}],"op":"tick",` +
		`"changed":{"index":1,"text":"a","done":true,"diff":"nope"}}`
	p, ok := Read([]byte(raw))
	if !ok || p.Items[0].Diff != "" || p.Items[1].Diff != strings.Repeat("c", 32) || p.Changed.Diff != "" {
		t.Fatalf("read = %+v changed %+v", p, p.Changed)
	}
}
