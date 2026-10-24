package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func waitWorkspaceResult(t *testing.T, s *agentTestSender, id string) protocol.WorkspaceFileResult {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, m := range s.msgs {
			if r, ok := m.(protocol.WorkspaceFileResult); ok && r.RequestID == id {
				s.mu.Unlock()
				return r
			}
		}
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no workspace result")
	return protocol.WorkspaceFileResult{}
}
func TestAgentWorkspaceReadUsesOwnFolderAndNeverResumes(t *testing.T) {
	host, sender, dir := newTestHost(t, &scriptedAgentProvider{}, "")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture contents"), 0600); err != nil {
		t.Fatal(err)
	}
	host.Spawn(protocol.SpawnSession{SessionID: "workspace", Repo: "proj", Kind: "agent"})
	defer host.Kill("workspace")
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: "valid", SessionID: "workspace", Path: "README.md"})
	got := waitWorkspaceResult(t, sender, "valid")
	content, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if !got.OK || err != nil || string(content) != "fixture contents" || got.View != "markdown" || got.Path != "README.md" {
		t.Fatalf("wrong result: %+v decode=%v", got, err)
	}
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: "unknown", SessionID: "absent", Path: "README.md"})
	if r := waitWorkspaceResult(t, sender, "unknown"); r.OK || r.ErrorCode != "unavailable" || host.Has("absent") {
		t.Fatalf("missing session resumed: %+v", r)
	}
	host.Kill("workspace")
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: "ended", SessionID: "workspace", Path: "README.md"})
	if r := waitWorkspaceResult(t, sender, "ended"); r.OK || r.ErrorCode != "unavailable" {
		t.Fatalf("ended session read: %+v", r)
	}
}
func TestManagerWorkspaceReadRoutesAgentAndTerminalFolders(t *testing.T) {
	host, sender, dir := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: "workspace", Repo: "proj", Kind: "agent"})
	defer host.Kill("workspace")
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	manager.agents = host
	raw, _ := json.Marshal(protocol.ReadWorkspaceFile{RequestID: "agent", SessionID: "workspace", Path: "fixture.txt"})
	manager.handleReadWorkspaceFile(raw)
	if got := waitWorkspaceResult(t, sender, "agent"); !got.OK {
		t.Fatalf("agent route: %+v", got)
	}
	terminalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(terminalDir, "fixture.txt"), []byte("terminal"), 0600); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	info, err := os.Stat(terminalDir)
	if err != nil {
		t.Fatal(err)
	}
	manager.sessions["terminal"] = &Session{ProjectPath: t.TempDir(), workspace: terminalDir, workspaceInfo: info, done: ended}
	raw, _ = json.Marshal(protocol.ReadWorkspaceFile{RequestID: "terminal", SessionID: "terminal", Path: "fixture.txt"})
	manager.handleReadWorkspaceFile(raw)
	got := waitWorkspaceResult(t, sender, "terminal")
	content, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if !got.OK || err != nil || string(content) != "terminal" {
		t.Fatalf("terminal route %+v err=%v", got, err)
	}
	// Reattachment uses a different PTY cwd; reads enter a separate runtime helper.
	remote := processExplorer(t, terminalDir, legacyExplorerStart("terminal"))
	manager.BindWorkspaceExplorer("terminal", &legacySandboxExplorer{id: "terminal", alias: terminalDir, live: func() bool {
		select {
		case <-ended:
			return false
		default:
			return true
		}
	}, remote: remote})
	for i, name := range []string{filepath.Join(terminalDir, "fixture.txt"), "/workspace/fixture.txt"} {
		manager.sessions["terminal"].sandboxed = true
		request := fmt.Sprintf("terminal-absolute-%d", i)
		raw, _ = json.Marshal(protocol.ReadWorkspaceFile{RequestID: request, SessionID: "terminal", Path: name})
		manager.handleReadWorkspaceFile(raw)
		if got := waitWorkspaceResult(t, sender, request); !got.OK || got.Path != "fixture.txt" {
			t.Fatalf("terminal absolute %q: %+v", name, got)
		}
	}
	close(ended)
	raw, _ = json.Marshal(protocol.ReadWorkspaceFile{RequestID: "stopped", SessionID: "terminal", Path: "fixture.txt"})
	manager.handleReadWorkspaceFile(raw)
	if got := waitWorkspaceResult(t, sender, "stopped"); got.OK || got.ErrorCode != "unavailable" {
		t.Fatalf("stopped terminal read %+v", got)
	}
}

func TestAgentWorkspaceRootReplacementRefused(t *testing.T) {
	host, sender, dir := newTestHost(t, &scriptedAgentProvider{}, "")
	host.Spawn(protocol.SpawnSession{SessionID: "workspace", Repo: "proj", Kind: "agent"})
	defer host.Kill("workspace")
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "safe-looking.txt"), []byte("outside fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: "swapped", SessionID: "workspace", Path: "safe-looking.txt"})
	if got := waitWorkspaceResult(t, sender, "swapped"); got.OK || got.ContentB64 != "" || got.ErrorCode != "forbidden" {
		t.Fatalf("replacement workspace read: %+v", got)
	}
}

// Project, artifact/output and process directories are deliberately distinct. Absolute
// references must not lose a prefix in chat or fall back to any other directory.
func TestProjectReadmeReferences(t *testing.T) {
	host, sender, project := newTestHost(t, &scriptedAgentProvider{}, "")
	output := t.TempDir()
	workspaceFixture(t, project, "README.md", "# Project readme")
	workspaceFixture(t, output, "README.md", "wrong output readme")
	host.cfg.RecordsDir = output
	host.Spawn(protocol.SpawnSession{SessionID: "readme", ProjectPath: project, Kind: "agent"})
	defer host.Kill("readme")
	for i, name := range []string{"README.md", "./README.md", filepath.Join(project, "README.md")} {
		id := fmt.Sprintf("read-%d", i)
		host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: id, SessionID: "readme", Path: name})
		got := waitWorkspaceResult(t, sender, id)
		body, err := base64.StdEncoding.DecodeString(got.ContentB64)
		if !got.OK || err != nil || string(body) != "# Project readme" || got.Path != "README.md" {
			t.Fatalf("reference %q: %+v decode=%v", name, got, err)
		}
	}
}

func TestBoundWorkspaceAbsoluteReferencesStayConfined(t *testing.T) {
	root, output, other := t.TempDir(), t.TempDir(), t.TempDir()
	workspaceFixture(t, root, "README.md", "project")
	workspaceFixture(t, output, "README.md", "output")
	workspaceFixture(t, other, "README.md", "unauthorized")
	workspaceFixture(t, root, ".env", "protected")
	workspaceLink(t, filepath.Join(other, "README.md"), filepath.Join(root, "escape.md"))
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, sandbox := range []bool{false, true} {
		binding := workspaceFileBinding{root: root, info: info}
		if sandbox {
			binding.alias = sandboxWorkdir
		}
		limits := workspaceFileLimits{expectedRoot: info}
		for _, name := range []string{filepath.Join(root, "README.md"), "README.md"} {
			got := readBoundWorkspaceFile(binding, name, limits)
			if !got.OK || got.Path != "README.md" {
				t.Fatalf("sandbox=%v path=%q: %+v", sandbox, name, got)
			}
			if filepath.IsAbs(name) && got.RequestedPath != name {
				t.Fatalf("request not bound: %+v", got)
			}
		}
		got := readBoundWorkspaceFile(binding, "/workspace/README.md", limits)
		if got.OK != sandbox {
			t.Fatalf("mount alias sandbox=%v: %+v", sandbox, got)
		}
		for _, name := range []string{
			filepath.Join(output, "README.md"), filepath.Join(other, "README.md"), root + "-other/README.md",
			root + "/../README.md", root + "/docs/../README.md", root + "/%2e%2e/README.md", root + "/%252e%252e/README.md",
			root + "/.env", root + "/escape.md", "/workspace/../README.md", "/workspace-other/README.md", "/etc/passwd", "//workspace/README.md",
		} {
			got := readBoundWorkspaceFile(binding, name, limits)
			if got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
				t.Fatalf("sandbox=%v escaped %q: %+v", sandbox, name, got)
			}
		}
		for _, name := range []string{"missing.md", filepath.Join(root, "missing.md")} {
			if got := readBoundWorkspaceFile(binding, name, limits); got.OK || got.ErrorCode != "not_found" {
				t.Fatalf("missing %q: %+v", name, got)
			}
		}
	}
	// The old chat conversion of /workspace/app/README.md produces app/README.md,
	// which reproduces the reported not_found even though the launch root has README.md.
	if got := readWorkspaceFile(root, "app/README.md", workspaceFileLimits{}); got.ErrorCode != "not_found" {
		t.Fatalf("old conversion: %+v", got)
	}
}

// Physical-directory tools may return a different absolute spelling than the
// symlinked path recorded at launch. Both must name the same pinned workspace.
func TestProjectReadmeSymlinkRoots(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		for _, replaceRoot := range []bool{false, true} {
			t.Run(fmt.Sprintf("ancestor=%v/replace=%v", ancestor, replaceRoot), func(t *testing.T) {
				host, sender, project := newTestHost(t, &scriptedAgentProvider{}, "")
				workspaceFixture(t, project, "README.md", "# Pinned project")
				alias := filepath.Join(t.TempDir(), "project-alias")
				link, target := alias, project
				if ancestor {
					target = filepath.Dir(project)
					alias = filepath.Join(link, filepath.Base(project))
				}
				workspaceLink(t, target, link)
				host.Spawn(protocol.SpawnSession{SessionID: "readme", ProjectPath: alias, Kind: "agent"})
				defer host.Kill("readme")
				canonical, err := filepath.EvalSymlinks(project)
				if err != nil {
					t.Fatal(err)
				}
				for i, name := range []string{"README.md", filepath.Join(alias, "README.md"), filepath.Join(canonical, "README.md"), filepath.Join(canonical, "missing.md")} {
					t.Run(fmt.Sprintf("reference-%d", i), func(t *testing.T) {
						id := fmt.Sprintf("read-%d", i)
						host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: id, SessionID: "readme", Path: name})
						got := waitWorkspaceResult(t, sender, id)
						if i == 3 {
							if got.OK || got.ErrorCode != "not_found" || got.ContentB64 != "" {
								t.Fatalf("canonical missing reference: %+v", got)
							}
							return
						}
						body, err := base64.StdEncoding.DecodeString(got.ContentB64)
						if !got.OK || err != nil || string(body) != "# Pinned project" || got.Path != "README.md" {
							t.Fatalf("reference %q: %+v decode=%v", name, got, err)
						}
						if filepath.IsAbs(name) && got.RequestedPath != name {
							t.Fatalf("request not bound: %+v", got)
						}
					})
				}
				other := t.TempDir()
				workspaceFixture(t, other, "README.md", "unrelated project")
				if replaceRoot {
					if err := os.Rename(project, project+"-original"); err != nil {
						t.Fatal(err)
					}
					workspaceLink(t, other, project)
				} else {
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if ancestor {
						replacement := filepath.Join(other, filepath.Base(project))
						workspaceFixture(t, replacement, "README.md", "unrelated project")
					}
					workspaceLink(t, other, link)
				}
				retargeted, err := filepath.EvalSymlinks(alias)
				if err != nil {
					t.Fatal(err)
				}
				for i, root := range []string{"", alias, canonical, retargeted} {
					id := fmt.Sprintf("retargeted-%d", i)
					host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{RequestID: id, SessionID: "readme", Path: filepath.Join(root, "README.md")})
					if got := waitWorkspaceResult(t, sender, id); got.OK || got.ErrorCode != "forbidden" || got.ContentB64 != "" {
						t.Fatalf("retargeted root %q: %+v", root, got)
					}
				}
			})
		}
	}
}
