package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func postRecover(t *testing.T, f *delegFx, token, session string, generation int64) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"host_generation":%d}`, session, generation)
	req := httptest.NewRequest("POST", "/api/messages/recover", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.api.HandleRecoverQuestions(w, req)
	return w
}

// postAsk creates a blocking ask through the real route, as the engine of one hosting.
func postAsk(t *testing.T, f *delegFx, token, session, body string, generation int64, extra string) string {
	t.Helper()
	raw := fmt.Sprintf(`{"session_id":%q,"kind":"ask","body":%q,"host_generation":%d%s}`, session, body, generation, extra)
	req := httptest.NewRequest("POST", "/api/messages", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.api.HandlePostMessages(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("post ask: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func TestChildTokenCannotCallQuestionRecovery(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id := postAsk(t, f, token, child, "Delete the production database?", 1, `,"person_required":true`)
	for _, attempt := range []string{token, f.parentTok} {
		if w := postRecover(t, f, attempt, child, 2); w.Code != http.StatusForbidden {
			t.Errorf("session credential reached recovery: %d %s", w.Code, w.Body.String())
		}
	}
	if row := mustChildMessage(t, f, id); row.Status != "open" || row.NoWait {
		t.Fatalf("session credential changed the question: %+v", row)
	}
}

func TestRecoveryKeepsPersonOnlyQuestionsForThePerson(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	required := postAsk(t, f, token, child, "Which production window?", 1, `,"person_required":true`)
	approval := postAsk(t, f, delegDaemonToken, child, "Allow the command?", 1, `,"owner_only":true`)
	escalated := postAsk(t, f, token, child, "Which region?", 1, "")
	row, err := db.GetSession(t.Context(), f.pool, child)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := db.GetSession(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.api.escalateChildQuestion(t.Context(), row, parent, escalated, "session:"+f.parent, "parent_requested"); err != nil {
		t.Fatal(err)
	}
	plain := postAsk(t, f, token, child, "Which branch?", 1, "")

	if w := postRecover(t, f, delegDaemonToken, child, 2); w.Code != 200 {
		t.Fatalf("recovery: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{required, approval, escalated} {
		got := mustChildMessage(t, f, id)
		if got.Status != "open" || !got.NoWait || !got.WaiterEnded || (id == escalated && !got.ChildEscalated) || (id != escalated && (!got.PersonRequired || got.ChildEscalated)) {
			t.Errorf("recovery removed a person-only question: %+v", got)
		}
	}
	if got := mustChildMessage(t, f, plain); got.Status != "withdrawn" {
		t.Errorf("recovery left a dead parent question open: %+v", got)
	}
	// Recovery preserves the requirement without bypassing Main. Only an explicit
	// escalation makes the held question answerable by the person.
	if err := f.api.escalateChildQuestion(t.Context(), row, parent, required); err != nil {
		t.Fatal(err)
	}
	reply := httptest.NewRequest("POST", "/api/messages/"+required+"/reply", strings.NewReader(`{"answer":"Tonight"}`))
	reply.SetPathValue("id", required)
	reply.Header.Set("Authorization", "Bearer "+f.human("owner-1"))
	w := httptest.NewRecorder()
	f.api.HandlePostMessageReply(w, reply)
	if w.Code != 200 {
		t.Fatalf("person answer: %d %s", w.Code, w.Body.String())
	}
	if got := mustChildMessage(t, f, required); got.Status != "answered" || !strings.HasPrefix(got.AnsweredBy, "person:") {
		t.Fatalf("person could not resolve held question: %+v", got)
	}
}

func TestRecoverySparesTheRestartedEnginesQuestions(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	dead := postAsk(t, f, token, child, "Asked before the restart?", 100, "")
	live := postAsk(t, f, token, child, "Asked by the restarted engine?", 200, "")

	// Recovery failed before the new engine asked, and succeeds only now. Two retries race.
	for range 2 {
		if w := postRecover(t, f, delegDaemonToken, child, 200); w.Code != 200 {
			t.Fatalf("recovery: %d %s", w.Code, w.Body.String())
		}
	}
	if got := mustChildMessage(t, f, dead); got.Status != "withdrawn" {
		t.Errorf("dead question stayed open: %+v", got)
	}
	if got := mustChildMessage(t, f, live); got.Status != "open" || got.NoWait {
		t.Fatalf("recovery reached the live engine's question: %+v", got)
	}
	// A retry left over from an earlier hosting cannot cross the newer boundary.
	if w := postRecover(t, f, delegDaemonToken, child, 100); w.Code != 200 {
		t.Fatalf("stale recovery: %d %s", w.Code, w.Body.String())
	}
	if got := mustChildMessage(t, f, live); got.Status != "open" {
		t.Fatalf("stale retry withdrew a newer question: %+v", got)
	}
	// Questions from a CLI too old to stamp its hosting belong to no live generation.
	unstamped := postAsk(t, f, token, child, "Asked by an older CLI?", 0, "")
	if w := postRecover(t, f, delegDaemonToken, child, 300); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := mustChildMessage(t, f, unstamped); got.Status != "withdrawn" {
		t.Errorf("unstamped question survived: %+v", got)
	}
}

func TestRecoveryNeedsAGeneration(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id := postAsk(t, f, token, child, "Which branch?", 1, "")
	if w := postRecover(t, f, delegDaemonToken, child, 0); w.Code != http.StatusBadRequest {
		t.Fatalf("recovery without a boundary: %d", w.Code)
	}
	if got := mustChildMessage(t, f, id); got.Status != "open" {
		t.Fatalf("unbounded recovery withdrew: %+v", got)
	}
}

func TestRecoveryCredentialIsTheHostsOwn(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	binding := *f.dc.DesktopBinding
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "bdt_fixture") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b := binding
		if strings.Contains(string(raw), "bdt_fixture-other") {
			b.DesktopID = "00000000-0000-4000-8000-0000000000d9"
		}
		_ = json.NewEncoder(w).Encode(desktopauth.Binding{DesktopID: b.DesktopID, OwnerID: b.OwnerID, Revision: b.Revision, TokenID: b.TokenID, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer core.Close()
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-internal-key"
	id := postAsk(t, f, token, session, "Which branch?", 1, "")
	if w := postRecover(t, f, "bdt_fixture-other", session, 2); w.Code != http.StatusForbidden {
		t.Fatalf("another desktop recovered this session: %d %s", w.Code, w.Body.String())
	}
	if got := mustChildMessage(t, f, id); got.Status != "open" {
		t.Fatalf("foreign desktop withdrew: %+v", got)
	}
	if w := postRecover(t, f, "bdt_fixture-mine", session, 2); w.Code != 200 {
		t.Fatalf("desktop recovery: %d %s", w.Code, w.Body.String())
	}
	if got := mustChildMessage(t, f, id); got.Status != "withdrawn" {
		t.Fatalf("desktop recovery did nothing: %+v", got)
	}
}

func TestRecoveryAcceptsThePodRuntimeTokenForItsOwnSession(t *testing.T) {
	f := newDelegFx(t)
	pod, message := f.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	other, _ := f.session(sessOpts{owner: "owner-1"})
	runtime, _, err := db.MintRuntimeLaunch(t.Context(), f.pool, pod, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id := postAsk(t, f, message, pod, "Which branch?", 1, "")
	foreign := postAsk(t, f, delegDaemonToken, other, "Another session?", 1, "")
	if w := postRecover(t, f, runtime, other, 2); w.Code != 200 {
		t.Fatalf("pod recovery: %d %s", w.Code, w.Body.String())
	}
	if got := mustChildMessage(t, f, foreign); got.Status != "open" {
		t.Fatalf("pod reached another session: %+v", got)
	}
	if got := mustChildMessage(t, f, id); got.Status != "withdrawn" {
		t.Fatalf("pod did not recover its own session: %+v", got)
	}
}

func TestOwnerlessEscalationReachesSignedInPeople(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{parent: f.parent})
	browser := &BrowserConn{ID: "b-person", AccountID: "person-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(browser)
	id := postAsk(t, f, token, child, "Which production window?", 0, `,"person_required":true`)
	if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}

	type frame struct {
		Type    string `json:"type"`
		Quiet   bool   `json:"quiet"`
		Message struct {
			ID         string `json:"id"`
			Answerable bool   `json:"answerable"`
		} `json:"message"`
	}
	deadline := time.After(5 * time.Second)
	for called := false; !called; {
		select {
		case raw := <-browser.send:
			var created frame
			called = json.Unmarshal(raw, &created) == nil && created.Type == "message_created" && created.Message.ID == id && created.Message.Answerable && !created.Quiet
		case <-deadline:
			t.Fatal("escalation never called a signed-in person")
		}
	}
	row := mustChildMessage(t, f, id)
	if infos := messageInfosFor(t.Context(), f.pool, []db.MessageRow{*row}, "person-1"); !infos[0].Answerable {
		t.Fatal("rebuilt feed drops the ownerless escalation")
	}
	if infos := messageInfosFor(t.Context(), f.pool, []db.MessageRow{*row}, ""); infos[0].Answerable {
		t.Fatal("agent or anonymous viewer offered the answer")
	}
	// The original owner rule holds where there is an owner.
	owned, ownedToken := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	ownedID := postAsk(t, f, ownedToken, owned, "Which window?", 0, `,"person_required":true`)
	ownedRow := mustChildMessage(t, f, ownedID)
	if infos := messageInfosFor(t.Context(), f.pool, []db.MessageRow{*ownedRow}, "person-1"); infos[0].Answerable {
		t.Fatal("another person was offered an owner's question")
	}
	// A pending ownerless child question stays with its parent.
	pending := postAsk(t, f, token, child, "Which branch?", 0, "")
	pendingRow := mustChildMessage(t, f, pending)
	if infos := messageInfosFor(t.Context(), f.pool, []db.MessageRow{*pendingRow}, "person-1"); infos[0].Answerable {
		t.Fatal("pending child question entered a person's inbox")
	}
}

func TestOwnerlessEscalationPushesUnlessPrivate(t *testing.T) {
	for _, private := range []bool{false, true} {
		f := newDelegFx(t)
		livePushCore(t)
		f.hub.SetSessionOwner(f.parent, f.dc.ID)
		child, token := f.session(sessOpts{parent: f.parent, private: private, startedByKind: "runner_key"})
		t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "test")
		t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "test")
		if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/ownerless", "key", "auth", "person-1", testSID); err != nil {
			t.Fatal(err)
		}
		saved := sendWebPush
		var pushes atomic.Int32
		sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
			pushes.Add(1)
			return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		id := postAsk(t, f, token, child, "Which production window?", 0, `,"person_required":true`)
		if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for !private && pushes.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if private {
			time.Sleep(100 * time.Millisecond)
		}
		sendWebPush = saved
		if got := pushes.Load() > 0; got == private {
			t.Fatalf("private=%t pushes=%d", private, pushes.Load())
		}
	}
}

// A clock rollback leaves the persisted counter one step above the dead hosting. The newer engine's
// question survives the older hosting's recovery landing after it.
func TestOutstandingOlderRecoverySparesTheHostingAfterARollback(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	const older = int64(1_900_000_000_000_000_000)
	dead := postAsk(t, f, token, session, "Before the restart?", older-1, "")
	live := postAsk(t, f, token, session, "After the rollback?", older+1, "")
	if w := postRecover(t, f, delegDaemonToken, session, older); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := mustChildMessage(t, f, live); got.Status != "open" {
		t.Fatalf("late recovery withdrew the newer question: %+v", got)
	}
	if got := mustChildMessage(t, f, dead); got.Status != "withdrawn" {
		t.Fatalf("dead question stayed: %+v", got)
	}
	floor, err := db.HighestHostGeneration(t.Context(), f.pool, session)
	if err != nil || floor != older+1 {
		t.Fatalf("floor = %d %v", floor, err)
	}
}
