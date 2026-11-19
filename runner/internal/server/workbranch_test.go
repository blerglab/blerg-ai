package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBranchPreferenceAndLaunchSnapshot(t *testing.T) {
	fx := prefsFx(t)
	if w := callPrefs(fx, "PUT", fx.human(), `{"branch_template":"dev/{username}/{card}/{slug}"}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	const id = "20000000-0000-0000-0000-000000000254"
	const daemon = "10000000-0000-0000-0000-000000000254"
	if err := db.UpsertDaemon(ctx, fx.pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	// The fixture's human owns its preference; no username comes from the request.
	var account string
	if err := fx.pool.QueryRow(ctx, `SELECT account_id FROM account_preferences WHERE key='branch_template'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSessionAs(ctx, fx.pool, id, daemon, "starting", "/repos/test", "test", "Fix the button", "", db.SessionOrigin{SpawningAccount: account}); err != nil {
		t.Fatal(err)
	}
	branch, err := ensureWorkBranch(ctx, fx.pool, id, func(context.Context, string) (string, error) { return "Example Person", nil })
	if err != nil || branch != "dev/example-person/fix-the-button" {
		t.Fatalf("branch=%q, %v", branch, err)
	}
	if err := db.PutAccountPreference(ctx, fx.pool, account, prefBranchTemplate, json.RawMessage(`"changed/{slug}"`)); err != nil {
		t.Fatal(err)
	}
	again, err := ensureWorkBranch(ctx, fx.pool, id, nil)
	if err != nil || again != branch {
		t.Fatalf("resume branch=%q, %v", again, err)
	}
	if w := callPrefs(fx, "PUT", fx.human(), `{"branch_template":"dev/{typo}"}`); w.Code != 400 {
		t.Fatalf("unknown placeholder accepted: %d", w.Code)
	}
}

func TestCustomBranchRefusesOlderWorkstation(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetWorkBranches(false)
	if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, prefBranchTemplate, json.RawMessage(`"dev/{slug}"`)); err != nil {
		t.Fatal(err)
	}
	principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
	err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: "docker", Engine: "claude", Title: "Fix button", Prompt: "p"}, principal, newUUID())
	if err == nil || err.Status != http.StatusConflict {
		t.Fatalf("custom branch sent to an older workstation: %v", err)
	}
	if len(fx.dc.send) != 0 {
		t.Fatal("unsupported custom branch was dispatched")
	}
}

func TestCustomBranchLaunchUsesRecordedName(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetWorkBranches(true)
	if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, prefBranchTemplate, json.RawMessage(`"dev/{slug}"`)); err != nil {
		t.Fatal(err)
	}
	principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
	id := newUUID()
	if err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: "docker", Engine: "claude", Title: "Fix button", Prompt: "p"}, principal, id); err != nil {
		t.Fatal(err)
	}
	var spawn protocol.SpawnSession
	if err := json.Unmarshal(<-fx.dc.send, &spawn); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, id)
	if err != nil || row == nil || row.WorkBranch != "dev/fix-button" || spawn.WorkBranch != row.WorkBranch {
		t.Fatalf("launch branch=%q, row=%+v, error=%v", spawn.WorkBranch, row, err)
	}
}

func TestWorkBranchCompatibility(t *testing.T) {
	for _, tc := range []struct {
		branch        string
		support, want bool
	}{
		{"", false, true}, {"wip/session", false, true}, {"wip/ticket-data-7d67a123", false, false}, {"wip/ticket-data-7d67a123", true, true}, {"dev/person/fix", false, false}, {"dev/person/fix", true, true},
	} {
		if got := recordedBranchCompatible("session", tc.branch, tc.support); got != tc.want {
			t.Errorf("branch=%q, supported=%v: got %v", tc.branch, tc.support, got)
		}
	}
}

func TestDefaultBranchSnapshotSurvivesRename(t *testing.T) {
	fx := prefsFx(t)
	ctx := t.Context()
	const daemon = "10000000-0000-0000-0000-000000000370"
	if err := db.UpsertDaemon(ctx, fx.pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"37000000-0000-0000-0000-000000000001", "37000000-0000-0000-0000-000000000002"} {
		if err := db.InsertSessionAs(ctx, fx.pool, id, daemon, "starting", "/repos/test", "test", "Ticket data", "", db.SessionOrigin{}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ensureWorkBranch(ctx, fx.pool, "37000000-0000-0000-0000-000000000001", nil)
	if err != nil || first != "wip/ticket-data-37000000" {
		t.Fatalf("default: %q, %v", first, err)
	}
	second, err := ensureWorkBranch(ctx, fx.pool, "37000000-0000-0000-0000-000000000002", nil)
	if err != nil || second == first {
		t.Fatalf("collision: %q, %v", second, err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET title='Renamed' WHERE id=$1`, "37000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	again, err := ensureWorkBranch(ctx, fx.pool, "37000000-0000-0000-0000-000000000001", nil)
	if err != nil || again != first {
		t.Fatalf("renamed: %q, %v", again, err)
	}
	// Pre-feature sessions already have a full-ID branch recorded by migration 098.
	const old = "37000000-0000-0000-0000-000000000002"
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET work_branch=$2 WHERE id=$1`, old, "wip/"+old); err != nil {
		t.Fatal(err)
	}
	legacy, err := ensureWorkBranch(ctx, fx.pool, old, nil)
	if err != nil || legacy != "wip/"+old {
		t.Fatalf("legacy: %q, %v", legacy, err)
	}
}

func TestDefaultBranchLaunchUsesRecordedName(t *testing.T) {
	fx := newMCPFx(t)
	principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
	id := newUUID()
	if err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: "docker", Engine: "claude", Title: "Ticket data", Prompt: "p"}, principal, id); err != nil {
		t.Fatal(err)
	}
	var spawn protocol.SpawnSession
	if err := json.Unmarshal(<-fx.dc.send, &spawn); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(t.Context(), fx.pool, id)
	if err != nil || row == nil || row.WorkBranch != "wip/ticket-data-"+id[:8] || spawn.WorkBranch != row.WorkBranch {
		t.Fatalf("launch branch=%q, row=%+v, error=%v", spawn.WorkBranch, row, err)
	}
}

func TestDefaultBranchOnOlderDaemon(t *testing.T) {
	for _, runtime := range []string{"docker", "daemon"} {
		t.Run(runtime, func(t *testing.T) {
			fx := newMCPFx(t)
			fx.dc.SetWorkBranches(false)
			principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
			id := newUUID()
			if err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: runtime, Engine: "claude", Title: "Ticket data", Prompt: "p"}, principal, id); err != nil {
				t.Fatal(err)
			}
			var spawn protocol.SpawnSession
			if err := json.Unmarshal(<-fx.dc.send, &spawn); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetSession(t.Context(), fx.pool, id)
			if err != nil || row == nil || row.WorkBranch != "wip/"+id || spawn.WorkBranch != row.WorkBranch {
				t.Fatalf("launch branch=%q, row=%+v, error=%v", spawn.WorkBranch, row, err)
			}
		})
	}
}

func TestHostBranchTemplateDoesNotRequireCapability(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetWorkBranches(false)
	if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, prefBranchTemplate, json.RawMessage(`"dev/{slug}"`)); err != nil {
		t.Fatal(err)
	}
	principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
	if err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: "daemon", Engine: "claude", Title: "Ticket data", Prompt: "p"}, principal, newUUID()); err != nil {
		t.Fatal(err)
	}
}

func TestBranchTemplateIntentSnapshot(t *testing.T) {
	for _, template := range []string{"", "wip/{session}", "dev/{slug}"} {
		t.Run(template, func(t *testing.T) {
			fx := newMCPFx(t)
			if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, prefBranchTemplate, json.RawMessage(strconv.Quote(template))); err != nil {
				t.Fatal(err)
			}
			principal := RunnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID, Caps: []string{"session.start"}}
			id := newUUID()
			if err := fx.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "app", DaemonID: fx.dc.ID, Runtime: "docker", Engine: "claude", Title: "Ticket data", Prompt: "p"}, principal, id); err != nil {
				t.Fatal(err)
			}
			if err := db.PutAccountPreference(t.Context(), fx.pool, mcpAcct, prefBranchTemplate, json.RawMessage(`"changed/{slug}"`)); err != nil {
				t.Fatal(err)
			}
			jm := newTestJobManager(t, &fakeK8s{})
			jm.historyPool = fx.pool
			jm.resolveWorkBranch = fx.api.sessionWorkBranch
			spec := SessionJobSpec{SessionID: id}
			if err := jm.assignWorkBranch(t.Context(), &spec); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetSession(t.Context(), fx.pool, id)
			if err != nil || row == nil || spec.WorkBranch != row.WorkBranch || spec.WorkBranchCustom != (template != "") || row.WorkBranchCustom != (template != "") {
				t.Fatalf("intent changed after preference edit: spec=%+v, row=%+v, err=%v", spec, row, err)
			}
		})
	}
}

func TestBranchCapabilityMessageUsesTemplateIntent(t *testing.T) {
	dc := &DaemonConn{}
	for _, custom := range []bool{false, true} {
		msg := workBranchDaemonProblem(dc, "session", "wip/readable-12345678", custom)
		if strings.Contains(msg, "custom") != custom || msg == "" {
			t.Fatalf("custom=%v, message=%q", custom, msg)
		}
	}
}

func TestLaunchSheetDefaultBranchOnOlderDaemon(t *testing.T) {
	for _, runtime := range []string{"docker", "daemon"} {
		t.Run(runtime, func(t *testing.T) {
			api, _, dc, pool := spawnTokenFixture(t)
			dc.SetWorkBranches(false)
			rec := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": runtime, "kind": "agent", "title": "Ticket data"})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			msg := recvSpawn(t, dc)
			row, err := db.GetSession(t.Context(), pool, msg.SessionID)
			if err != nil || row == nil || row.WorkBranch != "wip/"+msg.SessionID || msg.WorkBranch != row.WorkBranch {
				t.Fatalf("spawn=%+v, row=%+v, error=%v", msg, row, err)
			}
		})
	}
}

func TestLaunchSheetHostTemplateOnOlderDaemon(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	dc.SetWorkBranches(false)
	if err := db.PutAccountPreference(t.Context(), pool, "user-1", prefBranchTemplate, json.RawMessage(`"dev/{slug}"`)); err != nil {
		t.Fatal(err)
	}
	rec := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": "daemon", "kind": "agent", "title": "Ticket data"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
