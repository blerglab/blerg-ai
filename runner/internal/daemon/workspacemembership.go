package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A membership change is one journaled transaction per session. Lock order is
// admission -> LocalWorkspace.mu -> coordinator, as for every lifecycle call.
//
//	fencing  writers may be frozen; nothing was prepared
//	prepared the receipt is durable; the server may or may not have committed
//	committed state holds the revision; live readers and shells may still be old
//
// Before commit a failure restores the old revision. After commit recovery only
// moves forward. Writers stay fenced on any unknown outcome; a repeated apply,
// a reconnect or a restart resumes from this journal.
const (
	membershipFencing   = "fencing"
	membershipPrepared  = "prepared"
	membershipCommitted = "committed"
)

type workspaceMembershipJournal struct {
	Operation string                     `json:"operation"`
	Kind      string                     `json:"kind"`
	MemberID  string                     `json:"member_id"`
	Phase     string                     `json:"phase"`
	Receipt   string                     `json:"receipt,omitempty"`
	Proposed  protocol.WorkspaceManifest `json:"proposed"`
	Source    *WorkspaceSource           `json:"source,omitempty"`
	Prepared  *workspacePrivateMember    `json:"prepared,omitempty"` // a sandbox allocation
	// DiscardLocal is the person's confirmation of ignored files a removal may delete.
	DiscardLocal string `json:"discard_local,omitempty"`
	// Reopening a committed change: a repeated apply resumes after what already
	// happened, since a running container can't be unpaused twice.
	Thawed     bool `json:"thawed,omitempty"`
	ThawFailed bool `json:"thaw_failed,omitempty"`
}

type workspaceMembershipAuthority interface {
	MembershipStatus(context.Context, string, string) (string, protocol.WorkspaceManifest, error)
	MembershipCommit(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding, string, string) (protocol.WorkspaceRuntimeBinding, error)
	MembershipInstalled(context.Context, string, string) error
	MembershipReport(context.Context, string, protocol.WorkspaceMembershipReport) error
}

// membershipDiscovery is P1's seam: it re-proves a source and retargets children.
type membershipDiscovery interface {
	PinAddition(context.Context, string, protocol.SelectionRef, protocol.WorkspaceMember) (*discovery.SourcePin, error)
	RebindMembership(context.Context, protocol.WorkspaceManifest, []string) error
}

type membershipRefusal struct {
	code  string
	err   error
	local *protocol.WorkspaceLocalFiles
}

func (e *membershipRefusal) Error() string { return "membership refused: " + e.code }
func (e *membershipRefusal) Unwrap() error { return e.err }

func refuseMembership(code string, err error) error { return &membershipRefusal{code: code, err: err} }

// ApplyMembership runs at the server's request and again after any lost reply.
func (s *LocalWorkspaceService) ApplyMembership(ctx context.Context, msg protocol.WorkspaceMembershipApply) {
	a, ok := s.Authority.(workspaceMembershipAuthority)
	if !ok {
		return
	}
	wait := func(code string) {
		_ = a.MembershipReport(ctx, msg.SessionID, protocol.WorkspaceMembershipReport{OperationID: msg.OperationID, Waiting: code})
	}
	admission := workspaceAdmissionLock(msg.SessionID)
	if !admission.TryLock() {
		wait(protocol.MembershipWaitBusy)
		return
	}
	defer admission.Unlock()
	s.mu.Lock()
	w := s.sessions[msg.SessionID]
	s.mu.Unlock()
	if w == nil {
		wait(protocol.MembershipWaitRuntime)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := s.applyMembershipLocked(ctx, a, w, msg)
	var refused *membershipRefusal
	var waiting *membershipWaiting
	switch {
	case err == nil:
	case errors.As(err, &refused):
		_ = a.MembershipReport(ctx, msg.SessionID, protocol.WorkspaceMembershipReport{OperationID: msg.OperationID, Refusal: refused.code, Local: refused.local})
	case errors.As(err, &waiting):
		wait(waiting.code)
	default:
		log.Printf("workspace membership %s: %v", msg.SessionID, err)
		wait(protocol.MembershipWaitFence)
	}
}

type membershipWaiting struct{ code string }

func (e *membershipWaiting) Error() string { return "membership waits: " + e.code }

func (s *LocalWorkspaceService) applyMembershipLocked(ctx context.Context, a workspaceMembershipAuthority, w *LocalWorkspace, msg protocol.WorkspaceMembershipApply) error {
	j := w.state.Membership
	// A failed thaw is what this apply is here to finish.
	unavailable := w.state.Unavailable && (j == nil || !j.ThawFailed)
	if w.state.Binding == nil || w.state.RuntimePreparing || w.state.Preparing || w.state.StopRequested || w.state.Stopped || unavailable || w.state.FinalDelivery != nil {
		return &membershipWaiting{protocol.MembershipWaitRuntime}
	}
	if w.state.Launch.Digest == msg.Proposed.Digest && j == nil {
		return a.MembershipInstalled(ctx, msg.SessionID, msg.OperationID) // a lost acknowledgment
	}
	if j != nil && j.Operation != msg.OperationID {
		return &membershipWaiting{protocol.MembershipWaitBusy}
	}
	if j == nil {
		if err := msg.Proposed.Validate(); err != nil || w.state.Launch.Digest != msg.OldDigest || msg.Proposed.LaunchGeneration != w.state.Launch.LaunchGeneration || msg.Proposed.Revision != w.state.Launch.Revision+1 {
			return refuseMembership(protocol.MembershipRefusePrepare, err)
		}
		if w.state.HostChild {
			return refuseMembership(protocol.MembershipRefusePrepare, nil)
		}
		if s.TurnIdle == nil || !s.TurnIdle(msg.SessionID) {
			return &membershipWaiting{protocol.MembershipWaitTurn}
		}
		if wait := s.membershipShellWait(w); wait != "" {
			return &membershipWaiting{wait}
		}
		j = &workspaceMembershipJournal{Operation: msg.OperationID, Kind: msg.Kind, MemberID: msg.MemberID, Phase: membershipFencing, Proposed: msg.Proposed, DiscardLocal: msg.DiscardLocal}
		w.state.Membership = j
		if err := w.save(); err != nil {
			w.state.Membership = nil
			return err
		}
		workspaceCrashBoundary("membership-fencing")
		if err := s.fenceMembership(ctx, w); err != nil {
			return errors.Join(err, s.abandonMembership(ctx, w))
		}
		workspaceCrashBoundary("membership-fenced")
		// Shells share membership admission, so this answer cannot go stale.
		if wait := s.membershipShellWait(w); wait != "" {
			return errors.Join(&membershipWaiting{wait}, s.abandonMembership(ctx, w))
		}
		if err := s.prepareMembership(ctx, w, msg); err != nil {
			return errors.Join(err, s.abandonMembership(ctx, w))
		}
		j.Phase, j.Receipt = membershipPrepared, membershipReceipt(*j)
		if err := w.save(); err != nil {
			return err
		}
		workspaceCrashBoundary("membership-prepared")
	}
	if j.Phase == membershipPrepared {
		next, err := a.MembershipCommit(ctx, w.state.SupervisorLaunch, *w.state.Binding, j.Operation, j.Receipt)
		var refused *WorkspaceRefusedError
		if errors.As(err, &refused) {
			return errors.Join(refuseMembership(protocol.MembershipRefusePrepare, err), s.abandonMembership(ctx, w))
		}
		if err != nil {
			// The outcome is unknown: storage is retained and writers stay fenced.
			return fmt.Errorf("membership commit outcome unknown: %w", err)
		}
		workspaceCrashBoundary("membership-after-commit")
		if err = w.installMembershipState(next); err != nil {
			return err
		}
		workspaceCrashBoundary("membership-state-saved")
	}
	return s.finishMembership(ctx, a, w)
}

func (s *LocalWorkspaceService) shellsOpen(session string) bool {
	if s.membershipShells != nil {
		return s.membershipShells.open(session)
	}
	registry, ok := s.Supervisor.(*workspaceSupervisors)
	if !ok || registry.service == nil {
		return true
	}
	open, known := registry.service.WorkspaceShellsOpen(session)
	return !known || open
}

// membershipShellSeam replaces the shell coordinator in package tests only.
type membershipShellSeam interface {
	open(string) bool
	rebind(protocol.WorkspaceRuntimeBinding) error
}

func (s *LocalWorkspaceService) fenceMembership(ctx context.Context, w *LocalWorkspace) error {
	if s.CancelStatus != nil {
		if err := s.CancelStatus(ctx, w.state.Session); err != nil {
			return err
		}
	}
	if w.host != nil {
		if err := s.Supervisor.Notify(ctx, w.state.SupervisorLaunch, "syncing"); err != nil {
			return err
		}
		// Privileged shells only sync at turn end; they cannot supply writer
		// freeze proof for a membership change. Admission is now fenced.
		if source, ok := s.Supervisor.(interface{ PrivilegedShellOpen(string) (bool, bool) }); ok {
			open, known := source.PrivilegedShellOpen(w.state.Session)
			if !known {
				if wait := s.membershipShellWait(w); wait != "" {
					return &membershipWaiting{wait}
				}
			} else if open {
				return &membershipWaiting{protocol.MembershipWaitShell}
			}
		}
		return w.host.writers.Freeze(ctx)
	}
	if err := w.validateShared(ctx); err != nil {
		return err
	}
	if err := w.runtime.QuiesceStatus(ctx); err != nil {
		return err
	}
	if err := w.runtime.Freeze(ctx); err != nil {
		// An unsuccessful pause can still have taken effect.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxDockerTimeout)
		defer cancel()
		if thawErr := w.runtime.Thaw(cleanup); thawErr != nil {
			w.state.Unavailable = true
			return errors.Join(err, thawErr)
		}
		return err
	}
	return nil
}

// thawMembership reopens the session. A committed journal records each step, so a
// repeated apply resumes after whatever already happened.
func (s *LocalWorkspaceService) thawMembership(ctx context.Context, w *LocalWorkspace) error {
	j := w.state.Membership
	if w.host == nil {
		if j == nil || !j.Thawed {
			if err := w.runtime.Thaw(ctx); err != nil {
				w.state.Unavailable = true
				if j != nil {
					j.ThawFailed = true
				}
				return errors.Join(fmt.Errorf("workspace thaw failed; retained for recovery: %w", err), w.save())
			}
			if err := s.markThawed(w, j); err != nil {
				return err
			}
		}
		// A paused container runs no exec, so the reader helper is rebuilt here,
		// with readers still suspended; a failure keeps them suspended.
		if w.reader != nil {
			if err := w.reader.installMembership(ctx, w.explorerStart()); err != nil {
				return err
			}
		}
		if s.ResumeStatus != nil {
			return s.ResumeStatus(ctx, w.state.Session)
		}
		return nil
	}
	if j == nil || !j.Thawed {
		if err := w.host.writers.Thaw(ctx); err != nil {
			return fmt.Errorf("host workspace writer thaw: %w", err)
		}
		if err := s.markThawed(w, j); err != nil {
			return err
		}
	}
	if err := s.Supervisor.Notify(ctx, w.state.SupervisorLaunch, "running"); err != nil {
		return err
	}
	if s.ResumeStatus != nil {
		return s.ResumeStatus(ctx, w.state.Session)
	}
	return nil
}

func (s *LocalWorkspaceService) markThawed(w *LocalWorkspace, j *workspaceMembershipJournal) error {
	if j == nil {
		return nil
	}
	if j.ThawFailed {
		j.ThawFailed, w.state.Unavailable = false, false
	}
	j.Thawed = true
	return w.save()
}

// prepareMembership proves the change against the fenced runtime. Host additions
// allocate nothing; the pin is the receipt's identity evidence.
func (s *LocalWorkspaceService) prepareMembership(ctx context.Context, w *LocalWorkspace, msg protocol.WorkspaceMembershipApply) error {
	j := w.state.Membership
	if err := w.retireReserved(ctx); err != nil {
		log.Printf("workspace membership %s: reserved folder cleanup: %v", w.state.Session, err)
	}
	if msg.Kind == "remove" {
		return w.prepareRemoval(ctx, msg)
	}
	if msg.Kind != "add" {
		return refuseMembership(protocol.MembershipRefusePrepare, errors.New("unsupported membership change"))
	}
	var member *protocol.WorkspaceMember
	for i := range msg.Proposed.Members {
		if msg.Proposed.Members[i].ID == msg.MemberID {
			member = &msg.Proposed.Members[i]
		}
	}
	if member == nil || len(msg.Proposed.Members) != len(w.state.Launch.Members)+1 || s.Discovery == nil || s.Discovery() == nil {
		return refuseMembership(protocol.MembershipRefusePrepare, nil)
	}
	for i, old := range w.state.Launch.Members {
		if msg.Proposed.Members[i] != old {
			return refuseMembership(protocol.MembershipRefusePrepare, errors.New("existing members changed"))
		}
	}
	pin, err := s.Discovery().PinAddition(ctx, w.state.Launch.LaunchGeneration, msg.Source, *member)
	if err != nil {
		return refuseMembership(protocol.MembershipRefuseSource, err)
	}
	defer pin.Close()
	root, err := pin.Directory.Stat()
	if err != nil {
		return refuseMembership(protocol.MembershipRefuseSource, err)
	}
	common, err := pin.CommonDirectory.Stat()
	if err != nil {
		return refuseMembership(protocol.MembershipRefuseSource, err)
	}
	source := WorkspaceSource{Member: *member, Path: pin.CanonicalPath, CommonPath: pin.CommonDirectoryPath, RootIdentity: workspaceRootIdentity(root), CommonIdentity: workspaceRootIdentity(common)}
	if w.host != nil {
		if err := w.host.checkAddition(source); err != nil {
			return err
		}
	}
	j.Source = &source
	if w.host == nil {
		return w.cloneMember(ctx, source)
	}
	if protocol.ReservedMemberPath(member.Path) {
		return w.placeReservedMember(source)
	}
	return nil
}

// The freeze has a shell deadline, so the copy gets a share of the sync budget
// and is refused rather than allowed to outlive it.
const membershipCloneSeconds = 55

// cloneMember copies an addition into the session's enforced allocation with
// writers frozen. A failed copy removes only what this call created.
func (w *LocalWorkspace) cloneMember(ctx context.Context, source WorkspaceSource) error {
	j := w.state.Membership
	clone := filepath.Join(w.state.Root, filepath.FromSlash(source.Member.Path))
	if err := w.beginWorker(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, membershipCloneSeconds*time.Second)
	defer cancel()
	response, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "prepare", State: w.state, Source: source, Clone: clone})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), errWorkspaceMemberCollision.Error()):
			return refuseMembership(protocol.MembershipRefuseCollision, err)
		case strings.Contains(err.Error(), errWorkspaceMemberBudget.Error()):
			return refuseMembership(protocol.MembershipRefuseQuota, err)
		}
		return refuseMembership(protocol.MembershipRefusePrepare, err)
	}
	workspaceCrashBoundary("membership-cloned")
	m := response.Member
	m.WorkerPins = workerPins(m)
	// Keep the worker's allocation receipt even if host source revalidation
	// fails, so abandonment can remove precisely this installed clone.
	j.Prepared = &m
	if m.SourceIdentity, err = workspaceDirectoryIdentity(m.Source.Repo); err != nil {
		return err
	}
	if m.SourceObjectsIdentity, err = workspaceDirectoryIdentity(m.Source.Objects); err != nil {
		return err
	}
	if m.SourceCommonIdentity, err = workspaceDirectoryIdentity(m.SourceCommon); err != nil {
		return err
	}
	if m.SourceIdentity != source.RootIdentity || m.SourceCommonIdentity != source.CommonIdentity {
		return refuseMembership(protocol.MembershipRefuseSource, errors.New("source changed during the copy"))
	}
	m.Result = protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: source.Member.ID, LaunchGeneration: w.state.Launch.LaunchGeneration}, Base: m.Source.Base, Head: m.Source.Base, Branch: w.branch(), Pending: true, RecoveryLocation: clone}
	w.state.WorkerRoot = response.State.WorkerRoot
	j.Prepared = &m
	return nil
}

// discardPrepared removes a prepared allocation by its recorded identity. A
// failure retains it and the journal; it is never retried by pathname alone.
func (w *LocalWorkspace) discardPrepared(ctx context.Context) error {
	j := w.state.Membership
	if j == nil || j.Prepared == nil {
		return nil
	}
	if err := w.save(); err != nil {
		return err
	}
	if err := w.beginWorker(ctx); err != nil {
		return err
	}
	_, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "membership-discard", State: w.state, Record: w.record, Clone: j.Prepared.Clone})
	return err
}

// prepareRemoval is the settlement proof of ruling 1: nothing is deleted, moved
// or committed here. Any unfinished or undelivered work refuses, and the member
// stays. Its checkout, on the host or in the allocation, and its branches remain.
func (w *LocalWorkspace) prepareRemoval(ctx context.Context, msg protocol.WorkspaceMembershipApply) error {
	if len(msg.Proposed.Members) != len(w.state.Launch.Members)-1 {
		return refuseMembership(protocol.MembershipRefusePrepare, nil)
	}
	remaining := msg.Proposed.Members
	found := false
	for _, old := range w.state.Launch.Members {
		if old.ID == msg.MemberID {
			found = true
			continue
		}
		if len(remaining) == 0 || remaining[0] != old {
			return refuseMembership(protocol.MembershipRefusePrepare, errors.New("surviving members changed"))
		}
		remaining = remaining[1:]
	}
	if !found {
		return refuseMembership(protocol.MembershipRefusePrepare, errors.New("unknown member"))
	}
	if w.host == nil {
		return w.checkSandboxRemoval(ctx, msg.MemberID)
	}
	for _, source := range w.state.Sources {
		if source.Member.ID != msg.MemberID {
			continue
		}
		rel, err := filepath.Rel(source.Path, w.state.HostWorkDir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return refuseMembership(protocol.MembershipRefuseAnchor, nil)
		}
		status, err := hostChildGit(ctx, source.Path, source.RootIdentity, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=all")
		if err != nil {
			return refuseMembership(protocol.MembershipRefuseWork, err)
		}
		if status != "" {
			return refuseMembership(protocol.MembershipRefuseWork, nil)
		}
		return nil
	}
	return refuseMembership(protocol.MembershipRefusePrepare, errors.New("member has no source"))
}

// checkSandboxRemoval reads the clone from the trusted worker with writers frozen.
// The clone stays in the allocation; only its membership ends.
func (w *LocalWorkspace) checkSandboxRemoval(ctx context.Context, id string) error {
	if len(w.state.Launch.Members) > 0 && w.state.Launch.Members[0].ID == id {
		return refuseMembership(protocol.MembershipRefuseAnchor, nil)
	}
	for _, m := range w.state.Members {
		if m.Member.ID != id {
			continue
		}
		if m.Result.Pending || m.Result.SyncError != "" || m.Result.PushError != "" {
			return refuseMembership(protocol.MembershipRefuseWork, nil)
		}
		if err := w.save(); err != nil {
			return err
		}
		if err := w.beginWorker(ctx); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, membershipCloneSeconds*time.Second)
		response, err := w.runWorker(bounded, workspaceWorkerRequest{Action: "membership-check", State: w.state, Record: w.record, Clone: m.Clone})
		cancel()
		if err != nil {
			return refuseMembership(protocol.MembershipRefuseWork, err)
		}
		if local := response.Local; local.Count > 0 && local.Digest != w.state.Membership.DiscardLocal {
			return &membershipRefusal{code: protocol.MembershipRefuseLocal, local: &local}
		}
		return nil
	}
	return refuseMembership(protocol.MembershipRefusePrepare, errors.New("member has no clone"))
}

// checkAddition refuses an aliased or replaced source while writers are fenced.
// A physical folder keeps each repository at its real path; a folder adopted as a
// repository must be the very directory this runtime is pinned to.
func (h *hostWorkspace) checkAddition(source WorkspaceSource) error {
	state := h.w.state
	for _, existing := range state.Sources {
		if existing.CommonIdentity == source.CommonIdentity || existing.RootIdentity == source.RootIdentity {
			return refuseMembership(protocol.MembershipRefuseSource, errors.New("source aliases a member"))
		}
	}
	if !hostStateVirtual(state) && !protocol.ReservedMemberPath(source.Member.Path) {
		if source.Member.Path == "." && source.RootIdentity != state.RootIdentity {
			return refuseMembership(protocol.MembershipRefuseSource, errors.New("adopted folder replaced"))
		}
		if filepath.Clean(source.Path) != filepath.Join(state.Root, filepath.FromSlash(source.Member.Path)) {
			return refuseMembership(protocol.MembershipRefuseSource, errors.New("source outside the workspace folder"))
		}
	}
	return nil
}

func hostStateVirtual(state workspacePrivateManifest) bool {
	for _, source := range state.Sources {
		if filepath.Clean(source.Path) != filepath.Join(state.Root, filepath.FromSlash(source.Member.Path)) {
			return true
		}
	}
	return false
}

func membershipReceipt(j workspaceMembershipJournal) string {
	text := fmt.Sprintf("membership-v1\x00%s\x00%s\x00%s\x00%s", j.Operation, j.Kind, j.MemberID, j.Proposed.Digest)
	if j.Source != nil {
		text += fmt.Sprintf("\x00%+v\x00%+v", j.Source.RootIdentity, j.Source.CommonIdentity)
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// installMembershipState commits the revision to the signed journal in one write,
// so a crash leaves either the old state with a prepared receipt or the new state.
func (w *LocalWorkspace) installMembershipState(next protocol.WorkspaceRuntimeBinding) error {
	j := w.state.Membership
	if j == nil || next.Validate(j.Proposed) != nil {
		return errors.New("membership binding does not match the proposal")
	}
	switch j.Kind {
	case "add":
		if j.Source == nil {
			return errors.New("membership addition has no source")
		}
		member := workspacePrivateMember{Member: j.Source.Member, Clone: j.Source.Path, CloneIdentity: j.Source.RootIdentity, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: j.Proposed.LaunchGeneration, MemberID: j.Source.Member.ID}, Pending: true}}
		if j.Prepared != nil {
			member = *j.Prepared
		}
		w.state.Sources = append(w.state.Sources, *j.Source)
		w.state.Members = append(w.state.Members, member)
	case "remove":
		for _, member := range w.state.Members {
			if member.Member.ID == j.MemberID && protocol.ReservedMemberPath(member.Member.Path) {
				retired := workspaceReservedRetired{Path: member.Member.Path, Target: member.Clone, Identity: member.WorkerPins.Clone, Delivered: member.LastSynced, RefsDigest: member.InitialRefsDigest, DiscardLocal: j.DiscardLocal}
				w.state.ReservedRetired = append(w.state.ReservedRetired, retired)
			}
		}
		keep := func(id string) bool { return id == j.MemberID }
		w.state.Sources = slices.DeleteFunc(w.state.Sources, func(v WorkspaceSource) bool { return keep(v.Member.ID) })
		w.state.Members = slices.DeleteFunc(w.state.Members, func(v workspacePrivateMember) bool { return keep(v.Member.ID) })
	}
	w.state.Launch = j.Proposed
	w.state.Binding = &next
	j.Phase = membershipCommitted
	return w.save()
}

// finishMembership installs the new bindings, then reopens the session. The journal
// stays until the writers run and the reader is proved: a failure at any step leaves
// the commit standing and the server's sweep repeats the apply from here.
func (s *LocalWorkspaceService) finishMembership(ctx context.Context, a workspaceMembershipAuthority, w *LocalWorkspace) error {
	j := w.state.Membership
	if j == nil || j.Phase != membershipCommitted {
		return errors.New("membership installation has no commit")
	}
	if err := s.installLiveMembership(ctx, w); err != nil {
		return err
	}
	workspaceCrashBoundary("membership-live-installed")
	// Cleanup runs fenced. Once writers run it waits for the next change.
	if !j.Thawed {
		if err := w.retireReserved(ctx); err != nil {
			log.Printf("workspace membership %s: reserved folder cleanup: %v", w.state.Session, err)
		}
	}
	if err := s.thawMembership(ctx, w); err != nil {
		log.Printf("workspace membership %s: reopen: %v", w.state.Session, err)
		return errors.Join(&membershipWaiting{protocol.MembershipWaitInstall}, err)
	}
	workspaceCrashBoundary("membership-reopened")
	operation := j.Operation
	w.state.Membership = nil
	if err := w.save(); err != nil {
		w.state.Membership = j
		return err
	}
	workspaceCrashBoundary("membership-journal-closed")
	return a.MembershipInstalled(ctx, w.state.Session, operation)
}

func (s *LocalWorkspaceService) installLiveMembership(ctx context.Context, w *LocalWorkspace) error {
	state := w.state
	if s.membershipShells != nil {
		if err := s.membershipShells.rebind(*state.Binding); err != nil {
			return err
		}
	} else {
		registry, ok := s.Supervisor.(*workspaceSupervisors)
		if !ok {
			return errors.New("workspace lifecycle unavailable")
		}
		c, err := registry.peer(state.Session)
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.binding.ManifestDigest != state.Binding.ManifestDigest {
			err = c.service.UpdateManifest(state.Session, c.binding.ManifestDigest, state.Binding.ManifestDigest)
			if err == nil {
				c.binding = *state.Binding
			}
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if w.host != nil && w.host.reader != nil {
		if err := w.host.reader.installMembership(state); err != nil {
			return err
		}
	}
	if d, ok := w.runtime.(*DockerWorkspaceContainer); ok {
		paths := map[string]bool{}
		for _, member := range state.Launch.Members {
			paths[member.Path] = true
		}
		d.Paths = paths
	}
	if s.Discovery != nil && s.Discovery() != nil {
		sources := make([]string, 0, len(state.Sources))
		for _, source := range state.Sources {
			sources = append(sources, source.Path)
		}
		// A failure costs only new children, as for a resumed launch.
		if err := s.Discovery().RebindMembership(ctx, state.Launch, sources); err != nil {
			log.Printf("workspace membership %s: children cannot select the new revision: %v", state.Session, err)
		}
	}
	return nil
}

// abandonMembership clears a change that was never committed and reopens the
// session. If reopening fails the writers stay frozen until a restart.
func (s *LocalWorkspaceService) abandonMembership(ctx context.Context, w *LocalWorkspace) error {
	if w.state.ActiveWorker != "" {
		return errors.New("workspace worker outcome unknown; writers remain fenced")
	}
	// A canceled apply must still reopen what it fenced.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if w.host == nil {
		if err := w.discardPrepared(ctx); err != nil {
			return err
		}
	} else if err := w.undoReservedAddition(w.state.Membership); err != nil {
		return err
	}
	phase := w.state.Membership
	w.state.Membership = nil
	if err := w.save(); err != nil {
		w.state.Membership = phase
		return err
	}
	return s.thawMembership(ctx, w)
}

// reconcileMembershipJournal settles a journal found at startup, before any agent
// or shell is admitted. Every writer of the old process is gone or will be reaped
// by the recovery that follows; nothing here thaws or starts one. A commit moves
// forward but keeps its journal: the recovery that follows reopens the runtime, so
// the thaw is its job, and the server's repeated apply validates the reader and
// acknowledges through finishMembership.
func (s *LocalWorkspaceService) reconcileMembershipJournal(ctx context.Context, state workspacePrivateManifest, record string) (workspacePrivateManifest, error) {
	j := state.Membership
	if j == nil {
		return state, nil
	}
	a, ok := s.Authority.(workspaceMembershipAuthority)
	if !ok {
		return state, errors.New("membership authority unavailable")
	}
	w := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: record}
	w.mu.Lock()
	defer w.mu.Unlock()
	if j.Phase == membershipCommitted {
		return w.state, w.settleCommitted(ctx)
	}
	phase, _, err := a.MembershipStatus(ctx, state.Session, j.Operation)
	if err != nil {
		return state, err
	}
	switch phase {
	case "committed", "installed":
		if j.Phase != membershipPrepared || state.Binding == nil {
			return state, errors.New("committed membership without a prepared receipt")
		}
		next := *state.Binding
		next.ManifestDigest = j.Proposed.Digest
		if err := w.installMembershipState(next); err != nil {
			return state, err
		}
		return w.state, w.settleCommitted(ctx)
	default:
		// Never published: the old revision stands and the server may retry.
		switch {
		case state.Host:
			if err := w.undoReservedAddition(j); err != nil {
				log.Printf("workspace membership %s: reserved folder cleanup: %v", state.Session, err)
			}
		case j.Kind == "add":
			// A clone with no member stays excluded. Without a recorded identity
			// (a crash right after the copy) it is kept, never removed on a guess.
			for _, member := range j.Proposed.Members {
				if member.ID != j.MemberID || !protocol.ReservedMemberPath(member.Path) {
					continue
				}
				retired := workspaceReservedRetired{Path: member.Path}
				if j.Prepared != nil {
					retired.Identity = j.Prepared.WorkerPins.Clone
				}
				w.state.ReservedRetired = append(w.state.ReservedRetired, retired)
			}
		}
		w.state.Membership = nil
		return w.state, w.save()
	}
}

// settleCommitted hands the thaw to recovery, and clears what it can before any
// writer is admitted. A cleanup failure is only a retry.
func (w *LocalWorkspace) settleCommitted(ctx context.Context) error {
	w.state.Membership.Thawed = true
	if err := w.save(); err != nil {
		return err
	}
	if err := w.retireReserved(ctx); err != nil {
		log.Printf("workspace membership %s: reserved folder cleanup: %v", w.state.Session, err)
	}
	return nil
}

// settleHostedMembership resolves a live journal after a reconnect, before the
// binding is presented to Recover: a commit whose reply was lost moved it.
func (s *LocalWorkspaceService) settleHostedMembership(ctx context.Context, w *LocalWorkspace) error {
	a, ok := s.Authority.(workspaceMembershipAuthority)
	if !ok {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	j := w.state.Membership
	if j == nil {
		return nil
	}
	if j.Phase == membershipPrepared {
		phase, _, err := a.MembershipStatus(ctx, w.state.Session, j.Operation)
		if err != nil {
			return err
		}
		if phase == "refused" || phase == "denied" {
			return s.abandonMembership(ctx, w)
		}
		if phase != "committed" && phase != "installed" {
			return nil // the server redispatches; the same journal resumes at commit
		}
		next := *w.state.Binding
		next.ManifestDigest = j.Proposed.Digest
		if err := w.installMembershipState(next); err != nil {
			return err
		}
	}
	if j.Phase == membershipCommitted {
		return s.finishMembership(ctx, a, w)
	}
	return nil
}

func (m *Manager) handleWorkspaceMembership(raw []byte) {
	var msg protocol.WorkspaceMembershipApply
	if json.Unmarshal(raw, &msg) != nil || m.config.Workspaces == nil || !validWorkspaceSessionID(msg.SessionID) {
		return
	}
	// Runs off the read pump: every step waits on replies that arrive through it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.config.Workspaces.ApplyMembership(ctx, msg)
	}()
}

func validWorkspaceSessionID(id string) bool { return sandboxCloneIDPattern.MatchString(id) }

// turnIdle is true only between turns.
func (h *AgentHost) turnIdle(id string) bool {
	sess := h.get(id)
	if sess == nil {
		return false
	}
	status, _ := sess.status.Load().(string)
	return status == "idle"
}
