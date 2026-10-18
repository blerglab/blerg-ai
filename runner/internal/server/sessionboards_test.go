package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLaunchBoardRequiresConfiguredBoard(t *testing.T) {
	fx := newMCPFx(t)
	fx.hub.SetJobManager(nil)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "board_id": "board-7"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("board silently ignored: response=%d, want503", rec.Code)
	}
	if fx.count("sessions") != 0 || len(fx.dc.send) != 0 {
		t.Fatal("unavailable board created a session or sent a spawn")
	}
}

func TestLaunchBoardRequiresHumanBeforePlacement(t *testing.T) {
	fx := newMCPFx(t)
	fx.hub.SetJobManager(nil)
	rec := fx.do(fx.api.HandlePostSessions, fx.token("agent", ""), map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "board_id": "board-7"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent could choose board: response=%d, want403", rec.Code)
	}
	if fx.count("sessions") != 0 || len(fx.dc.send) != 0 {
		t.Fatal("agent board choice created a session or sent a spawn")
	}
}

func TestBoardSelectionCannotBeInjectedThroughAgentStartArguments(t *testing.T) {
	for _, raw := range []string{`{"board_id":"board-7"}`, `{"BOARD_ID":null}`} {
		apiErr := RejectMCPArgument(json.RawMessage(raw))
		if apiErr == nil || apiErr.Status != http.StatusBadRequest {
			t.Fatal("agent contract silently accepted a launch-sheet board selection")
		}
	}
}

type launchBoardList struct {
	*fakeMCPList
	boards []SessionBoard
	err    error
	proofs []mcpgw.Proof
}

func (l *launchBoardList) ListSessionBoards(_ context.Context, p mcpgw.Proof) ([]SessionBoard, error) {
	l.proofs = append(l.proofs, p)
	return l.boards, l.err
}
func enableLaunchBoard(fx *mcpFx) *launchBoardList {
	list := &launchBoardList{fakeMCPList: fx.list, boards: []SessionBoard{{ID: "board-7", Name: "Fixture board"}}}
	cfg := *fx.hub.mcpStart.get()
	cfg.Board = &recBoard{}
	cfg.Core = list
	fx.api.setMCPStart(&cfg)
	return list
}

func TestLaunchBoardAttachesExistingGrantOnEveryLaunchPath(t *testing.T) {
	for _, mode := range []struct{ path, engine string }{{"docker", "claude"}, {"cluster-repo", "claude"}, {"cluster-scratch", "claude"}, {"docker", "codex"}, {"cluster-repo", "codex"}, {"cluster-scratch", "codex"}} {
		path, engine := mode.path, mode.engine
		t.Run(path+"/"+engine, func(t *testing.T) {
			fx := newMCPFx(t)
			fx.dc.SetCodexRestricted(true)
			fx.dc.SetAvailableEngines([]string{"claude", "codex"})
			list := enableLaunchBoard(fx)
			body := map[string]any{"repo": "app", "kind": "agent", "engine": engine, "board_id": "board-7", "mcp": fx.selection()}
			if path == "docker" {
				fx.hub.SetJobManager(nil)
				body["runtime"] = "docker"
				body["daemon_id"] = fx.dc.ID
			} else {
				body["runtime"] = "cluster"
			}
			if path == "cluster-scratch" {
				delete(body, "repo")
				body["no_repo"] = true
			}
			rec := fx.do(fx.api.HandlePostSessions, fx.human(), body)
			sid := fx.sessionID(rec)
			grants := fx.grants(sid)
			if len(grants) != 2 {
				t.Fatalf("grants=%d, want personal connection and board", len(grants))
			}
			found := false
			for _, grant := range grants {
				if grant.Builtin == mcpgw.BuiltinBoard {
					found = true
					if grant.BuiltinRef != "board-7" || grant.ProofValue != testSID || grant.AccountID != mcpAcct {
						t.Fatal("board grant lost its verified scope or proof")
					}
					if len(grant.Tools) != len(mcpgw.BoardToolNames()) {
						t.Fatal("board grant changed fixed tool set")
					}
				}
			}
			if !found || len(list.proofs) != 1 || list.proofs[0].AccountID != mcpAcct || list.proofs[0].SessionID != testSID {
				t.Fatal("board discovery or grant lost its verified identity")
			}
			var gateway *protocol.MCPGatewayConfig
			if path == "docker" {
				var spawn protocol.SpawnSession
				if err := json.Unmarshal(<-fx.dc.send, &spawn); err != nil {
					t.Fatal(err)
				}
				gateway = spawn.MCPGateway
				if spawn.BoardID != "board-7" || spawn.RestrictTools {
					t.Fatal("board session lost board context or coding tools")
				}
			} else {
				gateway = fx.secretConfig(sid)
				fx.k8s.mu.Lock()
				for _, secret := range fx.k8s.createdSecrets {
					meta, _ := secret["metadata"].(map[string]any)
					if meta["name"] != sessionSecretName(sid) {
						continue
					}
					data, _ := secret["stringData"].(map[string]any)
					if data["BLERG_RUNNER_BOARD_ID"] != "board-7" || data["BLERG_RUNNER_BOARD_GATEWAY_URL"] != mcpGatewayURL+"/mcp-gw/board" || data["BLERG_RUNNER_BOARD_TOKEN"] == "" {
						t.Error("cluster Secret omitted board CLI grant")
					}
				}
				fx.k8s.mu.Unlock()
			}
			if gateway == nil || len(gateway.Servers) != 2 {
				t.Fatal("spawn omitted board MCP connection")
			}
			for _, server := range gateway.Servers {
				if !fx.tokenBelongsTo(server.Token, sid) {
					t.Fatal("spawn token is not scoped to session")
				}
			}
			revokeSessionGrants(t.Context(), fx.pool, sid, "fixture ending")
			waitBoardRevocations()
			if len(fx.grants(sid)) != 0 {
				t.Fatal("session end retained grants")
			}
		})
	}
}

func TestLaunchBoardRefusalCreatesNothing(t *testing.T) {
	for _, reason := range []string{"missing", "proof", "unreachable", "host", "terminal", "engine"} {
		t.Run(reason, func(t *testing.T) {
			fx := newMCPFx(t)
			fx.hub.SetJobManager(nil)
			l := enableLaunchBoard(fx)
			body := map[string]any{"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "board_id": "board-7"}
			want := http.StatusNotFound
			switch reason {
			case "missing":
				l.boards = []SessionBoard{}
			case "proof":
				l.err = ErrMCPProofInvalid
				want = http.StatusUnauthorized
			case "unreachable":
				l.err = errors.New("private sensitive response")
				want = http.StatusServiceUnavailable
			case "host":
				body["runtime"] = "daemon"
				want = http.StatusUnprocessableEntity
			case "terminal":
				body["kind"] = "tmux"
				want = http.StatusUnprocessableEntity
			case "engine":
				body["engine"] = "hermes"
				want = http.StatusUnprocessableEntity
			}
			rec := fx.do(fx.api.HandlePostSessions, fx.human(), body)
			if rec.Code != want {
				t.Fatalf("status=%d want%d", rec.Code, want)
			}
			if strings.Contains(rec.Body.String(), "sensitive") {
				t.Fatal("upstream details leaked")
			}
			if fx.count("sessions") != 0 || len(fx.dc.send) != 0 {
				t.Fatal("refusal created a session or sent spawn")
			}
		})
	}
}

func TestSessionBoardsDiscoveryAndTransport(t *testing.T) {
	fx := newMCPFx(t)
	request := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/session-boards", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		fx.api.HandleSessionBoards(rec, req)
		return rec
	}
	if rec := request(fx.human()); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatal("unconfigured discovery did not hide board choice")
	}
	l := enableLaunchBoard(fx)
	if rec := request(fx.human()); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Fixture board") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("configured discovery lost board choices or privacy")
	}
	if rec := request(fx.token("agent", "")); rec.Code != http.StatusForbidden {
		t.Fatal("agent could enumerate launch boards")
	}
	l.err = ErrMCPProofInvalid
	if rec := request(fx.human()); rec.Code != http.StatusUnauthorized {
		t.Fatal("dead proof did not fail closed")
	}
}

func TestLaunchBoardResumeRefreshesCLICredentials(t *testing.T) {
	fx := newMCPFx(t)
	enableLaunchBoard(fx)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{"repo": "app", "kind": "agent", "runtime": "cluster", "board_id": "board-7"})
	sid := fx.sessionID(rec)
	old := fx.secretConfig(sid).Servers[0].Token
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='disconnected' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	fx.k8s.finishSessionJob(sid)
	if err := resumeClusterSessionMode(t.Context(), fx.hub, fx.pool, sid, "continue", mcpAcct, "new-human-session", false); err != nil {
		t.Fatal(err)
	}
	grants := fx.grants(sid)
	if len(grants) != 1 || grants[0].BuiltinRef != "board-7" || grants[0].ProofValue != "new-human-session" {
		t.Fatal("resume lost its board scope or current human proof")
	}
	if fx.tokenBelongsTo(old, sid) {
		t.Fatal("old gateway credential survived resume")
	}
	fx.k8s.mu.Lock()
	defer fx.k8s.mu.Unlock()
	if len(fx.k8s.createdSecrets) < 2 {
		t.Fatal("resume did not create a fresh session Secret")
	}
	latest := fx.k8s.createdSecrets[len(fx.k8s.createdSecrets)-1]
	data, _ := latest["stringData"].(map[string]any)
	token, _ := data["BLERG_RUNNER_BOARD_TOKEN"].(string)
	if data["BLERG_RUNNER_BOARD_ID"] != "board-7" || data["BLERG_RUNNER_BOARD_GATEWAY_URL"] != mcpGatewayURL+"/mcp-gw/board" || token == "" || token == old {
		t.Fatal("resume omitted or reused its CLI grant")
	}
	if !fx.tokenBelongsTo(token, sid) {
		t.Fatal("resumed CLI credential does not match fresh gateway grant")
	}
}
