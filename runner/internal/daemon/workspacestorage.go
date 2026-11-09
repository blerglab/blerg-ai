package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Audit executes only in the capped worker, with no source mounts or agent
// processes. Hash working bytes and refs, not Git's index/stat-cache claims.
func workspaceStorageAudit(ctx context.Context, state workspacePrivateManifest) (*protocol.WorkspaceStoragePreview, error) {
	root, err := workspaceDirectoryIdentity(state.Root)
	if err != nil || root != state.WorkerRoot {
		return nil, errors.New("preview allocation replaced")
	}
	p := &protocol.WorkspaceStoragePreview{Files: []string{}, Commits: []string{}}
	sum := sha256.New()
	for _, m := range state.Members {
		if m.Released {
			continue
		}
		applyWorkerPins(&m)
		check := &LocalWorkspace{state: state, cloneOnly: true}
		check.state.RootIdentity = state.WorkerRoot
		if err := check.validateMember(m); err != nil {
			return nil, err
		}
		if err := checkSandboxCloneTreeContext(ctx, m.Clone); err != nil {
			return nil, err
		}
		head, ok := readCloneFile(m.Clone, "HEAD", 512)
		if !ok {
			return nil, errors.New("member HEAD unavailable")
		}
		_, _ = fmt.Fprintf(sum, "HEAD\x00%s\x00", head)
		if !strings.HasPrefix(head, "ref: ") && strings.TrimSpace(head) != m.LastSynced && strings.TrimSpace(head) != m.Source.Base {
			p.Commits = append(p.Commits, m.Member.Name+": detached HEAD "+strings.TrimSpace(head))
		}
		refs, err := workspaceCloneRefs(ctx, m.Clone)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(refs))
		for name := range refs {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", m.Member.ID, name, refs[name])
			if name == "refs/heads/"+(&LocalWorkspace{state: state}).branch() && refs[name] == m.LastSynced {
				continue
			}
			if refs[name] != m.Source.Base && strings.HasPrefix(name, "refs/") {
				p.Commits = append(p.Commits, m.Member.Name+": "+name+" "+refs[name])
			}
		}
		listed, err := cloneGit(ctx, m.Clone, "ls-files", "--others", "--directory", "--no-empty-directory", "-z")
		if err != nil {
			return nil, err
		}
		modified, err := cloneGit(ctx, m.Clone, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--name-only", "-z")
		if err != nil {
			return nil, err
		}
		flags, err := cloneGit(ctx, m.Clone, "ls-files", "-v", "-z")
		if err != nil {
			return nil, err
		}
		for _, entry := range strings.Split(flags, "\x00") {
			if len(entry) > 2 && entry[:2] != "H " {
				p.Files = append(p.Files, filepath.ToSlash(filepath.Join(m.Member.Path, entry[2:])))
			}
		}
		for _, name := range strings.Split(listed+"\x00"+modified, "\x00") {
			if name != "" {
				display := filepath.ToSlash(filepath.Join(m.Member.Path, name))
				if strings.HasSuffix(name, "/") {
					display += "/"
				}
				p.Files = append(p.Files, display)
			}
		}
		tree, err := os.OpenRoot(m.Clone)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(m.Clone, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(m.Clone, path)
			if err != nil {
				return err
			}
			if rel == ".git" {
				return filepath.SkipDir
			}
			if m.Member.Path == "." && d.IsDir() && slices.Contains(reservedPaths(state), rel) {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%o\x00%d\x00", m.Member.ID, rel, info.Mode(), info.Size())
			if info.Mode().IsRegular() {
				f, err := tree.Open(rel)
				if err != nil {
					return err
				}
				_, err = io.Copy(sum, f)
				return errors.Join(err, f.Close())
			}
			if info.Mode()&os.ModeSymlink != 0 {
				link, err := tree.Readlink(rel)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprint(sum, link)
			}
			return nil
		})
		err = errors.Join(err, tree.Close())
		if err != nil {
			return nil, err
		}
	}

	// Folder workspaces also contain files outside repositories. They are not
	// saved by any member fetch, so include every such path in the loss report.
	if !slices.ContainsFunc(state.Members, func(m workspacePrivateMember) bool { return m.Member.Path == "." }) {
		tree, err := os.OpenRoot(state.Root)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(state.Root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, m := range state.Members {
				if path == m.Clone && d.IsDir() {
					return filepath.SkipDir
				}
			}
			rel, err := filepath.Rel(state.Root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(sum, "outside\x00%s\x00%o\x00%d\x00", rel, info.Mode(), info.Size())
			if d.IsDir() {
				return nil
			}
			p.Files = append(p.Files, filepath.ToSlash(rel))
			if info.Mode().IsRegular() {
				f, err := tree.Open(rel)
				if err != nil {
					return err
				}
				_, err = io.Copy(sum, f)
				return errors.Join(err, f.Close())
			}
			if info.Mode()&os.ModeSymlink != 0 {
				value, err := tree.Readlink(rel)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprint(sum, value)
			}
			return nil
		})
		err = errors.Join(err, tree.Close())
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(p.Files)
	p.Files = slices.Compact(p.Files)
	boundWorkspaceStoragePreview(p)
	p.Digest = hex.EncodeToString(sum.Sum(nil))
	return p, nil
}

func (m *Manager) handleWorkspaceStorage(raw []byte) {
	var req protocol.WorkspaceStorageRequest
	if json.Unmarshal(raw, &req) != nil || !validWorkspaceSessionID(req.SessionID) || req.RequestID == "" {
		failurelog.Default.Event("storage:invalid_request", slog.LevelWarn, "component", "daemon", "operation", "release_storage", "cause", "invalid_request")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res := protocol.WorkspaceStorageResult{Type: "workspace_storage_result", RequestID: req.RequestID, SessionID: req.SessionID}
		p, err := m.config.Workspaces.storageOperation(ctx, req)
		if err != nil {
			res.Error = "Storage could not be released. The session must be stopped, with its completed cleanup acknowledged. Refresh and try again."
		} else {
			res.Preview = p
		}
		if err := m.client.Send(res); err != nil {
			failurelog.Default.Event("storage:reply_failed", slog.LevelWarn, "component", "daemon", "operation", "release_storage", "cause", "control_link_unavailable")
		}
	}()
}

func (s *LocalWorkspaceService) storageOperation(ctx context.Context, req protocol.WorkspaceStorageRequest) (preview *protocol.WorkspaceStoragePreview, resultErr error) {
	cause := "service_unavailable"
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("storage:"+cause, slog.LevelWarn, "component", "daemon", "operation", "release_storage", "cause", cause, "error_type", failurelog.Cause(resultErr))
		}
	}()
	if s == nil {
		return nil, errors.New("storage service unavailable")
	}
	s.recoverMu.Lock()
	defer s.recoverMu.Unlock()
	s.mu.Lock()
	hosted := s.sessions[req.SessionID]
	s.mu.Unlock()
	lock := workspaceLock(req.SessionID)
	if hosted != nil {
		lock = hosted.mu
	}
	lock.Lock()
	defer lock.Unlock()
	storage := s.Storage
	if storage == nil {
		storage = defaultWorkspaceStorage()
	}
	cause = "journal_unavailable"
	record := filepath.Join(sandboxCloneRoot(), req.SessionID+".workspace.json")
	if _, err := os.Lstat(record); os.IsNotExist(err) {
		// A pruned receipt means released only when no allocation journal remains.
		if remote, ok := storage.(*DockerWorkspaceStorage); ok {
			if _, err := remote.read(req.SessionID); !os.IsNotExist(err) {
				return nil, errors.New("workspace journal missing with allocation retained")
			}
		}
		if _, err := os.Lstat(filepath.Join(sandboxCloneRoot(), req.SessionID+".json")); !os.IsNotExist(err) {
			return nil, errors.New("legacy workspace retained")
		}
		return &protocol.WorkspaceStoragePreview{Released: true}, nil
	} else if err != nil {
		return nil, err
	}
	state, err := readPrivateWorkspace(record)
	if os.IsNotExist(err) {
		return &protocol.WorkspaceStoragePreview{Released: true}, nil
	}
	if err != nil {
		return nil, err
	}
	cause = "stopped_owner_or_acknowledgment_required"
	if state.Version != privateWorkspaceVersion || state.Launch.Runtime != "sandbox" || state.Session != req.SessionID || state.Launch.OwnerID != req.OwnerID || state.Host || !state.Stopped || !workspaceFinalComplete(state) || state.RuntimePreparing {
		return nil, errors.New("storage release not authorized")
	}
	w := &LocalWorkspace{state: state, record: record, storage: storage}
	defer func() {
		if hosted != nil {
			hosted.state = w.state
		}
	}()
	if w.state.ActiveWorker != "" {
		cause = "previous_worker_cleanup_unproved"
		if err := w.worker().Cleanup(ctx, state.ActiveWorker, state.WorkerContext); err != nil {
			return nil, err
		}
		w.state.ActiveWorker = ""
		if err := w.save(); err != nil {
			return nil, err
		}
	}
	if !state.StorageReleased {
		cause = "runtime_absence_unproved"
		if state.RuntimeIdentity == "" {
			return nil, errors.New("runtime identity unavailable")
		}
		dockerContext, id, ok := strings.Cut(state.RuntimeIdentity, ":")
		if !ok {
			return nil, errors.New("runtime identity invalid")
		}
		runtime := &DockerWorkspaceContainer{Context: dockerContext, ID: id, Session: state.Session, Launch: state.Launch.LaunchGeneration, Owner: sandboxOwner()}
		var absent bool
		if hosted != nil && hosted.runtime != nil {
			absent, err = workspaceRuntimeAbsent(ctx, hosted.runtime)
		} else {
			absent, err = workspaceRuntimeAbsent(ctx, runtime)
		}
		if err != nil || !absent {
			return nil, errors.New("runtime absence unproved")
		}
		if !state.AllocationReleasePending {
			cause = "allocation_identity_changed"
			if err := w.validateAllocation(ctx); err != nil {
				return nil, err
			}
			cause = "preview_worker_failed"
			if err := w.beginWorker(ctx); err != nil {
				return nil, err
			}
			response, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "storage-preview", State: state, Record: record})
			if err != nil {
				return nil, err
			}
			if err := w.save(); err != nil {
				return nil, err
			}
			if response.Storage == nil {
				return nil, errors.New("preview unavailable")
			}
			if req.Operation == "preview" {
				return response.Storage, nil
			}
			cause = "confirmation_missing_or_stale"
			if req.Operation != "release" || req.Confirm == "" || req.Confirm != response.Storage.Digest {
				return nil, errors.New("confirmation missing or stale")
			}
			w.state.StorageReleasePreview = response.Storage
			w.state.StorageConfirmation = req.Confirm
			w.state.AllocationReleasePending = true
			if err := w.save(); err != nil {
				return nil, err
			}
		} else {
			pending := state.StorageReleasePreview
			if pending == nil {
				digest := sha256.Sum256([]byte(state.Session + "\x00" + state.RuntimeIdentity + "\x00" + state.TerminalReceipt))
				pending = &protocol.WorkspaceStoragePreview{Digest: hex.EncodeToString(digest[:]), Files: []string{}, Commits: []string{}}
			}
			if req.Operation == "preview" {
				return pending, nil
			}
			cause = "release_retry_not_confirmed"
			if req.Operation != "release" || req.Confirm == "" || req.Confirm != pending.Digest {
				return nil, errors.New("release retry not confirmed")
			}
			w.state.StorageConfirmation = req.Confirm
			w.state.StorageReleasePreview = pending
			if err := w.save(); err != nil {
				return nil, err
			}
		}
		cause = "allocation_release_failed"
		if err := storage.Release(ctx, state.Session, state.Root); err != nil {
			return nil, err
		}
		w.state.StorageReleased = true
		w.state.AllocationReleasePending = false
		if err := w.save(); err != nil {
			return nil, err
		}
	}
	cause = "record_pruning_failed"
	if req.Operation == "acknowledge" {
		w.state.StorageAcknowledged = true
		if err := w.save(); err != nil {
			return nil, err
		}
		if err := pruneWorkspaceStorage(w, storage); err != nil {
			return nil, err
		}
	}
	return &protocol.WorkspaceStoragePreview{Released: true}, nil
}

// The release receipt survives until the server acknowledges it. Remove the
// allocation journal first, then the workspace journal; either step is retryable.
func pruneWorkspaceStorage(w *LocalWorkspace, storage WorkspaceStorage) (resultErr error) {
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("storage:prune_refused", slog.LevelWarn, "component", "daemon", "operation", "release_storage", "cause", "prune_refused", "error_type", failurelog.Cause(resultErr))
		}
	}()

	if !validWorkspaceSessionID(w.state.Session) || filepath.Base(w.record) != w.state.Session+".workspace.json" {
		return errors.New("workspace journal filename mismatch")
	}
	if !w.state.StorageReleased || !workspaceFinalComplete(w.state) || (w.state.StorageConfirmation != "" && !w.state.StorageAcknowledged) {
		return errors.New("release not acknowledged")
	}
	if _, err := os.Lstat(w.record); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	current, err := readPrivateWorkspace(w.record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !reflect.DeepEqual(current, w.state) {
		return errors.New("release journal changed")
	}
	if remote, ok := storage.(*DockerWorkspaceStorage); ok {
		r, err := remote.read(w.state.Session)
		if !os.IsNotExist(err) {
			if err != nil {
				return err
			}
			if !r.Released || r.logical() != w.state.Root {
				return errors.New("allocation release incomplete")
			}
			path, err := remote.recordPath(w.state.Session)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		path, err := remote.recordPath(w.state.Session)
		if err != nil {
			return err
		}
		if err := os.Remove(filepath.Dir(path)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	err = os.Remove(w.record)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Counts describe the full inventory. Only presentation is bounded: the digest
// above is calculated from all working bytes and references, including omissions.
func boundWorkspaceStoragePreview(p *protocol.WorkspaceStoragePreview) {
	p.FileCount, p.CommitCount = len(p.Files), len(p.Commits)
	// Reserve half for unsaved commits before allocating file-list space.
	// Files may use unused commit space, but cannot consume the reservation.
	bound := func(names []string, budget int) ([]string, int) {
		count := 0
		for _, name := range names {
			encoded, _ := json.Marshal(name)
			if count == 500 || len(encoded)+1 > budget {
				break
			}
			budget -= len(encoded) + 1
			count++
		}
		return names[:count], budget
	}
	var unused int
	p.Commits, unused = bound(p.Commits, 48<<10)
	p.Files, _ = bound(p.Files, (48<<10)+unused)
	p.Truncated = len(p.Files) != p.FileCount || len(p.Commits) != p.CommitCount
}
