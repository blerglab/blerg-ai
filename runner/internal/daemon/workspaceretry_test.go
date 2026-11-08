package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHostRecoveryRetrySequence(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "changed"}[terminal], func(t *testing.T) {
			w, s := finalWorkspaceFixture(t)
			w.state.Host = true
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			s.CommitFinal = acknowledgeWorkspaceFinal
			s.Authority = &testWorkspaceAuthority{}
			s.Supervisor = &testWorkspaceSupervisor{}
			calls := 0
			s.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) {
				calls++
				if terminal {
					return protocol.WorkspaceManifest{}, nil
				}
				return protocol.WorkspaceManifest{}, errors.New("disconnected")
			}
			// Follow the live sequence: an unknown in-memory agent receives
			// repeated stops while its durable workspace is still advertised.
			manager := &Manager{client: newRecordingSender(16), explorer: WorkspaceExplorerRouter{}, config: ManagerConfig{Workspaces: s, ReposRootSetting: NewReposRootSetting(t.TempDir(), "")}}
			manager.killSession("unrelated-session")
			if calls != 0 {
				t.Fatalf("unrelated stop swept this journal: calls=%d", calls)
			}
			for range 20 {
				manager.killSession(w.state.Session)
			}
			if calls != 1 {
				t.Fatalf("manifest calls=%d, want 1", calls)
			}
			if !terminal {
				for attempt := 0; attempt < 10; attempt++ {
					retry := s.recoveryRetries[w.state.Session]
					if retry.delay < time.Second || retry.delay > time.Minute {
						t.Fatalf("unbounded retry: %s", retry.delay)
					}
					retry.next = time.Time{}
					s.recoveryRetries[w.state.Session] = retry
					_ = s.RecoverAll(t.Context(), nil)
				}
				if calls != 11 || s.recoveryRetries[w.state.Session].delay != time.Minute {
					t.Fatalf("retry expiry/cap: calls=%d retry=%+v", calls, s.recoveryRetries[w.state.Session])
				}
			} else {
				saved, err := readPrivateWorkspace(w.record)
				if err != nil || !saved.AuthorityRefused || workspaceFinalComplete(saved) {
					t.Fatalf("terminal marker fabricated completion: %v", err)
				}
				s.recoveryRetries = nil // a fresh daemon still reads the terminal marker
				_ = s.RecoverAll(t.Context(), nil)
				if calls != 1 {
					t.Fatal("restart retried refusal")
				}
			}
			if got := len(manager.ActiveSessionIDs()); got != map[bool]int{false: 1, true: 0}[terminal] {
				t.Fatalf("pending=%d", got)
			}
		})
	}
}

// A terminal Current reply must not trigger another recovery RPC.
type refusedCleanupAuthority struct {
	preparationCleanupAuthority
	recoverCalls int
}

func (a *refusedCleanupAuthority) CleanupCurrent(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) error {
	return &WorkspaceRefusedError{Reason: "the session was archived or discarded"}
}
func (a *refusedCleanupAuthority) CleanupRecover(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	a.recoverCalls++
	return protocol.SupervisorLaunch{}, errors.New("unexpected retry")
}
func TestCleanupCurrentRefusalDoesNotRecover(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	a := &refusedCleanupAuthority{}
	s.Authority = a
	err := s.currentWorkspace(cleanupContext(t.Context()), w)
	var refused *WorkspaceRefusedError
	if !errors.As(err, &refused) || a.recoverCalls != 0 {
		t.Fatalf("terminal reply retried: %v calls=%d", err, a.recoverCalls)
	}
}
