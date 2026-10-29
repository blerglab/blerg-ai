package daemon

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// #267: a burst of legacy attachments past the desktop's two slots used to get no
// reply at all, so every caller waited out its whole ~2 s deadline. Each refused
// request must now be answered "busy" at once.
func TestLegacyAttachBurstIsAnsweredBusyAtOnce(t *testing.T) {
	waitUntil(t, "legacy attachment slots free", func() bool { return len(legacyWorkspaceSlots) == 0 })
	for range cap(legacyWorkspaceSlots) {
		legacyWorkspaceSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(legacyWorkspaceSlots) {
			<-legacyWorkspaceSlots
		}
	})
	sender := newRecordingSender(16)
	m := &Manager{client: sender}
	start := time.Now()
	for i := range 8 {
		raw, _ := json.Marshal(shellruntime.RPC{Type: "attach_session_workspace", Op: "attach_workspace", Version: 1, SessionID: "legacy", RequestID: fmt.Sprintf("burst-%d", i)})
		m.handleAttachSessionWorkspace(raw)
	}
	seen := map[string]bool{}
	for len(seen) < 8 {
		select {
		case msg := <-sender.ch:
			reply, ok := msg.(shellruntime.RPC)
			if !ok || reply.Type != "owner_shell_result" || reply.OK || reply.ErrorCode != "busy" || reply.SessionID != "legacy" || reply.Op != "attach_workspace" {
				t.Fatalf("refused attachment reply = %+v", msg)
			}
			seen[reply.RequestID] = true
		case <-time.After(time.Second):
			t.Fatalf("only %d of 8 refused attachments were answered", len(seen))
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("busy replies took %s", elapsed)
	}
}

func TestBusyRefusalsAreLoggedAtMostOncePerInterval(t *testing.T) {
	now := time.Unix(1000, 0)
	var lines []string
	report := &busyReport{interval: 10 * time.Second, now: func() time.Time { return now }, logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	for range 50 {
		report.note("legacy_attach")
	}
	report.note("workspace_file")
	if len(lines) != 1 || lines[0] != "workspace busy: refused legacy_attach=1" {
		t.Fatalf("first refusal log = %q", lines)
	}
	now = now.Add(10 * time.Second)
	report.note("legacy_attach")
	if len(lines) != 2 || lines[1] != "workspace busy: refused legacy_attach=50 workspace_file=1" {
		t.Fatalf("interval log = %q", lines)
	}
}

func TestRefusedWorkspaceFileReadIsAnsweredBusy(t *testing.T) {
	waitUntil(t, "workspace read slots free", func() bool { return len(workspaceFileReads) == 0 })
	for range cap(workspaceFileReads) {
		workspaceFileReads <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(workspaceFileReads) {
			<-workspaceFileReads
		}
	})
	sender := newRecordingSender(4)
	queueWorkspaceFileRead(sender, protocol.ReadWorkspaceFile{RequestID: "refused", SessionID: "workspace", Path: "README.md"}, workspaceFileBinding{})
	select {
	case msg := <-sender.ch:
		got, ok := msg.(protocol.WorkspaceFileResult)
		if !ok || got.RequestID != "refused" || got.OK || got.ErrorCode != "busy" || got.ContentB64 != "" {
			t.Fatalf("refused read reply = %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("refused read was not answered")
	}
}
