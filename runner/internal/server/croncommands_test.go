package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/commandsteps"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestCronCommandStepsApprovalAndAudit(t *testing.T) {
	fx := newCronsFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 10, MaxOutputBytes: 1024}}
	body := fx.body(func(m map[string]any) { m["command_steps"] = steps })
	rec := fx.call("POST", "/api/crons", fx.human(), body)
	if rec.Code != http.StatusBadRequest || len(fx.minter.minted()) != 0 {
		t.Fatalf("unapproved create: %d %s", rec.Code, rec.Body.String())
	}
	c, row := fx.create(func(m map[string]any) {
		m["command_steps"] = steps
		m["approve_command_steps"] = true
		m["runtime_target_id"] = "cluster"
	})
	if row.RuntimeTargetID == nil || *row.RuntimeTargetID != "cluster" {
		t.Fatal("command definition displaced runtime target on insert")
	}
	if row.CommandSteps == nil || row.CommandSteps.DefinedBy != mcpAcct || row.CommandSteps.Hash != approvedPlacementHash(t, steps, row.Runtime, derefOrEmpty(row.DaemonID), derefOrEmpty(row.RuntimeTargetID)) {
		t.Fatalf("definition: %+v", row.CommandSteps)
	}
	original := row.CommandSteps.DefinedAt
	steps[0].Argv[1] = "changed"
	patch, _ := json.Marshal(map[string]any{"command_steps": steps})
	rec = fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), string(patch))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unapproved patch: %d %s", rec.Code, rec.Body.String())
	}
	patch, _ = json.Marshal(map[string]any{"command_steps": steps, "approve_command_steps": true})
	rec = fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), string(patch))
	if rec.Code != http.StatusOK {
		t.Fatalf("approved patch: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.RuntimeTargetID == nil || *row.RuntimeTargetID != "cluster" {
		t.Fatal("command definition displaced runtime target on update")
	}
	if row.CommandSteps.Hash != approvedPlacementHash(t, steps, row.Runtime, derefOrEmpty(row.DaemonID), derefOrEmpty(row.RuntimeTargetID)) || !row.CommandSteps.DefinedAt.After(original) {
		t.Fatalf("changed definition: %+v", row.CommandSteps)
	}
	var count int
	if err := fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM cron_command_step_audit WHERE cron_id=$1`, c.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("audit entries: %d", count)
	}
	rec = fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), `{"name":"New name"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unrelated save: %d %s", rec.Code, rec.Body.String())
	}
	if err := fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM cron_command_step_audit WHERE cron_id=$1`, c.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("unrelated save altered audit: %d", count)
	}
}

func TestCronCommandStepsTransportAndOldDaemonRefusal(t *testing.T) {
	cf := newCronFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 10, MaxOutputBytes: 1024}}
	definition, err := commandsteps.Approve(steps, commandsteps.Hash(steps), mcpAcct, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.CommandSteps = definition })
	if _, err := cf.svc.Start(context.Background(), c, cf.run(c)); err == nil {
		t.Fatal("old daemon silently skipped commands")
	}
	cf.dc.FixedCommandSteps = true
	_, err = cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	msg := recvSpawn(t, cf.dc)
	if msg.CommandSteps == nil || msg.CommandSteps.Hash != definition.Hash || !msg.RestrictTools || !msg.Sandbox {
		t.Fatalf("unsafe snapshot: %+v", msg)
	}
	var decoded RunnerStartRequest
	if err := json.Unmarshal([]byte(`{"command_steps":{"steps":[{"argv":["echo","injected"]}]}}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CommandSteps != nil {
		t.Fatal("agent API accepted static commands")
	}
}

func TestCronCommandStepsClusterSecretSnapshot(t *testing.T) {
	cf := newCronFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 10, MaxOutputBytes: 1024}}
	definition, _ := commandsteps.Approve(steps, commandsteps.Hash(steps), mcpAcct, time.Now())
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "cluster"; c.CommandSteps = definition })
	sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	cf.k8s.mu.Lock()
	defer cf.k8s.mu.Unlock()
	found := false
	for _, secret := range cf.k8s.createdSecrets {
		meta, _ := secret["metadata"].(map[string]any)
		if meta["name"] != sessionSecretName(sid) {
			continue
		}
		data, _ := secret["stringData"].(map[string]any)
		raw, _ := data["BLERG_RUNNER_FIXED_COMMAND_STEPS"].(string)
		var got commandsteps.Definition
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.Hash != definition.Hash || got.DefinedBy != mcpAcct || got.Validate() != nil {
			t.Fatalf("snapshot changed: %+v", got)
		}
		found = true
	}
	if !found {
		t.Fatal("snapshot not delivered")
	}
}

func TestCronPlacementChangesRequireFreshApproval(t *testing.T) {
	fx := newCronsFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	c, _ := fx.create(func(m map[string]any) { m["command_steps"] = steps; m["approve_command_steps"] = true })
	for _, patch := range []string{`{"runtime":"cluster"}`, `{"daemon_id":"changed-daemon"}`, `{"runtime_target_id":"cluster"}`} {
		rec := fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), patch)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "approve the exact fixed commands") {
			t.Fatalf("placement escaped approval: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), `{"runtime_target_id":"cluster","approve_command_steps":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approved target: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetCron(context.Background(), fx.pool, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.CommandSteps.RuntimeTargetID != "cluster" || row.CommandSteps.Validate() != nil {
		t.Fatal("target absent from renewed approval")
	}

}

func TestFixedCommandRunHasDefinitionAudit(t *testing.T) {
	cf := newCronFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	definition, _ := commandsteps.Approve(steps, commandsteps.Hash(steps), mcpAcct, time.Now())
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.CommandSteps = definition })
	cf.dc.FixedCommandSteps = true
	run := cf.run(c)
	if _, err := cf.svc.Start(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := cf.pool.QueryRow(context.Background(), `SELECT definition_hash FROM cron_command_run_audit WHERE run_id=$1`, run.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != definition.Hash {
		t.Fatalf("audit hash: %s", hash)
	}
}

func approvedPlacementHash(t *testing.T, steps []commandsteps.Step, runtime, daemonID string, targetID ...string) string {
	t.Helper()
	d, err := commandsteps.ApproveForRuntime(steps, runtime, daemonID, "person", time.Now(), targetID...)
	if err != nil {
		t.Fatal(err)
	}
	return d.Hash
}

func TestCronCommandStepsClearingAllowsPlacementChange(t *testing.T) {
	fx := newCronsFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "health"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	c, _ := fx.create(func(m map[string]any) { m["command_steps"] = steps; m["approve_command_steps"] = true })
	rec := fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), `{"command_steps":[],"approve_command_steps":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear commands: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), `{"runtime":"cluster"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime without commands: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCronCommandBudgetRefusesCreateAndReducedRuntime(t *testing.T) {
	fx := newCronsFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo"}, TimeoutSeconds: 61, MaxOutputBytes: 32}}
	body := fx.body(func(m map[string]any) {
		m["command_steps"] = steps
		m["approve_command_steps"] = true
		m["max_runtime_seconds"] = 60
	})
	rec := fx.call("POST", "/api/crons", fx.human(), body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "step budget") {
		t.Fatalf("over budget create: %d %s", rec.Code, rec.Body.String())
	}
	c, _ := fx.create(func(m map[string]any) { m["command_steps"] = steps; m["approve_command_steps"] = true })
	rec = fx.call("PATCH", "/api/crons/"+c.ID, fx.human(), `{"max_runtime_seconds":60}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "step budget") {
		t.Fatalf("reduced runtime: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCronCommandStepsDefaultClusterTargetMatchesApproval(t *testing.T) {
	fx := newCronsFx(t)
	steps := []commandsteps.Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 10, MaxOutputBytes: 1024}}
	_, row := fx.create(func(m map[string]any) {
		m["runtime"] = "cluster"
		delete(m, "runtime_target_id")
		m["command_steps"] = steps
		m["approve_command_steps"] = true
	})
	if row.RuntimeTargetID == nil || row.CommandSteps == nil || row.CommandSteps.RuntimeTargetID != *row.RuntimeTargetID {
		t.Fatalf("approval target differs from the stored target: %+v %v", row.CommandSteps, row.RuntimeTargetID)
	}
}
