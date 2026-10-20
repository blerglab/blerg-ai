package checklist

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func workmapRequest(t *testing.T, raw string) Request {
	t.Helper()
	r, e := Parse([]byte(raw))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func workmapItems(t *testing.T, p Payload) []map[string]any {
	t.Helper()
	raw, e := json.Marshal(p.Items)
	if e != nil {
		t.Fatal(e)
	}
	var items []map[string]any
	if e = json.Unmarshal(raw, &items); e != nil {
		t.Fatal(e)
	}
	return items
}
func workmapApply(t *testing.T, cur []Item, raw string) Payload {
	t.Helper()
	p, e := Apply(cur, workmapRequest(t, raw))
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func workmapTarget(op, id string) string {
	raw, _ := json.Marshal(map[string]any{"op": op, "item_id": id})
	return string(raw)
}

func TestWorkmapStableIdentityDoesNotGuessDuplicateText(t *testing.T) {
	p := workmapApply(t, nil, `{"op":"set","items":["Same","Same","Ship"]}`)
	items := workmapItems(t, p)
	first, _ := items[0]["id"].(string)
	second, _ := items[1]["id"].(string)
	if first == "" || second == "" || first == second {
		t.Fatal("new checklist items need distinct stable identities, including duplicate text")
	}
	p = workmapApply(t, p.Items, workmapTarget("doing", second))
	items = workmapItems(t, p)
	if items[0]["in_progress"] == true || items[1]["in_progress"] != true {
		t.Fatal("opaque selector marked wrong duplicate item")
	}
	p = workmapApply(t, p.Items, `{"op":"move","item_id":"`+second+`","position":1}`)
	items = workmapItems(t, p)
	if items[0]["id"] != second || items[0]["in_progress"] != true || items[1]["id"] != first {
		t.Fatal("reorder changed checklist identity or state")
	}
	p = workmapApply(t, p.Items, `{"op":"rename","item_id":"`+second+`","item":"Renamed"}`)
	items = workmapItems(t, p)
	if items[0]["id"] != second || items[0]["text"] != "Renamed" || items[0]["in_progress"] != true {
		t.Fatal("rename replaced the original work item")
	}
	p = workmapApply(t, p.Items, workmapTarget("remove", second))
	if _, e := Apply(p.Items, workmapRequest(t, workmapTarget("doing", second))); e == nil {
		t.Fatal("removed identity rebound to another item")
	}
}
func TestWorkmapDoingAllowsSeveralAndTickClearsOnlyItsItem(t *testing.T) {
	p := workmapApply(t, nil, `{"op":"set","items":["One","Two"]}`)
	p = workmapApply(t, p.Items, `{"op":"doing","index":1}`)
	p = workmapApply(t, p.Items, `{"op":"doing","index":2}`)
	items := workmapItems(t, p)
	if items[0]["in_progress"] != true || items[1]["in_progress"] != true {
		t.Fatal("doing was incorrectly exclusive")
	}
	p = workmapApply(t, p.Items, `{"op":"tick","index":1,"commits":["abcdef1"]}`)
	items = workmapItems(t, p)
	if items[0]["in_progress"] == true || items[0]["done"] != true || items[1]["in_progress"] != true {
		t.Fatal("completion did not clear exactly the completed doing state")
	}
	p = workmapApply(t, p.Items, `{"op":"pending","index":1}`)
	items = workmapItems(t, p)
	if items[0]["done"] == true || items[0]["in_progress"] == true || len(p.Items[0].Commits) != 1 {
		t.Fatal("returning to pending lost previous completion evidence")
	}
}
func TestWorkmapExplicitAssignmentMovesAtomicallyAndRetainsItemOnRename(t *testing.T) {
	p := workmapApply(t, nil, `{"op":"set","items":["One","Two"]}`)
	p = workmapApply(t, p.Items, `{"op":"assign","index":1,"assignee":{"kind":"subagent","id":"native-owned"}}`)
	p = workmapApply(t, p.Items, `{"op":"assign","index":1,"assignee":{"kind":"child_session","id":"child-owned"}}`)
	p = workmapApply(t, p.Items, `{"op":"assign","index":2,"assignee":{"kind":"subagent","id":"native-owned"}}`)
	items := workmapItems(t, p)
	a, _ := items[0]["assignees"].([]any)
	b, _ := items[1]["assignees"].([]any)
	if len(a) != 1 || len(b) != 1 || a[0].(map[string]any)["kind"] != "child_session" || b[0].(map[string]any)["id"] != "native-owned" {
		t.Fatal("one worker was assigned to multiple current items or another worker was removed")
	}
	p = workmapApply(t, p.Items, `{"op":"tick","index":2}`)
	if got, _ := workmapItems(t, p)[1]["assignees"].([]any); len(got) != 1 {
		t.Fatal("completion erased explicit assignment history")
	}
}
func TestWorkmapLegacyReadCreatesStableNonCollidingIdentities(t *testing.T) {
	raw := []byte(`{"op":"set","items":[{"text":"Same","done":false},{"text":"Same","done":true}]}`)
	a, ok := Read(raw)
	if !ok {
		t.Fatal("legacy checklist rejected")
	}
	b, ok := Read(raw)
	if !ok {
		t.Fatal("legacy checklist rejected on replay")
	}
	ai, bi := workmapItems(t, a), workmapItems(t, b)
	first, _ := ai[0]["id"].(string)
	second, _ := ai[1]["id"].(string)
	if first == "" || second == "" || first == second || ai[0]["id"] != bi[0]["id"] || ai[1]["id"] != bi[1]["id"] {
		t.Fatal("legacy replay did not retain stable distinct item identities")
	}
	p := workmapApply(t, a.Items, `{"op":"doing","index":1}`)
	if workmapItems(t, p)[0]["id"] != first {
		t.Fatal("legacy first mutation replaced displayed item identity")
	}
}

func TestWorkmapLegacyMalformedSeedsAndDuplicateIDsNeverCollide(t *testing.T) {
	for _, seed := range []string{"", strings.Repeat("a", 49), "not/a/valid seed", "x\x00y"} {
		other := seed + "tail"
		a := Normalize([]Item{{Text: "Same"}, {Text: "Same"}}, seed)
		b := Normalize([]Item{{Text: "Same"}}, other)
		if a[0].ID == a[1].ID || a[0].ID == b[0].ID || !itemIDRe.MatchString(a[0].ID) {
			t.Fatal("whole malformed source seed must produce distinct bounded IDs")
		}
		if a[0].ID != Normalize([]Item{{Text: "Same"}}, seed)[0].ID {
			t.Fatal("legacy normalization changed on replay")
		}
	}
	items := Normalize([]Item{{ID: "valid", Text: "A"}, {ID: "valid", Text: "B"}, {ID: strings.Repeat("x", 97), Text: "C"}}, "event")
	if items[0].ID != "valid" || items[1].ID == "valid" || items[2].ID == items[1].ID {
		t.Fatal("duplicate or oversized IDs survived normalization")
	}
}

func TestWorkmapBoundsAndFailedChangesDoNotMutateInput(t *testing.T) {
	cur := Normalize([]Item{{Text: "A"}, {Text: "B"}}, "event")
	for i := range MaxAssignees {
		cur[0].Assignees = append(cur[0].Assignees, Assignee{Kind: "subagent", ID: fmt.Sprintf("worker-%d", i)})
	}
	before, _ := json.Marshal(cur)
	_, err := Apply(cur, Request{Op: "assign", Index: 1, Assignee: &Assignee{Kind: "subagent", ID: "overflow"}})
	after, _ := json.Marshal(cur)
	if err == nil || string(before) != string(after) {
		t.Fatal("overflow assignment accepted or mutated the original state")
	}
	p := workmapApply(t, cur, `{"op":"unassign","index":2,"assignee":{"kind":"subagent","id":"worker-0"}}`)
	if len(p.Items[0].Assignees) != MaxAssignees {
		t.Fatal("unassigning an unrelated item erased another item's worker")
	}
	many := Normalize(make([]Item, 5), "many")
	for i := range many {
		for j := range MaxAssignees {
			many[i].Assignees = append(many[i].Assignees, Assignee{Kind: "subagent", ID: fmt.Sprintf("worker-%d-%d", i, j)})
		}
	}
	if _, err = Apply(many, Request{Op: "doing", Index: 1}); err == nil {
		t.Fatal("global worker limit not enforced")
	}
	for _, id := range []string{"", " x", "x ", "x\n", strings.Repeat("x", 257)} {
		if ValidAssignee(Assignee{Kind: "subagent", ID: id}) {
			t.Fatal("invalid worker ID accepted")
		}
	}
	if _, err = Apply(cur, Request{Op: "set", Items: []string{"New"}, ItemID: cur[0].ID}); err == nil {
		t.Fatal("replacement silently ignored an existing-item selector")
	}
}
