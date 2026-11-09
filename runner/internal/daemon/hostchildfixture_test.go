package daemon

import (
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
)

func hostChildAllocationFixture(t *testing.T, parent workspacePrivateManifest, branch ...string) *LocalWorkspace {
	t.Helper()
	record, _ := workspaceRecord("child")
	w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "child", Launch: parent.Launch}}
	if len(branch) > 0 {
		w.state.WorkBranch = branch[0]
	}
	pins, err := w.allocateHostChild(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	pins.Close()
	for i, a := range w.state.HostChildren {
		w.state.Members = append(w.state.Members, workspacePrivateMember{Member: w.state.Launch.Members[i], Clone: a.Path, CloneIdentity: a.Root, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: w.state.Launch.LaunchGeneration, MemberID: a.MemberID}, Pending: true}})
	}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	return w
}
