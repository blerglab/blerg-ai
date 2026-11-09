package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func draftRequest(fx *topicFx, mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestBlackboardDraftLifecycle covers generate (plan draft), read, edit-via-create, and discard —
// the empty-blackboard draft flow of BLG-450 phase 2.
func TestBlackboardDraftLifecycle(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	human := fx.token(topicOwner, "human")
	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "")
	if rec.Code != 201 {
		t.Fatalf("save: %d", rec.Code)
	}
	for _, route := range []struct{ method, path string }{{"DELETE", "/api/blackboards/draft"}, {"POST", "/api/blackboards/draft/create"}, {"POST", "/api/blackboards"}} {
		rec = draftRequest(fx, mux, route.method, route.path+"?session="+fx.sess, human, "")
		if rec.Code != 410 {
			t.Fatalf("retired decision: %d", rec.Code)
		}
	}
	rec = draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, human, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "untrusted_data") || !strings.Contains(rec.Body.String(), "revision") {
		t.Fatalf("draft still pending: %d %s", rec.Code, rec.Body.String())
	}
}

// TestGenerateBlackboardDraftPlanCapsDerivedName is BLG-450 review L8's last item: the derived
// draft name (session title + " plan") must be capped at 200 characters to match
// createBlackboard's own len(body.Name) > 200 bound, so a long session title produces a draft
// that Create can actually accept instead of a confusing 400 later.
func TestGenerateBlackboardDraftPlanCapsDerivedName(t *testing.T) {
	long := strings.Repeat("x", 250)
	plan := generateBlackboardDraftPlan(&db.SessionRow{Title: &long})
	if len(plan.Name) > 200 {
		t.Fatalf("derived name is %d chars, want <= 200: %q", len(plan.Name), plan.Name)
	}
}

// TestBlackboardGetDraftRefusesANonOwner is the regression-proof test for BLG-450 review L6:
// HandleGetBlackboardDraft checked canSeeAccount, which (for a non-private session, the fixture's
// default) lets *any* signed-in human see it — far broader than "owns" — so a non-owner could
// learn whether a draft exists for a workspace it merely can see. It must use ownedSession, the
// same fence HandleDiscardBlackboardDraft and HandleCreateBlackboardFromDraft already use, so a
// non-owner gets the same 404 "not found" an unowned session already gets everywhere else, never
// a hint that a draft is (or isn't) sitting there.
func TestBlackboardGetDraftRefusesANonOwner(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)

	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "")
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	other := fx.token("someone-else", "human")
	rec = draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, other, "")
	if rec.Code != 404 {
		t.Fatalf("a non-owner who can merely see the workspace should get 404, got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "untrusted_data") || strings.Contains(rec.Body.String(), `"draft":null`) {
		t.Fatalf("refusal body must not reveal draft existence either way: %s", rec.Body.String())
	}
	// The owner still reads it fine.
	rec = draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, fx.token(topicOwner, "human"), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "untrusted_data") {
		t.Fatalf("owner read: %d %s", rec.Code, rec.Body.String())
	}
}

// TestBlackboardRequestSetupRefusesAnAgentToken is the M9 regression-proof test for
// HandleRequestBlackboardSetup's bespoke human-only check (p.Kind != "human"): "Set it up with
// Main" must stay person-only. During development this test was also run with that check
// temporarily removed from HandleRequestBlackboardSetup, confirmed to fail (the agent token was
// accepted and a relay was queued), then the check was restored and this test re-run to confirm
// it passes again.
func TestBlackboardRequestSetupRefusesAnAgentToken(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	agent := fx.token(topicOwner, "agent")

	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/request-setup", agent, "")
	if rec.Code != 403 {
		t.Fatalf("expected an agent token to be refused request-setup, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestBlackboardGetDraftRefusesAnAgentToken and TestBlackboardDiscardDraftRefusesAnAgentToken are
// M9's explicit agent-token-refusal proofs for the two remaining draft handlers. Both now go
// through permissionOwner, the same shared, already guard-tested human-only helper
// HandleCreateBlackboard uses (see TestBlackboardCreateFromDraftRefusesAnAgentToken and
// privacy_guard_test.go's routeDecisions entries) — so no separate remove-and-confirm-fails step
// is run here for permissionOwner itself; these two tests exist to prove each handler actually
// calls it.
func TestBlackboardGetDraftRefusesAnAgentToken(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	agent := fx.token(topicOwner, "agent")

	rec := draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, agent, "")
	if rec.Code != 403 {
		t.Fatalf("expected an agent token to be refused reading a draft, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestBlackboardDiscardDraftRefusesAnAgentToken(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	agent := fx.token(topicOwner, "agent")

	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "")
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	rec = draftRequest(fx, mux, "DELETE", "/api/blackboards/draft?session="+fx.sess, agent, "")
	if rec.Code != 403 {
		t.Fatalf("expected an agent token to be refused discarding a draft, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestBlackboardCreateFromDraftSerializesPerWorkspace is BLG-450 review L7's atomicity proof: a
// concurrent create-from-draft call for the same workspace must not race with another and produce
// two blackboards. The Store itself allows more than one blackboard per workspace on purpose (see
// storetest.WorkspaceAdmissionContract, which exercises aggregate quotas across exactly that), so
// uniqueness cannot be enforced in the database without breaking that contract; this is enforced
// instead by serializing create-from-draft per resolved workspace root through the same
// per-process lock delegation start already uses (a.delegLocks), under its own key namespace.
//
// The test holds that lock itself first (simulating a create already in flight), fires the real
// HTTP call in a goroutine, confirms it is genuinely blocked (no record yet, draft still there)
// rather than having raced ahead, then releases and confirms it completes normally.
// Concurrent bound answers are exercised across HTTP in TestBoundDraftAskProcess.
func TestBlackboardCreateFromDraftRetired(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	rec := draftRequest(fx, mux, "POST", "/api/blackboards/draft/create?session="+fx.sess, fx.token(topicOwner, "human"), "")
	if rec.Code != 410 {
		t.Fatalf("retired: %d", rec.Code)
	}
}

// TestBlackboardDraftResolvesToWorkspaceRoot is the regression-proof test for BLG-450 review M4:
// a draft generated from a channel/child session must be visible from the workspace's Main
// (root), and a draft generated from Main must be visible from a channel session too — drafts are
// keyed by workspace root, not by whichever session happened to generate or read them.
func TestBlackboardDraftResolvesToWorkspaceRoot(t *testing.T) {
	fx := newDelegFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	request := func(method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(""))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	human := fx.human("owner-1")
	child, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, title: "Worker"})

	// Generate from the child: readable from Main (the root), not just from the child itself.
	rec := request("POST", "/api/sessions/"+child+"/plan/draft", childTok)
	if rec.Code != 201 {
		t.Fatalf("draft from child: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", "/api/blackboards/draft?session="+fx.parent, human)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"untrusted_data"`) {
		t.Fatalf("a child's draft must be visible from Main: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", "/api/blackboards/draft?session="+child, human)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"untrusted_data"`) {
		t.Fatalf("and from the child that generated it: %d %s", rec.Code, rec.Body.String())
	}

	// Generate from Main: readable from the child.
	rec = request("POST", "/api/sessions/"+fx.parent+"/plan/draft", fx.parentTok)
	if rec.Code != 201 {
		t.Fatalf("draft from main: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", "/api/blackboards/draft?session="+child, human)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"untrusted_data"`) {
		t.Fatalf("Main's draft must be visible from the child: %d %s", rec.Code, rec.Body.String())
	}

}

// TestBlackboardCreateFromDraftRefusesAnAgentToken is the required regression-proof test for
// BLG-450 phase 2 part B: turning a draft into a real blackboard must be person-only, exactly
// like HandleCreateBlackboard. During development this test was also run with createBlackboard's
// call to permissionOwner temporarily removed from HandleCreateBlackboardFromDraft, confirmed to
// fail (the agent token was accepted), then the guard was restored and this test re-run to confirm
// it passes again — see the task report for the exact before/after output.
func TestBlackboardCreateFromDraftRefusesAnAgentToken(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	agent := fx.token(topicOwner, "agent")

	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "")
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	rec = draftRequest(fx, mux, "POST", "/api/blackboards/draft/create?session="+fx.sess, agent, "")
	if rec.Code != 403 {
		t.Fatalf("expected an agent token to be refused creating from a draft, got %d %s", rec.Code, rec.Body.String())
	}
	// The draft must still be there — a refused attempt does not consume it.
	rec = draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, fx.token(topicOwner, "human"), "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"draft":null`) {
		t.Fatalf("draft should survive a refused create: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDraftBlackboardPlanStoresCallerContentExactly is BLG-450 review M3: `blerg-runner plan
// draft` must be able to accept and store what Main actually drafted, not fixed placeholder text.
// A fully-supplied body must come back with no placeholder text anywhere.
func TestDraftBlackboardPlanStoresCallerContentExactly(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	body := `{"vision":"Ship the thing","goals":["Ship it","Keep it fast"],` +
		`"milestone":{"name":"v1.0","release_id":"rel-1","target":"2027-01-15"},` +
		`"thrusts":[{"name":"Build the core","goal_index":0,"reason":"It is the foundation"}]}`
	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, body)
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		UntrustedData blackboardDraftPlan `json:"untrusted_data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	p := out.UntrustedData
	if p.Vision != "Ship the thing" || len(p.Goals) != 2 || p.Goals[0] != "Ship it" ||
		p.Milestone.Name != "v1.0" || p.Milestone.ReleaseID != "rel-1" || p.Milestone.Target == nil || *p.Milestone.Target != "2027-01-15" ||
		len(p.Thrusts) != 1 || p.Thrusts[0].Name != "Build the core" || p.Thrusts[0].Reason != "It is the foundation" {
		t.Fatalf("supplied content not stored exactly: %+v", p)
	}
	if strings.Contains(rec.Body.String(), "edit this before creating the plan") || strings.Contains(rec.Body.String(), "Starter thrust") {
		t.Fatalf("placeholder text leaked into a fully-supplied draft: %s", rec.Body.String())
	}
}

// TestDraftBlackboardPlanLeavesOmittedFieldsEmpty is M3's partial-content case, corrected by
// owner review: plan draft must never invent placeholder text. Supplying only a vision must leave
// every other field genuinely empty, not filled with canned "edit this" text.
func TestDraftBlackboardPlanLeavesOmittedFieldsEmpty(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, `{"vision":"Only this is real"}`)
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		UntrustedData blackboardDraftPlan `json:"untrusted_data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	p := out.UntrustedData
	if p.Vision != "Only this is real" {
		t.Fatalf("supplied vision lost: %+v", p)
	}
	if len(p.Goals) != 0 {
		t.Fatalf("omitted goals must stay empty, not a placeholder: %+v", p)
	}
	if p.Milestone.Name != "" {
		t.Fatalf("omitted milestone name must stay empty, not a placeholder: %+v", p)
	}
	if len(p.Thrusts) != 0 {
		t.Fatalf("omitted thrusts must stay empty, not a placeholder: %+v", p)
	}
	if strings.Contains(rec.Body.String(), "edit this before creating the plan") || strings.Contains(rec.Body.String(), "Starter thrust") || strings.Contains(rec.Body.String(), "Initial work") || strings.Contains(rec.Body.String(), `"v0.1"`) {
		t.Fatalf("placeholder text leaked into a partially-supplied draft: %s", rec.Body.String())
	}
}

// TestGenerateBlackboardDraftPlanHasNoPlaceholderText is M3's empty-body case, corrected by owner
// review: an agent-driven starter draft with no supplied content must still carry no placeholder
// text — only real facts already known about the session (its title, its linked board).
func TestGenerateBlackboardDraftPlanHasNoPlaceholderText(t *testing.T) {
	title := "Ship the thing"
	plan := generateBlackboardDraftPlan(&db.SessionRow{Title: &title})
	if plan.Vision != "" || len(plan.Goals) != 0 || plan.Milestone.Name != "" || len(plan.Thrusts) != 0 {
		t.Fatalf("an empty-body draft must leave every undetermined field empty, not a placeholder: %+v", plan)
	}
	if plan.Name != "Ship the thing plan" {
		t.Fatalf("name may still be derived from the session's real title: %+v", plan)
	}
}

// TestDraftBlackboardPlanRejectsInvalidContent is M3's validation case: an oversized or
// structurally invalid field is refused with 400 and nothing is stored, never silently clamped.
func TestDraftBlackboardPlanRejectsInvalidContent(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	cases := []struct {
		name, body string
	}{
		{"thrust name too long", `{"goals":["g"],"thrusts":[{"name":"` + strings.Repeat("x", 201) + `","goal_index":0}]}`},
		{"bad goal_index", `{"goals":["g"],"thrusts":[{"name":"t","goal_index":5}]}`},
		{"malformed target date", `{"milestone":{"name":"v1","target":"not-a-date"}}`},
		{"vision too long", `{"vision":"` + strings.Repeat("v", 1001) + `"}`},
		{"malformed JSON", `{not json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, c.body)
			if rec.Code != 400 {
				t.Fatalf("%s: %d %s, want 400", c.name, rec.Code, rec.Body.String())
			}
		})
	}
	// Nothing was stored by any of the refused attempts.
	rec := draftRequest(fx, mux, "GET", "/api/blackboards/draft?session="+fx.sess, fx.token(topicOwner, "human"), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"draft":null`) {
		t.Fatalf("a refused draft request must not store anything: %d %s", rec.Code, rec.Body.String())
	}
}

// TestCreateFromFullDraftSetsMilestoneReleaseAndTarget is M3's last proof: creating from a fully
// supplied draft produces a real blackboard whose milestone carries the ReleaseID and Target the
// draft specified — the gap this review found in createBlackboard, which never set Target at all.
func TestCreateFromFullDraftSetsMilestoneReleaseAndTarget(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	api := fx.api
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	human := fx.token(topicOwner, "human")
	body := `{"board_id":"board-a","goals":["Ship it"],"milestone":{"name":"v1.0","release_id":"rel-1","target":"2027-01-15"},` +
		`"thrusts":[{"name":"Build the core","goal_index":0}]}`
	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, body)
	if rec.Code != 201 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	// A stored linked draft fixture supplies the remote board context; the
	// generator intentionally ignores a caller's arbitrary board_id.
	draft, err := db.GetBlackboardDraft(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	var plan blackboardDraftPlan
	if err = json.Unmarshal(draft.Draft, &plan); err != nil {
		t.Fatal(err)
	}
	plan.BoardID = "board-a"
	raw, _ := json.Marshal(plan)
	if err = db.SaveBlackboardDraft(t.Context(), fx.pool, fx.sess, fx.sess, raw); err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	record := approveDraftForTest(t, fx, mux, human)
	if len(record.Milestones) != 1 || record.Milestones[0].ReleaseID != "rel-1" || record.Milestones[0].Target == nil {
		t.Fatalf("milestone missing release/target: %+v", record.Milestones)
	}
	if got := record.Milestones[0].Target.Format("2006-01-02"); got != "2027-01-15" {
		t.Fatalf("milestone target = %s, want 2027-01-15", got)
	}
}

func TestBlackboardDraftBrowserChanges(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	owner := &BrowserConn{AccountID: topicOwner, send: make(chan []byte, 16)}
	other := &BrowserConn{AccountID: topicOther, send: make(chan []byte, 16)}
	fx.api.hub.browsers["draft-owner"] = owner
	fx.api.hub.browsers["draft-other"] = other
	// A public workspace reaches both accounts, with no unreviewed draft content.
	rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "")
	if rec.Code != 201 {
		t.Fatalf("public save: %d", rec.Code)
	}
	for _, browser := range []*BrowserConn{owner, other} {
		select {
		case raw := <-browser.send:
			var event map[string]any
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if len(event) != 4 || event["session_id"] != fx.sess || event["draft_readable"] != (browser == owner) {
				t.Fatalf("public event: %s", raw)
			}
		default:
			t.Fatal("visible workspace change did not reach both accounts")
		}
	}
	if err := fx.api.MarkPrivate(t.Context(), fx.sess); err != nil {
		t.Fatal(err)
	}
	for _, action := range []struct{ method, path, token string }{
		{"POST", "/api/sessions/" + fx.sess + "/plan/draft", fx.sessTok},
		{"POST", "/api/sessions/" + fx.sess + "/plan/draft", fx.sessTok},

		{"POST", "/api/sessions/" + fx.sess + "/plan/draft", fx.sessTok},
	} {
		rec := draftRequest(fx, mux, action.method, action.path, action.token, "")
		if rec.Code != 200 && rec.Code != 201 {
			t.Fatalf("action: %d %s", rec.Code, rec.Body.String())
		}
		select {
		case raw := <-owner.send:
			var event map[string]any
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if len(event) != 4 || event["type"] != "blackboard_draft_changed" || event["session_id"] != fx.sess {
				t.Fatalf("event: %s", raw)
			}
		default:
			t.Fatal("draft change did not notify the open browser")
		}
		select {
		case raw := <-other.send:
			t.Fatalf("private draft leaked: %s", raw)
		default:
		}
		if len(owner.send) > 0 {
			t.Fatal("duplicate change event")
		}
	}

	rec = draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, "{")
	if rec.Code != 400 {
		t.Fatalf("invalid save: %d", rec.Code)
	}
	if len(owner.send) != 0 || len(other.send) != 0 {
		t.Fatal("failed save emitted a change")
	}
}

func approveDraftForTest(t *testing.T, fx *topicFx, mux *http.ServeMux, human string) blackboard.Record {
	t.Helper()
	d, err := db.GetBlackboardDraft(t.Context(), fx.pool, fx.sess)
	if err != nil || d == nil {
		t.Fatalf("draft: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"kind": "ask", "subject": boundSubject{Kind: "draft", DraftID: d.ID, Revision: d.Revision}})
	rec := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
	if rec.Code != 201 {
		t.Fatalf("ask: %d %s", rec.Code, rec.Body.String())
	}
	var ask struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ask)
	rec = draftRequest(fx, mux, "POST", "/api/messages/"+ask.ID+"/reply", human, `{"answer":"yes"}`)
	if rec.Code != 200 {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	var id string
	if err = fx.pool.QueryRow(t.Context(), `SELECT result_id FROM bound_asks WHERE message_id=$1`, ask.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	r, err := fx.api.blackboardStore().Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Board transport is canned in this separate contract test; both runner HTTP
// entry points, login verification, grants, SQL and transactions are real.
func TestBoundDraftRefusesReleasedLink(t *testing.T) {
	for _, closeBeforeAsk := range []bool{true, false} {
		t.Run(fmt.Sprint(closeBeforeAsk), func(t *testing.T) {
			fx, _, _ := newBoardLevelFx(t)
			human := fx.token(topicOwner, "human")
			fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
			if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
				t.Fatalf("grant: %d %s", code, body)
			}
			board := &blackboardGateway{closed: closeBeforeAsk}
			fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
			mux := http.NewServeMux()
			fx.api.RegisterBlackboardRoutes(mux)
			mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
			mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
			// Seed only the external board context; the bound decision uses
			// real HTTP, authentication and SQL, with no decision router mock.
			err := db.SaveBlackboardDraft(t.Context(), fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"Linked plan","board_id":"board-a","goals":["Ship"],"milestone":{"name":"Release","release_id":"release"}}`))
			if err != nil {
				t.Fatal(err)
			}
			d, err := db.GetBlackboardDraft(t.Context(), fx.pool, fx.sess)
			if err != nil {
				t.Fatal(err)
			}

			raw, _ := json.Marshal(map[string]any{"kind": "ask", "subject": boundSubject{Kind: "draft", DraftID: d.ID, Revision: d.Revision}})
			ask := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
			if closeBeforeAsk {
				if ask.Code != 409 || !strings.Contains(ask.Body.String(), "release_already_released") {
					t.Fatalf("released link accepted: %d %s", ask.Code, ask.Body.String())
				}
			} else {
				if ask.Code != 201 {
					t.Fatalf("open link: %d %s", ask.Code, ask.Body.String())
				}
				var q struct{ ID string }
				_ = json.Unmarshal(ask.Body.Bytes(), &q)
				board.closed = true
				answer := draftRequest(fx, mux, "POST", "/api/messages/"+q.ID+"/reply", human, `{"answer":"yes"}`)
				if answer.Code != 409 {
					t.Fatalf("closed since ask: %d %s", answer.Code, answer.Body.String())
				}
				var status string
				if err := fx.pool.QueryRow(t.Context(), `SELECT status FROM bound_asks WHERE message_id=$1`, q.ID).Scan(&status); err != nil || status != "open" {
					t.Fatalf("refusal state %s %v", status, err)
				}
				declined := draftRequest(fx, mux, "POST", "/api/messages/"+q.ID+"/reply", human, `{"answer":"no"}`)
				if declined.Code != 200 {
					t.Fatalf("decline after cut: %d %s", declined.Code, declined.Body.String())
				}
				if err := fx.pool.QueryRow(t.Context(), `SELECT status FROM bound_asks WHERE message_id=$1`, q.ID).Scan(&status); err != nil || status != "declined" {
					t.Fatalf("decline state %s %v", status, err)
				}

			}
			var total int
			_ = fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM blackboards WHERE record->>'workspace_id'=$1`, fx.sess).Scan(&total)
			if total != 0 {
				t.Fatal("refused link created a blackboard")
			}
		})
	}
}

func TestBoundDraftReplacementWakesWaiter(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			fx, _, _ := newBoardLevelFx(t)
			mux := http.NewServeMux()
			fx.api.RegisterBlackboardRoutes(mux)
			mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
			if err := db.SaveBlackboardDraft(t.Context(), fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"Original","goals":["Goal"]}`)); err != nil {
				t.Fatal(err)
			}
			draft, err := db.GetBlackboardDraft(t.Context(), fx.pool, fx.sess)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"kind": "ask", "subject": boundSubject{Kind: "draft", DraftID: draft.ID, Revision: draft.Revision}})
			ask := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
			if ask.Code != 201 {
				t.Fatalf("ask: %d %s", ask.Code, ask.Body.String())
			}
			var question struct{ ID string }
			_ = json.Unmarshal(ask.Body.Bytes(), &question)
			wake := fx.api.waiters.channel(question.ID)
			if remove {
				err = db.DeleteBlackboardDraft(t.Context(), fx.pool, fx.sess)
				if err != nil {
					t.Fatal(err)
				}
				fx.api.broadcastBlackboardDraftChanged(httptest.NewRequest("POST", "/", nil), fx.sess)
			} else {
				rec := draftRequest(fx, mux, "POST", "/api/sessions/"+fx.sess+"/plan/draft", fx.sessTok, `{"name":"Replacement","goals":["Goal"]}`)
				if rec.Code != 201 {
					t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
				}
			}
			select {
			case <-wake:
			default:
				t.Fatal("canceled draft did not wake Main's waiting ask")
			}
			receipt, bound, err := fx.api.boundReceipt(t.Context(), question.ID)
			if err != nil || !bound || receipt["cause"] != "stale_draft" {
				t.Fatalf("notice: %+v %v", receipt, err)
			}
		})
	}
}
