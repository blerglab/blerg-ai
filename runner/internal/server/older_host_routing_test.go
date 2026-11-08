package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// legacyNativeHost is the input path of a daemon at c7a6096b, line for line where it decides:
// Loop.EnqueueInput hands anything to a waiting ask, and the ask's poll answers through the same
// call. The chain under test is the server's; this is the host it must work with.
type legacyNativeHost struct {
	t       *testing.T
	f       *delegFx
	session string
	base    string

	mu    sync.Mutex
	askCh chan string
	queue []string

	taken chan string // what the ask returned to the model
	id    string
}

func (l *legacyNativeHost) enqueue(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.askCh != nil {
		select {
		case l.askCh <- text:
			return
		default:
		}
	}
	l.queue = append(l.queue, text)
}

// pump is the daemon's socket: every input frame the server sends reaches Loop.EnqueueInput.
func (l *legacyNativeHost) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case raw := <-l.f.dc.send:
			var frame protocol.AgentUserMessage
			if json.Unmarshal(raw, &frame) == nil && frame.Type == "agent_user_message" && frame.SessionID == l.session {
				l.enqueue(frame.Text)
			}
		}
	}
}

func (l *legacyNativeHost) status(status string) {
	if err := HandleSessionStateChanged(context.Background(), l.f.hub, l.f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: l.session, Status: status}); err != nil {
		l.t.Errorf("status %s: %v", status, err)
	}
}

// ask follows the old askTool: open the channel, post, poll, and take whatever arrives first.
func (l *legacyNativeHost) ask(ctx context.Context, question string) {
	ch := make(chan string, 1)
	l.mu.Lock()
	l.askCh = ch
	l.mu.Unlock()
	l.id = postAsk(l.t, l.f, delegDaemonToken, l.session, question, 0, "")
	l.status("waiting")
	go func() {
		for ctx.Err() == nil {
			req, _ := http.NewRequestWithContext(ctx, "GET", l.base+"/api/messages/"+l.id+"/answer?wait=1", nil)
			req.Header.Set("Authorization", "Bearer "+delegDaemonToken)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			var out struct {
				Answered bool    `json:"answered"`
				Answer   *string `json:"answer"`
			}
			_ = json.NewDecoder(res.Body).Decode(&out)
			_ = res.Body.Close()
			if out.Answered {
				if out.Answer != nil {
					l.enqueue(*out.Answer)
				}
				return
			}
		}
	}()
	go func() {
		select {
		case text := <-ch:
			l.mu.Lock()
			l.askCh = nil
			l.mu.Unlock()
			l.status("running")
			l.taken <- text
		case <-ctx.Done():
		}
	}()
}

func startLegacyHost(t *testing.T, f *delegFx, session string) (*legacyNativeHost, context.Context) {
	t.Helper()
	f.dc.SetAuthoritativeAnswers(false)
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(session, f.dc.ID)
	srv := questionRoutes(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	l := &legacyNativeHost{t: t, f: f, session: session, base: srv.URL, taken: make(chan string, 1)}
	drain(f.dc.send)
	go l.pump(ctx)
	return l, ctx
}

func (l *legacyNativeHost) result() string {
	l.t.Helper()
	select {
	case text := <-l.taken:
		return text
	case <-time.After(5 * time.Second):
		l.t.Fatal("the ask never resolved")
		return ""
	}
}

func replyAs(t *testing.T, f *delegFx, id, token, answer string) {
	t.Helper()
	rec := privDo(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		f.api.HandlePostMessageReply(w, r)
	}, "POST", "/api/messages/"+id+"/reply", "", token, fmt.Sprintf(`{"answer":%q}`, answer))
	if rec.Code != 200 {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body.String())
	}
}

func routingOffNotes(t *testing.T, f *delegFx, session string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND payload::text LIKE '%Updating its daemon%'`, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Both sides of the capability, through the real route: the same worker question.
func TestWorkerQuestionRoutingFollowsTheHostCapability(t *testing.T) {
	ask := func(t *testing.T, authoritative bool) (*delegFx, string, string, db.MessageRow) {
		t.Helper()
		f := newDelegFx(t)
		child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
		f.dc.SetAuthoritativeAnswers(authoritative)
		f.hub.SetSessionOwner(child, f.dc.ID)
		id := postAsk(t, f, token, child, "Which branch?", 0, "")
		return f, child, token, *mustChildMessage(t, f, id)
	}
	t.Run("a current daemon asks the parent", func(t *testing.T) {
		f, child, _, row := ask(t, true)
		if row.ChildEscalated {
			t.Fatalf("escalated: %+v", row)
		}
		if n := routingOffNotes(t, f, child); n != 0 {
			t.Fatalf("notes = %d", n)
		}
	})
	t.Run("an older daemon stops waiting, with one note", func(t *testing.T) {
		f, child, token, row := ask(t, false)
		if row.ChildEscalated || row.Status != "withdrawn" {
			t.Fatalf("ordinary question not withdrawn: %+v", row)
		}
		postAsk(t, f, token, child, "Which tag?", 0, "")
		if n := routingOffNotes(t, f, child); n != 1 {
			t.Fatalf("notes = %d", n)
		}
	})
}

// Input behaves as in 0.5.4: nothing is held, and the old loop takes what reaches it.
func TestOlderDaemonInputIsNotHeld(t *testing.T) {
	f := newDelegFx(t)
	session, _ := f.session(sessOpts{owner: "owner-1"})
	host, ctx := startLegacyHost(t, f, session)
	host.ask(ctx, "Which branch?")
	out, problem := f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, "board note", "blerg-board")
	if problem != nil || out["queued"] == true {
		t.Fatalf("held: %v %v", out, problem)
	}
	if got := host.result(); got != "board note" {
		t.Fatalf("ask result = %q", got)
	}
	if pending, err := db.HasPendingSessionInputs(ctx, f.pool, session); err != nil || pending {
		t.Fatalf("pending=%t err=%v", pending, err)
	}
}

func TestOlderDaemonChatStaysOrdinaryUntilExplicitAnswer(t *testing.T) {
	f := newDelegFx(t)
	session, _ := f.session(sessOpts{owner: "owner-1"})
	host, ctx := startLegacyHost(t, f, session)
	host.ask(ctx, "Which branch?")
	out, problem := f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, "main", "human")
	if problem != nil || out["answered_ask"] == true {
		t.Fatalf("chat did not answer: %v %v", out, problem)
	}
	replyAs(t, f, host.id, f.human("owner-1"), "main")
	if got := host.result(); got != "main" {
		t.Fatalf("ask result = %q", got)
	}
}

// The old loop cannot manufacture an answer after an ordinary question was withdrawn.
func TestOlderLoopCannotAnswerAWithdrawnWorkerQuestion(t *testing.T) {
	closeWith := func(t *testing.T, f *delegFx, id string) int {
		t.Helper()
		return privDo(func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("id", id)
			f.api.HandlePostMessageReply(w, r)
		}, "POST", "/api/messages/"+id+"/reply", "", delegDaemonToken, `{"answer":"board note"}`).Code
	}
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	f.dc.SetAuthoritativeAnswers(false)
	f.hub.SetSessionOwner(child, f.dc.ID)
	required := postAsk(t, f, token, child, "Which window?", 0, `,"person_required":true`)
	if code := closeWith(t, f, required); code != 403 {
		t.Fatalf("daemon resolved a person-only question: %d", code)
	}
	unrouted := postAsk(t, f, token, child, "Which branch?", 0, "")
	f.dc.SetAuthoritativeAnswers(true)
	if code := closeWith(t, f, unrouted); code != 403 {
		t.Fatalf("an upgraded host's token resolved it: %d", code)
	}
	f.dc.SetAuthoritativeAnswers(false)
	if code := closeWith(t, f, unrouted); code != 403 {
		t.Fatalf("old loop changed a withdrawn question: %d", code)
	}
}

// A hello changes the decision with no timer: down closes the ordinary question without an answer at the
// next sweep, up routes the next question.
func TestRoutingFollowsTheDaemonsNextHello(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	f.hub.SetSessionOwner(child, f.dc.ID)
	f.dc.SetAuthoritativeAnswers(true)
	open := postAsk(t, f, token, child, "Which branch?", 0, "")
	if row := mustChildMessage(t, f, open); row.ChildEscalated {
		t.Fatalf("routed question escalated: %+v", row)
	}
	f.dc.SetAuthoritativeAnswers(false)
	if err := f.api.reconcileChildQuestions(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if row := mustChildMessage(t, f, open); row.ChildEscalated || row.Status != "withdrawn" {
		t.Fatalf("downgrade left it with the parent: %+v", row)
	}
	if n := routingOffNotes(t, f, child); n != 1 {
		t.Fatalf("notes = %d", n)
	}
	f.dc.SetAuthoritativeAnswers(true)
	next := postAsk(t, f, token, child, "Which tag?", 0, "")
	if row := mustChildMessage(t, f, next); row.ChildEscalated {
		t.Fatalf("upgrade did not route: %+v", row)
	}
}

// A person-only question stays protected on an older daemon too.
func TestOlderDaemonKeepsPersonOnlyFromTheParent(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	f.dc.SetAuthoritativeAnswers(false)
	f.hub.SetSessionOwner(child, f.dc.ID)
	id := postAsk(t, f, token, child, "Which window?", 0, `,"person_required":true`)
	if code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "now"}, nil); code != 403 {
		t.Fatalf("parent answered: %d %v", code, body)
	}
}
