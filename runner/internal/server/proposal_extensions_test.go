package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestProposalExtensionPreservesCreationAndFencesExecution(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{"to":"recipient@example.com"}`)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '6 days 23 hours' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	before := decodeProp(t, fx.req(http.MethodGet, "/api/proposals/"+p.ID, fx.human(), ""))
	body := fmt.Sprintf(`{"expected_expires_at":%q}`, before.ExpiresAt)
	rec := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.human(), body)
	if rec.Code != 200 {
		t.Fatalf("extend: %d %s", rec.Code, rec.Body.String())
	}
	after := decodeProp(t, rec)
	if before.CreatedAt != after.CreatedAt {
		t.Fatal("extension rewrote the original creation time")
	}
	oldDeadline, _ := time.Parse(time.RFC3339Nano, before.ExpiresAt)
	newDeadline, _ := time.Parse(time.RFC3339Nano, after.ExpiresAt)
	if newDeadline.Sub(oldDeadline) != 7*24*time.Hour {
		t.Fatal("extension did not add exactly seven days")
	}
	stale := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.human(), body)
	if stale.Code != 409 {
		t.Fatalf("stale extension: %d", stale.Code)
	}
	var n int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM proposal_extensions WHERE proposal_id=$1 AND extended_by=$2`, p.ID, mcpAcct).Scan(&n); err != nil || n != 1 {
		t.Fatalf("extension audit: %d %v", n, err)
	}
	// The open list carries the extension history, marked as the viewer's own.
	var listed struct {
		Proposals []struct {
			ID         string `json:"id"`
			Extensions []struct {
				ByYou     bool   `json:"by_you"`
				ExpiresAt string `json:"expires_at"`
			} `json:"extensions"`
		} `json:"proposals"`
	}
	list := fx.req(http.MethodGet, "/api/proposals?scope=open", fx.human(), "")
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Proposals) != 1 || len(listed.Proposals[0].Extensions) != 1 || !listed.Proposals[0].Extensions[0].ByYou {
		t.Fatalf("extension history not listed: %s", list.Body.String())
	}
	// Advance the original age without changing the explicit extension deadline.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '8 days' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExpirePendingProposals(t.Context(), fx.pool, db.ProposalExpiry); err != nil {
		t.Fatal(err)
	}
	if fx.row(p.ID).State != db.ProposalPending {
		t.Fatal("maintenance ignored the extended deadline")
	}
	if _, err := db.BeginProposalExecution(t.Context(), fx.pool, mcpAcct, p.ID, mcpAcct); err != nil {
		t.Fatalf("execution ignored the extended deadline: %v", err)
	}
}

func TestProposalExtensionRequiresOwnerAndPendingUnexpiredState(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{}`)
	before := decodeProp(t, fx.req(http.MethodGet, "/api/proposals/"+p.ID, fx.human(), ""))
	body := fmt.Sprintf(`{"expected_expires_at":%q}`, before.ExpiresAt)
	if r := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.otherHuman(), body); r.Code != 404 {
		t.Fatalf("other owner: %d", r.Code)
	}
	if r := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.token("agent", ""), body); r.Code != 403 {
		t.Fatalf("agent: %d", r.Code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '8 days' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	expired := decodeProp(t, fx.req(http.MethodGet, "/api/proposals/"+p.ID, fx.human(), ""))
	body = fmt.Sprintf(`{"expected_expires_at":%q}`, expired.ExpiresAt)
	if r := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.human(), body); r.Code != 409 || !strings.Contains(r.Body.String(), "pending") {
		t.Fatalf("expired: %d %s", r.Code, r.Body.String())
	}
	decided := fx.insert("send", `{}`)
	if _, err := db.RejectProposal(t.Context(), fx.pool, mcpAcct, decided.ID, mcpAcct); err != nil {
		t.Fatal(err)
	}
	view := decodeProp(t, fx.req(http.MethodGet, "/api/proposals/"+decided.ID, fx.human(), ""))
	body = fmt.Sprintf(`{"expected_expires_at":%q}`, view.ExpiresAt)
	if r := fx.req(http.MethodPost, "/api/proposals/"+decided.ID+"/extend", fx.human(), body); r.Code != 409 {
		t.Fatalf("decided: %d %s", r.Code, r.Body.String())
	}
}

// A later automatic approval neither removes nor hides the earlier pending request. Audit reads
// preserve the exact permission, including a later revocation, without exposing its login proof.
func TestProposalAttentionKeepsPendingAndAuditsPermission(t *testing.T) {
	fx, _ := newPermFx(t)
	human := fx.token(topicOwner, "human")
	code, first := fileOn(t, fx, "A", "First draft")
	if code != 202 {
		t.Fatalf("first: %d", code)
	}
	grantResponse := filingRequest(fx, "POST", "/api/sessions/"+fx.sess+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"})
	if grantResponse.Code != 200 {
		t.Fatalf("grant: %d", grantResponse.Code)
	}
	var grant proposalGrantView
	if err := json.Unmarshal(grantResponse.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	code, second := fileOn(t, fx, "A", "Second draft")
	if code != 200 {
		t.Fatalf("second: %d %v", code, second)
	}
	pending := filingRequest(fx, "GET", "/api/proposals?scope=open&state=pending", human, nil)
	if pending.Code != 200 || !strings.Contains(pending.Body.String(), first["proposal_id"].(string)) || strings.Contains(pending.Body.String(), second["proposal_id"].(string)) {
		t.Fatalf("pending queue: %d %s", pending.Code, pending.Body.String())
	}
	if _, err := db.RevokeProposalGrant(t.Context(), fx.pool, grant.ID, topicOwner, "fixture"); err != nil {
		t.Fatal(err)
	}
	decided := filingRequest(fx, "GET", "/api/proposals?scope=decided", human, nil)
	if decided.Code != 200 {
		t.Fatalf("decided: %d", decided.Code)
	}
	var result struct {
		Proposals []struct {
			DecidedBy    string               `json:"decided_by"`
			DecidedByYou bool                 `json:"decided_by_you"`
			Permission   *db.ProposalApproval `json:"approved_permission"`
		} `json:"proposals"`
	}
	if err := json.Unmarshal(decided.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Proposals) != 1 || result.Proposals[0].Permission == nil {
		t.Fatalf("missing permission audit: %s", decided.Body.String())
	}
	audit := result.Proposals[0]
	if audit.DecidedBy != topicOwner || audit.Permission.ID != grant.ID || audit.Permission.AccountID != topicOwner || audit.Permission.BoardName != "A" || audit.Permission.RevokedAt == nil || !audit.DecidedByYou || !audit.Permission.GrantedByYou {
		t.Fatalf("wrong permission audit: %+v", audit)
	}
	for _, secret := range []string{"proof_session_id", testSID, "tool_hash", "project_scope"} {
		if strings.Contains(decided.Body.String(), secret) {
			t.Fatalf("private permission field reached proposal audit: %q", secret)
		}
	}
}

// extendAndAge extends a proposal that is 6 d 23 h old, then ages it past the original seven
// days: only the extended deadline keeps it pending.
func extendAndAge(t *testing.T, fx *topicFx, id string) {
	t.Helper()
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '6 days 23 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	human := fx.token(topicOwner, "human")
	before := filingRequest(fx, http.MethodGet, "/api/proposals/"+id, human, nil)
	var view proposalView
	if before.Code != 200 || json.Unmarshal(before.Body.Bytes(), &view) != nil {
		t.Fatalf("get: %d %s", before.Code, before.Body.String())
	}
	rec := filingRequest(fx, http.MethodPost, "/api/proposals/"+id+"/extend", human, map[string]any{"expected_expires_at": view.ExpiresAt})
	if rec.Code != 200 {
		t.Fatalf("extend: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '7 days 1 hour' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

// The board filing compare-and-sets (edit and approve) honor an extension, not the original
// seven days: a ticket extended at day 6 and approved after day 7 is filed.
func TestProposalExtensionCoversBoardFilingEditAndApproval(t *testing.T) {
	fx, up := newFilingFx(t)
	p := queueFiling(t, fx)
	extendAndAge(t, fx, p.ID)
	human := fx.token(topicOwner, "human")
	edit := filingRequest(fx, "PATCH", "/api/proposals/"+p.ID+"/draft", human, map[string]any{"expected_arguments_raw": string(p.Arguments), "draft": map[string]any{"board_id": "board-a", "title": "Edited after day 7", "body": "x"}})
	if edit.Code != 200 {
		t.Fatalf("edit after extension: %d %s", edit.Code, edit.Body.String())
	}
	edited, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	approved := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", human, map[string]any{"expected_arguments_raw": string(edited.Arguments)})
	if approved.Code != 200 || up.creates != 1 || up.draft.Title != "Edited after day 7" {
		t.Fatalf("approve after extension: %d %s creates=%d", approved.Code, approved.Body.String(), up.creates)
	}
}

// A plugin request extended at day 6 can still be approved after day 7.
func TestProposalExtensionCoversPluginRequests(t *testing.T) {
	fx, f := pluginFx(t)
	p := queuePlugin(t, fx)
	extendAndAge(t, fx, p.ID)
	w := pluginReq(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]string{"scope": "always", "expected_arguments_raw": string(p.Arguments)})
	if w.Code != 200 || f.adds != 1 {
		t.Fatalf("approve after extension: %d %s adds=%d", w.Code, w.Body.String(), f.adds)
	}
}

// #68's root cause: a session's Needs you never listed its proposals (the live state counted asks,
// tool changes, started-session requests and reviews only), so a pending file_ticket waited unseen
// on the Proposals page. The live state now carries the viewer's pending proposals on the session,
// counts them in Needs you, and its ETag changes when one arrives or is decided.
func TestLiveNeedsYouListsEveryPendingProposal(t *testing.T) {
	fx, _ := newPermFx(t)
	human := fx.token(topicOwner, "human")
	type live struct {
		Proposals int `json:"proposals"`
		Counts    struct {
			NeedsYou int `json:"needs_you"`
		} `json:"counts"`
	}
	read := func(etag string) (live, string, int) {
		t.Helper()
		r := fx.getLive(fx.sess, human, etag)
		var l live
		if r.Code == 200 {
			if err := json.Unmarshal(r.Body.Bytes(), &l); err != nil {
				t.Fatal(err)
			}
		}
		return l, r.Header().Get("ETag"), r.Code
	}
	_, etag, _ := read("")
	if code, _ := fileOn(t, fx, "A", "First draft"); code != 202 {
		t.Fatalf("first: %d", code)
	}
	l, etag2, code := read(etag)
	if code != 200 || l.Proposals != 1 || l.Counts.NeedsYou != 1 {
		t.Fatalf("after one proposal: code=%d %+v", code, l)
	}
	code, second := fileOn(t, fx, "A", "Second draft")
	if code != 202 {
		t.Fatalf("second: %d", code)
	}
	if l, _, code = read(etag2); code != 200 || l.Proposals != 2 || l.Counts.NeedsYou != 2 {
		t.Fatalf("two proposals in a row: code=%d %+v", code, l)
	}
	if r := filingRequest(fx, "POST", "/api/proposals/"+second["proposal_id"].(string)+"/reject", human, map[string]any{}); r.Code != 200 {
		t.Fatalf("reject: %d", r.Code)
	}
	if l, _, _ = read(""); l.Proposals != 1 {
		t.Fatalf("a decided proposal still counted: %+v", l)
	}
	// Past its deadline but not yet swept to expired, it is shown as expired (it cannot be approved
	// or extended) and no longer needs the person: it is not counted.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at = now() - interval '8 days', expires_at_override = NULL
		WHERE session_id = $1::uuid AND state = 'pending'`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if l, _, _ = read(""); l.Proposals != 0 || l.Counts.NeedsYou != 0 {
		t.Fatalf("a proposal past its deadline still counted before the sweep: %+v", l)
	}
	// Someone else viewing the session never sees the owner's proposals.
	r := fx.getLive(fx.sess, fx.token(topicOther, "human"), "")
	var other live
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &other) != nil || other.Proposals != 0 {
		t.Fatalf("another viewer saw the owner's proposals: %d %s", r.Code, r.Body.String())
	}
}

// Owner ruling (M1): a proposal stays open at most 30 days from when the agent proposed it. The
// last extension is shortened to reach that exactly; after it the view says it cannot be extended
// and the server refuses with the reason.
func TestProposalExtensionCappedAtThirtyDays(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{}`)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE mcp_proposals SET created_at=now()-interval '27 days', expires_at_override=now()+interval '1 day' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	type capView struct {
		CreatedAt    string `json:"created_at"`
		ExpiresAt    string `json:"expires_at"`
		MaxExpiresAt string `json:"max_expires_at"`
		Extendable   bool   `json:"extendable"`
	}
	read := func() capView {
		var v capView
		if err := json.Unmarshal(fx.req(http.MethodGet, "/api/proposals/"+p.ID, fx.human(), "").Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	if !before.Extendable {
		t.Fatalf("a proposal 27 days old with a deadline at day 28 is extendable: %+v", before)
	}
	rec := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.human(), fmt.Sprintf(`{"expected_expires_at":%q}`, before.ExpiresAt))
	if rec.Code != 200 {
		t.Fatalf("extend: %d %s", rec.Code, rec.Body.String())
	}
	after := read()
	created, _ := time.Parse(time.RFC3339Nano, after.CreatedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, after.ExpiresAt)
	if !deadline.Equal(created.Add(30*24*time.Hour)) || after.MaxExpiresAt != after.ExpiresAt || after.Extendable {
		t.Fatalf("the last extension must stop at 30 days: %+v", after)
	}
	again := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/extend", fx.human(), fmt.Sprintf(`{"expected_expires_at":%q}`, after.ExpiresAt))
	if again.Code != 409 || !strings.Contains(again.Body.String(), "up to 30 days") {
		t.Fatalf("past the cap: %d %s", again.Code, again.Body.String())
	}
}
