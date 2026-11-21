package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type whatsNewFixture struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func startWhatsNewCore(t *testing.T) whatsNewFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	path := filepath.Join(t.TempDir(), "core.json")
	cmd := exec.Command("go", "test", "./internal/api", "-run", "^TestWhatsNewHTTPFixture$", "-timeout", "5m")
	cmd.Dir = "../../../core"
	goFlags := os.Getenv("GOFLAGS")
	if goFlags == "" {
		workers := os.Getenv("BLERG_TOOL_WORKERS")
		if workers == "" {
			workers = "2"
		}
		goFlags = "-p=" + workers
	}
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS="+goFlags, "DATABASE_URL="+os.Getenv("TEST_DATABASE_URL"), "BLERG_WHATS_NEW_CORE_FIXTURE="+path)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = os.WriteFile(path+".stop", nil, 0600)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("core fixture: %v\n%s", err, output.String())
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("core fixture did not stop")
		}
	})
	for range 900 {
		if raw, err := os.ReadFile(path); err == nil {
			var config whatsNewFixture
			if json.Unmarshal(raw, &config) == nil && config.URL != "" {
				return config
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("core fixture did not become ready")
	return whatsNewFixture{}
}

func newWhatsNewRunner(t *testing.T, core whatsNewFixture) *httptest.Server {
	t.Helper()
	app := NewAPI(NewHub(), nil, "", nil, "")
	app.SetCoreAuth(ownedCoreAuthClient(t, core.URL))
	app.SetCoreCredentials(core.URL, "fixture-internal-key")
	mux := http.NewServeMux()
	app.RegisterWhatsNewRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestWhatsNewRealRunnerToCore(t *testing.T) {
	core := startWhatsNewCore(t)
	runner := newWhatsNewRunner(t, core)
	options, err := http.NewRequest("OPTIONS", core.URL+"/internal/accounts/whats-new", nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Header.Set("Origin", "https://runner.example.com")
	response, err := http.DefaultClient.Do(options)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("core must remain without CORS")
	}
	call := func(method string) bool {
		t.Helper()
		req, err := http.NewRequest(method, runner.URL+"/api/me/whats-new?release=0.6.0", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+core.Token)
		resp, err := runner.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("real runner -> core %s: %d", method, resp.StatusCode)
		}
		var body struct {
			Pending  bool
			Releases []any
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Releases) < 2 {
			t.Fatal("missing real bundled release notes")
		}
		return body.Pending
	}
	for range 2 {
		if !call("GET") {
			t.Fatal("reading consumed the notes")
		}
	}
	if call("POST") || call("GET") {
		t.Fatal("dismissal did not persist")
	}
}

func TestWhatsNewBrowserHTTPFixture(t *testing.T) {
	path := os.Getenv("BLERG_WHATS_NEW_RUNNER_FIXTURE")
	if path == "" {
		t.Skip("subprocess browser fixture")
	}
	core := startWhatsNewCore(t)
	runner := newWhatsNewRunner(t, core)
	raw, _ := json.Marshal(whatsNewFixture{URL: runner.URL, Token: core.Token})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Minute)
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
			t.Fatal("browser fixture timed out")
		}
	}
}
