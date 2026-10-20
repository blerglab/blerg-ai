package server

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
	"time"
)

func TestParentDiscardStopsKeptChildrenAndRetainsHistory(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	children := []string{}
	for _, keep := range []bool{false, true} {
		id, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "running"})
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=$2 WHERE id=$1`, id, keep); err != nil {
			t.Fatal(err)
		}
		fx.hub.SetSessionOwner(id, fx.dc.ID)
		children = append(children, id)
		seedEvent(t, fx.pool, id, time.Now(), "turn_done", turnDonePayload("fixture", 12, 3, 0, 0))
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("discard=%d want200", code)
	}
	for _, id := range append(children, fx.parent) {
		row := fx.row(id)
		if row.ArchivedAt == nil || row.EndedAt == nil {
			t.Fatal("not stopped and archived")
		}
		blocked, err := db.DelegationResumeBlocked(t.Context(), fx.pool, id)
		if err != nil || !blocked {
			t.Fatal("discarded session can resume")
		}
	}
	for _, id := range children {
		if fx.count(`SELECT count(*) FROM agent_events WHERE session_id=$1`, id) == 0 {
			t.Fatal("history lost")
		}
	}
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatal("discard retry not idempotent")
	}
}
func TestParentDiscardHumanOwnerOnly(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	for _, tok := range []string{fx.parentTok, fx.agentToken("owner-1"), fx.human("owner-2"), delegDaemonToken} {
		code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", tok, nil, nil)
		if code == 200 {
			t.Fatal("unauthorized discard")
		}
	}
	if fx.row(fx.parent).ArchivedAt != nil {
		t.Fatal("unauthorized archive")
	}
}
func TestParentDiscardOfflineRefusesWithoutFalseArchiveAndFencesKeptInput(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "running"})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatalf("offline discard=%d", code)
	}
	for _, id := range []string{fx.parent, child} {
		if fx.row(id).ArchivedAt != nil {
			t.Fatal("offline worker falsely archived")
		}
		blocked, err := db.DelegationResumeBlocked(t.Context(), fx.pool, id)
		if err != nil || !blocked {
			t.Fatal("partial discard did not fence recovery")
		}
	}
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("retry=%d", code)
	}
}

func TestParentDiscardQueueFailureDoesNotTreatIntentAsStopped(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	for len(fx.dc.send) < cap(fx.dc.send) {
		fx.dc.send <- []byte(`{"type":"fixture"}`)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatalf("full queue=%d", code)
	}
	row := fx.row(fx.parent)
	if row.DiscardRequestedAt == nil || row.EndedAt != nil || row.ArchivedAt != nil {
		t.Fatal("intent falsely became actual stop")
	}
	for len(fx.dc.send) > 0 {
		<-fx.dc.send
	}
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("retry=%d", code)
	}
}
func TestParentDiscardForeignLineageFailsClosed(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent})
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatalf("foreignlineage=%d", code)
	}
	if fx.row(child).EndedAt != nil || fx.row(fx.parent).DiscardRequestedAt != nil {
		t.Fatal("discard crossed foreign authority")
	}
}
func TestParentDiscardLateNativeWritesAndEventsCannotRevive(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	for _, id := range []string{fx.parent, child} {
		err := db.ReviveSessionAs(t.Context(), fx.pool, id, fx.dc.ID, "running", "/fixture", "", "", "fixture", db.SessionOrigin{})
		if !errors.Is(err, db.ErrDelegationEnded) {
			t.Fatalf("nativeupsert=%v", err)
		}
		if err = db.UpdateSessionStatusGuarded(t.Context(), fx.pool, id, "running", nil); !errors.Is(err, db.ErrDelegationEnded) {
			t.Fatalf("nativestatus=%v", err)
		}
		before := fx.count(`SELECT count(*) FROM agent_events WHERE session_id=$1`, id)
		HandleAgentEvent(t.Context(), fx.hub, fx.pool, fx.dc, protocol.AgentEvent{SessionID: id, ClientEventID: newUUID(), Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)})
		if fx.count(`SELECT count(*) FROM agent_events WHERE session_id=$1`, id) != before {
			t.Fatal("discarded runtime event accepted")
		}
	}
}
func TestParentDiscardOfflinePreexistingTerminalIsAllowed(t *testing.T) {
	fx := newDelegFx(t)
	at := time.Now().Add(-time.Minute)
	if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", &at, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("terminaloffline=%d", code)
	}
}
func TestParentDiscardPostIntentSyntheticEndCannotBypassDelivery(t *testing.T) {
	fx := newDelegFx(t)
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatal(code)
	}
	at := time.Now()
	if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", &at, systemEnd(db.EndReasonParentEnded)); err != nil {
		t.Fatal(err)
	}
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 || fx.row(fx.parent).ArchivedAt != nil {
		t.Fatal("unconfirmed syntheticend bypassed delivery")
	}
}
