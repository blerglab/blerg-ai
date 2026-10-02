package server

// A cluster session's pod gets the same per-session messaging credential a desktop session does
// (BLERG_RUNNER_SESSION_TOKEN), so the `blerg-runner` CLI in the pod image (update, ask, note,
// publish) can reach the server. It travels in the per-session Secret, never as a literal in the
// Job spec, and is scoped to that one session.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// podSessionToken is the BLERG_RUNNER_SESSION_TOKEN the session's Secret carries ("" when none).
func (fx *mcpFx) podSessionToken(sessionID string) string {
	fx.t.Helper()
	fx.k8s.mu.Lock()
	defer fx.k8s.mu.Unlock()
	for _, s := range fx.k8s.createdSecrets {
		meta, _ := s["metadata"].(map[string]any)
		if meta["name"] != sessionSecretName(sessionID) {
			continue
		}
		sd, _ := s["stringData"].(map[string]any)
		v, _ := sd["BLERG_RUNNER_SESSION_TOKEN"].(string)
		return v
	}
	fx.t.Fatalf("no Secret for session %s", sessionID)
	return ""
}

func TestClusterStartGivesThePodASessionToken(t *testing.T) {
	fx := newMCPFx(t)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"})
	sid := fx.sessionID(rec)

	tok := fx.podSessionToken(sid)
	if tok == "" {
		t.Fatal("the pod's Secret has no BLERG_RUNNER_SESSION_TOKEN")
	}
	row, err := db.ValidateBoardToken(context.Background(), fx.pool, tok)
	if err != nil {
		t.Fatalf("the token does not validate: %v", err)
	}
	if row.SessionID != sid || row.BoardID != "" || len(row.Capabilities) != 1 || row.Capabilities[0] != "message" {
		t.Errorf("token = %+v, want a session-only token with the message capability for %s", row, sid)
	}

	// Secret only: the raw token must not appear in the Job spec.
	fx.k8s.mu.Lock()
	jobs, _ := json.Marshal(fx.k8s.created)
	fx.k8s.mu.Unlock()
	if strings.Contains(string(jobs), tok) {
		t.Error("the session token appears in the Job spec")
	}

	// And it is the credential the artifact upload accepts, for this session only.
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	mux := http.NewServeMux()
	fx.api.registerArtifactRoutes(mux)
	post := func(session string) int {
		req, _ := http.NewRequest(http.MethodPost, "/api/sessions/"+session+"/artifacts", strings.NewReader("hi"))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Artifact-Name", "a.txt")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if got := post(sid); got != http.StatusCreated {
		t.Errorf("upload with the pod's token = %d, want 201", got)
	}
	if got := post(newUUID()); got != http.StatusForbidden {
		t.Errorf("upload to another session with the pod's token = %d, want 403", got)
	}
}

func TestEachClusterStartGetsItsOwnToken(t *testing.T) {
	fx := newMCPFx(t)
	a := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"}))
	b := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"}))
	ta, tb := fx.podSessionToken(a), fx.podSessionToken(b)
	if ta == "" || tb == "" || ta == tb {
		t.Fatalf("tokens %q / %q: want two distinct, non-empty tokens", ta, tb)
	}
}

func TestClusterSessionEnvKeepsWhatTheCallerSent(t *testing.T) {
	fx := newMCPFx(t)
	sid := fx.sessionID(fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent"}))
	in := map[string]string{"BLERG_BOARD_TOKEN": "t"}
	out := withClusterSessionToken(context.Background(), fx.pool, sid, in)
	if out["BLERG_BOARD_TOKEN"] != "t" || out["BLERG_RUNNER_SESSION_TOKEN"] == "" {
		t.Errorf("out = %v", out)
	}
	if _, ok := in["BLERG_RUNNER_SESSION_TOKEN"]; ok {
		t.Error("the caller's map was modified")
	}
	if got := withClusterSessionToken(context.Background(), nil, sid, in); got["BLERG_BOARD_TOKEN"] != "t" || len(got) != 1 {
		t.Errorf("without a database the env is unchanged, got %v", got)
	}
}
