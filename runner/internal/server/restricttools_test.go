package server

// Tool restriction of unattended sessions (mcpstart.go restrictToolsEnvVar, protocol.SpawnSession.
// RestrictTools): every cron session, with or without connections, and every grant session runs
// with the built-in tool allow-list, no ambient MCP server and none of the user's settings. A
// daemon that predates the capability would ignore the field, so it is never sent one and a cron
// treats it as no capacity.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func restrictEnv(t *testing.T, cf *cronFx) (string, bool) {
	t.Helper()
	cf.k8s.mu.Lock()
	defer cf.k8s.mu.Unlock()
	if len(cf.k8s.created) == 0 {
		t.Fatal("no Job created")
	}
	e, ok := jobEnv(t, cf.k8s.created[len(cf.k8s.created)-1])[restrictToolsEnvVar]
	if !ok {
		return "", false
	}
	v, _ := e["value"].(string)
	if _, secret := e["valueFrom"]; secret {
		t.Errorf("%s must be a plain env value, not a secretKeyRef", restrictToolsEnvVar)
	}
	return v, true
}

func TestCronClusterSessionIsRestrictedWithAndWithoutMCP(t *testing.T) {
	for name, mcp := range map[string]json.RawMessage{"with a grant": nil, "no connections": json.RawMessage("[]")} {
		t.Run(name, func(t *testing.T) {
			cf := newCronFx(t)
			c := cf.newCron(func(c *db.Cron) {
				if mcp != nil {
					c.MCP = mcp
				}
			})
			if _, err := cf.svc.Start(context.Background(), c, cf.run(c)); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if v, ok := restrictEnv(t, cf); !ok || v != "1" {
				t.Errorf("%s = %q (present %v), want 1", restrictToolsEnvVar, v, ok)
			}
		})
	}
}

func TestCronDockerSessionIsRestrictedWithAndWithoutMCP(t *testing.T) {
	for name, mcp := range map[string]json.RawMessage{"with a grant": nil, "no connections": json.RawMessage("[]")} {
		t.Run(name, func(t *testing.T) {
			cf := newCronFx(t)
			c := cf.newCron(func(c *db.Cron) {
				c.Runtime = "docker"
				if mcp != nil {
					c.MCP = mcp
				}
			})
			if _, err := cf.svc.Start(context.Background(), c, cf.run(c)); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if msg := recvSpawn(t, cf.dc); !msg.RestrictTools || !msg.Sandbox {
				t.Errorf("spawn = %+v: a cron session must be restricted and sandboxed", msg)
			}
		})
	}
}

// A plain start (no grant, not a cron) is byte for byte what it was: no field, no variable.
func TestPlainSessionIsNotRestricted(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	if _, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker"}, ""); apiErr != nil {
		t.Fatalf("docker start: %v", apiErr)
	}
	raw := <-fx.dc.send
	if strings.Contains(string(raw), "restrict_tools") {
		t.Errorf("a plain spawn carries restrict_tools: %s", raw)
	}
	if _, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster"}, ""); apiErr != nil {
		t.Fatalf("cluster start: %v", apiErr)
	}
	if _, ok := jobEnv(t, fx.k8s.created[len(fx.k8s.created)-1])[restrictToolsEnvVar]; ok {
		t.Errorf("a plain Job carries %s", restrictToolsEnvVar)
	}
}

// Restriction follows who is watching (docs/design/interactive-mcp-sessions.md): a
// board-started grant session (StartSession with a Grant) is restricted; a launch-sheet session
// with connections (POST /api/sessions) is not, on the daemon and on both cluster paths.
func TestGrantSessionsRestrictedOnlyWhenUnattended(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	if _, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Grant: fx.mustGrant(fx.dockerTarget())}, ""); apiErr != nil {
		t.Fatalf("StartSession docker: %v", apiErr)
	}
	var msg struct {
		RestrictTools bool `json:"restrict_tools"`
	}
	if err := json.Unmarshal(<-fx.dc.send, &msg); err != nil || !msg.RestrictTools {
		t.Errorf("StartSession daemon spawn: restrict_tools = %v (%v)", msg.RestrictTools, err)
	}
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "agent", "mcp": fx.selection(),
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch sheet: %d %s", rec.Code, rec.Body.String())
	}
	msg.RestrictTools = false // omitempty: an absent field leaves the previous decode's value
	if err := json.Unmarshal(<-fx.dc.send, &msg); err != nil || msg.RestrictTools {
		t.Errorf("launch-sheet daemon spawn: restrict_tools = %v (%v), want a watched session with its tools", msg.RestrictTools, err)
	}
	for _, body := range []map[string]any{
		{"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "mcp": fx.selection()},
		{"runtime": "cluster", "kind": "agent", "no_repo": true, "mcp": fx.selection()},
	} {
		before := len(fx.k8s.created)
		if rec := fx.do(fx.api.HandlePostSessions, fx.human(), body); rec.Code != http.StatusAccepted {
			t.Fatalf("cluster launch %v: %d %s", body, rec.Code, rec.Body.String())
		}
		if len(fx.k8s.created) != before+1 {
			t.Fatalf("no Job for %v", body)
		}
		env := jobEnv(t, fx.k8s.created[before])
		if _, ok := env[restrictToolsEnvVar]; ok {
			t.Errorf("cluster launch-sheet Job for %v carries %s: a watched session keeps its tools", body, restrictToolsEnvVar)
		}
		if _, ok := env[mcpGatewayEnvVar]; !ok {
			t.Errorf("cluster launch-sheet Job for %v lacks the grant", body)
		}
	}
}

// A resumed cron session (or grant session) is still restricted.
func TestResumedCronSessionIsRestricted(t *testing.T) {
	ctx := context.Background()
	cf := newCronFx(t)
	c := cf.newCron(func(c *db.Cron) { c.MCP = json.RawMessage("[]") })
	sid, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cf.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	resumeClusterSession(ctx, cf.hub, cf.pool, sid, "carry on", mcpAcct, "sid-2")
	if len(cf.k8s.created) != 2 {
		t.Fatalf("%d Jobs, want 2", len(cf.k8s.created))
	}
	if e, ok := jobEnv(t, cf.k8s.created[1])[restrictToolsEnvVar]; !ok || e["value"] != "1" {
		t.Errorf("the resumed cron Job lacks %s=1", restrictToolsEnvVar)
	}
}

// A resumed session is restricted exactly when it was started so (migration 036): a launch-sheet
// session with connections comes back with its tools and its grant; a board-started grant
// session comes back restricted.
func TestResumedGrantSessionKeepsItsRestriction(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	disconnect := func(sid string) {
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
	}
	// Launch sheet, cluster: watched.
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "mcp": fx.selection()})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("launch sheet: %d %s", rec.Code, rec.Body.String())
	}
	sid := fx.sessionID(rec)
	if db.GetSessionRestrictTools(ctx, fx.pool, sid) {
		t.Fatal("a launch-sheet grant session was recorded as restricted")
	}
	disconnect(sid)
	n := len(fx.k8s.created)
	resumeClusterSession(ctx, fx.hub, fx.pool, sid, "carry on", mcpAcct, testSID)
	if len(fx.k8s.created) != n+1 {
		t.Fatalf("%d Jobs after the resume, want %d", len(fx.k8s.created), n+1)
	}
	env := jobEnv(t, fx.k8s.created[n])
	if _, ok := env[restrictToolsEnvVar]; ok {
		t.Errorf("a resumed launch-sheet grant session was restricted")
	}
	if _, ok := env[mcpGatewayEnvVar]; !ok {
		t.Errorf("the resume lost the grant")
	}
	// Board-started with a grant, cluster: unattended, recorded, and restricted again on resume.
	res, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster", Grant: fx.mustGrant(clusterTarget())}, "")
	if apiErr != nil {
		t.Fatalf("StartSession cluster: %v", apiErr)
	}
	bsid := res.SessionID
	n = len(fx.k8s.created)
	if !db.GetSessionRestrictTools(ctx, fx.pool, bsid) {
		t.Fatal("a board-started grant session was not recorded as restricted")
	}
	disconnect(bsid)
	resumeClusterSession(ctx, fx.hub, fx.pool, bsid, "carry on", mcpAcct, testSID)
	if len(fx.k8s.created) != n+1 {
		t.Fatalf("%d Jobs after the board resume, want %d", len(fx.k8s.created), n+1)
	}
	if e, ok := jobEnv(t, fx.k8s.created[n])[restrictToolsEnvVar]; !ok || e["value"] != "1" {
		t.Errorf("the resumed board-started grant Job lacks %s=1", restrictToolsEnvVar)
	}
}

// A caller's env can never shadow the variable.
func TestExtraEnvCannotShadowRestrictTools(t *testing.T) {
	cf := newCronFx(t)
	jm := cf.hub.JobManager()
	err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: newUUID(), Repo: "acme/widget", Engine: "claude", RestrictTools: true,
		ExtraEnv: map[string]string{restrictToolsEnvVar: "0"},
	})
	if err == nil {
		t.Fatal("an ExtraEnv entry naming the restriction variable was accepted")
	}
	if len(cf.k8s.created) != 0 {
		t.Errorf("%d Jobs created", len(cf.k8s.created))
	}
}

// ---- a daemon that predates the capability ----------------------------------------------------

func TestOldDaemonIsNoCapacityForACron(t *testing.T) {
	ctx := context.Background()
	for name, mcp := range map[string]json.RawMessage{"with a grant": nil, "no connections": json.RawMessage("[]")} {
		t.Run(name, func(t *testing.T) {
			cf := newCronFx(t)
			cf.dc.SetRestrictTools(false) // mcp_gateway stays true: only restrict_tools is missing
			c := cf.newCron(func(c *db.Cron) {
				c.Runtime = "docker"
				if mcp != nil {
					c.MCP = mcp
				}
			})
			for i := 0; i < 3; i++ {
				_, err := cf.svc.Start(ctx, c, cf.run(c))
				if !isCapacity(err) {
					t.Fatalf("attempt %d: err = %v, want capacity (held until the daemon is updated)", i, err)
				}
			}
			cf.assertNothingWritten("daemon without restrict_tools")
			if len(cf.dc.send) != 0 {
				t.Error("a spawn reached a daemon that cannot restrict")
			}
		})
	}
}

// The pinned daemon that cannot restrict is no capacity either, and so is one of several when
// it is the only candidate; a capable one is chosen over an old one.
func TestCronPicksACapableDaemon(t *testing.T) {
	ctx := context.Background()
	cf := newCronFx(t)
	old := "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0a0e" // sorts first, so it would win if it were considered
	if err := db.UpsertDaemon(ctx, cf.pool, old, "old", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	dc2 := &DaemonConn{ID: old, Name: "old", ReposRoot: "/repos", send: make(chan []byte, 8)}
	dc2.SetMCPGateway(true)
	dc2.SetSandboxAvailable(true)
	cf.hub.Register(dc2)

	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
	if _, err := cf.svc.Start(ctx, c, cf.run(c)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(dc2.send) != 0 || len(cf.dc.send) != 1 {
		t.Errorf("spawn went to the wrong daemon: old=%d capable=%d", len(dc2.send), len(cf.dc.send))
	}

	pinned := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.DaemonID = &old })
	if _, err := cf.svc.Start(ctx, pinned, cf.run(pinned)); !isCapacity(err) {
		t.Fatalf("pinned to an old daemon: err = %v, want capacity", err)
	}
}

// StartSession itself refuses (before recording anything) a cron start on a daemon that cannot
// restrict, and a launch-sheet grant on one, so no path can drop the restriction silently.
func TestStartRefusedOnADaemonThatCannotRestrict(t *testing.T) {
	ctx := context.Background()
	fx := newMCPFx(t)
	grant := fx.mustGrant(fx.dockerTarget())
	fx.dc.SetRestrictTools(false)

	_, apiErr := fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Prompt: "p", Engine: "claude", Runtime: "docker", NoRepo: true, AutoStop: true, CronID: newUUID(), NoOperatorFallback: true}, "")
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Message, msgRestrictDaemonOld) {
		t.Fatalf("cron start on an old daemon: %+v", apiErr)
	}
	_, apiErr = fx.api.StartSession(ctx, fx.owner(),
		RunnerStartRequest{Repo: "app", Prompt: "p", Runtime: "docker", Grant: grant}, "")
	if apiErr == nil || !strings.Contains(apiErr.Message, msgRestrictDaemonOld) {
		t.Fatalf("grant start on an old daemon: %+v", apiErr)
	}
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "runtime": "docker", "kind": "agent", "mcp": fx.selection(),
	})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), msgRestrictDaemonOld) {
		t.Fatalf("launch sheet on an old daemon: %d %s", rec.Code, rec.Body.String())
	}
	if fx.count("sessions") != 0 || len(fx.dc.send) != 0 {
		t.Error("a refused start recorded a session or reached the daemon")
	}
}

func TestDaemonConnRecordsTheRestrictToolsCapability(t *testing.T) {
	dc := &DaemonConn{ID: "d"}
	if dc.CanRestrictTools() {
		t.Error("a daemon that has not said so must not be assumed to restrict")
	}
	dc.SetRestrictTools(true)
	if !dc.CanRestrictTools() {
		t.Error("capability not recorded")
	}
	dc.SetRestrictTools(false)
	if dc.CanRestrictTools() {
		t.Error("capability not cleared")
	}
}
