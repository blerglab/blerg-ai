package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// contractDesktop is f.dc as a 0.5 desktop with workspace discovery over a real repos root holding
// the git repositories named, answered by the daemon's own discovery service. It returns the spawn
// messages the desktop receives.
func contractDesktop(t *testing.T, f *privFixture, repos ...string) <-chan protocol.SpawnSession {
	t.Helper()
	origins := map[string]string{}
	for _, name := range repos {
		origins[name] = ""
	}
	return contractDesktopWithOrigins(t, f, origins)
}

// contractDesktopWithOrigins is contractDesktop with each repository's origin remote ("" for none).
func contractDesktopWithOrigins(t *testing.T, f *privFixture, origins map[string]string) <-chan protocol.SpawnSession {
	t.Helper()
	var repos []string
	for name := range origins {
		repos = append(repos, name)
	}
	dc := f.dc
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range repos {
		for _, directory := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(root, name, ".git", directory), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, name, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if origin := origins[name]; origin != "" {
			config := "[remote \"origin\"]\n\turl = https://github.com/" + origin + ".git\n"
			if err := os.WriteFile(filepath.Join(root, name, ".git", "config"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	dc.SetReposRoot(root)
	dc.SetCheckedOutRepos(repos)
	dc.DiscoveryVersion = 1
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("sandbox", "linux")
	dc.SetSandboxAvailable(true)
	dc.SetAvailableEngines([]string{"claude"})
	dc.SetHostClaude(true, false, nil)
	service := discovery.NewOwned(func() string { return root }, dc.ID)
	spawned := make(chan protocol.SpawnSession, 4)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case raw := <-dc.send:
				var header struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &header)
				switch header.Type {
				case "discover_workspace":
					var req protocol.DiscoverWorkspace
					_ = json.Unmarshal(raw, &req)
					f.hub.deliverWorkspaceDiscovery(dc, service.Handle(t.Context(), req))
				case "spawn_session":
					var msg protocol.SpawnSession
					_ = json.Unmarshal(raw, &msg)
					spawned <- msg
				}
			}
		}
	}()
	return spawned
}

// A board start names its repository by folder; on a desktop with workspace discovery the runner
// resolves it into a fresh, bound selection of that repository and starts in the sandbox with it.
func TestContractStartResolvesABareRepoIntoAFreshWorkspace(t *testing.T) {
	f := newPrivFixture(t)
	spawned := contractDesktop(t, f, "blerg", "other")
	principal := RunnerPrincipal{Kind: "human", Sub: privAcctA, Sid: testSID, Caps: []string{"session.start"}}
	sid := newUUID()
	if apiErr := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "blerg", DaemonID: f.dc.ID, Runtime: "docker", Engine: "claude", Title: "Board", Prompt: "triage"}, principal, sid); apiErr != nil {
		t.Fatalf("start: %v", apiErr)
	}
	var msg protocol.SpawnSession
	select {
	case msg = <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("the board start was not dispatched")
	}
	if msg.Workspace == nil || len(msg.Workspace.Members) != 1 || msg.Workspace.Members[0].Name != "blerg" || msg.Workspace.OwnerID != privAcctA ||
		msg.Workspace.Runtime != "sandbox" || !msg.Sandbox || msg.Workspace.Members[0].SourceIdentity == "" {
		t.Fatalf("spawn workspace = %+v sandbox=%v", msg.Workspace, msg.Sandbox)
	}
	if stored, err := db.GetSessionWorkspace(t.Context(), f.pool, sid); err != nil || stored == nil || stored.Manifest.Digest != msg.Workspace.Digest {
		t.Fatalf("workspace not persisted: %v", err)
	}
}

// A repository the desktop does not have is refused with what to do, before anything starts.
func TestContractStartRefusesAMissingRepoPlainly(t *testing.T) {
	f := newPrivFixture(t)
	spawned := contractDesktop(t, f, "other")
	principal := RunnerPrincipal{Kind: "human", Sub: privAcctA, Sid: testSID, Caps: []string{"session.start"}}
	apiErr := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "blerg", DaemonID: f.dc.ID, Runtime: "docker", Engine: "claude"}, principal, newUUID())
	if apiErr == nil || !strings.Contains(apiErr.Error(), "has no repository named blerg") || strings.Contains(apiErr.Error(), "source references") {
		t.Fatalf("missing repo: %v", apiErr)
	}
	select {
	case msg := <-spawned:
		t.Fatalf("a refused start dispatched: %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

func contractPrincipal() RunnerPrincipal {
	return RunnerPrincipal{Kind: "human", Sub: privAcctA, Sid: testSID, Caps: []string{"session.start"}}
}

func noContractSpawn(t *testing.T, spawned <-chan protocol.SpawnSession) {
	t.Helper()
	select {
	case msg := <-spawned:
		t.Fatalf("a refused start dispatched: %+v", msg.Workspace)
	case <-time.After(200 * time.Millisecond):
	}
}

// A start pinned to a desktop is resolved from fresh discovery like an unpinned one: two
// repositories with the requested origin are ambiguous, never the first in sorted order.
func TestContractStartPinnedRefusesDuplicateOrigins(t *testing.T) {
	f := newPrivFixture(t)
	spawned := contractDesktopWithOrigins(t, f, map[string]string{"alpha": "example/app", "beta": "example/app"})
	f.dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"alpha": {Provider: "github", FullName: "example/app"}, "beta": {Provider: "github", FullName: "example/app"}}, nil)
	apiErr := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "example/app", DaemonID: f.dc.ID, Runtime: "docker", Engine: "claude"}, contractPrincipal(), newUUID())
	if apiErr == nil || !strings.Contains(apiErr.Error(), "2 repositories matching example/app") {
		t.Fatalf("pinned duplicate origin: %v", apiErr)
	}
	noContractSpawn(t, spawned)
}

// A pinned start never trusts the desktop's cached origin: the fresh origin decides, and a folder
// whose real origin is another repository is not the one asked for.
func TestContractStartPinnedRefusesAStaleOrigin(t *testing.T) {
	f := newPrivFixture(t)
	spawned := contractDesktopWithOrigins(t, f, map[string]string{"alpha": "unrelated/private"})
	f.dc.SetRepoOrigins(map[string]protocol.RepoOrigin{"alpha": {Provider: "github", FullName: "example/app"}}, nil) // stale cache
	apiErr := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "example/app", DaemonID: f.dc.ID, Runtime: "docker", Engine: "claude"}, contractPrincipal(), newUUID())
	if apiErr == nil || !strings.Contains(apiErr.Error(), "has no repository named example/app") {
		t.Fatalf("pinned stale origin: %v", apiErr)
	}
	noContractSpawn(t, spawned)
	// The exact fresh origin still resolves.
	f2 := newPrivFixture(t)
	spawned2 := contractDesktopWithOrigins(t, f2, map[string]string{"alpha": "example/app", "beta": "other/thing"})
	if apiErr := f2.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "example/app", DaemonID: f2.dc.ID, Runtime: "docker", Engine: "claude"}, contractPrincipal(), newUUID()); apiErr != nil {
		t.Fatalf("pinned exact origin: %v", apiErr)
	}
	select {
	case msg := <-spawned2:
		if msg.Workspace == nil || msg.Workspace.Members[0].Name != "alpha" {
			t.Fatalf("resolved %+v", msg.Workspace)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not dispatched")
	}
}

// fakeListingDesktop answers discovery with pages built by page(n) for the n-th list request, and
// records whether anything was resolved or spawned.
func fakeListingDesktop(t *testing.T, f *privFixture, page func(n int) protocol.DiscoveryPage) (resolved, spawned *bool) {
	t.Helper()
	dc := f.dc
	dc.DiscoveryVersion = 1
	dc.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("sandbox", "linux")
	dc.SetSandboxAvailable(true)
	dc.SetAvailableEngines([]string{"claude"})
	dc.SetHostClaude(true, false, nil)
	var r, s bool
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	n := 0
	go func() {
		for {
			select {
			case <-done:
				return
			case raw := <-dc.send:
				var req protocol.DiscoverWorkspace
				_ = json.Unmarshal(raw, &req)
				switch {
				case req.Type == "discover_workspace" && req.Operation == "list":
					p := page(n)
					n++
					f.hub.deliverWorkspaceDiscovery(dc, protocol.WorkspaceDiscoveryResult{Type: "workspace_discovery_result", RequestID: req.RequestID, OK: true, Page: &p})
				case req.Type == "discover_workspace":
					r = true
				case strings.Contains(string(raw), `"spawn_session"`):
					s = true
				}
			}
		}
	}()
	return &r, &s
}

func repoEntry(name string) protocol.DiscoveryEntry {
	return protocol.DiscoveryEntry{Name: name, Path: name, Kind: "repository", SourceIdentity: "source-" + name}
}

// A listing that cannot be shown complete never proves a match unique: a last page that is still
// truncated (a duplicate may lie beyond the daemon's read cap), or more pages than the budget,
// refuses before anything is resolved or started.
func TestContractStartRefusesAnIncompleteListing(t *testing.T) {
	for name, page := range map[string]func(int) protocol.DiscoveryPage{
		"truncated last page": func(int) protocol.DiscoveryPage {
			return protocol.DiscoveryPage{RootIdentity: "root", Root: protocol.DiscoveryEntry{Name: ".", Path: ".", Kind: "directory", SourceIdentity: "root"}, Entries: []protocol.DiscoveryEntry{repoEntry("blerg")}, Truncated: true}
		},
		"page budget exhausted": func(n int) protocol.DiscoveryPage {
			entries := []protocol.DiscoveryEntry{repoEntry(fmt.Sprintf("other-%d", n))}
			if n == 0 {
				entries = append(entries, repoEntry("blerg"))
			}
			return protocol.DiscoveryPage{RootIdentity: "root", Root: protocol.DiscoveryEntry{Name: ".", Path: ".", Kind: "directory", SourceIdentity: "root"}, Entries: entries, Truncated: true, NextCursor: fmt.Sprintf("cursor-%d", n+1)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPrivFixture(t)
			resolved, spawned := fakeListingDesktop(t, f, page)
			apiErr := f.api.startBoardSessionOnDaemon(t.Context(), runnerStartRequest{Repo: "blerg", DaemonID: f.dc.ID, Runtime: "docker", Engine: "claude"}, contractPrincipal(), newUUID())
			if apiErr == nil || !strings.Contains(apiErr.Error(), "too many folders to be sure") {
				t.Fatalf("incomplete listing: %v", apiErr)
			}
			time.Sleep(100 * time.Millisecond)
			if *resolved || *spawned {
				t.Fatalf("resolved=%v spawned=%v after an incomplete listing", *resolved, *spawned)
			}
		})
	}
}
