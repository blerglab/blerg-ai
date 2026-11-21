package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceStoppedCleanupDoesNotClaimLiveness(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	if err := w.RequestStop(protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}); err != nil {
		t.Fatal(err)
	}
	s.Authority = &testWorkspaceAuthority{}
	s.Supervisor = &testWorkspaceSupervisor{}
	s.CommitFinal = acknowledgeWorkspaceFinal
	calls := 0
	s.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) {
		calls++
		return protocol.WorkspaceManifest{}, errors.Join(&WorkspaceUnavailableError{Cause: protocol.WorkspaceControlStorage}, errors.New("CANARY_SECRET"))
	}
	var logs bytes.Buffer
	previous := failurelog.Default
	t.Cleanup(func() { failurelog.Default = previous })
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{Workspaces: s})
	for i := 0; i < 3; i++ {
		// Reset rate limiting so it cannot hide a repeated transition.
		failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
		if err := s.RecoverSession(t.Context(), w.state.Session, nil); err == nil {
			t.Fatal("expected retained cleanup")
		}
		if slices.Contains(manager.ActiveSessionIDs(), w.state.Session) {
			t.Error("stopped cleanup claims heartbeat liveness, triggering another kill")
		}
		if slices.Contains((&LocalWorkspaceService{}).PendingIDs(), w.state.Session) {
			t.Error("restart revived stopped cleanup")
		}
		retry := s.recoveryRetries[w.state.Session]
		retry.next = time.Time{}
		s.recoveryRetries[w.state.Session] = retry
	}
	if calls != 3 {
		t.Fatalf("cleanup recovery no longer retryable: %d", calls)
	}
	if _, err := os.Stat(w.record); err != nil {
		t.Fatal("retained journal lost", err)
	}
	if strings.Count(logs.String(), `"stage":"workspace cleanup retained"`) != 1 || !strings.Contains(logs.String(), `"cause":"storage_unavailable"`) || strings.Contains(logs.String(), "CANARY_SECRET") {
		t.Errorf("expected one private transition log: %s", logs.String())
	}
}
