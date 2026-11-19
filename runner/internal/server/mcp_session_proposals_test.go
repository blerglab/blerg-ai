package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type sessionProposalExecutor struct {
	liveToolHasher
	call    *mcpgw.ApprovedCall
	failure error
}

func (e *sessionProposalExecutor) ExecuteApproved(_ context.Context, call mcpgw.ApprovedCall) (mcpgw.ApprovedResult, error) {
	e.call = &call
	return mcpgw.ApprovedResult{Result: json.RawMessage(`{"content":[]}`)}, e.failure
}

func TestSessionScopedProposalRouteForwardsOriginalScopeWithoutAccountPreflight(t *testing.T) {
	for _, scenario := range []string{"success", "transient", "stale binding"} {
		t.Run(scenario, func(t *testing.T) {
			transient := scenario != "success"
			fx := newPropFx(t, 0)
			scope := db.MCPSessionScope{AccountID: newUUID(), RunnerInstallationID: "synthetic-runner", RunnerSessionID: newUUID(), HumanSessionID: newUUID(), LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
			p, err := db.InsertProposal(context.Background(), fx.pool, db.Proposal{SessionScope: &scope, AccountID: scope.AccountID, SessionID: scope.RunnerSessionID, ConnectionID: fx.connID, ConnectionName: mcpConn, URLSnapshot: fx.up.url(), Tool: "send", ToolHash: fx.up.hash("send"), Arguments: json.RawMessage(`{}`)}, 50)
			if err != nil {
				t.Fatal(err)
			}
			cfg := *fx.api.hub.mcpStart.get()
			exec := &sessionProposalExecutor{liveToolHasher: cfg.Hasher}
			if transient {
				exec.failure = &mcpgw.ApprovedError{Msg: "Remote MCP admission is unavailable.", NotSent: true}
			}
			if scenario == "stale binding" {
				exec.failure = &mcpgw.ApprovedError{Msg: "The original sign-in is no longer available.", NotSent: true, Err: mcpgw.ErrConnectionGone}
			}
			cfg.Hasher = exec
			fx.api.setMCPStart(&cfg)
			fx.list.mu.Lock()
			fx.list.conns = nil
			fx.list.proofs = nil
			fx.list.mu.Unlock()
			approver := newUUID()
			token := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: scope.AccountID, Aud: coreAuthAudience, Kind: "human", Sid: approver, Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
			if scenario == "success" {
				injected := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/approve", token, `{"session_scope":null}`)
				if injected.Code != http.StatusBadRequest || exec.call != nil {
					t.Fatal("request input could override frozen scope")
				}
			}
			response := fx.req(http.MethodPost, "/api/proposals/"+p.ID+"/approve", token, "")
			want := http.StatusOK
			if transient {
				want = http.StatusBadGateway
			}
			if response.Code != want {
				t.Fatalf("scoped approval status=%d want=%d", response.Code, want)
			}
			if exec.call == nil || exec.call.SessionScope == nil || *exec.call.SessionScope != scope || exec.call.Proof.AccountID != scope.AccountID || exec.call.Proof.SessionID != approver {
				t.Fatal("approval lost original scope or current approver")
			}
			fx.list.mu.Lock()
			calls := len(fx.list.proofs)
			fx.list.mu.Unlock()
			if calls != 0 {
				t.Fatal("scoped approval used account connection preflight")
			}
			stored, err := db.GetOwnedProposal(context.Background(), fx.pool, scope.AccountID, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := db.ProposalDone
			if transient {
				state = db.ProposalPending
			}
			if stored.SessionScope == nil || *stored.SessionScope != scope {
				t.Fatal("refusal lost the frozen scope, permitting account retry")
			}
			if stored.State != state {
				t.Fatalf("state=%s want=%s", stored.State, state)
			}
		})
	}
}

func TestSessionScopedProposalInvalidScopeNeverFallsBackToAccount(t *testing.T) {
	fx := newPropFx(t, 0)
	cfg := fx.api.hub.mcpStart.get()
	handler := &proposalsHandler{api: fx.api}
	scope := db.MCPSessionScope{AccountID: newUUID(), RunnerInstallationID: "synthetic-runner", RunnerSessionID: newUUID(), HumanSessionID: newUUID(), LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
	for _, foreign := range []bool{false, true} {
		frozen := scope
		proof := mcpgw.Proof{AccountID: scope.AccountID, SessionID: newUUID()}
		if foreign {
			proof.AccountID = newUUID()
		} else {
			frozen.CredentialGeneration = 0
		}
		p := &db.Proposal{SessionScope: &frozen, AccountID: scope.AccountID, SessionID: scope.RunnerSessionID, ConnectionID: fx.connID, URLSnapshot: fx.up.url()}
		rec := httptest.NewRecorder()
		if handler.snapshotHolds(context.Background(), rec, cfg, proof, p) || rec.Code != http.StatusConflict {
			t.Fatal("invalid frozen scope was not refused")
		}
	}
	fx.list.mu.Lock()
	calls := len(fx.list.proofs)
	fx.list.mu.Unlock()
	if calls != 0 {
		t.Fatal("invalid scope used account preflight")
	}
}
