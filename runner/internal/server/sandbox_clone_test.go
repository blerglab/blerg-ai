package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A daemon that would mount the person's checkout for a sandboxed child (in a repository or not)
// is refused before anything is recorded or sent.
func TestSandboxedChildRefusedOnADaemonWithoutClones(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"in a repository":      {"engine": "claude", "repo": "app", "prompt": "p", "runtime": "docker"},
		"without a repository": {"engine": "claude", "no_repo": true, "prompt": "p", "runtime": "docker"},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newDelegFx(t)
			fx.dc.SetSandboxClones(false)
			fx.rule("claude", runnerRuntimeDocker, false)
			code, out, _ := fx.postChild(fx.parent, fx.parentTok, body, "k")
			if code != http.StatusConflict || asString(out["error"]) != msgSandboxCloneDaemon {
				t.Fatalf("%d %v", code, out)
			}
			fx.noSpawn()
			fx.storedNothing(fx.parent)
		})
	}
}

// A host child is never refused for it: nothing is mounted.
func TestHostChildNeedsNoSandboxClones(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.SetSandboxClones(false)
	fx.rule("claude", runnerRuntimeDaemon, false)
	parent, tok := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeDaemon, title: "Parent"})
	code, out, _ := fx.postChild(parent, tok, map[string]any{"engine": "claude", "repo": "app", "prompt": "p", "runtime": "daemon"}, "k")
	if code != http.StatusCreated {
		t.Fatalf("%d %v", code, out)
	}
	if msg := fx.spawn(); msg.Sandbox {
		t.Fatal("a host child was sent as sandboxed")
	}
}

// The launch sheet's Local sandbox (POST /api/sessions, agent and terminal alike) is refused on a
// daemon that would mount the checkout, before any row or spawn.
func TestLaunchSheetSandboxRefusedOnADaemonWithoutClones(t *testing.T) {
	for _, kind := range []string{"agent", "tmux"} {
		t.Run(kind, func(t *testing.T) {
			api, _, dc, pool := spawnTokenFixture(t)
			dc.SetSandboxClones(false)
			rec := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": "docker", "kind": kind})
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code != http.StatusConflict || asString(out["error"]) != msgSandboxCloneDaemon {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if len(dc.send) != 0 {
				t.Fatal("a spawn was sent to a daemon that would mount the checkout")
			}
			var rows int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("rows = %d (%v): a session was recorded", rows, err)
			}
		})
	}
}

// A runner-contract start (a board's card run, a cron, an API caller) in the sandbox is refused
// the same way.
func TestRunnerStartSandboxRefusedOnADaemonWithoutClones(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000c1", "laptop", "app")
	dc.SetSandboxAvailable(true)
	dc.SetSandboxClones(false)
	for _, body := range []map[string]any{
		{"repo": "app", "prompt": "p", "runtime": "docker"},
		{"repo": "app", "prompt": "p"}, // the default on a sandbox-capable daemon is the sandbox
	} {
		rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", body)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusConflict || asString(out["error"]) != msgSandboxCloneDaemon {
			t.Fatalf("%v: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(dc.send) != 0 {
		t.Fatal("a spawn was sent to a daemon that would mount the checkout")
	}
	// The host is not refused for it: nothing is mounted.
	if rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"repo": "app", "prompt": "p", "runtime": "daemon"}); rec.Code != http.StatusAccepted {
		t.Fatalf("a host start was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// The result names a wip/<id> branch wherever the runtime makes one: a cluster pod, and every
// sandboxed session in a repository (child or not).
func TestSessionWorkBranch(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		row  db.SessionRow
		want string
	}{
		{"cluster", db.SessionRow{ID: "s1", Repo: "org/app", Runtime: str("cluster")}, "wip/s1"},
		{"no stored runtime", db.SessionRow{ID: "s1", Repo: "org/app"}, "wip/s1"},
		{"sandboxed child", db.SessionRow{ID: "s1", Repo: "app", Runtime: str("docker"), ParentSessionID: str("p")}, "wip/s1"},
		{"sandbox, not a child", db.SessionRow{ID: "s1", Repo: "app", Runtime: str("docker")}, "wip/s1"},
		{"host child", db.SessionRow{ID: "s1", Repo: "app", Runtime: str("daemon"), ParentSessionID: str("p")}, ""},
		{"host session", db.SessionRow{ID: "s1", Repo: "app", Runtime: str("daemon")}, ""},
		{"child without a repository", db.SessionRow{ID: "s1", Repo: ".scratch-0123456789abcdef", Runtime: str("docker"), ParentSessionID: str("p")}, ""},
		{"cluster without a repository", db.SessionRow{ID: "s1", Runtime: str("cluster")}, ""},
	} {
		if got := sessionWorkBranch(&tc.row); got != tc.want {
			t.Errorf("%s: branch %q, want %q", tc.name, got, tc.want)
		}
	}
}
