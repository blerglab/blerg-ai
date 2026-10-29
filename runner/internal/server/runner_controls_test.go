package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func controlFixture(t *testing.T) (*API, *DaemonConn, string) {
	t.Helper()
	pool := runnerContractPool(t)
	h := NewHub()
	dc := &DaemonConn{ID: "00000000-0000-4000-8000-00000000bc01", Name: "controls", send: make(chan []byte, 8), inputReceipts: true}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	h.Register(dc)
	if err := db.UpsertDaemon(t.Context(), pool, dc.ID, "controls", "daemon", ""); err != nil {
		t.Fatal(err)
	}
	id := seedSession(t, pool, dc.ID, "owner", "daemon", "idle", false, time.Now(), nil)
	h.SetSessionOwner(id, dc.ID)
	if err := db.SetSessionEngine(t.Context(), pool, id, "codex"); err != nil {
		t.Fatal(err)
	}
	a := NewAPI(h, pool, "fixture-daemon-token", nil, "")
	a.SetRunnerKey("fixture-runner-key")
	return a, dc, id
}

func controlPost(a *API, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, a)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer fixture-runner-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRunnerModelControlUsesSessionEngineAndAuthorization(t *testing.T) {
	a, dc, id := controlFixture(t)
	r := controlPost(a, "/api/runner/sessions/"+id+"/model", `{"model":"gpt-5-codex","effort":"ultra"}`)
	if r.Code != http.StatusAccepted {
		t.Fatalf("setter: %d %s", r.Code, r.Body.String())
	}
	var msg protocol.SetSessionModel
	if json.Unmarshal(<-dc.send, &msg) != nil || msg.SessionID != id || msg.Model != "gpt-5-codex" || msg.Effort != "ultra" {
		t.Fatalf("control frame %+v", msg)
	}
	for _, body := range []string{`{}`, `{"model":"--flag"}`, `{"effort":"auto"}`, `{"model":"gpt-5-codex","unknown":true}`} {
		r = controlPost(a, "/api/runner/sessions/"+id+"/model", body)
		if r.Code < 400 {
			t.Fatalf("accepted invalid body %s: %d", body, r.Code)
		}
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	r = controlPost(a, "/api/runner/sessions/"+id+"/model", `{"effort":"high"}`)
	if r.Code != http.StatusNotFound {
		t.Fatalf("private setter leaked: %d %s", r.Code, r.Body.String())
	}
	if len(dc.send) != 0 {
		t.Fatal("unauthorized or invalid control forwarded")
	}
}

func TestRunnerModelControlRejectsEndedAndUnavailableSession(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	r := controlPost(a, "/api/runner/sessions/"+id+"/model", `{"effort":"high"}`)
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("disconnected control: %d %s", r.Code, r.Body.String())
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	r = controlPost(a, "/api/runner/sessions/"+id+"/model", `{"effort":"high"}`)
	if r.Code != http.StatusConflict {
		t.Fatalf("ended control: %d %s", r.Code, r.Body.String())
	}
}

func TestRunnerMessageClientIDPersistsDeduplicatesAndReplays(t *testing.T) {
	a, dc, id := controlFixture(t)
	path := "/api/runner/sessions/" + id + "/message"
	body := `{"text":"Continue safely","client_id":"api-message-1"}`
	for range 2 {
		r := controlPost(a, path, body)
		if r.Code != http.StatusAccepted {
			t.Fatalf("send: %d %s", r.Code, r.Body.String())
		}
	}
	if len(dc.send) != 1 {
		t.Fatalf("duplicate forwarded %d frames", len(dc.send))
	}
	var sent protocol.AgentUserMessage
	if json.Unmarshal(<-dc.send, &sent) != nil || sent.ClientID != "api-message-1" {
		t.Fatalf("missing receipt identity: %+v", sent)
	}
	pending, err := db.PendingSessionInputs(t.Context(), a.dbPool, id)
	if err != nil || len(pending) != 1 {
		t.Fatalf("durable pending: %v %v", pending, err)
	}
	r := controlPost(a, path, `{"text":"Different task","client_id":"api-message-1"}`)
	if r.Code != http.StatusConflict {
		t.Fatalf("conflicting id: %d %s", r.Code, r.Body.String())
	}
	replayPendingSessionInputs(t.Context(), a.hub, a.dbPool, dc, []string{id})
	if len(dc.send) != 1 {
		t.Fatal("pending message did not replay")
	}
	if err := settleSessionInput(t.Context(), a.dbPool, protocol.AgentEvent{SessionID: id, Kind: "user_message", Payload: json.RawMessage(`{"client_id":"api-message-1"}`)}); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingSessionInputs(t.Context(), a.dbPool, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("receipt settlement: %v %v", pending, err)
	}
}

func TestRunnerMessageClientIDQueuesAcrossHostDisconnect(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	r := controlPost(a, "/api/runner/sessions/"+id+"/message", `{"text":"Continue on reconnect","client_id":"offline-message"}`)
	if r.Code != http.StatusAccepted {
		t.Fatalf("offline acceptance: %d %s", r.Code, r.Body.String())
	}
	pending, err := db.PendingSessionInputs(t.Context(), a.dbPool, id)
	if err != nil || len(pending) != 1 {
		t.Fatalf("offline pending: %v %v", pending, err)
	}
}

func TestRunnerMessageReportsClusterResumeFailure(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET status='disconnected',runtime='cluster',spawning_account_id=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	// No Cluster runtime can accept this resume. Success would lose the prompt.
	r := controlPost(a, "/api/runner/sessions/"+id+"/message", `{"text":"Resume"}`)
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("false successful resume: %d %s", r.Code, r.Body.String())
	}
}

func TestSessionInsightsUsesRunningChildModelUpdate(t *testing.T) {
	a, _, id := controlFixture(t)
	for _, e := range []struct{ kind, payload string }{
		{"subagent_started", `{"child_id":"child","agent_type":"reviewer"}`},
		{"subagent_update", `{"child_id":"child","model":"gpt-5-codex"}`},
	} {
		if _, _, err := db.AppendAgentEvent(t.Context(), a.dbPool, id, newUUID(), e.kind, e.payload); err != nil {
			t.Fatal(err)
		}
	}
	out, err := a.buildSessionInsights(t.Context(), mustSession(t, a, id), time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Subagents.ByModel) != 1 || out.Subagents.ByModel[0].Key != "gpt-5-codex" {
		t.Fatalf("running child model: %+v", out.Subagents)
	}
}

func mustSession(t *testing.T, a *API, id string) *db.SessionRow {
	t.Helper()
	row, err := db.GetSession(t.Context(), a.dbPool, id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestInsightsSplitsReroutedChildUsageWithoutDoubleCounting(t *testing.T) {
	a, _, id := controlFixture(t)
	payload := `{"child_id":"rerouted","model":"model-b","usage":{"input_tokens":30,"output_tokens":12},"usage_by_model":{"model-a":{"input_tokens":10,"output_tokens":4},"model-b":{"input_tokens":20,"output_tokens":8}}}`
	if _, _, err := db.AppendAgentEvent(t.Context(), a.dbPool, id, newUUID(), "subagent_done", payload); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListSessionTokenUsage(t.Context(), a.dbPool, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("usage not split: %+v", rows)
	}
	var total int64
	models := map[string]int64{}
	for _, row := range rows {
		total += row.Input
		models[row.Model] = row.Input
		if !row.Subagent || row.Turns != 0 {
			t.Fatalf("child counted as main: %+v", row)
		}
	}
	if total != 30 || models["model-a"] != 10 || models["model-b"] != 20 {
		t.Fatalf("double counted or mislabeled: %+v", rows)
	}
	global, err := db.ListTokenUsage(t.Context(), a.dbPool, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "")
	if err != nil || len(global) != 2 {
		t.Fatalf("global split: %+v %v", global, err)
	}
}

func TestRunnerMessageReceiptCapabilityAndFullQueue(t *testing.T) {
	a, dc, id := controlFixture(t)
	path := "/api/runner/sessions/" + id + "/message"
	dc.SetInputReceipts(false)
	r := controlPost(a, path, `{"text":"Next task","client_id":"receipt-required"}`)
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("receipt unsupported: %d %s", r.Code, r.Body.String())
	}
	pending, err := db.PendingSessionInputs(t.Context(), a.dbPool, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unsupported message persisted: %+v %v", pending, err)
	}
	dc.SetInputReceipts(true)
	for len(dc.send) < cap(dc.send) {
		dc.send <- []byte(`{}`)
	}
	r = controlPost(a, path, `{"text":"Next task","client_id":"queue-full"}`)
	if r.Code != http.StatusAccepted || !strings.Contains(r.Body.String(), `"queued":true`) {
		t.Fatalf("full queue lost accepted input: %d %s", r.Code, r.Body.String())
	}
	pending, err = db.PendingSessionInputs(t.Context(), a.dbPool, id)
	if err != nil || len(pending) != 1 {
		t.Fatalf("queue pending: %+v %v", pending, err)
	}
	r = controlPost(a, path, `{"text":"/model gpt-5-codex","client_id":"command-control"}`)
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("command falsely advertised durable: %d %s", r.Code, r.Body.String())
	}
}

func TestInsightsUnknownUsageFallbackForMalformedOrEmptyModelMap(t *testing.T) {
	a, _, id := controlFixture(t)
	for _, modelMap := range []string{`{}`, `null`, `[]`, `"invalid"`} {
		payload := `{"model":"legacy","usage":{"input_tokens":7},"usage_by_model":` + modelMap + `}`
		if _, _, err := db.AppendAgentEvent(t.Context(), a.dbPool, id, newUUID(), "subagent_done", payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListSessionTokenUsage(t.Context(), a.dbPool, id)
	if err != nil || len(rows) != 1 || rows[0].Model != "" || rows[0].Input != 28 {
		t.Fatalf("malformed partition must preserve aggregate under unknown model: %+v %v", rows, err)
	}
}

func TestRunnerModelControlUsesExistingEngineRulesAndReportsNativeTruth(t *testing.T) {
	a, dc, id := controlFixture(t)
	if err := db.SetSessionEngine(t.Context(), a.dbPool, id, "claude"); err != nil {
		t.Fatal(err)
	}
	r := controlPost(a, "/api/runner/sessions/"+id+"/model", `{"effort":"ultra"}`)
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Codex effort accepted on Claude: %d %s", r.Code, r.Body.String())
	}
	r = controlPost(a, "/api/runner/sessions/"+id+"/model", `{"model":"sonnet","effort":"high"}`)
	if r.Code != http.StatusAccepted || len(dc.send) != 1 {
		t.Fatalf("Claude dispatch: %d %s", r.Code, r.Body.String())
	}
	row := mustSession(t, a, id)
	if row.Model != nil && *row.Model == "sonnet" {
		t.Fatal("dispatch falsely recorded as native application")
	}
}

func TestRunnerResumePromptPreservesReceiptSource(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET status='disconnected',runtime='cluster',spawning_account_id=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	fixture := &fakeK8s{}
	jm := newTestJobManager(t, fixture)
	jm.historyPool = a.dbPool
	a.hub.SetJobManager(jm)
	r := controlPost(a, "/api/runner/sessions/"+id+"/message", `{"text":"Resume safely","client_id":"resume-receipt","source":"runner"}`)
	if r.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", r.Code, r.Body.String())
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	found := false
	for _, secret := range fixture.createdSecrets {
		values, _ := secret["stringData"].(map[string]any)
		if values["BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID"] == "resume-receipt" {
			found = true
			if values["BLERG_RUNNER_INITIAL_PROMPT_SOURCE"] != "runner" {
				t.Fatalf("receipt source changed: %+v", values)
			}
		}
	}
	if !found {
		t.Fatal("replacement did not preserve durable input identity")
	}
}

func TestRunnerMessageRetryDoesNotCancelNewLimitContinuation(t *testing.T) {
	a, _, id := controlFixture(t)
	path := "/api/runner/sessions/" + id + "/message"
	body := `{"text":"Original task","client_id":"original-retry"}`
	if r := controlPost(a, path, body); r.Code != http.StatusAccepted {
		t.Fatalf("initial: %d %s", r.Code, r.Body.String())
	}
	if err := db.SettleSessionInput(t.Context(), a.dbPool, id, "original-retry"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLimitResume(t.Context(), a.dbPool, id, true, "fixture-login"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE session_limit_resume SET state='pending',turn_id='new-turn' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if r := controlPost(a, path, body); r.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", r.Code, r.Body.String())
	}
	state, err := db.GetLimitResume(t.Context(), a.dbPool, id)
	if err != nil || state.State != "pending" {
		t.Fatalf("old retry cancelled later work: %+v %v", state, err)
	}
}

func TestRunnerDurableMessageRejectsTerminalAndFailedHostButKeepsLostRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name, kind, status string
		lost               bool
		want               int
	}{
		{"terminal", "tmux", "idle", false, http.StatusConflict},
		{"failed-host", "agent", "error", false, http.StatusConflict},
		{"lost-host", "agent", "error", true, http.StatusAccepted},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			a, dc, id := controlFixture(t)
			a.hub.UnregisterIfCurrent(dc)
			if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET kind=$2,status=$3,daemon_lost_at=CASE WHEN $4 THEN now() ELSE NULL END WHERE id=$1`, id, scenario.kind, scenario.status, scenario.lost); err != nil {
				t.Fatal(err)
			}
			r := controlPost(a, "/api/runner/sessions/"+id+"/message", `{"text":"Continue","client_id":"recovery-check"}`)
			if r.Code != scenario.want {
				t.Fatalf("%s: %d %s", scenario.name, r.Code, r.Body.String())
			}
			pending, err := db.PendingSessionInputs(t.Context(), a.dbPool, id)
			if err != nil || (scenario.want >= 400 && len(pending) != 0) {
				t.Fatalf("undeliverable persisted: %+v %v", pending, err)
			}
		})
	}
}

func TestRunnerResumeUsesFreshAgentTokenAuthority(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET status='disconnected',runtime='cluster',token_id='revoked-old-token' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	fixture := &fakeK8s{credentialFetchResponses: map[string][]byte{"owner:codex": []byte(`{"OPENAI_API_KEY":"fixture-access-only"}`)}}
	jm := newTestJobManager(t, fixture)
	jm.historyPool = a.dbPool
	a.hub.SetJobManager(jm)
	_, problem := a.SendMessage(t.Context(), RunnerPrincipal{Kind: "agent", Sub: "fresh-agent-token", OnBehalfOf: "owner"}, id, "Resume", "runner")
	if problem != nil {
		t.Fatal(problem)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.credentialFetchTokenIDs) == 0 {
		t.Fatal("no owner proof sent")
	}
	for _, proof := range fixture.credentialFetchTokenIDs {
		if proof != "fresh-agent-token" {
			t.Fatalf("stale resume authority: %q", proof)
		}
	}
}

func TestRunnerResumeEligibilityUsesRuntimeOwnerAndRetainedState(t *testing.T) {
	a, dc, id := controlFixture(t)
	a.hub.UnregisterIfCurrent(dc)
	fixture := &fakeK8s{}
	jm := newTestJobManager(t, fixture)
	jm.historyPool = a.dbPool
	a.hub.SetJobManager(jm)
	owner := "00000000-0000-4000-8000-00000000dd01"
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET status='disconnected',spawning_account_id=$2 WHERE id=$1`, id, owner); err != nil {
		t.Fatal(err)
	}
	principal := RunnerPrincipal{Kind: "human", Sub: owner, Sid: "fresh-owner-login"}
	body, problem := a.SessionStatus(t.Context(), principal, id)
	if problem != nil || body["resumable"] != false {
		t.Fatalf("workstation falsely Cluster resumable: %+v %v", body, problem)
	}
	if body["runtime"] == "cluster" {
		t.Fatalf("disconnected workstation mislabeled Cluster: %+v", body)
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.dbPool.Exec(t.Context(), `INSERT INTO session_native_history(session_id,session_ref,account_id,engine,claim_name,claim_uid) VALUES($1,$1,$2,'codex',$3,'fixture-claim-uid')`, id, owner, historyClaimName(id)); err != nil {
		t.Fatal(err)
	}
	body, problem = a.SessionStatus(t.Context(), principal, id)
	if problem != nil || body["resumable"] != false {
		t.Fatalf("disabled retained history falsely resumable: %+v %v", body, problem)
	}
	jm.NativeHistory.Enabled = true
	body, problem = a.SessionStatus(t.Context(), principal, id)
	if problem != nil || body["resumable"] != true || body["resume_mode"] != "native" {
		t.Fatalf("owned native eligibility: %+v %v", body, problem)
	}
	body, problem = a.SessionStatus(t.Context(), RunnerPrincipal{Kind: runnerKeyPrincipalKind}, id)
	if problem != nil || body["resumable"] != false {
		t.Fatalf("operator borrowed owner recovery authority: %+v %v", body, problem)
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE session_native_history SET reclaimed_at=now() WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	body, problem = a.SessionStatus(t.Context(), principal, id)
	if problem != nil || body["resumable"] != false {
		t.Fatalf("reclaimed native state advertised: %+v %v", body, problem)
	}
}

func TestExistingSessionDiscoveryUsesActualHostAndEngine(t *testing.T) {
	api, host, id := controlFixture(t)
	fixtureOwnDesktop(t, api.dbPool, host, "owner")
	host.SetAvailableEngines([]string{"codex", "claude"})
	host.SetEngineModels(map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-actual"))})
	other := reportingDaemon(api.hub, "fixture-other-host", map[string]models.Report{"codex": rep(time.Now(), mdl("gpt-other"))})
	other.SetAvailableEngines([]string{"codex"})
	api.SetModelSources(NewModelSources(nil, api.hub))
	principal := runnerPrincipal{Kind: "agent", Sub: "fixture-current-token", OnBehalfOf: "owner"}
	options, problem := api.DiscoverLaunchOptions(t.Context(), principal, LaunchOptionsRequest{SessionID: id, Runtime: "cluster", DaemonID: other.ID})
	if problem != nil {
		t.Fatal(problem)
	}
	if options.Runtime != "daemon" || len(options.Engines) != 1 || options.Engines[0].ID != "codex" || options.Engines[0].DaemonID != host.ID || len(options.Engines[0].Models.Models) != 1 || options.Engines[0].Models.Models[0].ID != "gpt-actual" {
		t.Fatalf("session discovery used a new-launch target: %+v", options)
	}
	if _, problem := api.DiscoverLaunchOptions(t.Context(), runnerPrincipal{Kind: "agent", Sub: "fixture-other-token", OnBehalfOf: "different-owner"}, LaunchOptionsRequest{SessionID: id}); problem == nil {
		t.Fatal("another account selected owner's session catalog")
	}
	api.hub.UnregisterIfCurrent(host)
	options, problem = api.DiscoverLaunchOptions(t.Context(), principal, LaunchOptionsRequest{SessionID: id})
	if problem != nil || len(options.Engines) != 0 || options.State != "unknown" {
		t.Fatal("disconnected session fell back to another host")
	}
}
