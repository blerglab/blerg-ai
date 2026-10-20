package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A start pinned to one daemon (a card run on a chosen desktop) names the card's repository,
// often as owner/name: it runs in the folder that daemon has for that repository.
func TestPinnedDaemonStartResolvesRepoOnThatDaemon(t *testing.T) {
	hub := NewHub()
	dc := &DaemonConn{ID: "fixture-laptop", Name: "laptop", send: make(chan []byte, 8)}
	dc.SetAvailableEngines([]string{"claude"})
	dc.SetCheckedOutRepos([]string{"app-checkout", "tool", "lib"})
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{
		"app-checkout": {Provider: gitprovider.GitHubID, FullName: "acme/app"},
		"lib":          {Provider: gitprovider.GitHubID, FullName: "someone-else/lib"},
	}, nil)
	hub.Register(dc)
	api := NewAPI(hub, nil, "fixture-token", nil, "")
	for _, tc := range []struct{ repo, want string }{
		{"acme/app", "app-checkout"},     // by the folder's origin
		{"app-checkout", "app-checkout"}, // a folder name is used as is
		{"acme/tool", "tool"},            // same name, origin unknown
		{"acme/lib", "acme/lib"},         // same name, but another repository: left to the daemon
		{"acme/missing", "acme/missing"},
	} {
		_, problem := api.StartSession(t.Context(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
			runnerStartRequest{Repo: tc.repo, Runtime: "daemon", TargetDaemonID: dc.ID}, "")
		if problem != nil {
			t.Fatalf("%s: %v", tc.repo, problem)
		}
		var spawn protocol.SpawnSession
		if err := json.Unmarshal(<-dc.send, &spawn); err != nil {
			t.Fatal(err)
		}
		if spawn.Repo != tc.want {
			t.Errorf("repo %q ran in folder %q, want %q", tc.repo, spawn.Repo, tc.want)
		}
	}
}
