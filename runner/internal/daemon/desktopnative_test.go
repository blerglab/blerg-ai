package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDesktopNativeHelpersUseRotatingSessionCredentials(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	var expected atomic.Value
	expected.Store("Bearer session-initial")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected.Load().(string) {
			t.Error("native helper escaped its session credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "memories") {
				_, _ = w.Write([]byte(`[{"name":"fixture","kind":"project","summary":"standing context fixture"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		} else {
			_, _ = w.Write([]byte(`{"url":"/m/fixture"}`))
		}
	}))
	defer server.Close()
	h, _, work := newTestHost(t, &scriptedAgentProvider{}, "")
	h.cfg.ServerHTTP, h.cfg.DaemonToken = server.URL, "bdt_"+strings.Repeat("a", 64)
	msg := protocol.SpawnSession{SessionID: "native-session", Repo: "app", SessionToken: "session-initial"}
	cfg := h.nativeLoopConfig(msg, nativeLoopInputs{workDir: work, model: "fixture", confined: true})
	if !strings.Contains(cfg.System, "standing context fixture") {
		t.Fatal("native context did not use its session credential")
	}
	if err := os.WriteFile(filepath.Join(work, "index.html"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionTokenFile(DaemonStateDir(), msg.SessionID, "session-renewed"); err != nil {
		t.Fatal(err)
	}
	expected.Store("Bearer session-renewed")
	for name, args := range map[string]string{"memory_write": `{"name":"fixture","content":"fixture"}`, "push_mockup": `{"dir":"."}`} {
		tool, ok := cfg.Registry.Get(name)
		if !ok {
			t.Fatal("native helper absent", name)
		}
		if _, err := tool.Execute(t.Context(), json.RawMessage(args)); err != nil {
			t.Fatal("native helper failed", name, err)
		}
	}
}
