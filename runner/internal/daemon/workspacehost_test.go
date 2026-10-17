package daemon

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func waitWorkspaceResult(t *testing.T, s *agentTestSender, id string) protocol.WorkspaceFileResult {
	t.Helper()
	deadline := time.Now().Add(time.Second)
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
	manager.sessions["terminal"] = &Session{ProjectPath: terminalDir, workspace: terminalDir, workspaceInfo: info, done: ended}
	raw, _ = json.Marshal(protocol.ReadWorkspaceFile{RequestID: "terminal", SessionID: "terminal", Path: "fixture.txt"})
	manager.handleReadWorkspaceFile(raw)
	got := waitWorkspaceResult(t, sender, "terminal")
	content, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if !got.OK || err != nil || string(content) != "terminal" {
		t.Fatalf("terminal route %+v err=%v", got, err)
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
