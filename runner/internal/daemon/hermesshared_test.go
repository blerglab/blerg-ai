package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A cluster's shared provider fragment lists the same models, under the same
// routing IDs and display names, as the identical ~/.hermes/config.yaml does
// on a desktop.
func TestProbeHermesModelsConfigMatchesDesktop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential sent")
		}
		fmt.Fprint(w, `{"data":[{"id":"nemotron-super"},{"id":"nemotron-nano"}]}`)
	}))
	defer srv.Close()
	vl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"nemotron-nano"}]}`)
	}))
	defer vl.Close()
	cfg := "model:\n  provider: custom\n  base_url: " + srv.URL + "/v1\n  default: nemotron-super\nproviders:\n  local-super-vl:\n    base_url: " + vl.URL + "/v1\n    api_mode: chat_completions\n"
	p := filepath.Join(t.TempDir(), "config.yaml")
	mustWrite(t, p, cfg)
	desktop, err := probeHermesModelsAt(context.Background(), p, newHermesProbeClient())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ProbeHermesModelsConfig(context.Background(), []byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != len(desktop) {
		t.Fatalf("shared %v, desktop %v", modelIDs(shared), modelIDs(desktop))
	}
	for i := range shared {
		if shared[i].ID != desktop[i].ID || shared[i].Name != desktop[i].Name {
			t.Errorf("model %d: shared %q/%q, desktop %q/%q", i, shared[i].ID, shared[i].Name, desktop[i].ID, desktop[i].Name)
		}
	}
	if !slices.Contains(modelIDs(shared), "custom:local-super-vl/nemotron-nano") {
		t.Errorf("named provider route missing: %v", modelIDs(shared))
	}
}

func TestProbeHermesModelsConfigRefusesUnusable(t *testing.T) {
	for name, cfg := range map[string]string{
		"empty":   "",
		"garbage": "model: [unterminated\n",
		"alias":   "model: &m\n  provider: custom\nother: *m\n",
	} {
		if _, err := ProbeHermesModelsConfig(context.Background(), []byte(cfg)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestHermesConfigLocal(t *testing.T) {
	for _, tc := range []struct {
		name, cfg string
		local     bool
	}{
		{"private providers", "model:\n  provider: custom\n  base_url: http://[fd12:3456::10]:8000/v1\nproviders:\n  local-super-vl:\n    base_url: http://[fd12:3456::11]:8000/v1\n", true},
		{"loopback", "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\n", true},
		{"public provider", "model:\n  provider: custom\n  base_url: http://[fd12:3456::10]:8000/v1\nproviders:\n  remote:\n    base_url: https://example.invalid/v1\n", false},
		{"documentation address is not LAN", "model:\n  provider: custom\n  base_url: http://192.0.2.10:8000/v1\n", false},
		{"provider key", "model:\n  provider: custom\n  base_url: http://[fd12:3456::10]:8000/v1\nproviders:\n  local-super-vl:\n    base_url: http://[fd12:3456::11]:8000/v1\n    api_key: synthetic\n", false},
		{"no default endpoint", "providers:\n  local-super-vl:\n    base_url: http://[fd12:3456::11]:8000/v1\n", false},
		{"unparseable", "model: [\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HermesConfigLocal([]byte(tc.cfg)); got != tc.local {
				t.Fatalf("HermesConfigLocal = %v, want %v", got, tc.local)
			}
		})
	}
}

// The pod materializes its config after its first hello; RefreshLocalEngines
// drops the cached answer so the next heartbeat reports the new one.
func TestRefreshLocalEnginesDropsCache(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	for _, k := range openclawProviderKeyEnv {
		t.Setenv(k, "")
	}
	resetLocalEnginesCache()
	if slices.Contains(localEngines(), "hermes") {
		t.Fatal("hermes local with no config")
	}
	mustWrite(t, filepath.Join(h, ".hermes", "config.yaml"), "model:\n  provider: custom\n  base_url: http://127.0.0.1:8000/v1\n")
	if slices.Contains(localEngines(), "hermes") {
		t.Fatal("cache not in effect (test premise)")
	}
	RefreshLocalEngines()
	if !slices.Contains(localEngines(), "hermes") {
		t.Fatal("hermes not local after RefreshLocalEngines")
	}
	localCache.mu.Lock()
	localCache.at = time.Time{}
	localCache.mu.Unlock()
}

// The install's template is a fragment the desktop parser accepts as it is,
// with both named providers.
func TestHermesProvidersExampleParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "install", "k8s", "hermes-providers.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseHermesRouteConfig(data)
	if err != nil {
		t.Fatalf("example does not parse: %v", err)
	}
	if cfg.Default.Provider != "custom" || len(cfg.Providers) != 2 || cfg.Fallback || cfg.Unknown {
		t.Fatalf("example = %+v", cfg)
	}
}
