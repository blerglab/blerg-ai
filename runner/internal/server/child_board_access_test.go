package server

// Children inherit board access (0.4.4): a parent passes board levels down at the start, never above
// its own; every call of the child is capped by the parent's level now; the person can lower a
// single child, never raise it above the parent; the child is told of changes; a parent's stop for
// good ends it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ---- the live cap (the session board tools fixture) ----------------------------------------------

// childFx is a child of fx.sess (same owner, started by it), with its own session token.
type childFx struct {
	id, tok string
}

func newChildOf(t *testing.T, fx *topicFx) childFx {
	t.Helper()
	ctx := context.Background()
	id := newUUID()
	origin := db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess, DelegationVia: "request:" + newUUID()}
	if err := db.InsertSessionAs(ctx, fx.pool, id, topicDaemonID, "running", "/repos/app", "app", "Worker", "", origin); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind = 'agent' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionStartedBy(ctx, fx.pool, id, "session", fx.sess); err != nil {
		t.Fatal(err)
	}
	tok, err := db.MintSessionToken(ctx, fx.pool, id, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return childFx{id: id, tok: tok}
}

// humanWith is the owner's browser token from the login session sid.
func humanWith(fx *topicFx, sid string) string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{Sub: topicOwner, Aud: coreAuthAudience, Kind: "human", Sid: sid,
		Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
}

// passDown stores the child's own level as a start that passed it down does.
func passDown(t *testing.T, fx *topicFx, child, board, name, level string) {
	t.Helper()
	parent, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	fx.api.seedChildBoards(t.Context(), parent, child, []childBoard{{Board: board, Name: name, Level: level}})
}

func childTool(fx *topicFx, c childFx, board, tool string) (int, string) {
	rec := filingRequest(fx, http.MethodPost, "/api/sessions/"+c.id+"/board-tools", c.tok, map[string]any{"board": board, "tool": tool})
	return rec.Code, rec.Body.String()
}

func setChildLevel(fx *topicFx, token string, c childFx, board, level string) (int, string) {
	rec := filingRequest(fx, http.MethodPut, "/api/sessions/"+c.id+"/board-access", token, map[string]any{"board_id": board, "level": level})
	return rec.Code, rec.Body.String()
}

// notesFor drains the daemon's frames and returns the board access notes per session.
func notesFor(t *testing.T, dc *DaemonConn) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for {
		select {
		case raw := <-dc.send:
			var m protocol.AgentUserMessage
			if json.Unmarshal(raw, &m) == nil && m.Type == "agent_user_message" && strings.HasPrefix(m.Text, "Board access changed: ") {
				if m.Source != "proposal" {
					t.Fatalf("note sent with source %q: %s", m.Source, m.Text)
				}
				out[m.SessionID] = append(out[m.SessionID], m.Text)
			}
		default:
			return out
		}
	}
}

// A child is capped by its parent's level on every call: lowering or revoking the parent lowers the
// child at its next call, raising the parent never lifts the child above its own level, and the
// child's calls act with the parent's sign-in.
func TestChildBoardCallsAreCappedByTheParentOnEveryCall(t *testing.T) {
	fx, fb, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	if code, body := setLevel(fx, human, "board-a", db.BoardLevelFull); code != 200 {
		t.Fatalf("parent full: %d %s", code, body)
	}
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFull)
	st, err := db.LatestBoardGrant(t.Context(), fx.pool, c.id, "board-a")
	if err != nil || st == nil || !st.Live || st.Grant.Level != db.BoardLevelFull || st.Grant.CreatedVia != "parent" || st.Grant.ProofSessionID != testSID {
		t.Fatalf("child row %+v %v", st, err)
	}
	expect := func(step string, read, write bool) {
		t.Helper()
		n := fb.count()
		code, body := childTool(fx, c, "board-a", "blerg_card_get")
		if (code == 200) != read {
			t.Fatalf("%s: get = %d %s", step, code, body)
		}
		if read {
			if call := fb.last(); call.SessionRef != c.id || call.Proof.SessionID != testSID || call.Proof.AccountID != topicOwner || !call.ReadOnly {
				t.Fatalf("%s: call %+v", step, call)
			}
		}
		code, body = childTool(fx, c, "board-a", "blerg_card_create")
		if (code == 200) != write {
			t.Fatalf("%s: create = %d %s", step, code, body)
		}
		if capped := strings.HasPrefix(step, "parent"); !write && read && capped != strings.Contains(body, "capped by the session that started you") {
			t.Fatalf("%s: refusal names the cap wrongly: %s", step, body)
		}
		want := n
		if read {
			want++
		}
		if write {
			want++
		}
		if fb.count() != want {
			t.Fatalf("%s: a refused call reached the board (%d calls, want %d)", step, fb.count(), want)
		}
	}
	expect("parent full", true, true)
	setLevel(fx, human, "board-a", db.BoardLevelRead)
	expect("parent lowered to read", true, false)
	pst, _ := db.LatestBoardGrant(t.Context(), fx.pool, fx.sess, "board-a")
	if rec := filingRequest(fx, http.MethodPost, "/api/proposal-grants/"+pst.Grant.ID+"/revoke", human, nil); rec.Code != 200 {
		t.Fatalf("revoke parent: %d", rec.Code)
	}
	expect("parent revoked", false, false)
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	expect("parent full again", true, true)

	// The child's own lower level holds whatever the parent has.
	if code, body := setChildLevel(fx, human, c, "board-a", db.BoardLevelRead); code != 200 {
		t.Fatalf("lower the child: %d %s", code, body)
	}
	expect("child lowered to read", true, false)
	// The parent is the child's whole authority: what the child sees of its access is the cap.
	rec := filingRequest(fx, http.MethodGet, "/api/sessions/"+c.id+"/board-access", c.tok, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"level":"read"`) || !strings.Contains(rec.Body.String(), `"parent_level":"full"`) {
		t.Fatalf("child access: %d %s", rec.Code, rec.Body.String())
	}
}

// The person may lower a single child, and raise it back up to the parent's level, never above.
func TestChildCannotBeRaisedAboveItsParent(t *testing.T) {
	fx, fb, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	setLevel(fx, human, "board-a", db.BoardLevelRead)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelRead)
	for _, level := range []string{db.BoardLevelFile, db.BoardLevelFull} {
		if code, body := setChildLevel(fx, human, c, "board-a", level); code != http.StatusConflict || !strings.Contains(body, "cannot have more board access") {
			t.Fatalf("raise child to %s: %d %s", level, code, body)
		}
	}
	// A board the parent has no access to cannot be given to the child at all.
	if code, _ := setChildLevel(fx, human, c, "board-b", db.BoardLevelRead); code != http.StatusConflict {
		t.Fatalf("board-b: %d", code)
	}
	// "Allow for this session" on a child's filing is a raise to file: refused above a read parent.
	if g := filingRequest(fx, http.MethodPost, "/api/sessions/"+c.id+"/proposal-grants", human, map[string]any{"kind": "board_file", "board_id": "board-a"}); g.Code != http.StatusConflict {
		t.Fatalf("allow filing above the parent: %d %s", g.Code, g.Body.String())
	}
	if st, _ := db.LatestBoardGrant(t.Context(), fx.pool, c.id, "board-a"); st.Grant.Level != db.BoardLevelRead {
		t.Fatalf("a refused raise stored %s", st.Grant.Level)
	}
	if code, body := setChildLevel(fx, human, c, "board-a", db.BoardLevelPropose); code != 200 {
		t.Fatalf("lower: %d %s", code, body)
	}
	if code, _ := childTool(fx, c, "board-a", "blerg_card_get"); code != 403 {
		t.Fatalf("lowered child read: %d", code)
	}
	if code, body := setChildLevel(fx, human, c, "board-a", db.BoardLevelRead); code != 200 {
		t.Fatalf("raise back to the parent's level: %d %s", code, body)
	}
	if code, _ := childTool(fx, c, "board-a", "blerg_card_get"); code != 200 {
		t.Fatalf("child read again: %d", code)
	}
	// The child can never change a level itself.
	if code, _ := setChildLevel(fx, c.tok, c, "board-a", db.BoardLevelPropose); code == 200 {
		t.Fatal("the child set its own level")
	}
	_ = fb
}

// Filing without asking needs file under the cap, and acts with the parent's sign-in.
func TestChildFilingIsApprovedOnlyUnderTheParentsLevel(t *testing.T) {
	fx, _, up := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFile)
	// Filing needs the session's own discovery proof (recorded at a start that carries a sign-in).
	if err := db.SaveBoardFilingProof(t.Context(), fx.pool, c.id, "session_id", "child-start-login", ""); err != nil {
		t.Fatal(err)
	}
	file := func(title string) (int, map[string]any) {
		return fx.postWork("file-ticket", c.id, c.tok, map[string]any{"board": "A", "title": title, "body": "Fixture details"})
	}
	creates := up.creates
	if code, out := file("Under a full parent"); out["state"] != "done" || up.creates != creates+1 {
		t.Fatalf("file under full: %d %v", code, out)
	}
	if p := up.proofs[len(up.proofs)-1]; p.SessionID != testSID || p.AccountID != topicOwner {
		t.Fatalf("the child's filing acted with %+v, not the parent's sign-in", p)
	}
	setLevel(fx, human, "board-a", db.BoardLevelRead)
	if code, out := file("Under a read parent"); code != 202 || out["state"] != "pending" || up.creates != creates+1 {
		t.Fatalf("file under read: %d %v", code, out)
	}
	// The parent is lowered after the permission was found but before the filing executes: the
	// cap is checked again right before it runs, so the filing waits for the person.
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	lowered := false
	fx.api.beforeGrantApproval = func() {
		if !lowered {
			lowered = true
			setLevel(fx, human, "board-a", db.BoardLevelRead)
		}
	}
	if code, out := file("Parent lowered in flight"); !lowered || out["state"] != "pending" || up.creates != creates+1 {
		t.Fatalf("file while the parent is lowered: %d %v (lowered %v, creates %d)", code, out, lowered, up.creates)
	}
}

// The child is told when its level changes, its parent's change included, as a note from Blerg.
func TestChildIsToldWhenTheParentChangesItsLevel(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	dc := fx.agentOn()
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	fx.api.hub.SetSessionOwner(c.id, dc.ID)
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFile)
	_ = notesFor(t, dc)

	setLevel(fx, human, "board-a", db.BoardLevelRead)
	notes := notesFor(t, dc)
	if len(notes[c.id]) != 1 || !strings.Contains(notes[c.id][0], `board "A" is now Read (was File without asking)`) ||
		!strings.Contains(notes[c.id][0], "session that started you") {
		t.Fatalf("child note on the parent's lowering: %q", notes)
	}
	if len(notes[fx.sess]) != 1 {
		t.Fatalf("parent note: %q", notes[fx.sess])
	}
	// Raising the parent to full lifts the child back to its own level (file), not to full.
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	if notes = notesFor(t, dc); len(notes[c.id]) != 1 || !strings.Contains(notes[c.id][0], `is now File without asking (was Read)`) {
		t.Fatalf("child note on the parent's raise: %q", notes)
	}
	// A parent change that leaves the child's level as it is tells the child nothing.
	setLevel(fx, human, "board-a", db.BoardLevelFile)
	if notes = notesFor(t, dc); len(notes[c.id]) != 0 {
		t.Fatalf("unchanged child told: %q", notes[c.id])
	}
	pst, _ := db.LatestBoardGrant(t.Context(), fx.pool, fx.sess, "board-a")
	if rec := filingRequest(fx, http.MethodPost, "/api/proposal-grants/"+pst.Grant.ID+"/revoke", human, nil); rec.Code != 200 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if notes = notesFor(t, dc); len(notes[c.id]) != 1 || !strings.Contains(notes[c.id][0], `is now Propose (was File without asking)`) {
		t.Fatalf("child note on the parent's revoke: %q", notes)
	}
	// The person lowering the child itself: the child hears it.
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	_ = notesFor(t, dc)
	setChildLevel(fx, human, c, "board-a", db.BoardLevelRead)
	if notes = notesFor(t, dc); len(notes[c.id]) != 1 || !strings.Contains(notes[c.id][0], `is now Read (was File without asking)`) || len(notes[fx.sess]) != 0 {
		t.Fatalf("child note on its own lowering: %q", notes)
	}
}

// A child's access never survives its parent's stop for good: not for a kept-running child, and
// not after the parent is resumed and given access again.
func TestChildBoardAccessEndsWithTheParentsLifetime(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running = true WHERE id = $1`, c.id); err != nil {
		t.Fatal(err)
	}
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFull)
	if code, _ := childTool(fx, c, "board-a", "blerg_card_create"); code != 200 {
		t.Fatalf("before: %d", code)
	}
	// The parent stops for good and is resumed: a new lifetime, given full again.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_ended_at = clock_timestamp() WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if code, body := setLevel(fx, human, "board-a", db.BoardLevelFull); code != 200 {
		t.Fatalf("parent's new lifetime: %d %s", code, body)
	}
	if code, _ := boardTool(fx, fx.sessTok, "board-a", "blerg_card_create", nil); code != 200 {
		t.Fatalf("parent in its new lifetime: %d", code)
	}
	if code, body := childTool(fx, c, "board-a", "blerg_card_get"); code != 403 {
		t.Fatalf("kept-running child after the parent's stop for good: %d %s", code, body)
	}
	// A child whose parent row is gone has no cap to act under either.
	d := newChildOf(t, fx)
	passDown(t, fx, d.id, "board-a", "A", db.BoardLevelRead)
	if code, _ := childTool(fx, d, "board-a", "blerg_card_get"); code != 200 {
		t.Fatalf("new child: %d", code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id = NULL WHERE id = $1`, d.id); err != nil {
		t.Fatal(err)
	}
	if code, _ := childTool(fx, d, "board-a", "blerg_card_get"); code != 403 {
		t.Fatalf("orphaned child: %d", code)
	}
}

// The child's Permissions dialog shows its own level, its parent's, the effective one and who caps it.
func TestChildPermissionsShowTheParentCap(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	c := newChildOf(t, fx)
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFull)
	setLevel(fx, human, "board-a", db.BoardLevelRead)
	rec := filingRequest(fx, http.MethodGet, "/api/sessions/"+c.id+"/permissions", human, nil)
	if rec.Code != 200 {
		t.Fatalf("permissions: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Boards    []boardLevelView `json:"boards"`
		StartedBy struct {
			ID     string              `json:"id"`
			Title  string              `json:"title"`
			Boards []map[string]string `json:"boards"`
		} `json:"started_by"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Boards) != 1 || out.Boards[0].Level != db.BoardLevelRead || out.Boards[0].OwnLevel != db.BoardLevelFull ||
		out.Boards[0].ParentLevel != db.BoardLevelRead || !out.Boards[0].Active || out.Boards[0].BoardName != "A" {
		t.Fatalf("boards %+v", out.Boards)
	}
	if out.StartedBy.ID != fx.sess || out.StartedBy.Title == "" || len(out.StartedBy.Boards) != 1 || out.StartedBy.Boards[0]["level"] != db.BoardLevelRead {
		t.Fatalf("started_by %+v", out.StartedBy)
	}
	if strings.Contains(rec.Body.String(), testSID) {
		t.Fatal("the permissions view carries a sign-in")
	}
	// A session that was not started by another one has no started_by.
	rec = filingRequest(fx, http.MethodGet, "/api/sessions/"+fx.sess+"/permissions", human, nil)
	if strings.Contains(rec.Body.String(), "started_by") || strings.Contains(rec.Body.String(), "parent_level") {
		t.Fatalf("parent view: %s", rec.Body.String())
	}
}

// ---- passing levels down at the start (the child sessions fixture) --------------------------------

// giveParent stores a board level on fx.parent as the person's Permissions would.
func (fx *delegFx) giveParent(board, name, level string) {
	fx.t.Helper()
	if _, err := db.InsertProposalGrant(context.Background(), fx.pool, db.ProposalGrant{SessionID: fx.parent, AccountID: "owner-1",
		Kind: db.ProposalGrantBoard, BoardID: board, BoardName: name, Level: level, ProofSessionID: "parent-login", CreatedVia: "panel"}); err != nil {
		fx.t.Fatal(err)
	}
}

func nineBoards() []map[string]string {
	var b []map[string]string
	for i := 0; i < 9; i++ {
		b = append(b, map[string]string{"board": fmt.Sprint("b", i), "level": "read"})
	}
	return b
}

// A start passes down only the parent's boards, at most at the parent's level, and the card shows them.
func TestChildBoardsAreCheckedAtTheStartAndShownOnTheCard(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	fx.giveParent("board-a", "Blerg", db.BoardLevelRead)
	fx.giveParent("board-b", "Ops", db.BoardLevelFull)
	ask := func(boards any) (int, map[string]any) {
		code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "codex", "repo": "app", "prompt": "work the cards", "boards": boards}, newUUID())
		return code, out
	}
	for name, c := range map[string]struct {
		boards any
		want   string
	}{
		"above the parent": {[]map[string]string{{"board": "Blerg", "level": "full"}}, "at most Read"},
		"not the parent's": {[]map[string]string{{"board": "board-z", "level": "read"}}, "not one this session has access to"},
		"level propose":    {[]map[string]string{{"board": "Blerg", "level": "propose"}}, "level read, file or full"},
		"unknown level":    {[]map[string]string{{"board": "Blerg", "level": "admin"}}, "level read, file or full"},
		"twice":            {[]map[string]string{{"board": "Blerg", "level": "read"}, {"board": "board-a", "level": "read"}}, "more than once"},
		"too many":         {nineBoards(), "at most 8"},
		"not a list":       {"Blerg:full", ""},
	} {
		code, out := ask(c.boards)
		if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out["error"]), c.want) {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
	fx.noSpawn()
	code, out := ask([]map[string]string{{"board": "Blerg", "level": "read"}, {"board": "board-b", "level": "full"}})
	if code != http.StatusAccepted {
		t.Fatalf("ask: %d %v", code, out)
	}
	id := asString(out["request_id"])
	var raw string
	if err := fx.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id = $1::uuid AND client_event_id = $2`,
		fx.parent, delegationEventID(id, "delegation_request")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ev delegationRequestPayload
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	want := []childBoard{{Board: "board-a", Name: "Blerg", Level: "read"}, {Board: "board-b", Name: "Ops", Level: "full"}}
	if fmt.Sprint(ev.Boards) != fmt.Sprint(want) {
		t.Fatalf("card boards %+v", ev.Boards)
	}
	var stored storedChildRequest
	if err := json.Unmarshal(fx.request(id).Request, &stored); err != nil || fmt.Sprint(stored.Boards) != fmt.Sprint(want) {
		t.Fatalf("stored boards %+v %v", stored.Boards, err)
	}

	// The parent is lowered before the person decides: the start is refused, nothing is started.
	fx.giveParent("board-b", "Ops", db.BoardLevelRead)
	code, out = fx.decide(id, fx.human("owner-1"), choice("once"))
	if code != http.StatusOK || out["child_session_id"] != nil || !strings.Contains(asString(out["start_error"]), "below the Full this start passes down") {
		t.Fatalf("decide after lowering: %d %v", code, out)
	}
	fx.noSpawn()

	// At the parent's level now, the decided start runs and the child holds the levels passed down.
	code, out = ask([]map[string]string{{"board": "Blerg", "level": "read"}, {"board": "Ops", "level": "read"}})
	if code != http.StatusAccepted {
		t.Fatalf("ask again: %d %v", code, out)
	}
	code, out = fx.decide(asString(out["request_id"]), fx.human("owner-1"), choice("once"))
	child := asString(out["child_session_id"])
	if code != http.StatusOK || child == "" {
		t.Fatalf("decide: %d %v", code, out)
	}
	fx.spawn()
	for board, level := range map[string]string{"board-a": "read", "board-b": "read"} {
		st, err := db.LatestBoardGrant(ctx, fx.pool, child, board)
		if err != nil || st == nil || !st.Live || st.Grant.Level != level || st.Grant.CreatedVia != "parent" || st.Grant.AccountID != "owner-1" {
			t.Fatalf("child %s: %+v %v", board, st, err)
		}
	}
	lv, err := fx.api.effectiveBoardLevel(ctx, fx.row(child), "board-b")
	if err != nil || lv.Level != db.BoardLevelRead || !lv.Child || lv.Cap != db.BoardLevelRead {
		t.Fatalf("child effective %+v %v", lv, err)
	}
}

// A rule (or a session grant) may start a child with boards: no card, the same checks, and the
// started reply lists them.
func TestChildBoardsThroughARule(t *testing.T) {
	fx := newDelegFx(t)
	fx.giveParent("board-a", "Blerg", db.BoardLevelFull)
	fx.rule("claude", runnerRuntimeDocker, false)
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "p",
		"boards": []map[string]string{{"board": "Blerg", "level": "full"}}}, newUUID())
	if code != http.StatusCreated {
		t.Fatalf("start: %d %v", code, out)
	}
	fx.spawn()
	ch, _ := out["child"].(map[string]any)
	if fmt.Sprint(ch["boards"]) != "[map[board:board-a level:full name:Blerg]]" {
		t.Fatalf("reply boards %v", ch["boards"])
	}
	st, err := db.LatestBoardGrant(context.Background(), fx.pool, asString(ch["id"]), "board-a")
	if err != nil || st == nil || st.Grant.Level != db.BoardLevelFull {
		t.Fatalf("child row %+v %v", st, err)
	}
	// The same key with other boards is another request: refused, never a replay naming boards the
	// child does not have.
	key := newUUID()
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "same",
		"boards": []map[string]string{{"board": "Blerg", "level": "read"}}}, key); code != http.StatusCreated {
		t.Fatalf("keyed start: %d %v", code, out)
	}
	fx.spawn()
	if code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "same",
		"boards": []map[string]string{{"board": "Blerg", "level": "full"}}}, key); code != http.StatusConflict {
		t.Fatalf("same key, other boards: %d %v", code, out)
	}
	if code, out, hdr := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "same",
		"boards": []map[string]string{{"board": "Blerg", "level": "read"}}}, key); code != http.StatusCreated || hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same key, same boards: %d %v", code, out)
	}
	fx.noSpawn()
	// The board is renamed between two attempts with the same key: the same request, a replay.
	if _, err := fx.pool.Exec(context.Background(), `UPDATE session_proposal_grants SET board_name = 'Blerg renamed' WHERE session_id = $1::uuid AND board_id = 'board-a' AND revoked_at IS NULL`, fx.parent); err != nil {
		t.Fatal(err)
	}
	if code, out, hdr := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "same",
		"boards": []map[string]string{{"board": "board-a", "level": "read"}}}, key); code != http.StatusCreated || hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same key after a rename: %d %v", code, out)
	}
	fx.noSpawn()
	// Above the parent, even a rule does not start it.
	code, out, _ = fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "q",
		"boards": []map[string]string{{"board": "board-c", "level": "read"}}}, newUUID())
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown board under a rule: %d %v", code, out)
	}
	fx.noSpawn()
	// A replay of the same start never raises a level the person lowered since.
	if _, err := db.InsertProposalGrant(context.Background(), fx.pool, db.ProposalGrant{SessionID: asString(ch["id"]), AccountID: "owner-1",
		Kind: db.ProposalGrantBoard, BoardID: "board-a", Level: db.BoardLevelRead, ProofSessionID: testSID, CreatedVia: "panel"}); err != nil {
		t.Fatal(err)
	}
	fx.api.seedChildBoards(context.Background(), fx.row(fx.parent), asString(ch["id"]), []childBoard{{Board: "board-a", Name: "Blerg", Level: "full"}})
	if st, _ := db.LatestBoardGrant(context.Background(), fx.pool, asString(ch["id"]), "board-a"); st.Grant.Level != db.BoardLevelRead {
		t.Fatalf("replay raised the child to %s", st.Grant.Level)
	}
}

// A parent that may not hold board permissions (restricted, cron, board-started) passes none down.
func TestChildBoardsRefusedFromAParentWithoutPermissions(t *testing.T) {
	fx := newDelegFx(t)
	fx.giveParent("board-a", "Blerg", db.BoardLevelFull)
	if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET restrict_tools = true WHERE id = $1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.postChild(fx.parent, fx.parentTok, map[string]any{"engine": "claude", "repo": "app", "prompt": "p",
		"boards": []map[string]string{{"board": "board-a", "level": "read"}}}, newUUID())
	if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out["error"]), "cannot pass board access") {
		t.Fatalf("restricted parent: %d %v", code, out)
	}
}

// A child acts with its parent's sign-in for the board as it is now: not the sign-in its own row was
// stored with (the seed's, or the person's who lowered it), and a renewed parent level carries the
// child along.
func TestChildActsWithTheParentsCurrentSignIn(t *testing.T) {
	fx, fb, _ := newBoardLevelFx(t)
	parentA, personB, parentC := humanWith(fx, "login-parent-a"), humanWith(fx, "login-person-b"), humanWith(fx, "login-parent-c")
	c := newChildOf(t, fx)
	if code, body := setLevel(fx, parentA, "board-a", db.BoardLevelFull); code != 200 {
		t.Fatalf("parent: %d %s", code, body)
	}
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFull)
	if code, body := setChildLevel(fx, personB, c, "board-a", db.BoardLevelRead); code != 200 {
		t.Fatalf("lower the child from another sign-in: %d %s", code, body)
	}
	if st, _ := db.LatestBoardGrant(t.Context(), fx.pool, c.id, "board-a"); st.Grant.ProofSessionID != "login-person-b" {
		t.Fatalf("child row proof %q", st.Grant.ProofSessionID)
	}
	call := func(step, want string) {
		t.Helper()
		if code, body := childTool(fx, c, "board-a", "blerg_card_get"); code != 200 {
			t.Fatalf("%s: %d %s", step, code, body)
		}
		if got := fb.last().Proof.SessionID; got != want {
			t.Fatalf("%s: the child acted with %q, want the parent's %q", step, got, want)
		}
	}
	call("parent set from A", "login-parent-a")
	// The parent's level is renewed from a new sign-in C: the child follows it at once.
	if code, body := setLevel(fx, parentC, "board-a", db.BoardLevelFull); code != 200 {
		t.Fatalf("renew parent: %d %s", code, body)
	}
	call("parent renewed from C", "login-parent-c")
}

// The guide states the per-child limits and their bound with the numbers the runner enforces, and
// that a desktop child cannot file.
func TestChildBoardDocsStateTheLimitsAndTheFilingGap(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/talking-to-an-agent.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"A desktop child cannot file proposals yet.",
		fmt.Sprintf("%d board calls at once and %d board requests a minute per session", boardCallsInFlight, boardCallsPerMinute),
		fmt.Sprintf("at most %d filings an hour approved without asking per permission", defaultGrantHourlyCap),
		fmt.Sprintf("A parent runs at most %d children at once and an account at most %d", maxRunningChildrenPerParent, maxRunningChildrenPerAccount),
		fmt.Sprintf("up to %d more board calls in flight", maxRunningChildrenPerAccount*boardCallsInFlight),
		fmt.Sprintf("up to %d filings an hour without asking", (1+maxRunningChildrenPerParent)*defaultGrantHourlyCap),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docs/talking-to-an-agent.md does not say %q", want)
		}
	}
}
