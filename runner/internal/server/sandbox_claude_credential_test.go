package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /api/daemons carries what the daemon said about a sandboxed Claude
// credential, and says nothing when the daemon didn't report it (an older
// daemon) — the launch sheet only warns on an explicit false.
func TestGetDaemonsReportsSandboxClaudeCredential(t *testing.T) {
	f := newReposRootFixture(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/daemons", nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/daemons = %d", rec.Code)
		}
		return rec.Body.String()
	}

	if body := get(); strings.Contains(body, "sandbox_claude_credential") {
		t.Errorf("unreported credential appeared: %s", body)
	}
	no := false
	f.dc.SetHostClaude(false, false, &no)
	if body := get(); !strings.Contains(body, `"sandbox_claude_credential":false`) {
		t.Errorf("want sandbox_claude_credential false: %s", body)
	}
	yes := true
	f.dc.SetHostClaude(true, false, &yes)
	if body := get(); !strings.Contains(body, `"sandbox_claude_credential":true`) {
		t.Errorf("want sandbox_claude_credential true: %s", body)
	}
}
