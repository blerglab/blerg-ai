package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopConfigNeverUsesHistoricalInstallWideUpload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	t.Setenv("BLERG_RUNNER_SHARED_AGENT_CONFIG_BUNDLE", "")
	private := []byte("private desktop skills and rules")
	if err := os.WriteFile(filepath.Join(dir, "agent-config.tar.gz"), private, 0o600); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(NewHub(), nil, "fixture-master", nil, "")
	request := func(method string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/agent-config", bytes.NewReader(private))
		r.Header.Set("Authorization", "Bearer fixture-master")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := request("GET", api.HandleGetAgentConfig); w.Code != 404 || bytes.Contains(w.Body.Bytes(), private) {
		t.Fatal("historical desktop archive was served")
	}
	if w := request("POST", api.HandlePostAgentConfig); w.Code != 410 {
		t.Fatal("automatic desktop config upload remained available")
	}
	operator := filepath.Join(dir, "operator-config.tar.gz")
	if err := os.WriteFile(operator, []byte("explicit operator source"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLERG_RUNNER_SHARED_AGENT_CONFIG_BUNDLE", operator)
	if w := request("GET", api.HandleGetAgentConfig); w.Code != 200 || w.Body.String() != "explicit operator source" {
		t.Fatal("operator source unavailable")
	}
}
