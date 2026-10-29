package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type launchExplorer struct{ requestServices }

func (*launchExplorer) Explore(context.Context, protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	return protocol.WorkspaceExplorerResult{OK: true}
}

type launchSender struct{ started chan protocol.SessionStarted }

func (s launchSender) Send(v any) error {
	if started, ok := v.(protocol.SessionStarted); ok {
		s.started <- started
	}
	return nil
}

// Exercise the same prepared-reader admission as Run through real AgentHost
// startup. Previously the pod supplied no callback and unconditionally refused
// every cluster workspace as "host workspace reader authority unavailable".
func TestClusterPreparedWorkspaceStartsAgentHost(t *testing.T) {
	for _, kind := range []string{"repo", "folder", "child"} {
		t.Run(kind, func(t *testing.T) {
			p := clusterworkspace.Plan{SessionID: "session", Manifest: protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/one"}}}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, Members: []clusterworkspace.Member{{ID: "one", Provider: "github", CloneURL: "https://github.com/org/one.git", PushURL: "https://github.com/org/one.git", CredentialHost: "github.com", DestinationIdentity: "org/one"}}}
			if kind == "folder" {
				p.Manifest.Kind = "folder"
				p.Manifest.Members[0].Path = "one"
				member := p.Manifest.Members[0]
				member.ID, member.Path, member.Name, member.SourceIdentity, member.CommonDirectoryIdentity, member.Destination = "two", "two", "two", "source-two", "common-two", "org/two"
				p.Manifest.Members = append(p.Manifest.Members, member)
				context := p.Members[0]
				context.ID, context.CloneURL, context.PushURL, context.DestinationIdentity = "two", "https://github.com/org/two.git", "https://github.com/org/two.git", "org/two"
				p.Members = append(p.Members, context)
			}
			if kind == "child" {
				p.Manifest.Members[0].ID, p.Manifest.Members[0].Name, p.Manifest.Members[0].Path = "two", "app", "app-2"
				p.Members[0].ID = "two"
				p.LayoutSources = []protocol.DiscoverySource{{ID: "one", Name: "app"}, {ID: "two", Name: "app"}}
			}
			expectedGuide := p.Manifest.Members[0].ID + ": " + filepath.Join(clusterworkspace.PublicRoot, p.Manifest.Members[0].Path) + " (wip/"
			if !strings.Contains(clusterWorkspaceGuide(p), expectedGuide) {
				t.Errorf("guide uses member ID instead of projected path: %s", clusterWorkspaceGuide(p))
			}
			rt, err := clusterworkspace.New(p, requestGit{})
			if err != nil {
				t.Fatal(err)
			}
			if err := rt.Prepare(t.Context(), false); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			cfg := Config{Home: home, SessionID: p.SessionID, Workspace: &p}
			cwd := filepath.Join(home, "projects", p.Manifest.Members[0].Path)
			if err := os.MkdirAll(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			reader := &launchExplorer{}
			var services ClusterServices = reader
			admit := clusterAgentWorkspace(cfg, rt, func() ClusterServices { return services })
			msg := podSpawn(cfg, cwd)
			// Missing readers, a changed manifest/path/session and fenced runtimes cannot
			// use the prepared root. The same adapter must still admit the exact launch.
			for _, change := range []func(*protocol.SpawnSession){
				func(m *protocol.SpawnSession) { m.SessionID = "other" },
				func(m *protocol.SpawnSession) { m.ProjectPath = home },
				func(m *protocol.SpawnSession) {
					changed := *m.Workspace
					changed.LaunchGeneration = "other"
					m.Workspace = &changed
				},
				func(m *protocol.SpawnSession) { m.Sandbox = true },
			} {
				bad := msg
				change(&bad)
				if _, _, err := admit(t.Context(), bad); err == nil {
					t.Fatal("foreign cluster admission")
				}
			}
			services = nil
			if _, _, err := admit(t.Context(), msg); err == nil {
				t.Fatal("missing reader admitted")
			}
			services = reader
			sender := launchSender{started: make(chan protocol.SessionStarted, 1)}
			host := newPodAgentHost(sender, cfg, rt, func() ClusterServices { return services }, daemon.AgentHostConfig{HomeDir: home, ReposRoot: filepath.Join(home, "projects"), APIKey: "fixture-api-key"})
			t.Cleanup(func() { host.Kill(p.SessionID) })
			host.Spawn(msg)
			select {
			case started := <-sender.started:
				if started.ProjectPath != cwd {
					t.Fatal("cluster root substituted")
				}
			default:
				t.Fatal("CLUSTER_AGENT_START: prepared cluster workspace did not reach session_started")
			}
			rt.FenceAdmission()
			if _, _, err := admit(t.Context(), msg); err == nil {
				t.Fatal("fenced runtime admitted")
			}

		})
	}
}
