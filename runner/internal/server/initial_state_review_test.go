package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestInitialStateEnvelopeType(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		want      bool
	}{
		{"top level", `{ "type" : "initial_state", "sessions":[] }`, true},
		{"nested", `{"type":"agent_event","payload":{"type":"initial_state"}}`, false},
		{"nested without envelope", `{"payload":{"type":"initial_state"}}`, false},
		{"invalid", `{"type":"initial_state"} trailing`, false},
		{"null", `null`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInitialState([]byte(tt.raw)); got != tt.want {
				t.Fatalf("isInitialState=%v want %v", got, tt.want)
			}
		})
	}
}

func TestInitialStateDecodeCause(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	var manifest protocol.WorkspaceManifest
	err := json.Unmarshal([]byte(`{"version":"canary-secret"}`), &manifest)
	if err == nil {
		t.Fatal("fixture did not fail decode")
	}
	logInitialStateFailure("workspace", err)
	if !strings.Contains(out.String(), `"cause":"decode"`) || strings.Contains(out.String(), "canary-secret") {
		t.Fatalf("unsafe or wrong cause: %s", out.String())
	}
}

func TestInitialStateMalformedWorkspaceSurvivesConnect(t *testing.T) {
	for _, kind := range []string{"manifest", "results"} {
		t.Run(kind, func(t *testing.T) {
			hub, mount, _, _ := seedInitialState(t)
			const id = "00000000-0000-0000-0000-000000000097"
			pool := hub.desktopPool
			if err := db.InsertSession(t.Context(), pool, id, "00000000-0000-0000-0000-000000000091", "idle", "/repos/app", "app", "Malformed workspace", ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetSessionSpawningAccount(t.Context(), pool, id, "owner"); err != nil {
				t.Fatal(err)
			}
			manifest := `{"version":2,"members":"canary-secret"}`
			results := `[]`
			if kind == "results" {
				manifest = `{"version":2}`
				results = `[{"pending":"canary-secret"}]`
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO session_workspaces(session_id,launch_generation,manifest,results) VALUES($1,'fixture',$2,$3)`, id, manifest, results); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			old := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
			defer func() { failurelog.Default = old }()
			mux := http.NewServeMux()
			mount(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			conn := dialBrowserTestServer(t, srv)
			defer conn.Close()
			var state protocol.InitialState
			if err := json.Unmarshal(readMessageWithDeadline(t, conn), &state); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, info := range state.Sessions {
				if info.ID == id {
					found = true
					if info.Workspace != nil || len(info.WorkspaceResults) != 0 {
						t.Fatal("malformed workspace fields survived")
					}
				}
			}
			if !found {
				t.Fatal("malformed workspace removed its session")
			}
			logs := strings.TrimSpace(out.String())
			if strings.Count(logs, "\n") != 0 || !strings.Contains(logs, `"cause":"decode"`) || !strings.Contains(logs, id) || strings.Contains(logs, "canary-secret") {
				t.Fatalf("want one safe session decode diagnostic: %s", logs)
			}
		})
	}
}

func TestInitialStateQuestionCreatedAfterSnapshot(t *testing.T) {
	hub, _, rows, _ := seedInitialState(t)
	messages, err := db.ListMessagesBySession(t.Context(), hub.desktopPool, rows[0].ID)
	if err != nil || len(messages) == 0 {
		t.Fatalf("question fixture: %v", err)
	}
	got, err := initialStateMessageInfos(t.Context(), hub.desktopPool, messages, "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !got[0].Answerable {
		t.Fatal("question created after session snapshot lost owner answerability")
	}
}

func TestListBrowserSessionsIDsPrivacy(t *testing.T) {
	hub, _, rows, _ := seedInitialState(t)
	ids := map[string]string{}
	for _, row := range rows {
		if row.Title != nil {
			ids[*row.Title] = row.ID
		}
	}
	requested := []string{ids["Fixture 1"], ids["Fixture 2"], ids["Fixture 4"]}
	for _, tt := range []struct {
		viewer string
		want   []string
	}{
		{"owner", []string{requested[0], requested[1]}},
		{"other", []string{requested[0], requested[2]}},
		{"", []string{requested[0]}},
	} {
		name := tt.viewer
		if name == "" {
			name = "anonymous"
		}
		t.Run(name, func(t *testing.T) {
			got, err := db.ListBrowserSessions(t.Context(), hub.desktopPool, tt.viewer, requested)
			if err != nil {
				t.Fatal(err)
			}
			wanted := map[string]bool{}
			for _, id := range tt.want {
				wanted[id] = true
			}
			if len(got) != len(wanted) {
				t.Fatalf("ids-filtered sessions=%d want %d", len(got), len(wanted))
			}
			for _, row := range got {
				if !wanted[row.ID] {
					t.Fatalf("ids or privacy guard leaked %s", row.ID)
				}
			}
		})
	}
	empty, err := db.ListBrowserSessions(t.Context(), hub.desktopPool, "owner", []string{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ids should select no sessions: %d %v", len(empty), err)
	}
}
