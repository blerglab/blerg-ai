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
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func TestLiveOutboxTerminalRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, operation, cause string
		status                 int
		terminal               bool
	}{
		{"revoked confirm", "confirm", "decision_revoked", 409, true},
		{"untyped conflict", "confirm", "", 409, false},
		{"unavailable", "confirm", "decision_revoked", 503, false},
		{"cancel still retries", "cancel", "decision_revoked", 409, false},
		{"cancel wins", "confirm", "decision_revoked", 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newTopicFx(t)
			var logs bytes.Buffer
			old := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer func() { failurelog.Default = old }()
			id := newUUID()
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.name == "cancel wins" && calls == 1 {
					if _, err := fx.pool.Exec(t.Context(), `UPDATE live_mcp_decision_outbox SET operation='cancel',delivered=false WHERE ask_id=$1`, id); err != nil {
						t.Error(err)
					}
				}
				w.Header().Set("X-Blerg-Live-Data-Cause", tc.cause)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("private-refusal-canary"))
			}))
			defer core.Close()
			fx.api.SetCoreCredentials(core.URL, "fixture-component")
			fx.api.SetLiveDataApprovalURL(core.URL)

			body, _ := json.Marshal(ld.Decision{AskID: id, AccountID: newUUID(), BindingID: newUUID(), Revision: 1, Digest: strings.Repeat("a", 64)})
			if _, err := fx.pool.Exec(t.Context(), `INSERT INTO live_mcp_decision_outbox(ask_id,body,operation) VALUES($1,$2,$3)`, id, body, tc.operation); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_ = fx.api.reconcileLiveDecisions(t.Context(), id)
			}
			var delivered bool
			if err := fx.pool.QueryRow(t.Context(), `SELECT delivered FROM live_mcp_decision_outbox WHERE ask_id=$1`, id).Scan(&delivered); err != nil {
				t.Fatal(err)
			}
			wantCalls := 2
			if tc.terminal {
				wantCalls = 1
			}
			if delivered != tc.terminal || calls != wantCalls {
				t.Fatalf("delivered=%v calls=%d, want terminal=%v calls=%d", delivered, calls, tc.terminal, wantCalls)
			}
			if tc.terminal && !strings.Contains(logs.String(), "decision_revoked") {
				t.Fatal("terminal refusal cause was not logged")
			}
			if strings.Contains(logs.String(), "private-refusal-canary") {
				t.Fatal("refusal body leaked to logs")
			}
		})
	}
}
