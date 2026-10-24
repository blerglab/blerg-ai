//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestP6DefaultVMBackendAgentDispatch(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1")
	}
	t.Setenv("BLERG_SANDBOX_CHILD_TEST", "1")
	_, msg, sources := workspaceServiceFixture(t)
	engine := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
set -e
cd /workspace/members/one
printf 'committed work\n' > committed.txt
git add committed.txt
git -c user.name=Example -c user.email=developer@example.com commit -qm work
printf 'leftover work\n' > /workspace/members/two/leftover.txt
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"workspace-engine"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Finished."}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"workspace-engine","result":"Finished."}
JSON
`
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	host, sender := realSandboxHost(t, map[string]string{"claude": engine})
	root := filepath.Join(t.TempDir(), "dispatch")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	authority := &testWorkspaceAuthority{}
	rig := newCoordRig(t)
	var deliveredResults []protocol.WorkspaceMemberResult
	host.cfg.Workspaces = &LocalWorkspaceService{CommitFinal: acknowledgeWorkspaceFinal, Results: func(_ context.Context, _ string, b protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
		if b.Generation != 7 {
			return errors.New("missing result generation")
		}
		deliveredResults = results
		return nil
	}, Authority: authority, Supervisor: newWorkspaceSupervisors(rig.service), Storage: nil, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return sources, "", nil
	}}
	host.cfg.Workspaces.CancelStatus = host.explorer.Suspend
	host.cfg.Workspaces.ResumeStatus = func(_ context.Context, id string) error { return host.explorer.Resume(id) }
	host.cfg.Workspaces.CloseStatus = host.explorer.Release
	t.Cleanup(func() { cleanupVMStorageTest(t, defaultWorkspaceStorage().(*DockerWorkspaceStorage), msg.SessionID) })
	msg.InitialPrompt = "work"
	host.Spawn(msg)
	if es := sender.sessionErrors(); len(es) > 0 {
		t.Fatalf("DEFAULT_VM_DISPATCH: %v", es)
	}
	sender.waitForKind(t, "turn_done")
	if authority.bound == nil || authority.bound.Root.Inode == 0 {
		t.Fatal("agent started without runtime authority")
	}
	if got := childRepoGit(t, sources[0].Path, "show", "wip/service-session:committed.txt"); got != "committed work" {
		t.Fatal("turn-end member sync missing")
	}
	local := host.get(msg.SessionID).localWorkspace
	if local.state.RemoteMountSource == "" {
		t.Fatal("default VM backend was not exercised")
	}
	if _, err := os.Stat(local.state.Root); !os.IsNotExist(err) {
		t.Fatal("VM dispatch relied on a host mirror")
	}
	rig.configure(t, msg.SessionID, nil)
	rig.open(t, "default-vm-shell", 0)
	rig.input(t, 1, "printf '\\126\\115\\122\\105\\101\\104\\131'\n")
	rig.wait(t, "VMREADY")
	local.mu.Lock()
	if local.state.Unavailable {
		t.Fatal("turn sync did not restore workspace availability")
	}
	local.mu.Unlock() // wait for the completed turn's thaw/save
	request := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "production-status", SessionID: msg.SessionID, WorkspaceRuntimeRef: authority.bound.WorkspaceRuntimeRef, Operation: "status", Path: "members/one", MemberID: "one"}
	raw, _ := json.Marshal(request)
	host.HandleWorkspaceExplorer(t.Context(), raw)
	if result := waitExplorerResult(t, sender, request.RequestID); !result.OK || result.Status == nil || result.Status.Branch != "wip/service-session" {
		t.Fatalf("production shared-clone status: %+v", result)
	}
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "production-file", SessionID: msg.SessionID, WorkspaceRuntimeRef: authority.bound.WorkspaceRuntimeRef, Path: "/workspace/members/one/committed.txt"})
	if result := waitWorkspaceResult(t, sender, "production-file"); !result.OK || result.Path != "members/one/committed.txt" || result.RequestedPath != "/workspace/members/one/committed.txt" {
		t.Fatalf("production shared-clone file: %+v", result)
	}
	host.Kill(msg.SessionID)
	var ended *protocol.SessionEnded
	sender.mu.Lock()
	for _, message := range sender.msgs {
		if value, ok := message.(protocol.SessionEnded); ok && value.SessionID == msg.SessionID {
			snapshot := value
			ended = &snapshot
		}
	}
	sender.mu.Unlock()
	if rig.service.Lifecycle(msg.SessionID) != nil {
		t.Fatal("VM stop retained shell registration")
	}
	if !local.state.TerminalAcknowledged {
		t.Fatal("VM stop did not retain a durable receipt")
	}
	if ended == nil || len(deliveredResults) != 2 || ended.ExitCode != 0 {
		t.Fatalf("member end results missing: %+v", ended)
	}
	if got := childRepoGit(t, sources[1].Path, "show", "wip/service-session:leftover.txt"); got != "leftover work" {
		t.Fatal("stop settlement lost member work")
	}
}
