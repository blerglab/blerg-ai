package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A session's first prompt is recorded with the source of whoever wrote it, never as the person's
// ("chat") when a board, a parent session or a cron did. The server says which in the spawn frame
// (initial_prompt_source) or, on the cluster, in the pod's BLERG_RUNNER_INITIAL_PROMPT_SOURCE.

// secretValue is one key of a session's per-session Secret, "" when it is not there.
func secretValue(t *testing.T, f *fakeK8s, sessionID, key string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.createdSecrets {
		meta, _ := s["metadata"].(map[string]any)
		if meta["name"] != sessionSecretName(sessionID) {
			continue
		}
		sd, _ := s["stringData"].(map[string]any)
		v, _ := sd[key].(string)
		return v
	}
	t.Fatalf("no Secret for session %s", sessionID)
	return ""
}

func TestBoardStartOnDaemonRecordsItsPromptAsTheBoards(t *testing.T) {
	api, hub := desktopRunnerAPI(t)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000b1", "laptop", "app")

	rec := runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{
		"repo": "app", "title": "card 43", "prompt": "You are working card #43",
		"env": map[string]string{"BLERG_BOARD_URL": "http://board", "BLERG_BOARD_TOKEN": "bt", "BLERG_BOARD_BOARD": "b1"},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if got := recvSpawn(t, dc).InitialPromptSource; got != "blerg-board" {
		t.Fatalf("board start: initial_prompt_source = %q, want blerg-board", got)
	}

	// The same contract with no board behind it leaves the source to the daemon: today's "chat".
	rec = runnerReq(t, api, http.MethodPost, "/api/runner/start", map[string]any{"repo": "app", "title": "t", "prompt": "do it"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if got := recvSpawn(t, dc).InitialPromptSource; got != "" {
		t.Fatalf("plain start: initial_prompt_source = %q, want empty (the daemon's chat)", got)
	}
}

func TestChildStartRecordsItsPromptAsTheParentSessions(t *testing.T) {
	fx := newDelegFx(t)
	_, apiErr := fx.api.startChild(context.Background(), fx.row(fx.parent), fx.childStart(), "rule:r1", "k-src")
	if apiErr != nil {
		t.Fatalf("startChild: %d %s", apiErr.Status, apiErr.Message)
	}
	if got := fx.spawn().InitialPromptSource; got != "session" {
		t.Fatalf("child start: initial_prompt_source = %q, want session", got)
	}
}

func TestClusterChildRecordsItsPromptAsTheParentSessions(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.clusterRule("claude")
	code, out, _ := cx.postChild(cx.cparent, cx.cparentTk, map[string]any{"repo": "app", "prompt": "do the thing", "title": "Worker"}, newUUID())
	if code != http.StatusCreated {
		t.Fatalf("start = %d %v", code, out)
	}
	child, _ := out["child"].(map[string]any)
	if got := secretValue(t, cx.k8s, asString(child["id"]), "BLERG_RUNNER_INITIAL_PROMPT_SOURCE"); got != "session" {
		t.Fatalf("cluster child: BLERG_RUNNER_INITIAL_PROMPT_SOURCE = %q, want session", got)
	}
}

func TestCronStartRecordsItsPromptAsTheCrons(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()

	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker" })
	if _, err := cf.svc.Start(ctx, c, cf.run(c)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := recvSpawn(t, cf.dc).InitialPromptSource; got != "cron" {
		t.Fatalf("docker cron: initial_prompt_source = %q, want cron", got)
	}

	cc := cf.newCron(func(c *db.Cron) { c.Name = "Cluster digest" })
	sid, err := cf.svc.Start(ctx, cc, cf.run(cc))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := secretValue(t, cf.k8s, sid, "BLERG_RUNNER_INITIAL_PROMPT_SOURCE"); got != "cron" {
		t.Fatalf("cluster cron: BLERG_RUNNER_INITIAL_PROMPT_SOURCE = %q, want cron", got)
	}
}

func TestClusterBoardStartRecordsItsPromptAsTheBoards(t *testing.T) {
	fx := newMCPFx(t)
	sid := fx.sessionID(fx.do(fx.api.HandleRunnerStart, fx.token("agent", ""), map[string]any{
		"repo": "app", "prompt": "You are working card #43", "runtime": "cluster",
		"env": map[string]string{"BLERG_BOARD_URL": "http://board", "BLERG_BOARD_TOKEN": "bt", "BLERG_BOARD_BOARD": "b1"},
	}))
	if got := secretValue(t, fx.k8s, sid, "BLERG_RUNNER_INITIAL_PROMPT_SOURCE"); got != "blerg-board" {
		t.Fatalf("cluster board start: BLERG_RUNNER_INITIAL_PROMPT_SOURCE = %q, want blerg-board", got)
	}
	// A person's own start through the contract stays theirs.
	sid = fx.sessionID(fx.do(fx.api.HandleRunnerStart, fx.human(), map[string]any{"repo": "app", "prompt": "mine", "runtime": "cluster"}))
	if got := secretValue(t, fx.k8s, sid, "BLERG_RUNNER_INITIAL_PROMPT_SOURCE"); got != "" {
		t.Fatalf("person's cluster start: BLERG_RUNNER_INITIAL_PROMPT_SOURCE = %q, want empty", got)
	}
}
