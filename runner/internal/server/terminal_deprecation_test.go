package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// kindWant is the refusal for a start of this kind: the deprecation for "tmux", and for an omitted
// kind (which used to mean Terminal) only that "agent" is required.
func kindWant(kind string) string {
	if kind == "" {
		return `kind \"agent\" is required`
	}
	return "Terminal sessions are deprecated and will be removed in 0.5.0"
}

func TestNewTerminalSessionsAreDeprecated(t *testing.T) {
	for _, runtime := range []string{"daemon", "docker", "cluster"} {
		for _, kind := range []string{"", "tmux"} {
			t.Run(runtime+"/"+kind, func(t *testing.T) {
				api, dc := runtimeTestAPI(t)
				rec := postSpawn(t, api, map[string]any{"daemon_id": "d1", "repo": "my-app", "runtime": runtime, "kind": kind})
				if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), kindWant(kind)) {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if kind == "" && strings.Contains(rec.Body.String(), "deprecated") {
					t.Fatalf("an omitted kind is answered with the deprecation: %s", rec.Body.String())
				}
				if len(dc.send) != 0 {
					t.Fatal("deprecated launch reached daemon")
				}
			})
		}
	}
}

func TestBrowserTerminalLaunchIsDeprecated(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	for _, kind := range []string{"", "tmux"} {
		_, err := api.hub.forwardBrowserSpawn(&BrowserConn{ID: "fixture-browser"}, protocol.BrowserSpawnSession{DaemonID: dc.ID, Repo: "my-app", Kind: kind}, nil)
		if err == nil || err.Error() != strings.ReplaceAll(kindWant(kind), `\"`, `"`) {
			t.Fatalf("error=%v", err)
		}
		if len(dc.send) != 0 {
			t.Fatal("deprecated launch reached daemon")
		}
	}
}

func TestExistingTerminalStillAcceptsInput(t *testing.T) {
	fx := newDelegFx(t)
	id, _ := fx.session(sessOpts{owner: "owner-1", status: "running"})
	if err := db.SetSessionKind(t.Context(), fx.pool, id, "tmux"); err != nil {
		t.Fatal(err)
	}
	fx.hub.SetSessionOwner(id, fx.dc.ID)
	browser := &BrowserConn{ID: "fixture-browser", AccountID: "owner-1"}
	if err := handleBrowserTerminalInput(t.Context(), fx.hub, browser, fx.pool, protocol.SendInput{SessionID: id, Data: "echo fixture\n"}); err != nil {
		t.Fatal(err)
	}
	var input protocol.SendInput
	select {
	case raw := <-fx.dc.send:
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("existing Terminal input was not forwarded")
	}
	if input.Type != "send_input" || input.SessionID != id || input.Data != "echo fixture\n" {
		t.Fatalf("input=%+v", input)
	}
}

func TestUnknownSessionKindCannotFallBackToTerminal(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	rec := postSpawn(t, api, map[string]any{"daemon_id": dc.ID, "repo": "my-app", "runtime": "docker", "kind": "unknown"})
	if rec.Code != 422 || len(dc.send) != 0 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := api.hub.forwardBrowserSpawn(&BrowserConn{}, protocol.BrowserSpawnSession{DaemonID: dc.ID, Repo: "my-app", Kind: "unknown"}, nil); err == nil || len(dc.send) != 0 {
		t.Fatal("unknown websocket kind reached the daemon")
	}
}

// The published contract no longer offers Terminal (tmux) sessions.
func TestOpenAPIDoesNotAdvertiseTerminalSessions(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(runnerOpenAPIDoc, &doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(runnerOpenAPIDoc)), "tmux") {
		t.Fatal("openapi.json still mentions tmux sessions")
	}
}
