package daemon

// This file owns local workspace lifecycle. Public manifests never contain a
// host path. Only the trusted selection adapter may construct local sources.
import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const privateWorkspaceVersion = 2
const workspaceSyncNotice = "Syncing the agent's commits…"

// WorkspaceStorage is an enforced, session-wide allocation, not a size estimate.
// Implementations must enforce both byte and inode quotas for all container
// writers and survive daemon restart. No implementation means launch refusal.
// Release is called only after all writers are proven gone and all work saved.
type WorkspaceStorage interface {
	Allocate(context.Context, string, protocol.WorkspaceBudget) (string, error)
	Validate(context.Context, string, string, protocol.WorkspaceBudget) error
	Release(context.Context, string, string) error
}

// WorkspaceContainer is the authenticated runtime adapter. Identity must verify
// the immutable Docker ID plus daemon/session/launch labels, never just a name.
// QuiesceStatus cancels and drains P4 status operations before Head or Freeze.
// Head and Settle execute inside the running container at a fixed member cwd.
// Remove must terminate all writers and prove that the container is absent.
// Freeze includes P5 shell supervisors and their descendants automatically.
type WorkspaceContainer interface {
	RuntimeID() string
	Reconcile(context.Context) error
	RootIdentity(context.Context) (protocol.WorkspaceRoot, error)
	Identity(context.Context, string, string) error
	QuiesceStatus(context.Context) error
	StopWriters(context.Context) error
	Head(context.Context, string) (string, error)
	Freeze(context.Context) error
	Thaw(context.Context) error
	Settle(context.Context, string, string) (bool, error)
	Remove(context.Context) error
	SyncSignal(string)
}

type WorkspaceSource struct {
	CommonPath     string
	RootIdentity   protocol.WorkspaceRoot
	CommonIdentity protocol.WorkspaceRoot
	Member         protocol.WorkspaceMember
	Path           string
}

type workspacePrivateMember struct {
	SavedTreeHead        string                   `json:"saved_tree_head"`
	InitialRefsDigest    string                   `json:"initial_refs_digest"`
	ReleasePending       bool                     `json:"release_pending"`
	SettledClean         bool                     `json:"settled_clean"`
	WorkerPins           workspaceMemberPins      `json:"worker_pins"`
	SourceCommon         string                   `json:"source_common"`
	SourceCommonIdentity protocol.WorkspaceRoot   `json:"source_common_identity"`
	Released             bool                     `json:"released"`
	Member               protocol.WorkspaceMember `json:"member"`
	Clone                string                   `json:"clone"`
	Source               sandboxClone             `json:"source"`
	// Identities are private, pinned after creation. They are not the public
	// launch root, which P0b binds separately after runtime allocation.
	CloneIdentity         protocol.WorkspaceRoot         `json:"clone_identity"`
	GitIdentity           protocol.WorkspaceRoot         `json:"git_identity"`
	ObjectsIdentity       protocol.WorkspaceRoot         `json:"objects_identity"`
	SourceIdentity        protocol.WorkspaceRoot         `json:"source_identity"`
	SourceObjectsIdentity protocol.WorkspaceRoot         `json:"source_objects_identity"`
	LastSynced            string                         `json:"last_synced"`
	Result                protocol.WorkspaceMemberResult `json:"result"`
}

type workspacePrivateManifest struct {
	HostChild                bool                              `json:"host_child"`
	HostChildren             []hostChildAllocation             `json:"host_children,omitempty"`
	HostHelpersJournaled     bool                              `json:"host_helpers_journaled"`
	HostController           hostHelperIdentity                `json:"host_controller"`
	Host                     bool                              `json:"host"`
	HostWorkDir              string                            `json:"host_workdir"`
	HostDriverStopped        bool                              `json:"host_driver_stopped"`
	HostHelpers              []hostHelperIdentity              `json:"host_helpers,omitempty"`
	ShellEligible            bool                              `json:"shell_eligible"`
	RemoteMountSource        string                            `json:"remote_mount_source"`
	RuntimeToken             string                            `json:"runtime_token"`
	RuntimeContext           string                            `json:"runtime_context"`
	RuntimePreparing         bool                              `json:"runtime_preparing"`
	BindingIntent            *protocol.WorkspaceRuntimeBinding `json:"binding_intent,omitempty"`
	StopRequested            bool                              `json:"stop_requested"`
	StopDelivery             *protocol.SessionEnded            `json:"stop_delivery,omitempty"`
	FinalDelivery            *protocol.SessionEnded            `json:"final_delivery,omitempty"`
	FinalResults             []protocol.WorkspaceMemberResult  `json:"final_results,omitempty"`
	ResultsAcknowledged      bool                              `json:"results_acknowledged"`
	TerminalReceipt          string                            `json:"terminal_receipt,omitempty"`
	TerminalAcknowledged     bool                              `json:"terminal_acknowledged"`
	ProbeUncertain           bool                              `json:"probe_uncertain"`
	Binding                  *protocol.WorkspaceRuntimeBinding `json:"binding,omitempty"`
	SupervisorLaunch         protocol.SupervisorLaunch         `json:"supervisor_launch"`
	ActiveWorker             string                            `json:"active_worker"`
	WorkerContext            string                            `json:"worker_context"`
	Preparing                bool                              `json:"preparing"`
	RemovalPending           bool                              `json:"removal_pending"`
	AllocationReleasePending bool                              `json:"allocation_release_pending"`
	Sources                  []WorkspaceSource                 `json:"sources"`
	WorkerRoot               protocol.WorkspaceRoot            `json:"worker_root"`
	StorageReleased          bool                              `json:"storage_released"`
	RuntimeRoot              protocol.WorkspaceRoot            `json:"runtime_root"`
	RuntimeIdentity          string                            `json:"runtime_identity"`
	Version                  int                               `json:"version"`
	Session                  string                            `json:"session"`
	Launch                   protocol.WorkspaceManifest        `json:"launch"`
	Root                     string                            `json:"root"`
	RootIdentity             protocol.WorkspaceRoot            `json:"root_identity"`
	Members                  []workspacePrivateMember          `json:"members"`
	Next                     int                               `json:"next"`
	Unavailable              bool                              `json:"unavailable"`
	Stopped                  bool                              `json:"stopped"`
}

// LocalWorkspace exposes no mutable manifest state. One mutex serializes sync,
// stop, recovery and deletion, including the runtime adapter's operations.
type LocalWorkspace struct {
	host      *hostWorkspace
	authorize func(context.Context) error
	mu        *sync.Mutex
	state     workspacePrivateManifest
	record    string
	storage   WorkspaceStorage
	runtime   WorkspaceContainer
}

// Admission precedes registry/P2 locks while a durable handoff is created.
// Recovery takes the same admission lock before interpreting a host handoff.
var localWorkspaceAdmissionLocks sync.Map

func workspaceAdmissionLock(session string) *sync.Mutex {
	v, _ := localWorkspaceAdmissionLocks.LoadOrStore(session, new(sync.Mutex))
	mu, ok := v.(*sync.Mutex)
	if !ok {
		panic("invalid workspace admission lock")
	}
	return mu
}

var localWorkspaceLocks sync.Map

func workspaceLock(session string) *sync.Mutex {
	v, _ := localWorkspaceLocks.LoadOrStore(session, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		panic("invalid private workspace lock")
	}
	return mu
}

func workspaceRecord(session string) (string, error) {
	if !sandboxCloneIDPattern.MatchString(session) || sandboxCloneRoot() == "" {
		return "", errors.New("invalid workspace session or missing private state")
	}
	return filepath.Join(sandboxCloneRoot(), session+".workspace.json"), nil
}

// PrepareLocalWorkspace clones every member before returning. sources must be
// resolved by the trusted daemon selection service, never from agent files.
// The supplied storage backend must enforce DefaultWorkspaceBudget; there is
// deliberately no ordinary-directory fallback on unsupported Docker hosts.
func PrepareLocalWorkspace(ctx context.Context, session string, launch protocol.WorkspaceManifest, sources []WorkspaceSource, storage WorkspaceStorage, before ...func(context.Context, *LocalWorkspace) error) (*LocalWorkspace, error) {
	if err := launch.Validate(); err != nil {
		return nil, err
	}
	if launch.Runtime != "sandbox" || storage == nil {
		return nil, errors.New("sandbox workspace requires enforced quota storage")
	}
	record, err := workspaceRecord(session)
	if err != nil {
		return nil, err
	}
	mu := workspaceLock(session)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		return nil, errors.New("workspace allocation already exists or is unavailable")
	}
	if len(sources) != len(launch.Members) {
		return nil, errors.New("incomplete trusted selection")
	}
	// Validate the whole selection before allocating or running clone commands.
	seen := map[string]bool{}
	commonIdentities := map[protocol.WorkspaceRoot]bool{}
	for i, source := range sources {
		if !reflect.DeepEqual(source.Member, launch.Members[i]) || !filepath.IsAbs(source.Path) {
			return nil, errors.New("source does not match immutable selection")
		}
		canonical, err := filepath.EvalSymlinks(source.Path)
		if err != nil || canonical != source.Path {
			return nil, errors.New("source is not canonical")
		}
		if seen[canonical] {
			return nil, errors.New("duplicate source")
		}
		for prev := range seen {
			if strings.HasPrefix(canonical, prev+string(filepath.Separator)) || strings.HasPrefix(prev, canonical+string(filepath.Separator)) {
				return nil, errors.New("overlapping sources")
			}
		}
		rootIdentity, err := workspaceDirectoryIdentity(canonical)
		if err != nil || source.RootIdentity.Inode == 0 || rootIdentity != source.RootIdentity || source.CommonIdentity.Inode == 0 {
			return nil, errors.New("trusted source identity changed or is missing")
		}
		if commonIdentities[source.CommonIdentity] {
			return nil, errors.New("duplicate source common directory")
		}
		commonIdentities[source.CommonIdentity] = true
		common := source.CommonPath
		if common == "" {
			common = filepath.Join(source.Path, ".git")
		}
		canonicalCommon, commonErr := filepath.EvalSymlinks(common)
		commonPin, pinErr := workspaceDirectoryIdentity(common)
		if commonErr != nil || pinErr != nil || !filepath.IsAbs(common) || canonicalCommon != common || commonPin != source.CommonIdentity {
			return nil, errors.New("trusted common-directory path is missing or changed")
		}
		seen[canonical] = true
	}
	budget := protocol.DefaultWorkspaceBudget()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(budget.PrepareSeconds)*time.Second)
	defer cancel()
	w := &LocalWorkspace{mu: mu, record: record, storage: storage, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Session: session, Launch: launch, Sources: sources, Preparing: true}}
	if err := w.save(); err != nil {
		return nil, err
	}
	// Allocate server authority before any quota allocation or clone worker.
	// The callback owns this existing P2 lock; it must not reacquire it.
	for _, prepare := range before {
		if err := prepare(ctx, w); err != nil {
			return nil, err
		}
	}
	root, err := storage.Allocate(ctx, session, budget)
	if err != nil {
		return nil, fmt.Errorf("workspace quota allocation: %w", err)
	}
	w.state.Root = root
	_, remote := storage.(workspaceRemoteStorage)
	if !filepath.IsAbs(root) || (!remote && !emptyDir(root)) {
		return nil, errors.New("quota allocation must be a new empty absolute directory")
	}
	// From this point failures retain the allocation and private recovery record.
	// A quota backend may have allocated state even if its validation fails.
	if err := storage.Validate(ctx, session, root, budget); err != nil {
		return nil, fmt.Errorf("workspace retained at %s: %w", root, err)
	}
	if remoteStorage, ok := storage.(workspaceRemoteStorage); ok {
		w.state.RootIdentity, err = remoteStorage.Identity(ctx, session, root)
	} else {
		w.state.RootIdentity, err = workspaceDirectoryIdentity(root)
	}
	if err != nil {
		return nil, err
	}
	if err := w.save(); err != nil {
		return nil, err
	}
	if err := w.resumePreparation(ctx); err != nil {
		return nil, err
	}

	return w, nil
}

func (w *LocalWorkspace) save() error {
	if err := os.MkdirAll(filepath.Dir(w.record), 0o700); err != nil { // #nosec G703 -- record is constructed by workspaceRecord under daemon-private state.
		return err
	}
	state, err := json.Marshal(w.state)
	if err != nil {
		return err
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(state)
	raw, err := json.Marshal(workspaceEnvelope{State: state, MAC: hex.EncodeToString(mac.Sum(nil))})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(w.record), filepath.Base(w.record)+".tmp-")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }() // #nosec G703 -- fixed .tmp suffix of the daemon-derived private record.
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, w.record); err != nil { // #nosec G703 -- same daemon-private journal directory; no request path.
		return err
	}
	dir, err := os.Open(filepath.Dir(w.record)) // #nosec G703 -- sync the daemon-derived private journal directory.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func readPrivateWorkspace(record string) (workspacePrivateManifest, error) {
	var state workspacePrivateManifest
	st, err := os.Lstat(record) // #nosec G703 -- caller derives the journal name under daemon-private state; ownership/mode/MAC validated below.
	if err != nil || !safeSandboxGitInfo(st, false) || st.Mode().Perm()&0077 != 0 || st.Size() > 1<<20 {
		return state, errors.New("private workspace record unavailable")
	}
	f, err := os.OpenFile(record, os.O_RDONLY, 0) //nolint:gosec // daemon-derived private record; ownership and file identity checked
	if err != nil {
		return state, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(st, after) {
		return state, errors.New("private workspace record replaced")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	var envelope workspaceEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return state, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return state, errors.New("trailing private manifest data")
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return state, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(envelope.State)
	signature, err := hex.DecodeString(envelope.MAC)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return state, errors.New("private workspace integrity check failed")
	}
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return state, err
	}
	return state, nil
}

// RecoverLocalWorkspace compares the private record with independently persisted
// trusted launch state. A replaced runtime or corrupted shared record blocks all
// fetches/deletes. Member-local identity errors are deferred to individual results.
func RecoverLocalWorkspace(ctx context.Context, session string, launch protocol.WorkspaceManifest, storage WorkspaceStorage, runtime WorkspaceContainer) (*LocalWorkspace, error) {
	return recoverLocalWorkspace(ctx, session, launch, storage, runtime, nil, nil)
}

func recoverLocalWorkspace(ctx context.Context, session string, launch protocol.WorkspaceManifest, storage WorkspaceStorage, runtime WorkspaceContainer, authorize, before func(context.Context) error) (*LocalWorkspace, error) {
	record, err := workspaceRecord(session)
	if err != nil {
		return nil, err
	}
	mu := workspaceLock(session)
	mu.Lock()
	defer mu.Unlock()
	state, err := readPrivateWorkspace(record)
	if err != nil {
		return nil, err
	}
	if err := launch.Validate(); err != nil {
		return nil, err
	}
	if state.Version != privateWorkspaceVersion || state.Session != session || !reflect.DeepEqual(state.Launch, launch) || len(state.Members) > len(launch.Members) || (!state.Preparing && len(state.Members) != len(launch.Members)) {
		return nil, errors.New("private workspace launch mismatch")
	}
	w := &LocalWorkspace{mu: mu, state: state, record: record, storage: storage, runtime: runtime, authorize: authorize}
	if authorize != nil {
		if err := authorize(ctx); err != nil {
			return nil, err
		}
	}
	if before != nil {
		if err := before(ctx); err != nil {
			return nil, err
		}
	}
	if state.ActiveWorker != "" {
		if err := w.worker().Cleanup(ctx, state.ActiveWorker, state.WorkerContext); err != nil {
			return nil, err
		}
		w.state.ActiveWorker = ""
		if err := w.save(); err != nil {
			return nil, err
		}
	}
	if state.Preparing {
		if state.Root == "" {
			root, err := storage.Allocate(ctx, session, protocol.DefaultWorkspaceBudget())
			if err != nil {
				return nil, err
			}
			w.state.Root = root
			w.state.RootIdentity, err = w.allocationIdentity(ctx)
			if err != nil {
				return nil, err
			}
			if err := w.save(); err != nil {
				return nil, err
			}
		}
		if state.RuntimeIdentity != "" || runtime != nil {
			return nil, errors.New("preparation unexpectedly has a runtime")
		}
		if err := w.validateAllocation(ctx); err != nil {
			return nil, err
		}
		budget := protocol.DefaultWorkspaceBudget()
		prepareCtx, cancel := context.WithTimeout(ctx, time.Duration(budget.PrepareSeconds)*time.Second)
		defer cancel()
		if err := w.resumePreparation(prepareCtx); err != nil {
			return nil, err
		}
		return w, nil
	}
	if state.RuntimeIdentity == "" && runtime == nil && !state.Stopped && !state.Unavailable {
		if err := w.validateAllocation(ctx); err != nil {
			return nil, err
		}
		for _, m := range state.Members {
			if err := w.validateMember(m); err != nil {
				return nil, err
			}
		}
		return w, nil
	}
	if runtime == nil || runtime.RuntimeID() != state.RuntimeIdentity {
		return nil, errors.New("workspace runtime identity changed")
	}
	absent, err := workspaceRuntimeAbsent(ctx, runtime)
	if err != nil {
		return nil, err
	}
	if !absent && state.ProbeUncertain {
		if err := runtime.Remove(ctx); err != nil {
			return nil, err
		}
		absent = true
	}
	if absent {
		w.state.Stopped = true
		if err := w.save(); err != nil {
			return nil, err
		}
	} else if state.Stopped {
		return nil, errors.New("stopped workspace still has writers")
	}
	if w.state.AllocationReleasePending {
		if !absent {
			return nil, errors.New("allocation release still has writers")
		}
		if err := w.storage.Release(ctx, w.state.Session, w.state.Root); err != nil {
			return nil, err
		}
		w.state.StorageReleased = true
		w.state.AllocationReleasePending = false
		for i := range w.state.Members {
			w.state.Members[i].Result.RecoveryLocation = ""
		}
		if err := w.save(); err != nil {
			return nil, err
		}
	}
	if err := w.validateShared(ctx); err != nil {
		return nil, err
	}
	if absent {
		for i := range w.state.Members {
			if w.state.Members[i].ReleasePending {
				if err := w.releaseMember(ctx, i); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if err := runtime.Reconcile(ctx); err != nil {
			return nil, err
		}
		w.state.Unavailable = false
		if err := w.save(); err != nil {
			return nil, err
		}
	}

	return w, nil
}

func (w *LocalWorkspace) validateShared(ctx context.Context) error {
	if w.state.RuntimePreparing {
		return errors.New("workspace runtime binding is pending")
	}
	if w.authorize != nil {
		if err := w.authorize(ctx); err != nil {
			return err
		}
	}
	if err := w.validateAllocation(ctx); err != nil {
		return err
	}
	if w.runtime == nil || w.runtime.RuntimeID() == "" || w.runtime.RuntimeID() != w.state.RuntimeIdentity {
		return errors.New("workspace runtime identity changed or unavailable")
	}
	if w.state.Stopped {
		return w.runtime.Remove(ctx)
	}
	return w.runtime.Identity(ctx, w.state.Session, w.state.Launch.LaunchGeneration)
}

func (w *LocalWorkspace) validateAllocation(ctx context.Context) error {
	state, err := readPrivateWorkspace(w.record)
	if err != nil || !reflect.DeepEqual(state, w.state) {
		return errors.New("private workspace manifest changed")
	}
	if w.state.StorageReleased {
		if !w.state.Stopped {
			return errors.New("released live workspace")
		}
		return nil
	}
	if w.storage == nil {
		return errors.New("workspace quota authority unavailable")
	}
	if err := w.storage.Validate(ctx, w.state.Session, w.state.Root, protocol.DefaultWorkspaceBudget()); err != nil {
		return err
	}
	root, err := w.allocationIdentity(ctx)
	if err != nil || root != w.state.RootIdentity {
		return errors.New("workspace root replaced")
	}
	if len(w.state.Members) > len(w.state.Launch.Members) || (!w.state.Preparing && len(w.state.Members) != len(w.state.Launch.Members)) {
		return errors.New("workspace preparation incomplete")
	}
	for i, m := range w.state.Members {
		if !reflect.DeepEqual(m.Member, w.state.Launch.Members[i]) || m.Clone != filepath.Join(w.state.Root, filepath.FromSlash(m.Member.Path)) || m.Source.Repo == "" || m.Result.MemberID != m.Member.ID || m.Result.LaunchGeneration != w.state.Launch.LaunchGeneration || m.Result.Branch != sandboxBranch(w.state.Session) {
			return errors.New("private member binding mismatch")
		}
	}
	return nil
}

func (w *LocalWorkspace) validateMember(m workspacePrivateMember) error {
	if _, remote := w.storage.(workspaceRemoteStorage); remote {
		for _, pin := range []struct {
			path     string
			identity protocol.WorkspaceRoot
		}{{m.Source.Repo, m.SourceIdentity}, {m.Source.Objects, m.SourceObjectsIdentity}, {m.SourceCommon, m.SourceCommonIdentity}} {
			actual, err := workspaceDirectoryIdentity(pin.path)
			if err != nil || actual != pin.identity {
				return errors.New("source identity changed")
			}
		}
		return nil
	}

	for _, pin := range []struct {
		path     string
		identity protocol.WorkspaceRoot
	}{
		{m.Clone, m.CloneIdentity}, {filepath.Join(m.Clone, ".git"), m.GitIdentity},
		{filepath.Join(m.Clone, ".git", "objects"), m.ObjectsIdentity}, {m.Source.Repo, m.SourceIdentity}, {m.Source.Objects, m.SourceObjectsIdentity}, {m.SourceCommon, m.SourceCommonIdentity},
	} {
		id, err := workspaceDirectoryIdentity(pin.path)
		if err != nil || id != pin.identity {
			return errors.New("workspace member directory replaced or unavailable")
		}
	}
	return nil
}

// BindRuntime runs only after all clones exist and before any agent/shell starts.
// It returns the runtime root identity for the P0b generation-fenced binding.
func (w *LocalWorkspace) BindRuntime(ctx context.Context, runtime WorkspaceContainer) (protocol.WorkspaceRoot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bindRuntime(ctx, runtime)
}

func (w *LocalWorkspace) bindRuntime(ctx context.Context, runtime WorkspaceContainer) (protocol.WorkspaceRoot, error) {
	if w.runtime != nil || w.state.RuntimeIdentity != "" || w.state.Stopped || w.state.Unavailable || w.state.Preparing || runtime == nil || runtime.RuntimeID() == "" {
		return protocol.WorkspaceRoot{}, errors.New("workspace cannot bind runtime")
	}
	if err := w.validateAllocation(ctx); err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	if err := runtime.Identity(ctx, w.state.Session, w.state.Launch.LaunchGeneration); err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	root, err := runtime.RootIdentity(ctx)
	if err != nil || root.Inode == 0 {
		return protocol.WorkspaceRoot{}, errors.New("runtime root identity unavailable")
	}
	w.runtime = runtime
	w.state.RuntimeRoot = root
	w.state.RuntimeIdentity = runtime.RuntimeID()
	workspaceCrashBoundary("before-runtime-root-save")
	if err := w.save(); err != nil {
		w.state.Unavailable = true
		return protocol.WorkspaceRoot{}, err
	}
	workspaceCrashBoundary("after-runtime-root-save")
	return w.state.RuntimeRoot, nil
}

// Sync runs only at a turn boundary. Head probes finish before Freeze, and no
// runtime exec is issued while frozen. A single deferred cleanup owns Thaw.
func (w *LocalWorkspace) Sync(ctx context.Context) (results []protocol.WorkspaceMemberResult, err error) {
	if w.host != nil {
		return w.host.sync(ctx)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() { results = w.results() }()
	if w.state.Stopped || w.state.Unavailable {
		return nil, errors.New("workspace unavailable")
	}
	if err := w.validateShared(ctx); err != nil {
		return nil, err
	}
	if err := w.runtime.QuiesceStatus(ctx); err != nil {
		return nil, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxDockerTimeout)
		defer cancel()
		if !w.state.Unavailable && !w.state.StopRequested {
			if d, ok := w.runtime.(*DockerWorkspaceContainer); ok && d.ResumeStatus != nil {
				if checkErr := w.validateShared(ctx); checkErr != nil {
					w.state.Unavailable = true
					err = errors.Join(err, checkErr, w.save())
					return
				}
				if root, rootErr := d.RootIdentity(ctx); rootErr != nil || root != w.state.RuntimeRoot {
					w.state.Unavailable = true
					err = errors.Join(err, errors.New("workspace root changed before reader resume"), w.save())
					return
				}
				if resumeErr := d.ResumeStatus(ctx); resumeErr != nil {
					w.state.Unavailable = true
					err = errors.Join(err, resumeErr, w.save())
				}
			}
		}
	}()
	changed := false
	for i := range w.state.Members {
		m := &w.state.Members[i]
		if memberErr := w.validateMember(*m); memberErr != nil {
			m.Result.SyncError = memberErr.Error()
			m.Result.Pending = true
			continue
		}
		head, headErr := w.runtime.Head(ctx, m.Member.Path)
		var uncertain *workspaceWorkerUncertain
		var probe *workspaceProbeUncertain
		if errors.As(headErr, &probe) {
			w.state.ProbeUncertain = true
		}
		if errors.As(headErr, &uncertain) || probe != nil {
			w.state.Unavailable = true
			return nil, errors.Join(headErr, w.save())
		}
		if headErr != nil || (head != "" && !protocol.ValidSessionOriginCommit(head)) {
			m.Result.SyncError = "runtime head unavailable"
			m.Result.Pending = true
			continue
		}
		m.Result.Head = head
		m.Result.SyncError = ""
		if head != m.LastSynced {
			changed = true
			m.Result.Pending = true
		} else {
			m.Result.Pending = false
		}
	}
	if !changed {
		return nil, w.save()
	}
	// No pre-freeze observation establishes that a member is saved in this round.
	// A different member can commit between its probe and the session freeze.
	for i := range w.state.Members {
		w.state.Members[i].Result.Pending = true
		w.state.Members[i].Result.SyncError = "copy round has not confirmed this member"
	}
	if err := w.save(); err != nil {
		return nil, err
	}
	if err := w.beginWorker(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds)*time.Second)
	defer cancel()
	w.runtime.SyncSignal(workspaceSyncNotice)
	defer w.runtime.SyncSignal("")
	// An unsuccessful pause can still have taken effect: always try thaw once.
	defer func() {
		var uncertain *workspaceWorkerUncertain
		if errors.As(err, &uncertain) {
			w.state.Unavailable = true
			err = errors.Join(err, w.save())
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxDockerTimeout)
		defer cancel()
		if thawErr := w.runtime.Thaw(cleanup); thawErr != nil {
			w.state.Unavailable = true
			err = errors.Join(err, fmt.Errorf("workspace thaw failed; retained for recovery: %w", thawErr))
		}
		err = errors.Join(err, w.save())
	}()
	if err := w.runtime.Freeze(ctx); err != nil {
		w.state.Unavailable = true
		return nil, fmt.Errorf("workspace freeze failed; no members fetched: %w", err)
	}
	deadline, _ := ctx.Deadline()
	copyCtx, copyCancel := context.WithDeadline(ctx, deadline.Add(-sandboxDockerTimeout-workspaceWorkerCleanup))
	defer copyCancel()
	response, copyErr := w.runWorker(copyCtx, workspaceWorkerRequest{Action: "copy", State: w.state, Record: w.record})
	if response.State.Session == w.state.Session {
		w.state = response.State
	}
	if copyErr != nil {
		return nil, copyErr
	}

	return nil, nil
}

func (w *LocalWorkspace) results() []protocol.WorkspaceMemberResult {
	results := make([]protocol.WorkspaceMemberResult, len(w.state.Members))
	for i, m := range w.state.Members {
		results[i] = m.Result
	}
	return results
}

func (w *LocalWorkspace) Results() []protocol.WorkspaceMemberResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.results()
}

// Stop proves supervisor cleanup and removes the entire runtime (all writers),
// then settles and fetches members in a separate capped helper. Unknown or refused work
// retains the enforced allocation. Never free storage after uncertain removal.
func (w *LocalWorkspace) Stop(ctx context.Context) (results []protocol.WorkspaceMemberResult, err error) {
	ctx = cleanupContext(ctx)
	if w.host != nil {
		return w.host.stop(ctx)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() { results = w.results() }()
	if err := w.validateShared(ctx); err != nil {
		return nil, err
	}
	w.state.StopRequested = true
	if err := w.save(); err != nil {
		return nil, err
	}
	if w.state.StorageReleased {
		return nil, nil
	}
	if err := w.runtime.QuiesceStatus(ctx); err != nil {
		return nil, err
	}
	if !w.state.Stopped {
		if err := w.runtime.StopWriters(ctx); err != nil {
			w.state.Unavailable = true
			return nil, errors.Join(err, w.save())
		}
	}
	clean := make([]bool, len(w.state.Members))
	for i, m := range w.state.Members {
		if m.Released || w.state.Stopped || w.validateMember(m) != nil {
			continue
		}
		settled, settleErr := w.runtime.Settle(ctx, m.Member.Path, w.state.Session)
		clean[i] = settled && settleErr == nil
		w.state.Members[i].SettledClean = clean[i]
	}
	w.state.RemovalPending = true
	if err := w.save(); err != nil {
		return nil, err
	}
	if err := w.runtime.Remove(ctx); err != nil {
		w.state.Unavailable = true
		return nil, errors.Join(err, w.save())
	}
	workspaceCrashBoundary("after-remove")
	w.state.Stopped = true
	w.state.RemovalPending = false
	if err := w.save(); err != nil {
		return nil, err
	}
	for i := range clean {
		clean[i] = w.state.Members[i].SettledClean
	}
	allClean := true
	deadline := time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second)
	copyCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for i := range w.state.Members {
		if !w.state.Members[i].Released {
			w.state.Members[i].Result.Pending = true
		}
	}
	if err := w.save(); err != nil {
		return nil, err
	}
	if err := w.beginWorker(ctx); err != nil {
		return nil, err
	}
	response, copyErr := w.runWorker(copyCtx, workspaceWorkerRequest{Action: "copy", State: w.state, Record: w.record})
	if response.State.Session == w.state.Session {
		w.state = response.State
	}
	if copyErr != nil {
		return nil, errors.Join(copyErr, w.save())
	}
	for i := range w.state.Members {
		m := &w.state.Members[i]
		if m.Released {
			continue
		}
		if m.Result.Pending || m.Result.SyncError != "" || !m.SettledClean || m.SavedTreeHead == "" || m.SavedTreeHead != m.Result.Head {
			allClean = false
			continue
		}
		if m.Member.Path != "." {
			m.ReleasePending = true
			if err := w.save(); err != nil {
				return nil, err
			}
			if err := w.releaseMember(ctx, i); err != nil {
				allClean = false
				m.Result.SyncError = err.Error()
			}
		}
	}

	if err := w.save(); err != nil {
		return nil, err
	}
	if !allClean {
		return nil, nil
	}
	w.state.AllocationReleasePending = true
	if err := w.save(); err != nil {
		return nil, err
	}
	if err := w.storage.Release(ctx, w.state.Session, w.state.Root); err != nil {
		return nil, err
	}
	workspaceCrashBoundary("after-allocation-release")
	w.state.StorageReleased = true
	w.state.AllocationReleasePending = false
	for i := range w.state.Members {
		w.state.Members[i].Result.RecoveryLocation = ""
	}
	return nil, w.save()
}

// PrepareLocalWorkspaceChild preserves parent membership/authorization and gives
// each selected member the child's own wip branch. Caller still checks existing
// child authorization and approval; shell grants are never accepted here.
func PrepareLocalWorkspaceChild(ctx context.Context, session string, parent, child protocol.WorkspaceManifest, sources []WorkspaceSource, storage WorkspaceStorage, before ...func(context.Context, *LocalWorkspace) error) (*LocalWorkspace, error) {
	if err := protocol.ValidateWorkspaceChild(parent, child); err != nil {
		return nil, err
	}
	return PrepareLocalWorkspace(ctx, session, child, sources, storage, before...)
}

// A MAC binds recovery metadata to daemon-private launch state. A changed
// source, base, mount, identity or result cannot be accepted after restart.
type workspaceEnvelope struct {
	State json.RawMessage `json:"state"`
	MAC   string          `json:"mac"`
}

func workspaceManifestKey() ([]byte, error) {
	root := sandboxCloneRoot()
	if root == "" {
		return nil, errors.New("workspace private state unavailable")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	name := filepath.Join(root, "workspace-integrity.key")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // fixed name under daemon-private state; exclusive creation
	if err == nil {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			_ = f.Close()
			return nil, err
		}
		_, writeErr := f.Write(key)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return nil, err
		}
		return key, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	st, err := os.Lstat(name)
	if err != nil || !safeSandboxGitInfo(st, false) || st.Mode().Perm() != 0o600 || st.Size() != 32 {
		return nil, errors.New("workspace integrity authority unavailable")
	}
	key, err := os.ReadFile(name) //nolint:gosec // daemon-private integrity key, validated ownership/mode/size
	if err != nil || len(key) != 32 {
		return nil, errors.New("workspace integrity authority unreadable")
	}
	return key, nil
}

// Walk only private clone metadata while all writers are frozen. This is one
// aggregate budget; invalid member trees are refused individually by the fetch
// gate. Symlinks are never followed. Quotas enforce checkout bytes/entries even
// while the session writes between these bounded copy operations.
func (w *LocalWorkspace) checkAggregateGitBudget(ctx context.Context) error {
	budget := protocol.DefaultWorkspaceBudget()
	var objects, metadata int64
	var entries int
	exhausted := errors.New("aggregate workspace Git budget exceeded")
	for _, m := range w.state.Members {
		if w.validateMember(m) != nil {
			continue
		}
		git := filepath.Join(m.Clone, ".git")
		objectPrefix := filepath.Join(git, "objects") + string(filepath.Separator)
		err := filepath.WalkDir(git, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			entries++
			if entries > budget.MetadataEntries {
				return exhausted
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if strings.HasPrefix(p, objectPrefix) {
				objects += info.Size()
			} else {
				metadata += info.Size()
			}
			if !(&workspaceSessionLedger{}).fits(sessionGitUsage{metadata: metadata, objects: objects, entries: entries}) {
				return exhausted
			}
			return nil
		})
		if errors.Is(err, exhausted) || ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		// IO/refusal in one member is handled by that member's hardened fetch gate.

	}
	return nil
}

// WithRuntimeMember is the P4 adapter's common lifecycle gate. Bounded runtime
// status work must enter here so a new worker cannot start after QuiesceStatus
// and race Freeze/Stop. The callback runs only for a current launch member and
// receives a runtime-relative path, never a host worktree for status parsing.
func (w *LocalWorkspace) WithRuntimeMember(ctx context.Context, ref protocol.WorkspaceMemberRef, operation func(context.Context, WorkspaceContainer, string) error) error {
	if err := workspaceReadLock(ctx, w.mu); err != nil {
		return err
	}
	defer w.mu.Unlock()
	if w.state.Binding != nil {
		return errors.New("runtime generation required")
	}
	return w.withRuntimeMember(ctx, ref, operation)
}
func (w *LocalWorkspace) withRuntimeMember(ctx context.Context, ref protocol.WorkspaceMemberRef, operation func(context.Context, WorkspaceContainer, string) error) error {
	if w.state.Stopped || w.state.Unavailable || ref.LaunchGeneration != w.state.Launch.LaunchGeneration || operation == nil {
		return errors.New("workspace member operation unavailable")
	}
	if err := w.validateShared(ctx); err != nil {
		return err
	}
	for _, m := range w.state.Members {
		if m.Member.ID != ref.MemberID {
			continue
		}
		if err := w.validateMember(m); err != nil {
			return err
		}
		return operation(ctx, w.runtime, m.Member.Path)
	}
	return errors.New("unknown workspace member")
}

func (w *LocalWorkspace) resumePreparation(ctx context.Context) error {
	commonSeen := map[string]bool{}
	for _, m := range w.state.Members {
		commonSeen[m.SourceCommon] = true
	}
	for _, source := range w.state.Sources[len(w.state.Members):] {
		if err := ctx.Err(); err != nil {
			return err
		}
		clone := filepath.Join(w.state.Root, filepath.FromSlash(source.Member.Path))
		_, remote := w.storage.(workspaceRemoteStorage)
		if err := func() error {
			if remote {
				return nil
			}
			return os.MkdirAll(filepath.Dir(clone), 0700)
		}(); err != nil {
			return err
		}
		if err := w.beginWorker(ctx); err != nil {
			return err
		}
		response, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "prepare", State: w.state, Source: source, Clone: clone})
		if err != nil {
			return err
		}
		workspaceCrashBoundary("after-clone")
		m := response.Member
		m.WorkerPins = workerPins(m)
		if err := func() error {
			if !remote {
				return pinWorkspaceMember(&m)
			}
			var err error
			m.SourceIdentity, err = workspaceDirectoryIdentity(m.Source.Repo)
			if err != nil {
				return err
			}
			m.SourceObjectsIdentity, err = workspaceDirectoryIdentity(m.Source.Objects)
			if err != nil {
				return err
			}
			m.SourceCommonIdentity, err = workspaceDirectoryIdentity(m.SourceCommon)
			return err
		}(); err != nil {
			return err
		}
		if commonSeen[m.SourceCommon] || m.SourceIdentity != source.RootIdentity || m.SourceCommonIdentity != source.CommonIdentity {
			return errors.New("source/common identity changed or duplicated")
		}
		commonSeen[m.SourceCommon] = true
		m.Result = protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: source.Member.ID, LaunchGeneration: w.state.Launch.LaunchGeneration}, Base: m.Source.Base, Head: m.Source.Base, Branch: sandboxBranch(w.state.Session), Pending: true, RecoveryLocation: clone}
		w.state.WorkerRoot = response.State.WorkerRoot
		w.state.Members = append(w.state.Members, m)
		if err := w.save(); err != nil {
			return err
		}
	}

	w.state.Preparing = false
	return w.save()
}

func (w *LocalWorkspace) releaseMember(ctx context.Context, index int) error {
	m := &w.state.Members[index]
	if !w.state.Stopped || !m.ReleasePending || !m.SettledClean || m.SavedTreeHead == "" || m.SavedTreeHead != m.Result.Head || m.Result.Pending || m.Result.SyncError != "" {
		return errors.New("member release lacks durable saved/clean proof")
	}
	if _, remote := w.storage.(workspaceRemoteStorage); remote {
		if err := w.beginWorker(ctx); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "release-member", State: w.state, Record: w.record, Clone: m.Clone}); err != nil {
			return err
		}
		workspaceCrashBoundary("after-member-delete")
		m.Released = true
		m.ReleasePending = false
		m.Result.RecoveryLocation = ""
		return w.save()
	}
	root, err := workspaceDirectoryIdentity(w.state.Root)
	if err != nil || root != w.state.RootIdentity {
		return errors.New("release root replaced")
	}
	if _, err := os.Lstat(m.Clone); err == nil {
		if err := w.validateMember(*m); err != nil {
			return err
		}
		if err := os.RemoveAll(m.Clone); err != nil {
			return err
		}
		workspaceCrashBoundary("after-member-delete")
	} else if !os.IsNotExist(err) {
		return err
	}
	m.Released = true
	m.ReleasePending = false
	m.Result.RecoveryLocation = ""
	return w.save()
}

type workspaceAbsenceProof interface {
	Absent(context.Context) (bool, error)
}

func workspaceRuntimeAbsent(ctx context.Context, runtime WorkspaceContainer) (bool, error) {
	proof, ok := runtime.(workspaceAbsenceProof)
	if !ok {
		return false, errors.New("runtime absence proof unavailable")
	}
	return proof.Absent(ctx)
}

var workspaceCrashHook func(string)

func workspaceCrashBoundary(stage string) {
	if workspaceCrashHook != nil {
		workspaceCrashHook(stage)
	}
}

func (w *LocalWorkspace) allocationIdentity(ctx context.Context) (protocol.WorkspaceRoot, error) {
	if s, ok := w.storage.(workspaceRemoteStorage); ok {
		return s.Identity(ctx, w.state.Session, w.state.Root)
	}
	return workspaceDirectoryIdentity(w.state.Root)
}
