package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runnerTestAPI(t *testing.T) (*API, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	api := NewAPI(hub, pool, "daemon-token", nil, "")
	return api, pool
}

func runnerMux(api *API) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runner/start", api.HandleRunnerStart)
	mux.HandleFunc("GET /api/runner/sessions/{id}", api.HandleRunnerStatus)
	mux.HandleFunc("GET /api/runner/sessions/{id}/events", api.HandleRunnerEvents)
	return mux
}

func TestRunnerAuth(t *testing.T) {
	api, _ := runnerTestAPI(t)
	srv := httptest.NewServer(runnerMux(api))
	defer srv.Close()

	// Unconfigured ⇒ 404 (the surface does not exist).
	resp, _ := http.Get(srv.URL + "/api/runner/sessions/x")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured: %d, want 404", resp.StatusCode)
	}

	api.SetRunnerKey("runner-secret")
	// Wrong key ⇒ 401. The master daemon token must NOT work here.
	for _, key := range []string{"", "wrong", "daemon-token"} {
		req, _ := http.NewRequest("GET", srv.URL+"/api/runner/sessions/x", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("key %q: %d, want 401", key, resp.StatusCode)
		}
	}
}

func TestRunnerStartRejectsReservedEnv(t *testing.T) {
	api, _ := runnerTestAPI(t)
	api.SetRunnerKey("runner-secret")
	srv := httptest.NewServer(runnerMux(api))
	defer srv.Close()

	body := `{"repo":"blerg-board","env":{"BLERG_RUNNER_DAEMON_TOKEN":"steal"}}`
	req, _ := http.NewRequest("POST", srv.URL+"/api/runner/start", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer runner-secret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("reserved env: %d, want 422", resp.StatusCode)
	}
}

func TestRunnerEventsEnvelope(t *testing.T) {
	api, pool := runnerTestAPI(t)
	api.SetRunnerKey("runner-secret")
	srv := httptest.NewServer(runnerMux(api))
	defer srv.Close()
	ctx := context.Background()

	const daemonID = "00000000-0000-4000-8000-00000000f00d"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/w/x", "x", "t", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := db.AppendAgentEvent(ctx, pool, sessionID,
			newUUID(), "assistant_turn", `{"text":"hi"}`); err != nil {
			t.Fatal(err)
		}
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/runner/sessions/"+sessionID+"/events?limit=2", nil)
	req.Header.Set("Authorization", "Bearer runner-secret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	var out struct {
		Events []struct {
			Seq     int64           `json:"seq"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
		HasMore bool `json:"has_more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 2 || !out.HasMore {
		t.Fatalf("envelope: %d events, has_more=%v (want 2, true)", len(out.Events), out.HasMore)
	}
	if out.Events[0].Seq != 1 || out.Events[0].Kind != "assistant_turn" {
		t.Errorf("first event wrong: %+v", out.Events[0])
	}
}
