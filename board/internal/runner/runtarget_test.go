package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A start that names its own target (a person picked where a card runs) sends it, over the
// board's configured runtime; one that does not keeps the configured runtime.
func TestBlergRunnerStartSendsChosenTarget(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"session_id":"ext-1"}`))
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "key", "")
	k.SetRuntime("cluster")
	if _, err := k.Start(context.Background(), StartRequest{Repo: "app", Token: "automation-tok", Runtime: "docker", DaemonID: "daemon-1"}); err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != "docker" || got["daemon_id"] != "daemon-1" {
		t.Fatalf("chosen target not sent: %v", got)
	}
	if _, err := k.Start(context.Background(), StartRequest{Repo: "app", Token: "automation-tok"}); err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != "cluster" || got["daemon_id"] != nil {
		t.Fatalf("default start changed: %v", got)
	}
	if k.DefaultRuntime() != "cluster" {
		t.Fatalf("DefaultRuntime = %q", k.DefaultRuntime())
	}
}

// Discovery returns the runner's execution targets, typed: an id, a name and the runtimes.
func TestDiscoveryReturnsTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"known","engines":[],"targets":[{"name":"Cluster","runtimes":["cluster"]},{"daemon_id":"daemon-1","name":"laptop","runtimes":["daemon","docker"],"secret":"must-not-be-returned"}]}`))
	}))
	defer srv.Close()
	opts, err := NewBlergRunner(srv.URL, "operator", "").Discover(context.Background(), "owner-proof", "app", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Targets) != 2 || opts.Targets[1].DaemonID != "daemon-1" || opts.Targets[1].Name != "laptop" || len(opts.Targets[1].Runtimes) != 2 {
		t.Fatalf("targets = %+v", opts.Targets)
	}
	raw, _ := json.Marshal(opts)
	if strings.Contains(string(raw), "must-not-be-returned") {
		t.Fatal("arbitrary upstream metadata escaped typed discovery")
	}
}
