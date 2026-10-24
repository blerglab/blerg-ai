package daemon

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"testing"
)

type prepareOrderStorage struct {
	*testWorkspaceStorage
	t         *testing.T
	allocated bool
}

func (s *prepareOrderStorage) Allocate(_ context.Context, id string, _ protocol.WorkspaceBudget) (string, error) {
	s.allocated = true
	record, _ := workspaceRecord(id)
	state, err := readPrivateWorkspace(record)
	if err != nil || !state.RuntimePreparing || state.RuntimeToken == "" || state.SupervisorLaunch.Validate() != nil {
		s.t.Fatal("AUTHORITY_BEFORE_STORAGE: quota allocation preceded durable server authority", err)
	}
	return "", errors.New("interrupt allocation")
}
func TestP6AuthorityPrecedesQuotaAllocation(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	original, ok := service.Storage.(*testWorkspaceStorage)
	if !ok {
		t.Fatal("fixture storage")
	}
	storage := &prepareOrderStorage{testWorkspaceStorage: original, t: t}
	service.Storage = storage
	if _, err := service.Prepare(t.Context(), msg); err == nil || !storage.allocated {
		t.Fatal("allocation fault did not exercise boundary", err)
	}
}

type preparationCleanupAuthority struct{ testWorkspaceAuthority }

func (a *preparationCleanupAuthority) CleanupLaunch(ctx context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	return a.Launch(ctx, id, m, token)
}
func (a *preparationCleanupAuthority) CleanupBind(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	return a.Bind(ctx, l, b)
}
func (a *preparationCleanupAuthority) CleanupCurrent(_ context.Context, _ protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	if a.bound == nil || *a.bound != b {
		return errors.New("cleanup tuple mismatch")
	}
	return nil
}
func (a *preparationCleanupAuthority) CleanupRecover(_ context.Context, l protocol.SupervisorLaunch, _ protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	return l, nil
}
func (a *preparationCleanupAuthority) CleanupResults(_ context.Context, _ protocol.SupervisorLaunch, _ protocol.WorkspaceRuntimeBinding, _ []protocol.WorkspaceMemberResult) error {
	return nil
}
func TestP6PreparedStopRetainsAllocationAndCompletes(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	authority := &preparationCleanupAuthority{}
	service.Authority = authority
	w, err := service.Prepare(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	// No process was ever admitted; model a server abort/absence acknowledgement.
	w.mu.Lock()
	w.state.RuntimePreparing = false
	w.state.RuntimeToken = ""
	w.state.SupervisorLaunch = protocol.SupervisorLaunch{}
	err = w.save()
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	authority.stale = true // ordinary authority must remain revoked
	if err = service.cleanupPrepared(t.Context(), w, nil); err != nil {
		t.Fatal("PREPARED_STOP:", err)
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil || !saved.TerminalAcknowledged {
		t.Fatal("PREPARED_STOP: no durable receipt", err)
	}
	if saved.StorageReleased {
		t.Fatal("erased unresolved preparation")
	}
	if len(saved.FinalResults) != len(msg.Workspace.Members) {
		t.Fatal("missing member recovery outcomes")
	}
	for _, result := range saved.FinalResults {
		if !result.Pending || result.RecoveryLocation == "" {
			t.Fatal("missing retained member recovery")
		}
	}
}

func TestP6PreparedStopCrashBeforeFinalPayload(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	authority := &preparationCleanupAuthority{}
	service.Authority = authority
	w, err := service.Prepare(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.state.RuntimePreparing = false
	w.state.RuntimeToken = ""
	err = w.save()
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = service.cleanupPrepared(t.Context(), w, nil); err != nil {
		t.Fatal(err)
	}
	// Recreate the earlier durable boundary, before a final payload ever existed.
	w.state.FinalDelivery = nil
	w.state.FinalResults = nil
	w.state.ResultsAcknowledged = false
	w.state.TerminalAcknowledged = false
	w.state.TerminalReceipt = ""
	if err = w.save(); err != nil {
		t.Fatal(err)
	}
	parentRecord, _ := workspaceRecord("p2-session")
	if err = os.Remove(parentRecord); err != nil {
		t.Fatal(err)
	}
	fresh := &LocalWorkspaceService{Authority: authority, Supervisor: &testWorkspaceSupervisor{}, Storage: service.Storage, Results: service.Results, CommitFinal: acknowledgeWorkspaceFinal, TrustedLaunch: func(context.Context, string) (protocol.WorkspaceManifest, error) { return *msg.Workspace, nil }}
	if err = fresh.RecoverAll(t.Context(), nil); err != nil {
		t.Fatal("PREPARED_CRASH:", err)
	}
	state, err := readPrivateWorkspace(w.record)
	if err != nil || !state.TerminalAcknowledged {
		t.Fatal("PREPARED_CRASH: completion lost", err)
	}
}
