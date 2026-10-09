package server

// A session's interaction mode (interaction.go): decided at start, recorded on the row, sent to
// the engine in the spawn message or the pod's environment, and reported back.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestResolveInteraction(t *testing.T) {
	const ia, un = protocol.InteractionInteractive, protocol.InteractionUnattended
	for _, c := range []struct {
		name        string
		asked       string
		fromBrowser bool
		cron        bool
		want        string
	}{
		{"browser default", "", true, false, ia},
		{"browser asks unattended", un, true, false, un},
		{"browser asks interactive", ia, true, false, ia},
		{"contract default", "", false, false, un},
		{"contract asks interactive", ia, false, false, ia},
		{"contract asks unattended", un, false, false, un},
		{"cron default", "", false, true, un},
		{"cron asked interactive", ia, false, true, un},
		{"cron from a browser flag", ia, true, true, un},
	} {
		got, apiErr := resolveInteraction(c.asked, c.fromBrowser, c.cron)
		if apiErr != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, apiErr, c.want)
		}
	}
	for _, bad := range []string{"Interactive", "attended", "auto", " interactive", "true"} {
		for _, cron := range []bool{false, true} {
			got, apiErr := resolveInteraction(bad, false, cron)
			if apiErr == nil || apiErr.Status != http.StatusBadRequest || apiErr.Message != interactionMessage || got != "" {
				t.Errorf("%q (cron %v): %q, %+v; want a 400", bad, cron, got, apiErr)
			}
		}
	}
}

// A row from before the column holds "": the person's own session is interactive, anything a
// cron, a tool or the operator key started is unattended. A stored mode always wins.
func TestEffectiveInteractionOfStoredRows(t *testing.T) {
	const ia, un = protocol.InteractionInteractive, protocol.InteractionUnattended
	cron, none := "c-1", ""
	for name, c := range map[string]struct {
		row  *db.SessionRow
		want string
	}{
		"no row":                       {nil, ia},
		"legacy, the person's":         {&db.SessionRow{}, ia},
		"legacy, human kind":           {&db.SessionRow{StartedByKind: "human"}, ia},
		"legacy, empty cron id":        {&db.SessionRow{CronID: &none}, ia},
		"legacy, a cron's":             {&db.SessionRow{CronID: &cron}, un},
		"legacy, an agent token's":     {&db.SessionRow{StartedByKind: "agent", StartedByName: "x"}, un},
		"legacy, the operator key's":   {&db.SessionRow{StartedByKind: "runner_key"}, un},
		"stored interactive, a tool's": {&db.SessionRow{StartedByKind: "agent", Interaction: ia}, ia},
		"stored unattended, a person":  {&db.SessionRow{Interaction: un}, un},
		"an unknown stored value":      {&db.SessionRow{Interaction: "sometimes"}, ia},
	} {
		if got := effectiveInteraction(c.row); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestInteractionEnvVarMirrorsTheReceiver(t *testing.T) {
	if interactionEnvVar != daemon.InteractionEnvVar {
		t.Errorf("env var %q != daemon's %q", interactionEnvVar, daemon.InteractionEnvVar)
	}
}

// A request that does not state a mode hashes exactly as it did before the field existed, so a
// retry across the upgrade is still a retry.
func TestStartRequestHashIgnoresAnAbsentInteraction(t *testing.T) {
	raw, err := json.Marshal(RunnerStartRequest{Repo: "app", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "interaction") {
		t.Errorf("a request without the field marshals it: %s", raw)
	}
}

func interactionOfRow(t *testing.T, fx *mcpFx, sessionID string) (stored, info string) {
	t.Helper()
	row, err := db.GetSession(context.Background(), fx.pool, sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession %s: %v", sessionID, err)
	}
	return row.Interaction, sessionRowToInfo(*row, mcpAcct).Interaction
}

func jobInteraction(t *testing.T, job map[string]any) (string, bool) {
	t.Helper()
	e, ok := jobEnv(t, job)[interactionEnvVar]
	if !ok {
		return "", false
	}
	if _, secret := e["valueFrom"]; secret {
		t.Errorf("%s must be a plain env value, not a secretKeyRef", interactionEnvVar)
	}
	v, _ := e["value"].(string)
	return v, true
}

// The contract's start is unattended unless the caller says a person is reading; the mode is
// recorded, sent in the spawn message and reported by the status body and the session JSON. A
// cron's start is unattended even when it asks for interactive, and an unknown value is a 400
// that starts nothing.
func TestContractStartRecordsAndSendsInteraction(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	for _, c := range []struct{ asked, want string }{
		{"", protocol.InteractionUnattended},
		{protocol.InteractionUnattended, protocol.InteractionUnattended},
		{protocol.InteractionInteractive, protocol.InteractionInteractive},
	} {
		res, apiErr := fx.api.StartSession(ctx, fx.owner(),
			RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Interaction: c.asked}, "")
		if apiErr != nil {
			t.Fatalf("start asking %q: %+v", c.asked, apiErr)
		}
		if msg := recvSpawn(t, fx.dc); msg.SessionID != res.SessionID || msg.Interaction != c.want {
			t.Errorf("asked %q: spawn interaction = %q, want %q", c.asked, msg.Interaction, c.want)
		}
		if stored, info := interactionOfRow(t, fx, res.SessionID); stored != c.want || info != c.want {
			t.Errorf("asked %q: row %q, session JSON %q, want %q", c.asked, stored, info, c.want)
		}
		status, apiErr := fx.api.SessionStatus(ctx, fx.owner(), res.SessionID)
		if apiErr != nil {
			t.Fatalf("SessionStatus: %+v", apiErr)
		}
		if status["interaction"] != c.want {
			t.Errorf("asked %q: status interaction = %v, want %q", c.asked, status["interaction"], c.want)
		}
	}

	// A cron's session is unattended whatever was asked.
	res, apiErr := fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{
		Prompt: "p", Runtime: "docker", Engine: "claude", NoRepo: true, AutoStop: true,
		CronID: newUUID(), NoOperatorFallback: true, Interaction: protocol.InteractionInteractive,
	}, "")
	if apiErr != nil {
		t.Fatalf("cron start: %+v", apiErr)
	}
	if msg := recvSpawn(t, fx.dc); msg.Interaction != protocol.InteractionUnattended || !msg.RestrictTools {
		t.Errorf("cron spawn: interaction %q restrict %v, want unattended and restricted", msg.Interaction, msg.RestrictTools)
	}
	if stored, info := interactionOfRow(t, fx, res.SessionID); stored != protocol.InteractionUnattended || info != protocol.InteractionUnattended {
		t.Errorf("cron row %q, session JSON %q, want unattended", stored, info)
	}

	// An unknown value: 400, before anything is recorded or sent.
	before := fx.count("sessions")
	_, apiErr = fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Interaction: "watched"}, "idem-bad-interaction")
	if apiErr == nil || apiErr.Status != http.StatusBadRequest || apiErr.Message != interactionMessage {
		t.Fatalf("unknown interaction: %+v, want 400 %q", apiErr, interactionMessage)
	}
	if fx.count("sessions") != before || len(fx.dc.send) != 0 || fx.count("runner_idempotency") != 0 {
		t.Error("a refused start recorded a session, claimed its idempotency key or reached the daemon")
	}
}

// The same over HTTP: the body's field is read, and the error has the contract's shape.
func TestRunnerStartHTTPInteraction(t *testing.T) {
	fx := newMCPFx(t)
	rec := fx.do(fx.api.HandleRunnerStart, fx.human(), map[string]any{"repo": "app", "prompt": "p", "runtime": "docker", "interaction": "nobody"})
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); rec.Code != http.StatusBadRequest || err != nil || body["error"] != interactionMessage {
		t.Fatalf("unknown interaction over HTTP: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.do(fx.api.HandleRunnerStart, fx.human(), map[string]any{"repo": "app", "prompt": "p", "runtime": "docker", "interaction": "interactive"})
	sid := fx.sessionID(rec)
	if msg := recvSpawn(t, fx.dc); msg.SessionID != sid || msg.Interaction != protocol.InteractionInteractive {
		t.Errorf("spawn interaction = %q, want interactive", msg.Interaction)
	}
}

// The launch sheet is a person: its start is interactive unless it says nobody will be reading,
// on a daemon and on both cluster paths.
func TestBrowserStartDefaultsToInteractive(t *testing.T) {
	fx := newMCPFx(t)
	for _, c := range []struct {
		asked any
		want  string
	}{
		{nil, protocol.InteractionInteractive},
		{"interactive", protocol.InteractionInteractive},
		{"unattended", protocol.InteractionUnattended},
	} {
		body := map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "agent"}
		if c.asked != nil {
			body["interaction"] = c.asked
		}
		sid := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), body))
		if msg := recvSpawn(t, fx.dc); msg.SessionID != sid || msg.Interaction != c.want {
			t.Errorf("asked %v: daemon spawn interaction = %q, want %q", c.asked, msg.Interaction, c.want)
		}
		if stored, info := interactionOfRow(t, fx, sid); stored != c.want || info != c.want {
			t.Errorf("asked %v: row %q, session JSON %q, want %q", c.asked, stored, info, c.want)
		}
		for _, cluster := range []map[string]any{
			{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"},
			{"runtime": "cluster", "kind": "agent", "no_repo": true},
		} {
			if c.asked != nil {
				cluster["interaction"] = c.asked
			}
			n := len(fx.k8s.created)
			csid := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), cluster))
			if len(fx.k8s.created) != n+1 {
				t.Fatalf("no Job for %v", cluster)
			}
			if v, ok := jobInteraction(t, fx.k8s.created[n]); !ok || v != c.want {
				t.Errorf("asked %v: cluster Job %v carries %s=%q (present %v), want %q", c.asked, cluster, interactionEnvVar, v, ok, c.want)
			}
			if stored, _ := interactionOfRow(t, fx, csid); stored != c.want {
				t.Errorf("asked %v: cluster row %q, want %q", c.asked, stored, c.want)
			}
		}
	}
	before := fx.count("sessions")
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "agent", "interaction": "maybe",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "interaction must be") {
		t.Fatalf("unknown interaction from the launch sheet: %d %s", rec.Code, rec.Body.String())
	}
	if fx.count("sessions") != before || len(fx.dc.send) != 0 {
		t.Error("a refused launch recorded a session or reached the daemon")
	}
}

// The browser websocket's spawn is a browser's start too.
func TestForwardBrowserSpawnInteraction(t *testing.T) {
	_, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	for asked, want := range map[string]string{"": protocol.InteractionInteractive, "unattended": protocol.InteractionUnattended} {
		sid, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "b1"}, protocol.BrowserSpawnSession{
			Type: "spawn_session", DaemonID: dc.ID, Repo: "app", Title: "t", Interaction: asked,
		}, pool)
		if err != nil {
			t.Fatalf("forwardBrowserSpawn: %v", err)
		}
		if msg := recvSpawn(t, dc); msg.SessionID != sid || msg.Interaction != want {
			t.Errorf("asked %q: spawn interaction = %q, want %q", asked, msg.Interaction, want)
		}
		if row, err := db.GetSession(ctx, pool, sid); err != nil || row == nil || row.Interaction != want {
			t.Errorf("asked %q: row = %+v, %v", asked, row, err)
		}
	}
	if _, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "b1"}, protocol.BrowserSpawnSession{
		Type: "spawn_session", DaemonID: dc.ID, Repo: "app", Interaction: "maybe",
	}, pool); err == nil || len(dc.send) != 0 {
		t.Errorf("an unknown interaction was forwarded (err %v)", err)
	}
}

// A cluster Job carries the mode as a plain variable, and a resume rebuilds it from the row: the
// stored mode when there is one, and the legacy rule for a row from before the column.
func TestClusterJobCarriesInteractionAndResumeKeepsIt(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	resumeEnv := func(sid string) (string, bool) {
		t.Helper()
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
		n := len(fx.k8s.created)
		resumeClusterSession(ctx, fx.hub, fx.pool, sid, "carry on", mcpAcct, testSID)
		if len(fx.k8s.created) != n+1 {
			t.Fatalf("%d Jobs after the resume, want %d", len(fx.k8s.created), n+1)
		}
		return jobInteraction(t, fx.k8s.created[n])
	}
	for _, c := range []struct{ asked, want string }{
		{"", protocol.InteractionUnattended},
		{protocol.InteractionInteractive, protocol.InteractionInteractive},
	} {
		n := len(fx.k8s.created)
		res, apiErr := fx.api.StartSession(ctx, fx.owner(),
			RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster", Interaction: c.asked}, "")
		if apiErr != nil {
			t.Fatalf("cluster start asking %q: %+v", c.asked, apiErr)
		}
		if v, ok := jobInteraction(t, fx.k8s.created[n]); !ok || v != c.want {
			t.Errorf("asked %q: Job carries %s=%q (present %v), want %q", c.asked, interactionEnvVar, v, ok, c.want)
		}
		if stored, _ := interactionOfRow(t, fx, res.SessionID); stored != c.want {
			t.Errorf("asked %q: row %q, want %q", c.asked, stored, c.want)
		}
		if v, ok := resumeEnv(res.SessionID); !ok || v != c.want {
			t.Errorf("asked %q: the resumed Job carries %s=%q (present %v), want %q", c.asked, interactionEnvVar, v, ok, c.want)
		}
	}

	// Rows from before the column: the person's launch-sheet session resumes interactive, a
	// tool's resumes unattended.
	mine := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"}))
	tool, apiErr := fx.api.StartSession(ctx, fx.owner(), RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster"}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %+v", apiErr)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET interaction = '' WHERE id = ANY($1)`, []string{mine, tool.SessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET started_by_kind = 'agent' WHERE id = $1`, tool.SessionID); err != nil {
		t.Fatal(err)
	}
	if v, _ := resumeEnv(mine); v != protocol.InteractionInteractive {
		t.Errorf("a legacy launch-sheet session resumed %q, want interactive", v)
	}
	if v, _ := resumeEnv(tool.SessionID); v != protocol.InteractionUnattended {
		t.Errorf("a legacy tool-started session resumed %q, want unattended", v)
	}
}

// A caller's env can never shadow the variable.
func TestExtraEnvCannotShadowInteraction(t *testing.T) {
	cf := newCronFx(t)
	err := cf.hub.JobManager().CreateSessionJob(SessionJobSpec{
		SessionID: newUUID(), Repo: "acme/widget", Engine: "claude", Interaction: protocol.InteractionUnattended,
		ExtraEnv: map[string]string{interactionEnvVar: "interactive"},
	})
	if err == nil {
		t.Fatal("an ExtraEnv entry naming the interaction variable was accepted")
	}
	if len(cf.k8s.created) != 0 {
		t.Errorf("%d Jobs created", len(cf.k8s.created))
	}
}
