package server

// Children file board proposals through their parent's sign-in (0.4.5): a session started by another
// one has no board sign-in of its own, so its filings (ticket file, blerg_ticket_file) discover and
// prepare with its parent's, as its other board calls act with the parent's sign-in. What it may file
// without asking stays capped by min(its own level, the parent's level now), checked again right
// before the filing executes.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// signInLister is the core board list with a set of ended sign-ins, recording each proof it saw.
type signInLister struct {
	filingBoardList
	mu    sync.Mutex
	dead  map[string]bool
	proof []mcpgw.Proof
}

func (l *signInLister) ListSessionBoards(ctx context.Context, p mcpgw.Proof) ([]SessionBoard, error) {
	l.mu.Lock()
	l.proof = append(l.proof, p)
	dead := l.dead[p.SessionID]
	l.mu.Unlock()
	if dead {
		return nil, ErrMCPProofInvalid
	}
	return l.filingBoardList.ListSessionBoards(ctx, p)
}

func (l *signInLister) ListConnections(_ context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dead[p.SessionID] {
		return nil, ErrMCPProofInvalid
	}
	return nil, nil
}

func (l *signInLister) end(sid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dead[sid] = true
}

func (l *signInLister) last() mcpgw.Proof {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.proof) == 0 {
		return mcpgw.Proof{}
	}
	return l.proof[len(l.proof)-1]
}

// newChildFilingFx is the board levels fixture with a recording core list and a child of fx.sess
// that has no board sign-in of its own (as a desktop child has none).
func newChildFilingFx(t *testing.T) (*topicFx, *fixtureFiling, *signInLister, childFx) {
	t.Helper()
	fx, _, up := newBoardLevelFx(t)
	cfg := *fx.api.hub.mcpStart.get()
	l := &signInLister{dead: map[string]bool{}}
	cfg.Core = l
	fx.api.hub.mcpStart.set(&cfg)
	c := newChildOf(t, fx)
	if _, _, _, err := db.BoardFilingProof(t.Context(), fx.pool, c.id); err == nil {
		t.Fatal("the child fixture has a board sign-in of its own")
	}
	return fx, up, l, c
}

func childFile(fx *topicFx, c childFx, title string) (int, map[string]any) {
	return fx.postWork("file-ticket", c.id, c.tok, map[string]any{"board": "A", "title": title, "body": "Fixture details"})
}

func proposalRow(t *testing.T, fx *topicFx, id string) (account, session, state string) {
	t.Helper()
	if err := fx.pool.QueryRow(t.Context(), `SELECT account_id, session_id, state FROM mcp_proposals WHERE id = $1`, id).Scan(&account, &session, &state); err != nil {
		t.Fatal(err)
	}
	return
}

// A child at Propose (the default: nothing passed down) files with its parent's sign-in, and the
// filing waits for the person as a proposal.
func TestChildAtProposeFilesAProposalForTheOwner(t *testing.T) {
	fx, up, l, c := newChildFilingFx(t)
	code, out := childFile(fx, c, "From a child at propose")
	if code != 202 || out["state"] != "pending" || out["auto_approved"] != nil || up.creates != 0 {
		t.Fatalf("child at propose: %d %v (creates %d)", code, out, up.creates)
	}
	if p := l.last(); p.SessionID != "fixture-login" || p.AccountID != topicOwner {
		t.Fatalf("the child's filing discovered boards with %+v, not its parent's sign-in", p)
	}
	id, _ := out["proposal_id"].(string)
	if account, session, state := proposalRow(t, fx, id); account != topicOwner || session != c.id || state != db.ProposalPending {
		t.Fatalf("proposal %s: account=%s session=%s state=%s", id, account, session, state)
	}
	// Board discovery and duplicate search work for the child the same way.
	rec := filingRequest(fx, http.MethodGet, "/api/sessions/"+c.id+"/board-options", c.tok, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"board-a"`) {
		t.Fatalf("child boards: %d %s", rec.Code, rec.Body.String())
	}
}

// A child given File without asking files without asking, acting with the parent's current sign-in
// for the board; when the parent is lowered, the child's next filing becomes a proposal.
func TestChildAtFileFilesWithoutAskingUntilTheParentIsLowered(t *testing.T) {
	fx, up, _, c := newChildFilingFx(t)
	human := fx.token(topicOwner, "human")
	setLevel(fx, human, "board-a", db.BoardLevelFull)
	passDown(t, fx, c.id, "board-a", "A", db.BoardLevelFile)

	code, out := childFile(fx, c, "From a child at file")
	if code != 200 || out["state"] != "done" || out["auto_approved"] != true || up.creates != 1 {
		t.Fatalf("child at file: %d %v (creates %d)", code, out, up.creates)
	}
	if p := up.proofs[len(up.proofs)-1]; p.SessionID != testSID || p.AccountID != topicOwner {
		t.Fatalf("the child's filing was created with %+v, not the parent's sign-in for the board", p)
	}

	setLevel(fx, human, "board-a", db.BoardLevelRead)
	code, out = childFile(fx, c, "After the parent was lowered")
	if code != 202 || out["state"] != "pending" || out["auto_approved"] != nil || up.creates != 1 {
		t.Fatalf("child under a lowered parent: %d %v (creates %d)", code, out, up.creates)
	}
}

// A child's filing is refused once its parent's sign-in has ended, and once the parent has been
// stopped for good: it never files with a sign-in the parent no longer lends it.
func TestChildFilingIsRefusedWhenTheParentCannotLendItsSignIn(t *testing.T) {
	fx, up, l, c := newChildFilingFx(t)
	if code, out := childFile(fx, c, "Before"); code != 202 {
		t.Fatalf("before: %d %v", code, out)
	}
	before := countProposals(t, fx, c.id)

	l.end("fixture-login")
	code, out := childFile(fx, c, "Parent signed out")
	if code != 409 || !strings.Contains(out["error"].(string), "sign-in of the session that started it") || !strings.Contains(out["error"].(string), "ended") {
		t.Fatalf("parent signed out: %d %v", code, out)
	}
	if n := countProposals(t, fx, c.id); n != before || up.creates != 0 {
		t.Fatalf("a refused filing queued a proposal: %d -> %d (creates %d)", before, n, up.creates)
	}

	// The parent stops for good: the child's filings are refused before any board call.
	d := newChildOf(t, fx)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_ended_at = clock_timestamp() WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	calls := len(l.proof)
	code, out = childFile(fx, d, "Parent stopped for good")
	if code != 409 || !strings.Contains(out["error"].(string), "ended for good") || len(l.proof) != calls {
		t.Fatalf("parent stopped for good: %d %v (board lists %d -> %d)", code, out, calls, len(l.proof))
	}
}

func countProposals(t *testing.T, fx *topicFx, session string) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM mcp_proposals WHERE session_id = $1`, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Review M1: a child that has a sign-in of its own on record (a cluster child's start, or its owner
// opening its page) still files only with its parent's, so a parent stopped for good stops the
// child's filing too.
func TestChildsOwnSignInIsIgnoredForFiling(t *testing.T) {
	fx, up, l, c := newChildFilingFx(t)
	if err := db.SaveBoardFilingProof(t.Context(), fx.pool, c.id, "session_id", "child-own-login", ""); err != nil {
		t.Fatal(err)
	}
	code, out := childFile(fx, c, "With a parent")
	if code != 202 || l.last().SessionID != "fixture-login" {
		t.Fatalf("with a parent: %d %v, discovered with %+v", code, out, l.last())
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET delegation_ended_at = clock_timestamp() WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	calls := len(l.proof)
	code, out = childFile(fx, c, "Parent stopped for good")
	if code != 409 || up.creates != 0 || len(l.proof) != calls {
		t.Fatalf("parent stopped for good, child has its own sign-in: %d %v (board lists %d -> %d)", code, out, calls, len(l.proof))
	}
}
