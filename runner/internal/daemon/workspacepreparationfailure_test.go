package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspacePreparationFailureDoesNotClaimLiveness(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	// The fixture creates a separate parent; isolate reconnect recovery to this launch.
	parentRecord, err := workspaceRecord("p2-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parentRecord); err != nil {
		t.Fatal(err)
	}
	service.Storage = &prepareOrderStorage{testWorkspaceStorage: service.Storage.(*testWorkspaceStorage), t: t}
	sender := newRecordingSender(8)
	manager := newManagerWithSender(sender, ManagerConfig{Workspaces: service})
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	manager.handleSpawnSession(raw)
	messages := strings.Join(sender.messages(), "\n")
	if !strings.Contains(messages, `"status":"error"`) || !strings.Contains(messages, "workspace quota allocation: interrupt allocation") || strings.Contains(messages, "session_started") {
		t.Fatalf("missing failed launch cause or premature start: %s", messages)
	}
	if !strings.Contains(logs.String(), `"stage":"workspace quota allocation"`) || !strings.Contains(logs.String(), `"cause":`) {
		t.Errorf("missing launch failure diagnostic: %s", logs.String())
	}
	if slices.Contains(manager.ActiveSessionIDs(), msg.SessionID) {
		t.Error("failed preparation claims heartbeat liveness, reviving error to running")
	}
	// A fresh service reads only durable state, before cleanup has refused.
	if slices.Contains((&LocalWorkspaceService{}).PendingIDs(), msg.SessionID) {
		t.Error("restart revived failed preparation before cleanup")
	}
	calls := 0
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) {
		calls++
		return protocol.WorkspaceManifest{}, &WorkspaceRefusedError{Reason: "session ended"}
	}
	manager.killSession(msg.SessionID)
	manager.killSession(msg.SessionID)
	if err := service.RecoverAll(t.Context(), nil); err != nil {
		t.Fatal("refused cleanup retried on reconnect", err)
	}
	if calls != 1 {
		t.Errorf("cleanup authority called %d times, want once", calls)
	}
	fresh := &LocalWorkspaceService{}
	if slices.Contains(fresh.PendingIDs(), msg.SessionID) {
		t.Error("restart revived failed preparation")
	}
	record, _ := workspaceRecord(msg.SessionID)
	if _, err := os.Stat(record); err != nil {
		t.Fatal("cleanup refusal deleted recovery evidence", err)
	}
}

func TestWorkspacePreparationFailureLogKeepsPrivateCausePrivate(t *testing.T) {
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	logDaemonFailure("workspace quota allocation", &os.PathError{Op: "CANARY_SECRET", Path: "/CANARY_SECRET", Err: os.ErrPermission})
	if strings.Contains(logs.String(), "CANARY_SECRET") || !strings.Contains(logs.String(), `"cause":"permission"`) {
		t.Fatal(logs.String())
	}
}
