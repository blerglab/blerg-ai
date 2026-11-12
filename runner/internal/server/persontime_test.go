package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPersonTimePreferenceValidation(t *testing.T) {
	for _, raw := range []string{`{"timezone":"America/New_York"}`, `{"timezone":"UTC"}`} {
		if _, err := validatePreference("person_time", json.RawMessage(raw)); err != nil {
			t.Error(err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"timezone":"Local"}`, `{"timezone":"Mars/Olympus"}`, `{"timezone":"UTC","extra":1}`} {
		if _, err := validatePreference("person_time", json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestPersonTimePropagationWithoutPlanNudges(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	row, err := db.GetSession(context.Background(), fx.pool, fx.sess)
	if err != nil || row.SpawningAccountID == nil {
		t.Fatal("fixture needs an owner", err)
	}
	if err := db.PutAccountPreference(context.Background(), fx.pool, *row.SpawningAccountID, "person_time", json.RawMessage(`{"timezone":"America/New_York"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPlanNudges(context.Background(), fx.pool, fx.sess, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "Continue"})
	out := fx.api.attachPlanNudge(&DaemonConn{planNudges: false}, raw)
	if !strings.Contains(string(out), `"timezone":"America/New_York"`) {
		t.Fatalf("time missing from input: %s", out)
	}
}

func TestPersonTimeInternalAccountSettings(t *testing.T) {
	fx := prefsFx(t)
	fx.api.coreInternalKey = "account-clock-test-key"
	call := func(method, account, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/internal/account-time", strings.NewReader(body))
		r.Header.Set("X-Internal-Key", key)
		r.Header.Set("X-Blerg-Account", account)
		w := httptest.NewRecorder()
		fx.api.HandleInternalAccountTime(w, r)
		return w
	}
	if w := call("GET", "11111111-1111-4111-8111-111111111111", "", ""); w.Code != 401 {
		t.Fatalf("missing service key: %d", w.Code)
	}
	if w := call("PUT", "11111111-1111-4111-8111-111111111111", fx.api.coreInternalKey, `{"timezone":"America/New_York"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "11111111-1111-4111-8111-111111111111", fx.api.coreInternalKey, `{"timezone":"UTC","initialize":true}`); !strings.Contains(w.Body.String(), "America/New_York") {
		t.Fatal("browser initialization overwrote account setting")
	}
	if w := call("GET", "22222222-2222-4222-8222-222222222222", fx.api.coreInternalKey, ""); strings.Contains(w.Body.String(), "America/New_York") {
		t.Fatal("cross-account read")
	}
	if w := call("PUT", "11111111-1111-4111-8111-111111111111", fx.api.coreInternalKey, `{"timezone":"Local"}`); w.Code != 400 {
		t.Fatal("accepted server local zone")
	}
}

func TestPersonTimeBrowserInitializationPreservesSavedZone(t *testing.T) {
	fx := prefsFx(t)
	human := fx.human()
	if w := callPrefs(fx, "PUT", human, `{"person_time":{"timezone":"America/New_York"}}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	req := httptest.NewRequest("PUT", "/api/me/preferences", strings.NewReader(`{"person_time":{"timezone":"Europe/Paris"}}`))
	req.Header.Set("Authorization", "Bearer "+human)
	req.Header.Set("If-None-Match", "*")
	w := httptest.NewRecorder()
	fx.api.HandlePutMyPreferences(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := callPrefs(fx, "GET", human, "").Body.String(); !strings.Contains(got, "America/New_York") {
		t.Fatal("browser overwrote saved zone", got)
	}
}

func TestPersonTimeSharedViewerAndWorkerUseOwnerClock(t *testing.T) {
	fx := newDelegFx(t)
	for account, zone := range map[string]string{"owner-1": "America/New_York", "viewer": "Europe/Paris"} {
		raw, _ := json.Marshal(map[string]string{"timezone": zone})
		if err := db.PutAccountPreference(t.Context(), fx.pool, account, prefPersonTime, raw); err != nil {
			t.Fatal(err)
		}
	}
	worker, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	p, ok := fx.api.verifyBrowserToken(fx.human("viewer"))
	if !ok {
		t.Fatal("viewer fixture")
	}
	for _, sid := range []string{fx.parent, worker} {
		fx.hub.SetSessionOwner(sid, fx.dc.ID)
		frame := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: sid, Text: "Continue", ClientID: newUUID()}
		handleBrowserAgentMessage(t.Context(), fx.hub, &BrowserConn{AccountID: p.Sub, SessionID: p.Sid, verified: p}, fx.pool, frame)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		found := false
		for !found {
			select {
			case raw := <-fx.dc.send:
				var in protocol.AgentUserMessage
				if json.Unmarshal(raw, &in) == nil && in.Type == "agent_user_message" && in.ClientID == frame.ClientID {
					if in.PersonTime == nil || in.PersonTime.Timezone != "America/New_York" {
						t.Fatalf("viewer replaced owner clock: %+v", in.PersonTime)
					}
					found = true
				}
			case <-ctx.Done():
				cancel()
				t.Fatal("viewer input did not reach daemon")
			}
		}
		cancel()
	}
}

func TestPersonTimePartialPutReplacesWindow(t *testing.T) {
	fx := prefsFx(t)
	fx.api.coreInternalKey = "clock-replacement-key"
	account := "11111111-1111-4111-8111-111111111111"
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO held_pushes(account_id,target,title,body,lease_until) VALUES($1,'','Activity','Update',now()+interval '12 hours')`, account); err != nil {
		t.Fatal(err)
	}
	if err := db.PutAccountPreference(t.Context(), fx.pool, account, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York","quiet_hours":{"enabled":true,"start":"22:00","end":"07:00"}}`)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/internal/account-time", strings.NewReader(`{"timezone":"UTC"}`))
	req.Header.Set("X-Internal-Key", fx.api.coreInternalKey)
	req.Header.Set("X-Blerg-Account", account)
	w := httptest.NewRecorder()
	fx.api.HandleInternalAccountTime(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "quiet_hours") {
		t.Fatal("partial PUT retained an omitted window", w.Code, w.Body.String())
	}
	var released bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT lease_until<=now() FROM held_pushes WHERE account_id=$1`, account).Scan(&released); err != nil || !released {
		t.Fatal("changed clock did not release old quiet lease", err)
	}
}

func TestPersonTimeDiagnosticsKeepCausesPrivate(t *testing.T) {
	f := newPrivFixture(t)
	var output bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	const canary = "clock-diagnostic-private-canary"
	logPersonTimeFailure("canary", errors.New(canary))
	closed, err := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	f.api.dbPool = closed
	f.api.coreInternalKey = "clock-diagnostic-key"
	for _, method := range []string{"GET", "PUT"} {
		r := httptest.NewRequest(method, "/internal/account-time", strings.NewReader(`{"timezone":"UTC"}`))
		r.Header.Set("X-Internal-Key", f.api.coreInternalKey)
		r.Header.Set("X-Blerg-Account", "11111111-1111-4111-8111-111111111111")
		w := httptest.NewRecorder()
		f.api.HandleInternalAccountTime(w, r)
		if w.Code != 500 {
			t.Fatal(w.Code)
		}
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession})
	f.api.attachPersonTime(raw)
	held := holdQuietPush(t.Context(), closed, privAcctA, "Work", "Update", "", time.Now())
	if held {
		t.Fatal("diagnostic path failed closed")
	}
	if err := flushHeldPushes(t.Context(), closed, time.Now()); err == nil {
		t.Fatal("closed pool did not fail")
	}
	f.api.dbPool = f.pool
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE account_preferences RENAME TO temporarily_unavailable_clock`); err != nil {
		t.Fatal(err)
	}
	f.api.attachPersonTime(raw)
	for _, stage := range []string{"canary", "read_account", "save_account", "input_session", "input_account", "push_clock_read", "push_drain"} {
		if !strings.Contains(output.String(), "stage="+stage) {
			t.Errorf("missing diagnostic stage %s", stage)
		}
	}
	if !strings.Contains(output.String(), "cause=") || strings.Contains(output.String(), canary) || strings.Contains(output.String(), "clock-diagnostic-key") {
		t.Fatal("cause missing or private data logged")
	}
}

func TestPersonTimeClockOnlyRefreshPreservesPlanFacts(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Work")
	fx.event("tool_call", `{"tool":"Bash","input":{"command":"make"}}`)
	seq := fx.event("turn_done", `{}`)
	fx.api.checkPlanTurn(t.Context(), fx.sess, seq, time.Now(), "end_turn")
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil || row.SpawningAccountID == nil {
		t.Fatal(err)
	}
	if err := db.PutAccountPreference(t.Context(), fx.pool, *row.SpawningAccountID, prefPersonTime, json.RawMessage(`{"timezone":"America/New_York"}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Source: "clock_metadata"})
	out, finish := fx.api.preparePlanNudgeMode(&DaemonConn{planNudges: true}, raw, true)
	var in protocol.AgentUserMessage
	if err := json.Unmarshal(out, &in); err != nil {
		t.Fatal(err)
	}
	if finish != nil || in.PlanNudge != "" || in.PersonTime == nil {
		t.Fatal("clock-only refresh claimed plan facts or lost clock")
	}
	if got := planCarryingInput(t, fx.api, fx.sess, true); got == "" {
		t.Fatal("metadata refresh consumed the next turn's nudge")
	}
}
