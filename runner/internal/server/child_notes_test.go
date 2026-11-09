package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestChildNotesReachParentAndResult(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-daemon", true: "current-daemon"}[receipts], func(t *testing.T) {
			f := newDelegFx(t)
			f.dc.SetInputReceipts(receipts)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", private: true})
			for _, text := range []string{"First private note", "Second private note"} {
				rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"note","body":"`+text+`"}`)
				if rec.Code != http.StatusCreated {
					t.Fatalf("note: %d %s", rec.Code, rec.Body.String())
				}
				notice := f.parentNote(f.parent, time.Second)
				if !strings.Contains(notice.Text, "kind=note") || strings.Contains(notice.Text, text) {
					t.Fatalf("missing or unsafe parent notice: %+v", notice)
				}
			}
			code, result, _ := f.do("GET", childRoute(f.parent, child, "/result"), f.parentTok, nil, nil)
			raw, _ := json.Marshal(result)
			if code != http.StatusOK || !strings.Contains(string(raw), "First private note") || !strings.Contains(string(raw), "Second private note") {
				t.Fatalf("result lost notes: %d %s", code, raw)
			}
			var pending int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM messages WHERE session_id=$1 AND NOT child_parent_notified`, child).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatal("note notification was not persisted")
			}
		})
	}
}

func TestWorkerNoteNoticeNamesExplicitRead(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	got := workerNotice(id, "note", id)
	if !strings.Contains(got, "kind=note") || !strings.Contains(got, "session result "+id) {
		t.Fatalf("note not identified for Main: %s", got)
	}
}

func TestChildNotesRetryWithoutAlertingPerson(t *testing.T) {
	f := newDelegFx(t)
	livePushCore(t)
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "test")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "test")
	if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example.com/notes", "key", "auth", "owner-1", testSID); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	pushes := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		pushes++
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
	}
	t.Cleanup(func() { sendWebPush = saved })
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", private: true})
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	browser := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(browser)
	// With Main offline the note remains pending, rather than becoming a question.
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"note","body":"Information, no decision"}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if pushes != 0 {
		t.Fatal("worker information pushed to person")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for raw := range awaitAll(browser.send) {
		var event protocol.MessageCreated
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "message_created" && event.Message.ID == created.ID && (!event.Quiet || event.Message.Answerable) {
			t.Fatalf("note alerted person: %s", raw)
		}
	}
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	if err := f.api.reconcileChildNotes(t.Context()); err != nil {
		t.Fatal(err)
	}
	notice := f.parentNote(f.parent, time.Second)
	if !isWorkerNotice(notice.Text, child, "note") {
		t.Fatalf("pending note lost: %+v", notice)
	}
	if err := f.api.reconcileChildNotes(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Check the synchronous relay frames directly: a repeat sweep must send nothing.
	for raw := range awaitAll(f.dc.send) {
		var event protocol.AgentUserMessage
		_ = json.Unmarshal(raw, &event)
		if event.Type == "agent_user_message" && event.SessionID == f.parent {
			t.Fatalf("duplicate note: %s", raw)
		}
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, child)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range messageInfosFor(t.Context(), f.pool, rows, "owner-1") {
		if info.Kind == "ask" || info.Answerable {
			t.Fatalf("note entered Needs you: %+v", info)
		}
	}
}

func TestWorkerNoteDeliveryKeyReserved(t *testing.T) {
	if !reservedClientID("child-note-11111111-1111-4111-8111-111111111111") {
		t.Fatal("worker note outbox key can be claimed by callers")
	}
}

func TestChildNotesBacklogDoesNotStarveNewNotes(t *testing.T) {
	f := newDelegFx(t)
	oldParent, _ := f.session(sessOpts{owner: "owner-1"})
	oldChild, _ := f.session(sessOpts{owner: "owner-1", parent: oldParent, startedByKind: "session"})
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, oldParent); err != nil {
		t.Fatal(err)
	}
	endedParent, _ := f.session(sessOpts{owner: "owner-1"})
	endedChild, _ := f.session(sessOpts{owner: "owner-1", parent: endedParent, startedByKind: "session"})
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),end_reason='stopped_by_agent' WHERE id=$1`, endedParent); err != nil {
		t.Fatal(err)
	}
	for i := range 101 {
		target := oldChild
		if i%2 == 0 {
			target = endedChild
		}
		if _, err := db.CreateMessage(t.Context(), f.pool, target, "note", "Old information"); err != nil {
			t.Fatal(err)
		}
	}
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	id, err := db.CreateMessage(t.Context(), f.pool, child, "note", "New information")
	if err != nil {
		t.Fatal(err)
	}
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	for range 2 {
		if err := f.api.reconcileChildNotes(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var notified bool
	if err := f.pool.QueryRow(t.Context(), `SELECT child_parent_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if !notified {
		t.Fatal("old undeliverable notes starved new note")
	}
	var pending int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM messages WHERE session_id IN ($1,$2) AND NOT child_parent_notified`, oldChild, endedChild).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("undeliverable notes still pending: %d", pending)
	}
}

func TestChildNotesResultNewestTwenty(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	var newest string
	for i := range 25 {
		id, err := db.CreateMessage(t.Context(), f.pool, child, "note", "Information")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET created_at=now()+$2*interval '1 second' WHERE id=$1`, id, i); err != nil {
			t.Fatal(err)
		}
		newest = id
	}
	other, err := db.CreateMessage(t.Context(), f.pool, child, "update", "Other message")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET created_at=now()+interval '1 day' WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	code, result, _ := f.do("GET", childRoute(f.parent, child, "/result"), f.parentTok, nil, nil)
	raw, _ := json.Marshal(result)
	var body struct {
		Notes []struct {
			MessageID string `json:"message_id"`
		} `json:"notes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || len(body.Notes) != 20 || body.Notes[0].MessageID != newest {
		t.Fatalf("expected newest 20 notes: %d %s", code, raw)
	}
}

func TestChildNotesPostDeliversOnlyPostedNote(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	old, err := db.CreateMessage(t.Context(), f.pool, child, "note", "Pending information")
	if err != nil {
		t.Fatal(err)
	}
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"note","body":"Current information"}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	var notified bool
	if err := f.pool.QueryRow(t.Context(), `SELECT child_parent_notified FROM messages WHERE id=$1`, old).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified {
		t.Fatal("posting one note swept unrelated backlog")
	}
}

func TestChildNotesUpgradeSettlesHistoricalNotes(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	id, err := db.CreateMessage(t.Context(), f.pool, child, "note", "Historical information")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../db/migrations/172_child_notes_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	var notified bool
	if err := f.pool.QueryRow(t.Context(), `SELECT child_parent_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if !notified {
		t.Fatal("historical note pending after upgrade")
	}
}
