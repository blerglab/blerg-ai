package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRecoveryInputReturnOneDeviceAndOwnerOnly(t *testing.T) {
	for _, source := range []string{"chat", "ask_answer"} {
		t.Run(source, func(t *testing.T) {
			f := newOwnedEndFixture(t, "waiting", 8)
			if _, err := acceptSessionInput(t.Context(), f.hub, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, ClientID: "refused", Text: "canonical original", Source: source}); err != nil {
				t.Fatal(err)
			}
			if err := db.SettleSessionInput(t.Context(), f.pool, f.sessionID, "refused"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.AppendAgentEvent(t.Context(), f.pool, f.sessionID, newUUID(), "error", `{"code":"input_recovery_failed","client_id":"refused","input_text":"untrusted replacement"}`); err != nil {
				t.Fatal(err)
			}
			call := func(token string) (int, string) {
				req := httptest.NewRequest(http.MethodPost, "/return", nil)
				req.SetPathValue("id", f.sessionID)
				req.SetPathValue("client", "refused")
				req.Header.Set("Authorization", "Bearer "+token)
				res := httptest.NewRecorder()
				f.api.HandleReturnRecoveryInput(res, req)
				var body struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(res.Body.Bytes(), &body)
				return res.Code, body.Text
			}
			if code, _ := call(f.mintAs("other-owner")); code != 404 {
				t.Fatalf("foreign owner=%d", code)
			}
			var wg sync.WaitGroup
			results := make(chan string, 8)
			for range 8 {
				wg.Go(func() {
					code, text := call(f.bearer)
					if code != 200 {
						t.Errorf("claim=%d", code)
					}
					if text != "" {
						results <- text
					}
				})
			}
			wg.Wait()
			close(results)
			count := 0
			for text := range results {
				count++
				if text != "canonical original" {
					t.Errorf("claimed daemon-supplied text: %q", text)
				}
			}
			want := 1
			if source != "chat" {
				want = 0
			}
			if count != want {
				t.Fatalf("winning devices=%d want %d", count, want)
			}
			var markers int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='input_return_claimed'`, f.sessionID).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if markers != want {
				t.Fatalf("durable return markers=%d want %d", markers, want)
			}
			if _, text := call(f.bearer); text != "" {
				t.Fatal("replay returned the same input twice")
			}
		})
	}
}

func TestRecoveryInputReturnRefusesNonHumanTokens(t *testing.T) {
	fx := newDelegFx(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/inputs/{client}/return", fx.api.HandleReturnRecoveryInput)
	for name, token := range map[string]string{
		"agent with browser caps": fx.coreToken(identity.Claims{Kind: agentPrincipalKind, Sub: "fixture-agent", OnBehalfOf: "owner-1", Sid: testSID, Caps: []string{coreAuthBrowserCap}}),
		"delegated human":         fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", OnBehalfOf: "owner-1", Sid: testSID, Caps: []string{coreAuthBrowserCap}}),
		"session delegate":        fx.parentTok,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+fx.parent+"/inputs/refused/return", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			want := http.StatusForbidden
			if name == "session delegate" {
				want = http.StatusUnauthorized
			}
			if res.Code != want {
				t.Fatalf("non-human recovery claim = %d, want %d", res.Code, want)
			}
		})
	}
}
