package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Started and joined by the browser harness. Persistence, authorization,
// resolver, socket serialization and browser reconciliation are all real.
func TestDatasetBrowserFixture(t *testing.T) {
	path := os.Getenv("BLERG_DATASET_FIXTURE")
	if path == "" {
		t.Skip("browser-owned fixture")
	}
	fx := newCommitFx(t)
	owner := topicOwner
	var mcpDenied *atomic.Bool
	if os.Getenv("BLERG_DATASET_MCP") == "1" {
		owner = mcpFixtureOwner
		_, mcpDenied = setupMCPDataset(t, fx.topicFx)
	}
	token := fx.token(owner, "human")
	hub := fx.api.hub
	fx.api.RegisterWorklistRoutes(fx.mux)
	runID := newUUID()
	// Gate datasets use a rolling seven-day window; fixture samples must not age out.
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	gate := func(seconds float64, revision int) {
		code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, map[string]any{"run_id": runID, "revision": revision, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339), "report": map[string]any{"seconds": seconds, "complete": true, "suites": []any{}}})
		if code != 201 {
			t.Fatalf("gate fixture ingestion=%d", code)
		}
	}
	gate(60, 1)
	fx.mux.HandleFunc("/ws/browser", hub.ServeBrowser(fx.pool, fx.api.AuthorizeBrowserWS))
	report := func(percent float64) {
		fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", FetchedAt: time.Now().UTC(), Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: percent, ResetsAt: time.Now().Add(24 * time.Hour)}}})
		hub.datasets.invalidate("blerg.engine.usage", fx.dc.ID)
		hub.invalidateDatasetSession(t.Context(), fx.pool, fx.sess, false)
	}
	report(40)
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/ws/browser" {
			fx.mux.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			writeError(w, 403, "fixture owner only")
			return
		}
		switch r.URL.Path {
		case "/fixture/stop":
			writeJSON(w, 200, map[string]bool{"ok": true})
			select {
			case done <- struct{}{}:
			default:
			}
		case "/fixture/mcp/change":
			if mcpDenied == nil {
				writeError(w, 404, "Unavailable.")
				return
			}
			if _, err := fx.pool.Exec(r.Context(), `UPDATE live_mcp_binding_state SET last_payload='[{"key":"quote","value":189.75}]',payload_at=clock_timestamp(),sample_sequence=sample_sequence+1`); err != nil {
				t.Error(err)
				writeError(w, 500, "Unavailable.")
				return
			}
			hub.datasets.invalidate("blerg.mcp", fx.sess)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/fixture/mcp/failure":
			cause := r.URL.Query().Get("cause")
			switch cause {
			case "provider_rate_limited", "mapping_failed", "execution_unavailable", "authority_unavailable":
			default:
				writeError(w, 400, "Invalid fixture cause.")
				return
			}
			if _, err := fx.pool.Exec(r.Context(), `UPDATE live_mcp_binding_state SET cause=$1,last_payload='[{"key":"quote","value":189.75,"market_status":"closed"}]',next_due_at=clock_timestamp()+interval '2 minutes',sample_sequence=sample_sequence+1`, cause); err != nil {
				t.Error("fixture failure update")
				writeError(w, 500, "Unavailable.")
				return
			}
			hub.datasets.invalidate("blerg.mcp", fx.sess)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/fixture/mcp/expire", "/fixture/mcp/recover":
			if mcpDenied == nil {
				writeError(w, 404, "Unavailable.")
				return
			}
			statement := `UPDATE live_mcp_binding_state SET payload_at=clock_timestamp()-interval '25 hours',cause='sample_expired',sample_sequence=sample_sequence+1`
			if r.URL.Path == "/fixture/mcp/recover" {
				statement = `UPDATE live_mcp_binding_state SET payload_at=clock_timestamp(),cause='',sample_sequence=sample_sequence+1`
			}
			if _, err := fx.pool.Exec(r.Context(), statement); err != nil {
				t.Error("fixture expiry update")
				writeError(w, 500, "Unavailable.")
				return
			}
			hub.datasets.invalidate("blerg.mcp", fx.sess)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/fixture/mcp/revoke":
			if mcpDenied == nil {
				writeError(w, 404, "Unavailable.")
				return
			}
			mcpDenied.Store(true)
			hub.datasets.invalidate("blerg.mcp", fx.sess)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/fixture/change":
			child := newUUID()
			if err := db.InsertSessionAs(r.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "Worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
				t.Error("insert fixture worker")
				writeError(w, 500, "fixture failed")
				return
			}
			report(75)
			gate(45, 2)
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/fixture/revoke":
			if err := fx.api.MarkPrivate(r.Context(), fx.privSess); err != nil {
				t.Error(err)
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			fx.mux.ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"url": server.URL, "token": token, "sessionId": fx.sess})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("browser fixture timed out")
	}
}
