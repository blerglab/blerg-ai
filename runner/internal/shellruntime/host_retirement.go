package shellruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Retirement grants closure only, never adoption or a cleanup receipt. The
// authenticated daemon owns this workspace on this installation. Mutable
// manifest digests and connection nonces cannot veto its terminal intent.
type workspaceRetirement struct {
	Version       int
	Workspace, ID string
}
type hostObservation struct {
	Workspace        string
	Open, Privileged bool
}

func retirementName(workspace string) string {
	h := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(h[:]) + ".json"
}
func retirementPrivate(path string, directory bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	uid, ok := st.Sys().(*syscall.Stat_t)
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if !ok || uint64(uid.Uid) != uint64(os.Getuid()) || st.Mode().Perm() != mode || st.IsDir() != directory || st.Mode()&os.ModeSymlink != 0 || !directory && !st.Mode().IsRegular() { //nolint:gosec // Linux/macOS getuid returns a nonnegative kernel uid.
		return ErrClosed
	}
	return nil
}

// ConfigureHostRetirements is daemon state, deliberately outside keeper keys.
// Load it before publishing a host bridge, including when the keeper is down.
func (s *Service) ConfigureHostRetirements(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.loadHostRetirements(dir)
	s.hostRetirementDir = dir
	if err != nil {
		return bridgeFailure("retirement_load", err)
	}
	return nil
}
func (s *Service) loadHostRetirements(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := retirementPrivate(dir, true); err != nil {
		st, statErr := os.Lstat(dir)
		if statErr != nil {
			return statErr
		}
		uid, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || uint64(uid.Uid) != uint64(os.Getuid()) { //nolint:gosec // Kernel uid is nonnegative.
			return err
		}
		if repairErr := os.Chmod(dir, 0700); repairErr != nil { //nolint:gosec // Private directory requires owner traversal as well as read/write.
			return repairErr
		}
		_ = bridgeFailure("retirement_directory_repaired", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".retire-") || strings.HasPrefix(entry.Name(), ".quarantine-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		record, err := readRetirement(path, entry.Name())
		if err != nil {
			id, nonceErr := newHelperID()
			if nonceErr != nil {
				_ = bridgeFailure("retirement_quarantine_nonce", nonceErr)
				continue
			}
			if moveErr := os.Rename(path, filepath.Join(dir, ".quarantine-"+id)); moveErr != nil {
				_ = bridgeFailure("retirement_quarantine_failed", moveErr)
			} else {
				_ = bridgeFailure("retirement_quarantined", err)
				if syncErr := syncRetirementDir(dir); syncErr != nil {
					_ = bridgeFailure("retirement_quarantine_sync", syncErr)
				}
			}
			continue
		}
		s.hostIntents[record.Workspace] = record.ID
	}
	return nil
}
func readRetirement(path, name string) (workspaceRetirement, error) {
	var record workspaceRetirement
	if err := retirementPrivate(path, false); err != nil {
		return record, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // Private validated journal directory; no-follow record descriptors.
	if err != nil {
		return record, err
	}
	decoder := json.NewDecoder(io.LimitReader(f, 8193))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&record)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = ErrClosed
	}
	_ = f.Close()
	if err != nil {
		return record, err
	}
	if record.Version != 1 || record.Workspace == "" || len(record.Workspace) > 1024 || len(record.ID) != 64 || name != retirementName(record.Workspace) {
		return record, ErrClosed
	}
	return record, nil
}
func (s *Service) writeRetirement(record workspaceRetirement) error {
	if s.hostRetirementDir == "" {
		return ErrClosed
	}
	if err := retirementPrivate(s.hostRetirementDir, true); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.hostRetirementDir, ".retire-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(record)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.hostRetirementDir, retirementName(record.Workspace))); err != nil {
		return err
	}
	return syncRetirementDir(s.hostRetirementDir)
}
func syncRetirementDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // Validated private daemon journal directory, synced after mutation.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// BeginWorkspaceRetirement must precede writer/status/reader side effects.
func (s *Service) BeginWorkspaceRetirement(workspace string) error {
	if workspace == "" || len(workspace) > 1024 {
		return bridgeFailure("retirement_workspace", ErrClosed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostUnpersisted == nil {
		s.hostUnpersisted = map[string]bool{}
	}
	if _, exists := s.hostIntents[workspace]; !exists {
		id, err := newHelperID()
		if err != nil {
			_ = bridgeFailure("retirement_nonce", err)
			id = strings.TrimSuffix(retirementName(workspace), ".json")
		}
		record := workspaceRetirement{1, workspace, id}
		if err := s.writeRetirement(record); err != nil {
			_ = bridgeFailure("retirement_write_memory_only", err)
			s.hostUnpersisted[workspace] = true
		}
		s.hostIntents[workspace] = id
	} else if s.hostUnpersisted[workspace] {
		s.persistRetirementLocked(workspace, s.hostIntents[workspace])
	}
	if e := s.launches[workspace]; e != nil {
		e.mu.Lock()
		e.fenced, e.terminal = true, true
		e.phase = "stopping"
		e.mu.Unlock()
	}
	delete(s.hostJobs, workspace)
	s.wakeHostWorkLocked()
	return nil
}
func (s *Service) persistRetirementLocked(workspace, id string) {
	if !s.hostUnpersisted[workspace] {
		return
	}
	if err := s.writeRetirement(workspaceRetirement{1, workspace, id}); err != nil {
		_ = bridgeFailure("retirement_persist_retry", err)
		return
	}
	delete(s.hostUnpersisted, workspace)
}

func (s *Service) RetireWorkspace(ctx context.Context, l protocol.SupervisorLaunch) error {
	if !s.bridged(l.SessionID) {
		proof, err := s.Retire(ctx, l)
		if err != nil {
			return err
		}
		return proof.ValidateFor(l, "")
	}
	if err := s.BeginWorkspaceRetirement(l.SessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.launches[l.SessionID]; e != nil {
		e.retiring = true
		delete(s.launches, l.SessionID)
	}
	delete(s.hostJobs, l.SessionID)
	s.signalHostChangeLocked()
	return nil
}

func (s *Service) queueHostControl(workspace string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostRevision++
	s.hostJobs[workspace] = s.hostRevision
	s.signalHostChangeLocked()
	s.wakeHostWorkLocked()
}
func (s *Service) wakeHostWorkLocked() {
	select {
	case <-s.done:
		return
	default:
	}
	if s.hostRetirementWake == nil {
		s.hostRetirementWake = make(chan struct{}, 1)
	}
	select {
	case s.hostRetirementWake <- struct{}{}:
	default:
	}
	if !s.hostRetirementRunning {
		s.hostRetirementRunning = true
		s.workers.Add(1)
		go s.hostWork()
	}
}
func (s *Service) hostWork() {
	defer s.workers.Done()
	defer func() { s.mu.Lock(); s.hostRetirementRunning = false; s.mu.Unlock() }()
	backoff := 5 * time.Second
	for {
		if s.hostLife.Err() != nil {
			return
		}
		s.mu.Lock()
		intents := make(map[string]string, len(s.hostIntents))
		for workspace, id := range s.hostIntents {
			s.persistRetirementLocked(workspace, id)
			intents[workspace] = id
		}
		jobs := make(map[string]uint64, len(s.hostJobs))
		for workspace, rev := range s.hostJobs {
			jobs[workspace] = rev
		}
		s.mu.Unlock()
		if s.hostBridgeReady() {
			for workspace, id := range intents {
				s.applyHostRetirement(s.hostLife, workspace, id)
			}
			for workspace, rev := range jobs {
				s.applyHostControl(s.hostLife, workspace, rev)
			}
		}
		timer := time.NewTimer(backoff)
		select {
		case <-s.hostLife.Done():
			timer.Stop()
			return
		case <-s.hostRetirementWake:
			timer.Stop()
			backoff = 5 * time.Second
		case <-timer.C:
			backoff = nextRetirementBackoff(backoff)
		}
	}
}
func nextRetirementBackoff(previous time.Duration) time.Duration {
	return min(time.Minute, max(5*time.Second, previous*2))
}
func (s *Service) applyHostRetirement(ctx context.Context, workspace, id string) {
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.bridgeCall(call, "retire_workspace", workspaceRetirement{1, workspace, id}, nil); err != nil {
		_ = bridgeFailure("workspace_retirement_pending", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostIntents[workspace] != id {
		return
	}
	path := filepath.Join(s.hostRetirementDir, retirementName(workspace))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = bridgeFailure("retirement_remove", err)
		return
	}
	if err := syncRetirementDir(s.hostRetirementDir); err != nil {
		_ = bridgeFailure("retirement_sync", err)
		return
	}
	delete(s.hostIntents, workspace)
	delete(s.hostUnpersisted, workspace)
	s.signalHostChangeLocked()
	for e := range s.hostRetiring {
		if e.binding.Session == workspace {
			delete(s.hostRetiring, e)
		}
	}
}
func (s *Service) applyHostControl(ctx context.Context, workspace string, revision uint64) {
	s.mu.Lock()
	e := s.launches[workspace]
	pending := s.hostIntents[workspace] != ""
	if e == nil {
		if s.hostJobs[workspace] == revision {
			delete(s.hostJobs, workspace)
			s.signalHostChangeLocked()
		}
		s.mu.Unlock()
		return
	}
	if pending {
		s.mu.Unlock()
		return
	}
	r := hostRegistration{e.binding, e.command.Identity, e.command.Root, e.command.PublicRoot}
	e.mu.Lock()
	phase := e.phase
	terminal := e.terminal
	dirty := e.hostPhaseDirty
	e.mu.Unlock()
	s.mu.Unlock()
	if terminal {
		s.mu.Lock()
		if s.hostJobs[workspace] == revision {
			delete(s.hostJobs, workspace)
			s.signalHostChangeLocked()
		}
		s.mu.Unlock()
		return
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.reconcileWorkspaceRegistration(call, r, !dirty); err != nil {
		_ = bridgeFailure("workspace_control_registration", err)
		return
	}
	if !dirty {
		s.mu.Lock()
		if s.hostJobs[workspace] == revision {
			delete(s.hostJobs, workspace)
			s.signalHostChangeLocked()
		}
		s.mu.Unlock()
		return
	}
	var remote ownershell.Binding
	if err := s.bridgeCall(call, "binding", workspace, &remote); err != nil {
		_ = bridgeFailure("workspace_control_binding", err)
		return
	}
	if !sameWorkspaceBinding(r.Binding, remote) {
		_ = bridgeFailure("workspace_control_pins", ErrClosed)
		return
	}
	var reply RPC
	if err := s.bridgeCall(call, "call", RPC{Version: 1, SessionID: workspace, Op: "phase", Phase: phase, Launch: ownershell.SupervisorIdentity(remote, ownershell.SignedGrant{}).Launch}, &reply); err != nil || !reply.OK {
		if err == nil {
			err = ErrClosed
		}
		_ = bridgeFailure("workspace_control_phase", err)
		return
	}
	s.mu.Lock()
	if s.hostJobs[workspace] == revision {
		e.mu.Lock()
		e.hostPhaseDirty = false
		e.mu.Unlock()
		delete(s.hostJobs, workspace)
		s.signalHostChangeLocked()
	}
	s.mu.Unlock()
}

func (s *Service) signalHostChangeLocked() { close(s.hostChanged); s.hostChanged = make(chan struct{}) }

// Shell callers may await queued control; agent lifecycle callers never do.
func (s *Service) flushHostControl(ctx context.Context, workspace string) error {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.hostJobs[workspace] != 0 || s.hostIntents[workspace] != "" {
		s.wakeHostWorkLocked()
	}
	s.mu.Unlock()
	for {
		if !s.hostBridgeReady() {
			_ = bridgeFailure("control_unavailable", ErrKeeperUnavailable)
			return ErrKeeperUnavailable
		}
		s.mu.Lock()
		pending := s.hostJobs[workspace] != 0 || s.hostIntents[workspace] != ""
		changed := s.hostChanged
		s.mu.Unlock()
		if !pending {
			return nil
		}
		select {
		case <-call.Done():
			_ = bridgeFailure("control_pending", call.Err())
			return ErrKeeperUnavailable
		case <-s.hostLife.Done():
			return ErrKeeperUnavailable
		case <-changed:
		}
	}
}
