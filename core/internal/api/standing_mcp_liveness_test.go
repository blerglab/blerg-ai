package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestStandingExternalMCPTokenFetch(t *testing.T) {
	e := newMCPEnv(t)
	if _, err := e.pool.Exec(t.Context(), `UPDATE human_sessions SET expires_at=now()-interval '1 day' WHERE chain_id=$1`, e.sidA); err != nil {
		t.Fatal(err)
	}
	insertLiveHumanSession(t, e.pool, e.acctA)
	for _, standing := range []bool{false, true} {
		code, body, _ := e.internal(t, "/internal/mcp/connections/token", e.deps.InternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA, "standing": standing})
		want := http.StatusNotFound
		if standing {
			want = http.StatusOK
		}
		if code != want {
			t.Fatalf("standing=%v external token status=%d want %d", standing, code, want)
		}
		if standing {
			var credential struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal([]byte(body), &credential); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(credential.Value, mcpSecret) {
				t.Fatal("standing exchange did not return the account connection credential")
			}
		}
	}
}
