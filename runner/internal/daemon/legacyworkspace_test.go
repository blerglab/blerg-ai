package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestLegacySessionGetsProductionExplorerAndShell(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native host containment is Linux-only")
	}
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	host, _, workdir := newTestHost(t, &scriptedAgentProvider{}, "")
	initChildRepo(t, workdir)
	workspaceFixture(t, workdir, "README.md", "legacy project")
	// An old-protocol recovered session has a trusted project path, no manifest.
	host.spawn(protocol.SpawnSession{SessionID: "legacy-session", Kind: "agent", ProjectPath: workdir, Repo: "proj"}, nil, "", &adoptRequest{})
	defer host.Kill("legacy-session")
	service := hostLifecycleTestService(t, nil)
	shells := service.Supervisor.(*workspaceSupervisors).service
	manager := newManagerWithSender(newRecordingSender(16), ManagerConfig{ReposRoot: filepath.Dir(workdir), Workspaces: service})
	manager.agents = host
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	source := host.workspaceFileBinding("legacy-session")
	if source.info == nil {
		t.Fatal("legacy recovery did not start")
	}
	root := workspaceRootIdentity(source.info)
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: "legacy-legacy-session", Digest: "legacy-digest", Members: []protocol.WorkspaceMember{{ID: "repo", Name: "repository", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: root}
	allowed := true
	authorize := func(context.Context, protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		return protocol.LocalWorkspaceControlResult{OK: allowed, Manifest: &m, Binding: b, ShellEligible: true, Launch: protocol.SupervisorLaunch{SessionID: "legacy-session", LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: 1, Runtime: "host", ConnectionID: "connection"}}, nil
	}
	// A capable reconnect must also upgrade an earlier explorer-only attachment.
	caps.ShellVersion = 0
	if err := manager.attachLegacyWorkspaceWith(t.Context(), "legacy-session", caps, shells, authorize); err != nil {
		t.Fatal(err)
	}
	if !host.explorer.registered("legacy-session") {
		t.Fatal("recovered session has no explorer")
	}
	companion := manager.legacyWorkspaces["legacy-session"]
	for _, op := range []string{"list", "search", "status"} {
		req := protocol.WorkspaceExplorerRequest{SessionID: "legacy-session", Operation: op, Filter: "README", WorkspaceRuntimeRef: b.WorkspaceRuntimeRef}
		result := companion.Explore(t.Context(), req)
		if !result.OK {
			t.Fatalf("production %s failed: %+v", op, result)
		}
		if op == "search" && (len(result.Entries) != 1 || result.Entries[0].Path != "README.md") {
			t.Fatal(result)
		}
	}
	if _, err := shells.Binding("legacy-session"); err == nil {
		t.Fatal("unnegotiated shell was registered")
	}
	caps.ShellVersion = 1
	if err := manager.attachLegacyWorkspaceWith(t.Context(), "legacy-session", caps, shells, authorize); err != nil {
		t.Fatal("shell-capable reconnect", err)
	}
	binding, err := shells.Binding("legacy-session")
	if err != nil || binding.Owner != "owner" || binding.Root != root || !binding.RuntimeCapabilities.Supports("host", true) || binding.HostConsentValid {
		t.Fatalf("shell binding/consent: %+v %v", binding, err)
	}
	if err := manager.attachLegacyWorkspaceWith(t.Context(), "legacy-session", caps, shells, authorize); err != nil || manager.legacyWorkspaces["legacy-session"] != companion {
		t.Fatal("reconnect attachment", err)
	}
	allowed = false
	if result := companion.Explore(t.Context(), protocol.WorkspaceExplorerRequest{SessionID: "legacy-session", Operation: "list", WorkspaceRuntimeRef: b.WorkspaceRuntimeRef}); result.OK {
		t.Fatal("revoked owner can browse")
	}
	allowed = true
	host.Kill("legacy-session")
	if _, err := shells.Binding("legacy-session"); err == nil {
		t.Fatal("shell registration survived session stop")
	}
}

func TestLegacyWorkspaceRefusesMissingChangedOrUnnegotiatedProject(t *testing.T) {
	host, _, workdir := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: "legacy-refusal", Kind: "agent", ProjectPath: workdir, Repo: "proj"})
	defer host.Kill("legacy-refusal")
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{})
	manager.agents = host
	calls := 0
	authorize := func(context.Context, protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		calls++
		return protocol.LocalWorkspaceControlResult{}, nil
	}
	caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
	for _, id := range []string{"missing", "legacy-refusal"} {
		if err := manager.attachLegacyWorkspaceWith(t.Context(), id, protocol.WorkspaceCapabilities{}, nil, authorize); err == nil {
			t.Fatal("missing capability/project accepted")
		}
	}
	if err := os.Rename(workdir, workdir+"-original"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workdir + "-original")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.attachLegacyWorkspaceWith(t.Context(), "legacy-refusal", caps, nil, authorize); err == nil {
		t.Fatal("replacement project directory accepted")
	}
	if calls != 0 {
		t.Fatal("refusal allocated authority")
	}
}

func TestLegacySandboxAttachmentUsesRuntimeHelper(t *testing.T) {
	runtimeRoot, clone := t.TempDir(), t.TempDir()
	sourceRepo := t.TempDir()
	initChildRepo(t, sourceRepo)
	runtimeRoot = filepath.Join(runtimeRoot, "shared")
	cmd := exec.Command("git", "clone", "--shared", "--quiet", sourceRepo, runtimeRoot)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shared runtime fixture: %v %s", err, output)
	}
	workspaceFixture(t, runtimeRoot, "README.md", "runtime contents")
	workspaceFixture(t, clone, "HOST-ONLY.md", "must not appear")
	info, err := os.Stat(clone)
	if err != nil {
		t.Fatal(err)
	}
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{})
	manager.sessions["legacy-sandbox"] = &Session{workspace: clone, workspaceInfo: info, sandboxed: true, done: make(chan struct{})}
	start := legacyExplorerStart("legacy-sandbox")
	start.GitTargets = map[string]string{"repo": filepath.Join(sourceRepo, ".git", "objects")}
	remote := processExplorer(t, runtimeRoot, start)
	remote.container, remote.dockerContext = strings.Repeat("a", 64), "pinned-context"
	manager.legacySandboxReader = func(context.Context, string, workspaceFileBinding) *remoteWorkspaceExplorer { return remote }
	caps := protocol.WorkspaceCapabilitiesFor("sandbox", "linux")
	caps.ShellVersion = 0 // Exercise explorer independently of a locally installed Docker image.
	authorize := func(_ context.Context, root protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		m := legacyExplorerStart("legacy-sandbox").Manifest
		m.OwnerID = "owner"
		b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: root}
		return protocol.LocalWorkspaceControlResult{OK: true, Manifest: &m, Binding: b, Launch: protocol.SupervisorLaunch{SessionID: "legacy-sandbox", LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: 1, Runtime: "sandbox", ConnectionID: "connection"}}, nil
	}
	if err := manager.attachLegacyWorkspaceWith(t.Context(), "legacy-sandbox", caps, nil, authorize); err != nil {
		t.Fatal(err)
	}
	defer manager.explorer.Unbind("legacy-sandbox")
	companion := manager.legacyWorkspaces["legacy-sandbox"]
	for _, op := range []string{"list", "search", "status"} {
		result := companion.Explore(t.Context(), protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: op, SessionID: "legacy-sandbox", Operation: op, Filter: "README", WorkspaceRuntimeRef: companion.binding.WorkspaceRuntimeRef})
		if !result.OK {
			t.Fatalf("runtime %s: %+v", op, result)
		}
		for _, entry := range result.Entries {
			if entry.Name == "HOST-ONLY.md" {
				t.Fatal("sandbox read host clone")
			}
		}
	}
	command, _, err := manager.legacyShellCommand(t.Context(), "legacy-sandbox", manager.workspaceFileBinding("legacy-sandbox"), remote, protocol.LocalWorkspaceControlResult{Binding: companion.binding}, nil)
	if err != nil || command.Root != companion.binding.Root || command.PublicRoot != "/workspace" || !strings.Contains(strings.Join(command.Args, " "), "--context pinned-context exec -i --user") || !strings.Contains(strings.Join(command.Args, " "), remote.container) {
		t.Fatalf("unconfined sandbox shell: %+v %v", command, err)
	}
}

func legacyTestAuthority(id string, source workspaceFileBinding) func(context.Context, protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
	return func(_ context.Context, root protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Runtime: "host", OwnerID: "owner", LaunchGeneration: "legacy-" + id, Digest: "legacy-digest", Members: []protocol.WorkspaceMember{{ID: "repo", Name: "repository", Path: ".", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "source"}}}
		b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: 1}, ManifestDigest: m.Digest, Root: workspaceRootIdentity(source.info)}
		return protocol.LocalWorkspaceControlResult{OK: root == b.Root, Manifest: &m, Binding: b, Launch: protocol.SupervisorLaunch{SessionID: id, LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: 1, Runtime: "host", ConnectionID: "connection"}}, nil
	}
}

func TestLegacyAttachmentPreservesGitAndFileConsumers(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprintf("agent=%v", agent), func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
			initChildRepo(t, project)
			head := commitFile(t, project, "README.md", "legacy contents\n", "Project")
			child := "0f0e0d0c-0b0a-4908-8706-050403020100"
			if _, err := createSandboxClone(project, child, filepath.Dir(project)); err != nil {
				t.Fatal(err)
			}
			originGit(t, project, "checkout", "-qb", "wip/"+child)
			head = commitFile(t, project, "child.txt", "merged child\n", "Child")
			originGit(t, project, "checkout", "main")
			originGit(t, project, "merge", "--ff-only", "wip/"+child)
			sender := newRecordingSender(32)
			manager := newManagerWithSender(sender, ManagerConfig{})
			const id = "legacy-consumers"
			router := &manager.explorer
			if agent {
				host.sender = sender
				host.spawn(protocol.SpawnSession{SessionID: id, Kind: "agent", ProjectPath: project, Repo: "proj"}, nil, "", &adoptRequest{})
				defer host.Kill(id)
				manager.agents = host
				router = &host.explorer
			} else {
				info, err := os.Stat(project)
				if err != nil {
					t.Fatal(err)
				}
				manager.sessions[id] = &Session{workspace: project, workspaceInfo: info, done: make(chan struct{})}
			}
			source := manager.workspaceFileBinding(id)
			caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
			caps.ShellVersion = 0
			check := func(stage string) {
				t.Helper()
				// Exercise Manager dispatch, including the AgentHost branch, rather than a reader directly.
				for _, op := range []string{"origin", "commit", "patch", "child", "file"} {
					request := stage + "-" + op
					var msg any = protocol.ReadCommit{Type: "read_commit", RequestID: request, SessionID: id, SHA: head, Patch: op == "patch"}
					if op == "origin" {
						msg = protocol.ReadSessionOrigin{Type: "read_session_origin", RequestID: request, SessionID: id}
					}
					if op == "child" {
						msg = protocol.ReadCommit{Type: "read_commit", RequestID: request, SessionID: id, MergedChild: child}
					}
					if op == "file" {
						msg = protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: request, SessionID: id, Path: filepath.Join(project, "README.md")}
					}
					raw, err := json.Marshal(msg)
					if err != nil {
						t.Fatal(err)
					}
					switch op {
					case "origin":
						manager.handleReadSessionOrigin(raw)
					case "file":
						manager.handleReadWorkspaceFile(raw)
					default:
						manager.handleReadCommit(raw)
					}
					deadline := time.NewTimer(10 * time.Second)
					received := false
					for !received {
						select {
						case value := <-sender.ch:
							switch r := value.(type) {
							case protocol.CommitResult:
								if r.RequestID != request {
									continue
								}
								received = true
								if !r.OK || (op == "child" && !r.Merged) || (op == "patch" && r.PatchB64 == "") || (op == "commit" && (r.Commit == nil || r.Commit.SHA != head)) {
									t.Errorf("%s/%s: %+v", stage, op, r)
								}
							case protocol.SessionOriginResult:
								if r.RequestID != request {
									continue
								}
								received = true
								if !r.OK || r.Commit != head {
									t.Errorf("%s/origin: %+v", stage, r)
								}
							case protocol.WorkspaceFileResult:
								if r.RequestID != request {
									continue
								}
								received = true
								if !r.OK || r.ContentB64 != base64.StdEncoding.EncodeToString([]byte("legacy contents\n")) {
									t.Errorf("%s/file: %+v", stage, r)
								}
							}
						case <-deadline.C:
							t.Fatal("consumer reply timed out", stage, op)
						}
					}
					deadline.Stop()
				}
			}
			check("before")
			if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, nil, legacyTestAuthority(id, source)); err != nil {
				t.Fatal(err)
			}
			defer router.Unbind(id)
			check("attached")
			// Reader suspension must not reroute scalar consumers into v2; they
			// retain their existing pinned source and lifecycle checks.
			if err := router.Suspend(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			check("suspended")
			if err := router.Resume(id); err != nil {
				t.Fatal(err)
			}
			check("resumed")
			router.Unbind(id)
			check("detached")
		})
	}
}

func TestLegacyAttachmentProjectAliases(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		for _, change := range []string{"before", "after", "during-read", "replacement"} {
			t.Run(fmt.Sprintf("ancestor=%v/%s", ancestor, change), func(t *testing.T) {
				host, _, project := newTestHost(t, &scriptedAgentProvider{}, "")
				initChildRepo(t, project)
				workspaceFixture(t, project, "README.md", "pinned contents")
				link := filepath.Join(t.TempDir(), "project-alias")
				alias, target := link, project
				if ancestor {
					target = filepath.Dir(project)
					alias = filepath.Join(link, filepath.Base(project))
				}
				workspaceLink(t, target, link)
				const id = "legacy-alias"
				host.spawn(protocol.SpawnSession{SessionID: id, Kind: "agent", ProjectPath: alias, Repo: "proj"}, nil, "", &adoptRequest{})
				defer host.Kill(id)
				manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{})
				manager.agents = host
				source := manager.workspaceFileBinding(id)
				authorize := legacyTestAuthority(id, source)
				caps := protocol.WorkspaceCapabilitiesFor("host", "linux")
				caps.ShellVersion = 0
				retarget := func() {
					other := t.TempDir()
					if ancestor {
						workspaceFixture(t, filepath.Join(other, filepath.Base(project)), "README.md", "other")
					}
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, other, link)
				}
				if change == "before" {
					retarget()
					if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, nil, authorize); !errors.Is(err, errLegacyProjectChanged) {
						t.Fatal("retargeted alias admitted", err)
					}
					return
				}
				if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, nil, authorize); err != nil {
					t.Fatal("unchanged project alias refused", err)
				}
				defer host.explorer.Unbind(id)
				companion := manager.legacyWorkspaces[id]
				list := protocol.WorkspaceExplorerRequest{SessionID: id, Operation: "list", WorkspaceRuntimeRef: companion.binding.WorkspaceRuntimeRef}
				if got := companion.Explore(t.Context(), list); !got.OK {
					t.Fatal("alias explorer", got)
				}
				for _, root := range []string{"", alias, project} {
					got := companion.ReadFile(t.Context(), protocol.ReadWorkspaceFile{SessionID: id, Path: filepath.Join(root, "README.md"), WorkspaceRuntimeRef: companion.binding.WorkspaceRuntimeRef})
					if !got.OK || got.ContentB64 != base64.StdEncoding.EncodeToString([]byte("pinned contents")) {
						t.Fatal("alias/canonical reference", got)
					}
				}
				switch change {
				case "during-read":
					companion.reader = &legacyRetargetReader{WorkspaceExplorer: companion.reader, retarget: retarget}
				case "replacement":
					original := project + "-original"
					if err := os.Rename(project, original); err != nil {
						t.Fatal(err)
					}
					defer os.RemoveAll(original)
					if err := os.Mkdir(project, 0700); err != nil {
						t.Fatal(err)
					}
				default:
					retarget()
				}
				if got := companion.Explore(t.Context(), list); got.OK {
					t.Fatal("changed alias delivered explorer result")
				}
				if got := companion.ReadFile(t.Context(), protocol.ReadWorkspaceFile{SessionID: id, Path: "README.md", WorkspaceRuntimeRef: companion.binding.WorkspaceRuntimeRef}); got.OK || got.ContentB64 != "" {
					t.Fatal("changed alias delivered file", got)
				}
				if err := manager.attachLegacyWorkspaceWith(t.Context(), id, caps, nil, authorize); err == nil {
					t.Fatal("changed alias reattached")
				}
			})
		}
	}
}

type legacyRetargetReader struct {
	WorkspaceExplorer
	retarget func()
}

func (r *legacyRetargetReader) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	result := r.WorkspaceExplorer.Explore(ctx, req)
	r.retarget()
	return result
}
func (r *legacyRetargetReader) Close() error { return r.WorkspaceExplorer.(io.Closer).Close() }
