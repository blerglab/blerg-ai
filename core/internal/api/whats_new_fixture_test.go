package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Subprocess fixture: real core routes, migrations, sign-in and persisted account
// state. Configuration stays in a private temporary file, never test output.
func TestWhatsNewHTTPFixture(t *testing.T) {
	path := os.Getenv("BLERG_WHATS_NEW_CORE_FIXTURE")
	if path == "" {
		t.Skip("subprocess fixture")
	}
	t.Setenv("BLERG_RELEASE", "0.6.0")
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-internal-key"
	id := insertCredAccount(t, pool, "browser-reader")
	svc := deps.Identity.(*identity.Service)
	refresh, err := svc.IssueRefreshToken(t.Context(), id, "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.RefreshAccessToken(t.Context(), refresh, "blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE accounts SET whats_new_seen='0.5.4' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]string{"url": srv.URL, "token": token})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("fixture timed out")
		}
	}
}
