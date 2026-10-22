package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHostTargetsExcludePodRolesBeforeFallback(t *testing.T) {
	for _, role := range []struct{ name, mode, bound string }{{"legacy-runner", "runner", ""}, {"forged-host-mode", "local", "fixture-owned-session"}} {
		t.Run(role.name, func(t *testing.T) {
			h := NewHub()
			pod := &DaemonConn{ID: "a-pod", Mode: role.mode, BoundSessionID: role.bound, send: make(chan []byte, 8)}
			pod.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
			pod.SetCheckedOutRepos([]string{"victim-repository"})
			pod.SetSandboxAvailable(true)
			h.Register(pod)
			host := &DaemonConn{ID: "z-host", Mode: "local", send: make(chan []byte, 8)}
			host.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
			h.Register(host)
			for name, got := range map[string]*DaemonConn{"repo": h.DaemonForRepo("victim-repository"), "scratch-host": h.DaemonForScratch("daemon"), "scratch-sandbox": h.DaemonForScratch("docker"), "assist": h.AnyDaemon()} {
				if got != host {
					t.Errorf("%s chose pod or lost sole eligible host", name)
				}
			}
			if h.GetHostDaemon(pod.ID) != nil || len(h.GetHostDaemons()) != 1 {
				t.Error("pod exposed as explicit host target")
			}
			h.SetSessionOwner("fixture-owned-session", pod.ID)
			if h.GetDaemon(pod.ID) != pod || h.FindDaemonForSession("fixture-owned-session") != pod {
				t.Error("host filtering broke own-session routing")
			}
		})
	}
}

func TestHostTargetsExplicitPinsCannotLaunchIntoPods(t *testing.T) {
	for _, mode := range []string{"runner", "local"} {
		t.Run(mode, func(t *testing.T) {
			api, hub, host, pool := spawnTokenFixture(t)
			podID := "00000000-0000-4000-8000-000000000e01"
			if err := db.UpsertDaemon(context.Background(), pool, podID, "fixture-pod", mode, "/workspace"); err != nil {
				t.Fatal(err)
			}
			bound := ""
			if mode == "local" {
				bound = "fixture-owned-session"
			}
			pod := &DaemonConn{ID: podID, Name: "fixture-pod", Mode: mode, BoundSessionID: bound, send: make(chan []byte, 8)}
			pod.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
			pod.SetCheckedOutRepos([]string{"victim-repository"})
			pod.SetSandboxAvailable(true)
			pod.SetRestrictTools(true)
			pod.SetMCPGateway(true)
			hub.Register(pod)
			host.SetSandboxAvailable(true)
			host.SetRestrictTools(true)
			var sessions, tokens int
			if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM board_tokens)`).Scan(&sessions, &tokens); err != nil {
				t.Fatal(err)
			}
			rec := postSpawn(t, api, map[string]any{"daemon_id": podID, "repo": "victim-repository", "runtime": "daemon", "kind": "agent", "initial_prompt": "fixture-private-start"})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("explicit REST pin status=%d", rec.Code)
			}
			if _, err := hub.forwardBrowserSpawn(&BrowserConn{ID: "fixture-browser"}, protocol.BrowserSpawnSession{Kind: "agent", Type: "spawn_session", DaemonID: podID, Repo: "victim-repository", InitialPrompt: "fixture-private-start"}, pool); err == nil {
				t.Error("browser websocket pin accepted a pod")
			}
			for _, noRepo := range []bool{false, true} {
				if problem := api.startBoardSessionOnDaemon(context.Background(), runnerStartRequest{DaemonID: podID, Repo: "victim-repository", NoRepo: noRepo}, runnerPrincipal{}, newUUID()); problem == nil {
					t.Error("internal runner contract pin accepted a pod")
				}
			}
			svc := &CronService{api: api}
			if _, err := svc.pickDaemon(&podID, false); err == nil {
				t.Error("cron explicit pin accepted a pod")
			}
			if got, err := svc.pickDaemon(nil, false); err != nil || got != host {
				t.Error("cron automatic selection did not retain eligible host")
			}
			var afterSessions, afterTokens int
			if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM board_tokens)`).Scan(&afterSessions, &afterTokens); err != nil {
				t.Fatal(err)
			}
			if sessions != afterSessions || tokens != afterTokens || len(pod.send) != 0 || len(host.send) != 0 {
				t.Fatal("rejected pin wrote launch state, minted credentials or forwarded data")
			}
		})
	}
}

func TestHostTargetsBoardConfiguredPodPinIsRejected(t *testing.T) {
	api, hub, host, pool := spawnTokenFixture(t)
	podID := "00000000-0000-4000-8000-000000000e02"
	if err := db.UpsertDaemon(context.Background(), pool, podID, "fixture-pod", "local", "/workspace"); err != nil {
		t.Fatal(err)
	}
	pod := &DaemonConn{ID: podID, Mode: "local", BoundSessionID: "fixture-owned-session", send: make(chan []byte, 8)}
	pod.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	pod.SetCheckedOutRepos([]string{"victim-repository"})
	hub.Register(pod)
	board, err := db.CreateBoard(context.Background(), pool, "Fixture board", nil, []string{"victim-repository"}, &podID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/boards/"+board.ID+"/assist", strings.NewReader(`{}`))
	req.SetPathValue("id", board.ID)
	req.Header.Set("Authorization", "Bearer "+spawnTokenDaemonTok)
	rec := httptest.NewRecorder()
	api.HandlePostAssist(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("board pod pin status=%d", rec.Code)
	}
	var sessions, tokens int
	if err = pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM board_tokens)`).Scan(&sessions, &tokens); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || tokens != 0 || len(pod.send) != 0 || len(host.send) != 0 {
		t.Fatal("board pin leaked launch state or silently fell back to another daemon")
	}
}
