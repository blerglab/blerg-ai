package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func checklistMapFixture(t *testing.T) *delegFx {
	t.Helper()
	fx := newDelegFx(t)
	fx.api.RegisterWorklistRoutes(fx.mux)
	fx.api.RegisterLiveRoutes(fx.mux)
	return fx
}
func checklistMapPost(fx *delegFx, sid, token string, body any) (int, map[string]any) {
	code, out, _ := fx.do(http.MethodPost, "/api/sessions/"+sid+"/checklist", token, body, nil)
	return code, out
}
func checklistMapEvent(t *testing.T, fx *delegFx, sid, kind string, payload any) string {
	t.Helper()
	raw, _ := json.Marshal(payload)
	id := newUUID()
	if _, _, e := db.AppendAgentEventAt(t.Context(), fx.pool, sid, id, kind, string(raw), time.Time{}); e != nil {
		t.Fatal(e)
	}
	return id
}
func checklistMapItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("missing checklist items: %v", body)
	}
	return items
}
func TestChecklistWorkmapRejectsForeignWorkersAndCanonicalizesAlias(t *testing.T) {
	fx := checklistMapFixture(t)
	code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"One", "Two"}})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	ownChild, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	foreignParent, _ := fx.session(sessOpts{owner: "owner-2"})
	foreignChild, _ := fx.session(sessOpts{owner: "owner-2", parent: foreignParent})
	wrongOwner, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]any{"child_id": "native-owned", "call_id": "native-alias"})
	checklistMapEvent(t, fx, foreignParent, "subagent_started", map[string]any{"child_id": "native-foreign"})
	for _, tc := range []struct {
		kind, id string
		valid    bool
	}{{"subagent", "native-owned", true}, {"subagent", "native-alias", true}, {"child_session", ownChild, true}, {"subagent", "native-foreign", false}, {"subagent", "unknown", false}, {"child_session", foreignChild, false}, {"child_session", wrongOwner, false}, {"child_session", fx.parent, false}} {
		code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": tc.kind, "id": tc.id}})
		if tc.valid {
			if code != http.StatusCreated {
				t.Fatalf("valid %s assignment refused: %d %v", tc.kind, code, out)
			}
		} else if code != http.StatusBadRequest {
			t.Fatalf("foreign/unknown worker accepted: %d", code)
		}
	}
	code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 2, "assignee": map[string]string{"kind": "subagent", "id": "native-alias"}})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	items := checklistMapItems(t, out)
	assignments, _ := items[1].(map[string]any)["assignees"].([]any)
	if len(assignments) != 1 || assignments[0].(map[string]any)["id"] != "native-owned" {
		t.Fatal("native call alias was not canonicalized")
	}
	code, _, _ = fx.do(http.MethodPost, "/api/sessions/"+foreignParent+"/checklist", fx.parentTok, map[string]any{"op": "doing", "index": 1}, nil)
	if code != http.StatusForbidden {
		t.Fatal("current session token crossed session scope")
	}
}
func TestChecklistWorkmapLegacyLiveAndMutationShareCanonicalIdentity(t *testing.T) {
	fx := checklistMapFixture(t)
	eventID := checklistMapEvent(t, fx, fx.parent, "checklist", map[string]any{"op": "set", "items": []map[string]any{{"text": "Same", "done": false}, {"text": "Same", "done": false}}})
	code, live, _ := fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, live)
	}
	current, _ := live["checklist"].(map[string]any)
	items := checklistMapItems(t, current)
	first, _ := items[0].(map[string]any)["id"].(string)
	second, _ := items[1].(map[string]any)["id"].(string)
	if first != "legacy-"+eventID+"-1" || second != "legacy-"+eventID+"-2" {
		t.Fatal("live state did not seed legacy identity from its source event")
	}
	code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "doing", "item_id": second})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	changed := checklistMapItems(t, out)
	if changed[0].(map[string]any)["id"] != first || changed[1].(map[string]any)["id"] != second || changed[1].(map[string]any)["in_progress"] != true {
		t.Fatal("legacy mutation replaced or mistargeted item")
	}
	latest, e := db.LatestChecklist(context.Background(), fx.pool, fx.parent, 1<<62)
	if e != nil || latest == nil {
		t.Fatal(e)
	}
	if !strings.Contains(latest.Payload, second) {
		t.Fatal("full durable event omitted canonical identity")
	}
}
func TestChecklistWorkmapConcurrentAssignmentHasOneCurrentItem(t *testing.T) {
	fx := checklistMapFixture(t)
	checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"One", "Two"}})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "one-worker"})
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for i := range 12 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			code, _ := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "doing", "index": 1 + index%2, "assignee": map[string]string{"kind": "subagent", "id": "one-worker"}})
			codes <- code
		}(i)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusCreated {
			t.Fatalf("concurrent write failed: %d", code)
		}
	}
	latest, e := db.LatestChecklist(t.Context(), fx.pool, fx.parent, 1<<62)
	if e != nil || latest == nil {
		t.Fatal(e)
	}
	var saved map[string]any
	if e = json.Unmarshal([]byte(latest.Payload), &saved); e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, raw := range checklistMapItems(t, saved) {
		a, _ := raw.(map[string]any)["assignees"].([]any)
		n += len(a)
	}
	if n != 1 {
		t.Fatalf("one worker ended with %d current assignments", n)
	}
}

func TestChecklistWorkmapNativeAliasEvolutionAndAmbiguity(t *testing.T) {
	fx := checklistMapFixture(t)
	code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"Work"}})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"call_id": "early-call"})
	code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "early-call"}})
	if code != http.StatusCreated {
		t.Fatal("call-only legacy worker refused", code, out)
	}
	checklistMapEvent(t, fx, fx.parent, "subagent_update", map[string]string{"call_id": "early-call", "child_id": "canonical-child"})
	code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "early-call"}})
	if code != http.StatusCreated {
		t.Fatal("new canonical child ID was not preferred to earlier call-only event", code, out)
	}
	assignments, _ := checklistMapItems(t, out)[0].(map[string]any)["assignees"].([]any)
	if len(assignments) != 1 || assignments[0].(map[string]any)["id"] != "canonical-child" {
		t.Fatal("alias evolution left duplicate references")
	}
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"call_id": "ambiguous", "child_id": "child-A"})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"call_id": "ambiguous", "child_id": "child-B"})
	code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "ambiguous"}})
	if code != http.StatusBadRequest {
		t.Fatal("ambiguous call ID guessed a worker", code)
	}
	body, _ := json.Marshal(out)
	if strings.Contains(string(body), "child-A") || strings.Contains(string(body), "child-B") {
		t.Fatal("refusal exposed worker identities")
	}
}

func TestChecklistWorkmapLiveResolvesNewChildIdentityWithoutChecklistMutation(t *testing.T) {
	fx := checklistMapFixture(t)
	checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"Work"}})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"call_id": "original-call"})
	code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "original-call"}})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	checklistMapEvent(t, fx, fx.parent, "subagent_update", map[string]string{"call_id": "original-call", "child_id": "permanent-child"})
	code, out, _ = fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	current, _ := out["checklist"].(map[string]any)
	workers, _ := checklistMapItems(t, current)[0].(map[string]any)["assignees"].([]any)
	if len(workers) != 1 || workers[0].(map[string]any)["id"] != "permanent-child" {
		t.Fatal("live association disappeared or retained obsolete call identity", workers)
	}
	latest, e := db.LatestChecklist(t.Context(), fx.pool, fx.parent, 1<<62)
	if e != nil || latest == nil {
		t.Fatal(e)
	}
	if !strings.Contains(latest.Payload, "original-call") || strings.Contains(latest.Payload, "permanent-child") {
		t.Fatal("live normalization rewrote immutable history")
	}
	// A second child uses the previous call identity as its canonical ID. It is
	// now ambiguous; retaining that raw reference would retarget it incorrectly.
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "original-call", "call_id": "different-call"})
	code, out, _ = fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	current, _ = out["checklist"].(map[string]any)
	workers, _ = checklistMapItems(t, current)[0].(map[string]any)["assignees"].([]any)
	if len(workers) != 0 {
		t.Fatal("ambiguous historical call reference retargeted to another native child")
	}
}

func TestChecklistWorkmapCallOnlyReferenceCannotBecomeUnrelatedCanonicalChild(t *testing.T) {
	fx := checklistMapFixture(t)
	checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"Work"}})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"call_id": "first-call"})
	code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "first-call"}})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	// There is deliberately NO permanent identity event for the first worker.
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "first-call", "call_id": "other-worker-call"})
	code, out, _ = fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	current, _ := out["checklist"].(map[string]any)
	workers, _ := checklistMapItems(t, current)[0].(map[string]any)["assignees"].([]any)
	if len(workers) != 0 {
		t.Fatal("call-only assignment retargeted to unrelated canonical child")
	}
	code, out = checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "doing", "index": 1})
	if code != http.StatusCreated {
		t.Fatal(code, out)
	}
	workers, _ = checklistMapItems(t, out)[0].(map[string]any)["assignees"].([]any)
	if len(workers) != 0 {
		t.Fatal("next mutation persisted ambiguous assignment")
	}
}

func TestChecklistWorkersDiscoveryIsScopedAndKeepsActiveWorkersFirst(t *testing.T) {
	fx := checklistMapFixture(t)
	ownChild, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	foreignParent, _ := fx.session(sessOpts{owner: "owner-2"})
	foreignChild, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent})
	checklistMapEvent(t, fx, foreignParent, "subagent_started", map[string]string{"child_id": "foreign-native", "summary": "Hidden"})
	for i := range 101 {
		id := fmt.Sprintf("old-%03d", i)
		checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": id, "summary": "Old"})
		checklistMapEvent(t, fx, fx.parent, "subagent_done", map[string]string{"child_id": id})
	}
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "display-id", "call_id": "call-alias", "summary": "Current worker"})
	path := "/api/sessions/" + fx.parent + "/checklist/workers"
	code, out, _ := fx.do(http.MethodGet, path, fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	workers, ok := out["workers"].([]any)
	if !ok || len(workers) != 100 || out["truncated"] != true {
		t.Fatal("missing bounded discovery list", out)
	}
	found := map[string]bool{}
	for _, raw := range workers {
		worker := raw.(map[string]any)
		id := worker["id"].(string)
		found[id] = true
		if id == "display-id" && (worker["title"] != "Current worker" || worker["status"] != "running") {
			t.Fatal("incorrect current native metadata", worker)
		}
	}
	if !found["display-id"] || !found[ownChild] || found["foreign-native"] || found[foreignChild] {
		t.Fatal("active worker missing or foreign identity exposed")
	}
	for _, tc := range []struct {
		path, token string
		want        int
	}{{path, fx.human("owner-1"), 401}, {"/api/sessions/" + foreignParent + "/checklist/workers", fx.parentTok, 403}} {
		code, _, _ = fx.do(http.MethodGet, tc.path, tc.token, nil, nil)
		if code != tc.want {
			t.Fatal("worker discovery crossed credential scope", code)
		}
	}
}

func TestChecklistWorkersUsesActualSessionStatusAndSafeTitles(t *testing.T) {
	fx := checklistMapFixture(t)
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "display-current", "summary": "Title\x1b\n" + strings.Repeat("a", 300)})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/checklist/workers", fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	workers, _ := out["workers"].([]any)
	if len(workers) != 1 {
		t.Fatal("missing recorded worker", workers)
	}
	worker := workers[0].(map[string]any)
	if worker["status"] == "running" || worker["status"] == "background" {
		t.Fatal("idle foreground worker falsely reported active")
	}
	title, _ := worker["title"].(string)
	if utf8.RuneCountInString(title) > 200 || strings.ContainsFunc(title, unicode.IsControl) {
		t.Fatal("unsafe or unbounded title")
	}
}

// The live checklist names its assignees (a subagent by its description, a started session by its
// title) so the panel never has to show an id.
func TestChecklistLiveNamesAssignees(t *testing.T) {
	fx := checklistMapFixture(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET title='Port the parser' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"One", "Two"}})
	checklistMapEvent(t, fx, fx.parent, "subagent_started", map[string]string{"child_id": "toolu_01TJqzn5hgEU53pBc", "summary": "Audit the\x1b plan tab"})
	checklistMapEvent(t, fx, fx.parent, "subagent_done", map[string]string{"child_id": "toolu_01TJqzn5hgEU53pBc"})
	for _, a := range []map[string]any{
		{"op": "assign", "index": 1, "assignee": map[string]string{"kind": "subagent", "id": "toolu_01TJqzn5hgEU53pBc"}},
		{"op": "assign", "index": 2, "assignee": map[string]string{"kind": "child_session", "id": child}},
	} {
		if code, out := checklistMapPost(fx, fx.parent, fx.parentTok, a); code != http.StatusCreated {
			t.Fatal(code, out)
		}
	}
	code, out, _ := fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	current, _ := out["checklist"].(map[string]any)
	titles, _ := current["worker_titles"].(map[string]any)
	if titles["subagent:toolu_01TJqzn5hgEU53pBc"] != "Audit the plan tab" || titles["child_session:"+child] != "Port the parser" {
		t.Fatal("assignees not named", titles)
	}
}

// A started session is named only to a viewer who may see it: a private child's title reaches its
// owner, never another viewer of the shared parent; an archived child is named to no one.
func TestChecklistLiveNamesChildrenPerViewer(t *testing.T) {
	fx := checklistMapFixture(t)
	private, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true, title: "Investigate the customer incident"})
	archived, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Archived worker"})
	shared, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Shared worker"})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, archived); err != nil {
		t.Fatal(err)
	}
	checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "set", "items": []string{"One", "Two", "Three"}})
	for i, child := range []string{private, archived, shared} {
		if code, out := checklistMapPost(fx, fx.parent, fx.parentTok, map[string]any{"op": "assign", "index": i + 1, "assignee": map[string]string{"kind": "child_session", "id": child}}); code != http.StatusCreated {
			t.Fatal(code, out)
		}
	}
	titles := func(viewer string) map[string]any {
		code, out, _ := fx.do(http.MethodGet, "/api/sessions/"+fx.parent+"/live", fx.human(viewer), nil, nil)
		if code != http.StatusOK {
			t.Fatal(viewer, code, out)
		}
		current, _ := out["checklist"].(map[string]any)
		got, _ := current["worker_titles"].(map[string]any)
		return got
	}
	owner := titles("owner-1")
	if owner["child_session:"+private] != "Investigate the customer incident" || owner["child_session:"+shared] != "Shared worker" {
		t.Fatal("owner does not see the names of its children", owner)
	}
	other := titles("owner-2")
	if other["child_session:"+shared] != "Shared worker" {
		t.Fatal("another viewer lost the shared child's name", other)
	}
	if _, ok := other["child_session:"+private]; ok {
		t.Fatal("a private child's title leaked to another viewer", other)
	}
	for _, got := range []map[string]any{owner, other} {
		if _, ok := got["child_session:"+archived]; ok {
			t.Fatal("an archived child is named", got)
		}
	}
}
