package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// LocalWorkspaceService connects trusted P1 selection/child authorization and
// P0b runtime authority to the concrete P2 lifecycle. Missing authority refuses
// before any agent or shell starts; no capability advertisement is added here.
type LocalWorkspaceService struct {
	RecoverableHost func(string) bool // persisted agent recovery owns live host sessions
	TurnIdle        func(string) bool // an engine is between turns; nil refuses every membership change
	Discovery       func() membershipDiscovery

	membershipShells membershipShellSeam
	membershipClock  func() time.Time

	capabilityReady func() bool                                      // installed manager, not merely allocated callback closures
	HostExecutable  string                                           // trusted writer helper image, never decoded from requests
	HostCommand     func(shellruntime.Identity) shellruntime.Command // trusted image/test launcher; never decoded
	CloseStatus     func(context.Context, string) error
	Authority       WorkspaceRuntimeAuthority
	Supervisor      protocol.RuntimeSupervisor
	Storage         WorkspaceStorage
	Resolve         func(context.Context, protocol.SpawnSession) (sources []WorkspaceSource, parentSession string, err error)
	AuthorizeChild  func(context.Context, string, string) error
	TrustedLaunch   func(context.Context, string) (protocol.WorkspaceManifest, error)
	CancelStatus    func(context.Context, string) error
	ResumeStatus    func(context.Context, string) error
	Signal          func(string, string)
	CommitFinal     func(context.Context, protocol.SupervisorLaunch, WorkspaceFinalization) (WorkspaceFinalReceipt, error)
	Results         func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error
	recoveryRetries map[string]workspaceRecoveryRetry // protected by recoverMu
	recoverMu       sync.Mutex
	mu              sync.Mutex
	sessions        map[string]*LocalWorkspace
}

func (s *LocalWorkspaceService) Prepare(ctx context.Context, msg protocol.SpawnSession) (*LocalWorkspace, error) {
	if err := recordWorkBranch(msg); err != nil {
		return nil, err
	}
	if s == nil || s.Resolve == nil || s.Authority == nil || s.Supervisor == nil || s.Results == nil || s.CommitFinal == nil {
		return nil, errors.New("sandbox workspace-v2 requires trusted selection and P0b runtime authority")
	}
	if !msg.Sandbox || msg.Kind != "agent" || msg.Workspace == nil || !msg.WorkspaceCapabilities.Supports("sandbox", false) || !protocol.WorkspaceRuntimeEnabled("sandbox", runtime.GOOS) {
		return nil, errors.New("unsupported sandbox workspace launch")
	}
	if err := msg.Workspace.Validate(); err != nil {
		return nil, err
	}
	admission := workspaceAdmissionLock(msg.SessionID)
	admission.Lock()
	defer admission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]*LocalWorkspace{}
	}
	if w := s.sessions[msg.SessionID]; w != nil {
		w.mu.Lock()
		equal := reflect.DeepEqual(w.state.Launch, *msg.Workspace)
		w.mu.Unlock()
		if !equal {
			return nil, errors.New("workspace launch replacement refused")
		}
		return w, nil
	}
	sources, parentID, err := s.Resolve(ctx, msg)
	if err != nil {
		return nil, err
	}
	storage := s.Storage
	if storage == nil {
		storage = defaultWorkspaceStorage()
	}
	var w *LocalWorkspace
	if parentID != "" {
		if s.AuthorizeChild == nil {
			return nil, errors.New("workspace child authorization unavailable")
		}
		if err := s.AuthorizeChild(ctx, parentID, msg.SessionID); err != nil {
			return nil, err
		}
		record, recordErr := workspaceRecord(parentID)
		if recordErr != nil {
			return nil, recordErr
		}
		parent, parentErr := readPrivateWorkspace(record)
		if parentErr != nil {
			return nil, parentErr
		}
		if err := protocol.ValidateWorkspaceChild(parent.Launch, *msg.Workspace); err != nil {
			return nil, err
		}
		sources = nil
		for _, member := range msg.Workspace.Members {
			found := false
			for _, source := range parent.Sources {
				if source.Member.ID == member.ID {
					sources = append(sources, source)
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("child source outside parent launch")
			}
		}
		w, err = PrepareLocalWorkspaceChild(ctx, msg.SessionID, parent.Launch, *msg.Workspace, sources, storage, s.beginRuntimeLocked)
	} else {
		w, err = PrepareLocalWorkspace(ctx, msg.SessionID, *msg.Workspace, sources, storage, s.beginRuntimeLocked)
	}
	if err != nil {
		return nil, err
	}
	w.state.ShellEligible = parentID == "" && !msg.RestrictTools
	if err := w.save(); err != nil {
		return nil, err
	}
	s.sessions[msg.SessionID] = w
	return w, nil
}
func (s *LocalWorkspaceService) Runtime(ctx context.Context, w *LocalWorkspace, container string) (*DockerWorkspaceContainer, error) {
	if s == nil || s.Authority == nil || s.Supervisor == nil || s.Results == nil || s.CommitFinal == nil {
		return nil, errors.New("P0b runtime authority unavailable")
	}
	if !w.state.RuntimePreparing || w.state.SupervisorLaunch.Validate() != nil {
		return nil, errors.New("runtime creation intent missing")
	}
	raw, err := dockerOutput(pinnedDockerArgs(container, "inspect", "--format", "{{.Id}}", container)...)
	if err != nil {
		return nil, err
	}
	d := &DockerWorkspaceContainer{Context: w.state.RuntimeContext, ID: strings.TrimSpace(string(raw)), Owner: sandboxOwner(), Session: w.state.Session, Launch: w.state.Launch.LaunchGeneration, Paths: map[string]bool{}}
	for _, m := range w.state.Launch.Members {
		d.Paths[m.Path] = true
	}
	d.CancelStatus = func(ctx context.Context) error {
		if s.CancelStatus != nil {
			return s.CancelStatus(ctx, w.state.Session)
		}
		return nil
	}
	d.CloseStatus = func(ctx context.Context) error {
		if s.CloseStatus != nil {
			return s.CloseStatus(ctx, w.state.Session)
		}
		return nil
	}
	d.ResumeStatus = func(ctx context.Context) error {
		if s.ResumeStatus != nil {
			return s.ResumeStatus(ctx, w.state.Session)
		}
		return nil
	}
	d.Signal = func(message string) {
		if s.Signal != nil {
			s.Signal(w.state.Session, message)
		}
	}
	if err := d.validateEntryAuthority(ctx); err != nil {
		return nil, err
	}
	pinSandboxContext(d.ID, d.Context)
	root, err := w.BindRuntime(ctx, d)
	if err != nil {
		return nil, err
	}
	if err := s.bindAuthority(ctx, w, d, root); err != nil {
		return nil, err
	}
	return d, nil
}
func (s *LocalWorkspaceService) Report(ctx context.Context, w *LocalWorkspace) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Binding == nil || w.authorize == nil {
		return errors.New("unbound workspace results")
	}
	if err := w.authorize(ctx); err != nil {
		return fmt.Errorf("authorize workspace result report: %w", err)
	}
	results := w.results()
	if err := w.state.Launch.ValidateResults(results); err != nil {
		return err
	}
	if s.Results != nil {
		if err := s.Results(ctx, w.state.Session, *w.state.Binding, results); err != nil {
			return fmt.Errorf("send workspace result report: %w", err)
		}
	}
	return nil
}
func (s *LocalWorkspaceService) RecoverAll(ctx context.Context, report func(protocol.SessionEnded) error) error {
	return s.recoverSelected(ctx, "", report)
}

// A stop for one session must not retry every unrelated retained journal.
func (s *LocalWorkspaceService) RecoverSession(ctx context.Context, id string, report func(protocol.SessionEnded) error) error {
	if id == "" {
		return nil
	}
	return s.recoverSelected(ctx, id, report)
}

func (s *LocalWorkspaceService) recoverSelected(ctx context.Context, id string, report func(protocol.SessionEnded) error) error {
	if s == nil {
		return nil
	}
	if s.TrustedLaunch == nil || s.Authority == nil || s.Supervisor == nil || s.Results == nil || s.CommitFinal == nil {
		return errors.New("workspace recovery awaits P0b authority")
	}
	s.recoverMu.Lock()
	defer s.recoverMu.Unlock()
	entries, err := os.ReadDir(sandboxCloneRoot())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var combined error
	for _, entry := range entries {
		if (id != "" && entry.Name() != id+".workspace.json") || !strings.HasSuffix(entry.Name(), ".workspace.json") || !entry.Type().IsRegular() {
			continue
		}
		state, err := readPrivateWorkspace(filepath.Join(sandboxCloneRoot(), entry.Name()))
		if err != nil {
			combined = errors.Join(combined, err)
			continue
		}
		if !validWorkspaceSessionID(state.Session) || entry.Name() != state.Session+".workspace.json" {
			failurelog.Default.Event("storage:journal_filename_mismatch", slog.LevelWarn, "component", "daemon", "operation", "workspace_recovery", "cause", "journal_filename_mismatch")
			combined = errors.Join(combined, errors.New("workspace journal filename mismatch"))
			continue
		}
		if retry := s.recoveryRetries[state.Session]; retry.token == state.RuntimeToken && retry.digest == state.Launch.Digest && time.Now().Before(retry.next) {
			continue
		}
		refusalState := state
		if state.AuthorityRefused {
			continue
		}
		if state.Host {
			admission := workspaceAdmissionLock(state.Session)
			if !admission.TryLock() {
				continue
			} // preparation owns this journal
			s.mu.Lock()
			hosted := s.sessions[state.Session]
			s.mu.Unlock()
			var recoveryErr error
			if hosted != nil {
				recoveryErr = s.reconnectHosted(ctx, hosted, report)
			} else {
				recoveryErr = s.recoverHost(ctx, state, filepath.Join(sandboxCloneRoot(), entry.Name()), report)
			}
			admission.Unlock()
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, recoveryErr))
			continue
		}
		if workspaceFinalComplete(state) {
			if state.StorageReleased && (state.StorageConfirmation == "" || state.StorageAcknowledged) {
				storage := s.Storage
				if storage == nil {
					storage = defaultWorkspaceStorage()
				}
				w := &LocalWorkspace{state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name())}
				lock := workspaceLock(state.Session)
				s.mu.Lock()
				if hosted := s.sessions[state.Session]; hosted != nil {
					lock = hosted.mu
				}
				s.mu.Unlock()
				lock.Lock()
				pruneErr := pruneWorkspaceStorage(w, storage)
				lock.Unlock()
				if err := pruneErr; err != nil {
					failurelog.Default.Event("storage:pruning_failed", slog.LevelWarn, "component", "daemon", "operation", "release_storage", "cause", "journal_or_allocation_incomplete", "error_type", failurelog.Cause(err))
					combined = errors.Join(combined, err)
				}
			}
			continue
		}
		// A registry entry belongs to this process, not an orphan. Rebind the
		// authoritative object under its lifecycle lock; never apply orphan Stop.
		s.mu.Lock()
		hosted := s.sessions[state.Session]
		s.mu.Unlock()
		if hosted != nil {
			if err := s.reconnectHosted(ctx, hosted, report); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			}
			continue
		}
		// A completion may already exist remotely even if its receipt was lost.
		// This authenticated adapter reconciles the immutable transaction before
		// ordinary Current/Recover (which correctly reject ended sessions).
		if state.FinalDelivery != nil && state.ResultsAcknowledged {
			w := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name())}
			if err := s.Finalize(ctx, w, *state.FinalDelivery, report); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			}
			continue
		}
		if state, err = s.reconcileMembershipJournal(ctx, state, filepath.Join(sandboxCloneRoot(), entry.Name())); err != nil {
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			continue
		}

		launch, err := s.TrustedLaunch(ctx, state.Session)
		if err != nil {
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			continue
		}

		if state.Binding == nil && state.RuntimeIdentity != "" && !state.RuntimePreparing {
			pending := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name())}
			if err := s.abortRootOnly(ctx, pending); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
				continue
			}
			state = pending.state
		}
		if state.RuntimePreparing {
			pending := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name())}
			if err := s.abortRuntime(ctx, pending); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
				continue
			}
			state = pending.state
		}
		storage := s.Storage
		if storage == nil {
			storage = defaultWorkspaceStorage()
		}
		// A prepared allocation can crash after its no-admission/worker-join stop
		// journal but before freezing the final payload. It has no runtime to
		// resurrect and no missing-supervisor proof to manufacture.
		if state.RuntimeIdentity == "" && state.Stopped && state.StopRequested && state.Binding != nil {
			w := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name()), storage: storage}
			if err := w.validateAllocation(ctx); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
				continue
			}
			w.authorize = func(ctx context.Context) error { return s.currentWorkspace(ctx, w) }
			if err := s.Finalize(ctx, w, protocol.SessionEnded{Type: "session_ended", SessionID: state.Session}, report); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			}
			continue
		}
		var peer WorkspaceContainer
		if state.RuntimeIdentity != "" {
			dockerContext, id, ok := strings.Cut(state.RuntimeIdentity, ":")
			if !ok {
				combined = errors.Join(combined, errors.New("invalid recorded runtime"))
				continue
			}
			d := &DockerWorkspaceContainer{Context: dockerContext, ID: id, Owner: sandboxOwner(), Session: state.Session, Launch: launch.LaunchGeneration, Paths: map[string]bool{}, CancelStatus: func(context.Context) error { return nil }}
			for _, m := range launch.Members {
				d.Paths[m.Path] = true
			}
			peer = d
			if !state.Stopped {
				if absent, err := d.Absent(ctx); err != nil {
					combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
					continue
				} else if !absent {
					if err := d.validateEntryAuthority(ctx); err != nil {
						combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
						continue
					}
				}
			}
			if state.Binding == nil || state.Binding.Validate(launch) != nil || state.Binding.Root != state.RuntimeRoot || state.SupervisorLaunch.Validate() != nil {
				combined = errors.Join(combined, errors.New("missing runtime binding on recovery"))
				continue
			}
			resumed, resumeErr := workspaceRecoverCleanup(ctx, s.Authority, state.SupervisorLaunch, *state.Binding)
			if resumeErr != nil || resumed.Validate() != nil || resumed.SessionID != state.SupervisorLaunch.SessionID || resumed.LaunchGeneration != state.SupervisorLaunch.LaunchGeneration || resumed.RuntimeGeneration != state.SupervisorLaunch.RuntimeGeneration || resumed.Runtime != state.SupervisorLaunch.Runtime {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, resumeErr), errors.New("recovery incarnation not authorized"))
				continue
			}
			if resumed != state.SupervisorLaunch {
				state.SupervisorLaunch = resumed
				rebound := &LocalWorkspace{state: state, record: filepath.Join(sandboxCloneRoot(), entry.Name())}
				if err := rebound.save(); err != nil {
					combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
					continue
				}
			}
			if err := workspaceCurrentAuthority(cleanupContext(ctx), s.Authority, state.SupervisorLaunch, *state.Binding); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
				continue
			}
			d.Supervisor = s.Supervisor
			d.SupervisorLaunch = state.SupervisorLaunch

		}
		var authorize, before func(context.Context) error
		if state.Binding != nil {
			authorize = func(ctx context.Context) error {
				return workspaceCurrentAuthority(ctx, s.Authority, state.SupervisorLaunch, *state.Binding)
			}
			before = func(ctx context.Context) error {
				if registry, ok := s.Supervisor.(*workspaceSupervisors); ok {
					d, ok := peer.(*DockerWorkspaceContainer)
					if !ok {
						return errors.New("workspace recovery runtime unavailable")
					}
					if err := registry.registerRecovery(ctx, state, d); err != nil {
						return err
					}
				}
				proof, err := s.Supervisor.RecoverLaunch(ctx, state.SupervisorLaunch)
				if err != nil {
					return err
				}
				return proof.ValidateFor(state.SupervisorLaunch, "")
			}
		}
		w, err := recoverLocalWorkspace(cleanupContext(ctx), state.Session, launch, storage, peer, authorize, before)
		if err != nil {
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			continue
		}
		if w.state.RuntimeIdentity == "" {
			if _, ok := s.Authority.(workspacePreparationCleanupAuthority); ok {
				if err := s.cleanupPrepared(ctx, w, report); err != nil {
					combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
				}
			} else {
				s.mu.Lock()
				if s.sessions == nil {
					s.sessions = map[string]*LocalWorkspace{}
				}
				s.sessions[state.Session] = w
				s.mu.Unlock()
			}
			continue
		}
		if w.state.FinalDelivery != nil {
			if err := s.Finalize(ctx, w, *w.state.FinalDelivery, report); err != nil {
				combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
			}
			continue
		}
		_, stopErr := w.Stop(ctx)
		message := ""
		exit := 0
		if stopErr != nil {
			message = "workspace recovery retained unresolved work"
			exit = 1
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, stopErr))
		}
		if err := s.Finalize(ctx, w, protocol.SessionEnded{Type: "session_ended", SessionID: state.Session, ExitCode: exit, Message: message}, report); err != nil {
			combined = errors.Join(combined, s.recordRecoveryRefusal(refusalState, err))
		}

	}
	return combined
}
func workspaceGuide(manifest *protocol.WorkspaceManifest) string {
	if manifest == nil || manifest.Runtime == "host" {
		return "" // host paths come only from the private pinned handoff
	}
	var b strings.Builder
	b.WriteString("\nWorkspace repositories (immutable launch membership):\n")
	for _, m := range manifest.Members {
		fmt.Fprintf(&b, "- %s: /workspace/%s\n", m.ID, m.Path)
	}
	b.WriteString("Work in the affected member path. This selection grants no shell permission. Commit in each affected member repository and report every commit ID. Newly discovered repositories are not launch members. Folder children may select only launch members; shell grants are never inherited.\n")
	return b.String()
}

func defaultWorkspaceStorage() WorkspaceStorage {
	return &DockerWorkspaceStorage{Root: filepath.Join(DaemonStateDir(), "workspace-volumes")}
}

// PendingIDs is available before recovery/hello. Valid pending transactions
// can own a live session even after the agent leaves the in-memory map.
// Failed preparations, durable stop intents and refused records retain work
// without claiming liveness; QueuedSpawnIDs covers preparation in flight.
// Cleanup retry ownership is independent of agent liveness.
func (s *LocalWorkspaceService) PendingIDs() []string {
	if s == nil {
		return nil
	}
	entries, err := os.ReadDir(sandboxCloneRoot())
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".workspace.json") {
			continue
		}
		state, err := readPrivateWorkspace(filepath.Join(sandboxCloneRoot(), e.Name()))
		if err == nil && state.Version == privateWorkspaceVersion && e.Name() == state.Session+".workspace.json" && !workspaceFinalComplete(state) && !state.AuthorityRefused && !state.Preparing && !state.StopRequested {
			ids = append(ids, state.Session)
		}
	}
	return ids
}

// A definitive, correlated refusal cannot be repaired by another heartbeat.
// Retain the journal and all storage; refusal is not proof of successful cleanup.
func (s *LocalWorkspaceService) recordRecoveryRefusal(expected workspacePrivateManifest, err error) (result error) {
	id := expected.Session
	defer func() {
		if result != nil {
			result = fmt.Errorf("workspace %s recovery: %w", id, result)
		}
	}()
	if err == nil {
		delete(s.recoveryRetries, id)
		return nil
	}
	var refused *WorkspaceRefusedError
	if !errors.As(err, &refused) {
		if s.recoveryRetries == nil {
			s.recoveryRetries = map[string]workspaceRecoveryRetry{}
		}
		retry := s.recoveryRetries[id]
		if retry.token != expected.RuntimeToken || retry.digest != expected.Launch.Digest {
			retry = workspaceRecoveryRetry{}
		}
		if retry.delay == 0 && expected.StopRequested {
			cause := failurelog.Cause(err)
			var unavailable *WorkspaceUnavailableError
			if errors.As(err, &unavailable) && unavailable.Cause == protocol.WorkspaceControlStorage {
				cause = "storage_unavailable"
			}
			failurelog.Default.Event("storage:cleanup_retained:"+id, slog.LevelWarn,
				"component", "daemon", "stage", "workspace cleanup retained",
				"session_id", id, "cause", cause)
		}
		retry.delay *= 2
		if retry.delay < time.Second {
			retry.delay = time.Second
		}
		if retry.delay > time.Minute {
			retry.delay = time.Minute
		}
		retry.token, retry.digest, retry.next = expected.RuntimeToken, expected.Launch.Digest, time.Now().Add(retry.delay)
		s.recoveryRetries[id] = retry
		return err
	}
	s.mu.Lock()
	hosted := s.sessions[id]
	s.mu.Unlock()
	lock := workspaceLock(id)
	if hosted != nil {
		lock = hosted.mu
	}
	lock.Lock()
	defer lock.Unlock()
	record := filepath.Join(sandboxCloneRoot(), id+".workspace.json")
	state, readErr := readPrivateWorkspace(record)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	// A refusal belongs to the incarnation that made the RPC. A concurrent
	// preparation or membership change may have replaced its journal meanwhile.
	if state.RuntimeToken != expected.RuntimeToken || !reflect.DeepEqual(state.Launch, expected.Launch) || state.SupervisorLaunch.RuntimeGeneration != expected.SupervisorLaunch.RuntimeGeneration {
		return err
	}
	state.AuthorityRefused = true
	if hosted != nil {
		hosted.state.AuthorityRefused = true
	}
	w := &LocalWorkspace{state: state, record: record}
	return errors.Join(err, w.save())
}

type workspaceRecoveryRetry struct {
	token, digest string
	delay         time.Duration
	next          time.Time
}
