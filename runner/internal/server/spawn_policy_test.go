package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func spawnPolicyAPI(t *testing.T) (*API, *DaemonConn, func(...string) string) {
	t.Helper()
	pool := connectSrvTestDB(t)
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	dc := &DaemonConn{ID: "00000000-0000-4000-8000-00000000d001", Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 8)}
	hub.Register(dc)
	if err := db.UpsertDaemon(context.Background(), pool, dc.ID, dc.Name, "local", dc.ReposRoot); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	return api, dc, enableBrowserAuth(t, api)
}

func postSessions(t *testing.T, api *API, tok string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandlePostSessions(rec, req)
	return rec
}

func TestSkipPermissionsRequiresDockerRuntime(t *testing.T) {
	api, dc, mint := spawnPolicyAPI(t)
	tok := mint("session.start")

	rec := postSessions(t, api, tok, map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "daemon", "dangerously_skip_permissions": true})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Docker sandbox") {
		t.Fatalf("daemon+bypass = %d %s, want 422 mentioning Docker sandbox", rec.Code, rec.Body.String())
	}
	select {
	case m := <-dc.send:
		t.Fatalf("rejected spawn reached the daemon: %s", m)
	default:
	}

	rec = postSessions(t, api, tok, map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "docker", "dangerously_skip_permissions": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("docker+bypass = %d %s, want 202", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	<-dc.send

	// Agent-kind on the bare daemon is allowed (R5 deviation) and recorded as such.
	rec = postSessions(t, api, tok, map[string]any{"daemon_id": dc.ID, "repo": "app", "runtime": "daemon", "kind": "agent"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("agent+daemon = %d %s, want 202", rec.Code, rec.Body.String())
	}
	<-dc.send

	// Posture and attribution are persisted and exposed.
	row, err := db.GetSession(context.Background(), api.dbPool, out.SessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Runtime == nil || *row.Runtime != "docker" || !row.SkipPermissions {
		t.Fatalf("row posture = %v/%v, want docker/true", row.Runtime, row.SkipPermissions)
	}
	if row.SpawningAccountID == nil || *row.SpawningAccountID != "user-1" {
		t.Fatalf("spawning_account_id = %v, want user-1 (from the verified token)", row.SpawningAccountID)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	lrec := httptest.NewRecorder()
	api.HandleGetSessions(lrec, req)
	var infos []protocol.SessionInfo
	if err := json.Unmarshal(lrec.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode sessions: %v (%s)", err, lrec.Body.String())
	}
	var found bool
	for _, s := range infos {
		if s.ID == out.SessionID {
			found = true
			if s.Runtime != "docker" || !s.SkipPermissions {
				t.Fatalf("SessionInfo posture = %q/%v, want docker/true", s.Runtime, s.SkipPermissions)
			}
		}
	}
	if !found {
		t.Fatal("spawned session missing from GET /api/sessions")
	}
}
