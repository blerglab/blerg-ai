package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
)

// WorkspaceExplorer is implemented at the runtime boundary. A sandbox adapter
// must send requests into the container, never construct a host LocalExplorer
// over its clone. P2/P3 own adapter lifetime and cancel it before freeze/stop.
type WorkspaceExplorer interface {
	Explore(context.Context, protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult
}

// LocalExplorer runs in the process that owns the public tree (host daemon or
// in-runtime helper). It holds a launch-bound descriptor for its whole lifetime.
// The caller must close it before releasing/replacing runtime storage.
type LocalExplorer struct {
	// Optional private test diagnostics. Never included in public explorer replies.
	statusDiagnostic workspaceStatusDiagnostic
	closeOnce        sync.Once
	closeErr         error
	ledger           *workspaceSessionLedger
	anchors          map[string]workspaceMemberAnchor
	root             *os.Root
	folder           string
	info             os.FileInfo
	manifest         protocol.WorkspaceManifest
	generation       int64
	key              [32]byte
	live             func() bool
	mu               sync.Mutex
	statusBusy       bool
	cache            map[string]explorerStatusCache
	gitPins          map[string]WorkspaceGitPin
	memberRoots      map[string]protocol.WorkspaceRoot
	gitRoots         map[string]protocol.WorkspaceRoot
	discoveries      map[string]explorerGitIdentity
	worktrees        map[string]hostWorktreePin
}
type workspaceMemberAnchor struct {
	root   *os.Root
	folder string
	info   os.FileInfo
}
type explorerGitIdentity struct{ root, git protocol.WorkspaceRoot }
type explorerStatusCache struct {
	result      protocol.WorkspaceExplorerResult
	until       time.Time
	fingerprint string
}

// WorkspaceGitPin is private runtime state, supplied by P2's clone identity
// validator. It authorizes only an exact .git and alternates file/target, never
// an agent-written gitdir pointer. It is not a public protocol DTO.
type WorkspaceGitPin struct {
	GitRoot    protocol.WorkspaceRoot `json:"git_root"`
	Alternates string                 `json:"alternates"`
	ObjectRoot protocol.WorkspaceRoot `json:"object_root"`
}

// NewLocalExplorerWithGitPins supplies the already-vetted private object pins
// for shared sandbox clones. The map is copied and cannot be changed by requests.
func NewLocalExplorerWithGitPins(folder string, manifest protocol.WorkspaceManifest, binding protocol.WorkspaceRuntimeBinding, live func() bool, pins map[string]WorkspaceGitPin) (*LocalExplorer, error) {
	e, err := NewLocalExplorer(folder, manifest, binding, live)
	if err != nil {
		return nil, err
	}
	e.gitPins = map[string]WorkspaceGitPin{}
	for id, pin := range pins {
		found := false
		for _, member := range manifest.Members {
			if member.ID == id {
				found = true
				break
			}
		}
		target := strings.TrimSuffix(pin.Alternates, "\n")
		if !found || pin.GitRoot.Inode == 0 || !filepath.IsAbs(target) || strings.ContainsAny(target, "\r\n\x00") || pin.ObjectRoot.Inode == 0 {
			_ = e.Close()
			return nil, os.ErrPermission
		}
		e.gitPins[id] = pin
	}
	return e, nil
}

// NewLocalExplorer consumes only the prepared runtime binding. Runtime launch
// code must call this in the owning process, never on a host sandbox clone.
func NewLocalExplorer(folder string, manifest protocol.WorkspaceManifest, binding protocol.WorkspaceRuntimeBinding, live func() bool) (*LocalExplorer, error) {
	if err := binding.Validate(manifest); err != nil {
		return nil, err
	}
	info, err := os.Lstat(folder) //nolint:gosec // trusted launch path; identity must match the authenticated binding before descriptor traversal
	if err != nil || !info.IsDir() || workspaceRootIdentity(info) != binding.Root {
		return nil, os.ErrPermission
	}
	return newLocalExplorer(folder, info, manifest, binding.Generation, live)
}

// newLocalExplorer is the descriptor implementation; production callers use
// NewLocalExplorer to validate the frozen runtime binding as well.
func newLocalExplorer(folder string, expected os.FileInfo, manifest protocol.WorkspaceManifest, runtimeGeneration int64, live func() bool) (*LocalExplorer, error) {
	if !workspacePublicRoot(folder) {
		return nil, os.ErrPermission
	}
	// Names are display metadata; unsafe names can legitimately use an ID path.
	// Filesystem access is confined by the public path and pinned physical root.
	for _, m := range manifest.Members {
		if !workspacePublicDirectoryPath(m.Path) {
			return nil, os.ErrPermission
		}
	}
	if expected == nil || !expected.IsDir() || runtimeGeneration <= 0 || manifest.LaunchGeneration == "" || live == nil || !live() {
		return nil, os.ErrPermission
	}
	named, err := os.Lstat(folder) //nolint:gosec // no-follow root identity check before opening the pinned descriptor
	if err != nil || !named.IsDir() || !os.SameFile(expected, named) {
		return nil, os.ErrPermission
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, err
	}
	if !workspaceRootUnchanged(root, folder, expected) {
		_ = root.Close()
		return nil, os.ErrPermission
	}
	// Probe the required backend now. Linux without openat2 is unavailable.
	probe, err := workspaceOpenDirectory(root, ".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	_ = probe.Close()
	manifest.Members = append([]protocol.WorkspaceMember(nil), manifest.Members...)
	e := &LocalExplorer{root: root, folder: folder, info: expected, manifest: manifest, generation: runtimeGeneration, live: live, cache: map[string]explorerStatusCache{}}
	e.pinMembers()
	if _, err := rand.Read(e.key[:]); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *LocalExplorer) Close() error {
	e.closeOnce.Do(func() {
		for _, anchor := range e.anchors {
			_ = anchor.root.Close()
		}
		for _, pin := range e.worktrees {
			_ = pin.git.Close()
			_ = pin.common.Close()
		}
		err := e.root.Close()
		if e.ledger != nil && !e.ledger.empty() {
			e.closeErr = errors.Join(err, errors.New("status snapshot cleanup unproven"))
			return
		}
		e.closeErr = err

	})
	return e.closeErr
}

func (e *LocalExplorer) valid() bool {
	return e.live() && workspacePublicRoot(e.folder) && workspaceRootUnchanged(e.root, e.folder, e.info)
}
func explorerPath(name string) (string, bool) {
	return workspacepolicy.ExplorerPath(name)
}

func explorerFailure(req protocol.WorkspaceExplorerRequest, code string) protocol.WorkspaceExplorerResult {
	return protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Operation: req.Operation, Path: req.Path, ErrorCode: code, Entries: []protocol.WorkspaceEntry{}}
}
func (e *LocalExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	res := explorerFailure(req, "unavailable")
	fail := func(stage string, err error) protocol.WorkspaceExplorerResult {
		e.diagnoseStatus(ctx, stage, err, "")
		return res
	}
	if req.LaunchGeneration != e.manifest.LaunchGeneration || req.Generation != e.generation || !e.valid() {
		return fail("runtime/root binding", os.ErrPermission)
	}
	name, ok := explorerPath(req.Path)
	if !e.memberAvailable(name) {
		return fail("prepared member unavailable", os.ErrPermission)
	}
	if !ok || len(req.Filter) > 256 || !utf8.ValidString(req.Filter) || len(req.Cursor) > 8192 {
		res.ErrorCode = "forbidden"
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch req.Operation {
	case "list":
		res = e.list(ctx, req, name)
	case "search":
		res = e.search(ctx, req, name)
	case "status":
		res = e.status(ctx, req, name)
	default:
		res.ErrorCode = "invalid"
	}
	if !e.valid() || !e.memberAvailable(name) {
		res = explorerFailure(req, "unavailable")
		return fail("delivery identity", os.ErrPermission)
	}
	if ctx.Err() != nil {
		if req.Operation != "search" || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			e.diagnoseStatus(ctx, "request deadline/cancellation", ctx.Err(), "")
			return explorerFailure(req, "cancelled")
		}
		if res.OK {
			res.Truncated = true
		}
	}
	raw, err := json.Marshal(res)
	if err != nil || len(raw) > protocol.WorkspaceExplorerReplyBytes {
		res = explorerFailure(req, "unavailable")
		return fail("reply envelope", err)
	}
	return res
}

type explorerCursor struct {
	Binding string
	After   string
	Expires int64
}

func (e *LocalExplorer) signCursor(c explorerCursor) string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, e.key[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(raw, mac.Sum(nil)...))
}
func (e *LocalExplorer) readCursor(token string, want explorerCursor) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < sha256.Size {
		return "", false
	}
	body, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, e.key[:])
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	var c explorerCursor
	if json.Unmarshal(body, &c) != nil || c.Expires < time.Now().Unix() || c.Binding != want.Binding {
		return "", false
	}
	return c.After, true
}

// readDirectory opens each child through the pinned root before obtaining
// metadata. ReadDir's d_type and paths assembled from directory names are never
// authority. Protected names are removed before Info/counts/serialization.
func (e *LocalExplorer) readDirectory(ctx context.Context, name string, budget *int) ([]protocol.WorkspaceEntry, string, bool, error) {
	return e.readDirectoryUsing(ctx, name, budget, workspaceOpenListing)
}

func (e *LocalExplorer) readDirectoryUsing(ctx context.Context, name string, budget *int, openListing func(*LocalExplorer, string) (*workspaceListing, error)) ([]protocol.WorkspaceEntry, string, bool, error) {
	if !e.memberAvailable(name) {
		return nil, "", false, os.ErrPermission
	}
	listing, err := openListing(e, name)
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = listing.close() }()
	dir := listing.file
	before, err := dir.Stat()
	if err != nil || !before.IsDir() {
		return nil, "", false, os.ErrPermission
	}
	var entries []protocol.WorkspaceEntry
	fingerprints := map[string]string{}
	truncated := false
	reportedOpenFailure := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", false, err
		}
		batch, readErr := dir.ReadDir(128)
		for _, ent := range batch {
			if *budget <= 0 {
				truncated = true
				break
			}
			*budget--
			child := path.Join(name, ent.Name())
			if !utf8.ValidString(ent.Name()) || !workspacePublicDirectoryPath(child) || (!ent.IsDir() && workspaceSecretFile(ent.Name())) {
				continue
			}
			// Unsafe/control-bearing names are valid on disk but cannot be passed to
			// the existing viewer. Do not normalize them to another file's name.
			if _, ok := explorerPath(child); !ok {
				continue
			}
			file, err := listing.openChild(ent.Name())
			if err != nil {
				if !reportedOpenFailure {
					e.diagnoseStatus(ctx, "list child open", err, "")
					reportedOpenFailure = true
				}
				continue
			}
			info, err := file.Stat()
			_ = file.Close()
			if err != nil || (!info.IsDir() && (!workspaceRegularFile(info) || workspaceSecretFile(ent.Name()))) {
				continue
			}
			row := protocol.WorkspaceEntry{Name: ent.Name(), Path: child, Directory: info.IsDir()}
			if !row.Directory {
				row.Size = info.Size()
			}
			e.decorateWorkspaceEntry(&row, info)
			fingerprints[row.Path] = workspaceMetadataFingerprint(info)
			entries = append(entries, row)
		}
		if readErr != nil && readErr != io.EOF {
			return nil, "", false, readErr
		}
		if truncated || readErr != nil {
			break
		}
	}
	if listing.validate != nil && !listing.validate() {
		return nil, "", false, errExplorerStale
	}
	after, err := dir.Stat()
	if err != nil || !os.SameFile(before, after) || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(after) {
		return nil, "", false, errExplorerStale
	}
	named, err := e.openDirectory(name)
	if err != nil {
		return nil, "", false, err
	}
	namedInfo, err := named.Stat()
	_ = named.Close()
	if err != nil || !os.SameFile(before, namedInfo) || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(namedInfo) {
		return nil, "", false, errExplorerStale
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	h := sha256.New()
	_, _ = h.Write([]byte(workspaceMetadataFingerprint(before)))
	for _, row := range entries {
		raw, _ := json.Marshal(row)
		_, _ = h.Write(raw)
		_, _ = h.Write([]byte(fingerprints[row.Path]))
	}
	return entries, base64.RawURLEncoding.EncodeToString(h.Sum(nil)), truncated, nil
}

// Browsing and tracked-only search publish the same membership and discovery badges.
func (e *LocalExplorer) decorateWorkspaceEntry(row *protocol.WorkspaceEntry, info os.FileInfo) {
	child := row.Path
	for _, member := range e.manifest.Members {
		if member.Path == child {
			row.MemberID = member.ID
			row.Repository = true
		}
	}
	// Discovery is a host-only badge. No member is appended to the manifest.
	if row.Directory && e.manifest.Runtime == "host" && !row.Repository {
		git, err := e.openDirectory(path.Join(child, ".git"))
		if err == nil {
			st, statErr := git.Stat()
			row.Repository = statErr == nil && st.IsDir()
			if row.Repository {
				e.mu.Lock()
				if e.discoveries == nil {
					e.discoveries = map[string]explorerGitIdentity{}
				}
				e.discoveries[child] = explorerGitIdentity{root: workspaceRootIdentity(info), git: workspaceRootIdentity(st)}
				e.mu.Unlock()
			}
			_ = git.Close()
		}
	}
}
func (e *LocalExplorer) list(ctx context.Context, req protocol.WorkspaceExplorerRequest, name string) protocol.WorkspaceExplorerResult {
	res := explorerFailure(req, "unavailable")
	budget := protocol.WorkspaceSearchVisited
	rows, fingerprint, truncated, err := e.readDirectory(ctx, name, &budget)
	if err != nil {
		e.diagnoseStatus(ctx, "list directory", err, "")
		if errors.Is(err, errExplorerStale) {
			res.ErrorCode = "stale"
		}
		return res
	}
	requestBinding, _ := json.Marshal(struct {
		Launch, Directory, Filter, Fingerprint string
		Runtime                                int64
	}{e.manifest.LaunchGeneration, name, req.Filter, fingerprint, e.generation})
	bindingHash := sha256.Sum256(requestBinding)
	cursor := explorerCursor{Binding: base64.RawURLEncoding.EncodeToString(bindingHash[:]), Expires: time.Now().Add(time.Minute).Unix()}
	after := ""
	if req.Cursor != "" {
		var ok bool
		after, ok = e.readCursor(req.Cursor, cursor)
		if !ok {
			res.ErrorCode = "stale"
			return res
		}
	}
	bytes := 0
	byteBudget := e.entryReplyBudget(req)
	for _, row := range rows {
		if row.Name <= after || !strings.Contains(strings.ToLower(row.Name), strings.ToLower(req.Filter)) {
			continue
		}
		raw, _ := json.Marshal(row)
		if len(res.Entries) >= protocol.WorkspaceExplorerEntries || bytes+len(raw) > byteBudget {
			res.Truncated = true
			if !truncated && len(res.Entries) > 0 {
				cursor.After = res.Entries[len(res.Entries)-1].Name
				res.Cursor = e.signCursor(cursor)
			}
			break
		}
		bytes += len(raw)
		res.Entries = append(res.Entries, row)
	}
	if name == "." {
		for _, member := range e.manifest.Members {
			if !e.memberAvailable(member.Path) {
				res.UnavailableMembers = append(res.UnavailableMembers, member.ID)
				continue
			}
			public, allowed := explorerPath(member.Path)
			if member.Path == "." {
				public = "."
				allowed = true
			}
			if !allowed {
				res.UnavailableMembers = append(res.UnavailableMembers, member.ID)
				continue
			}
			file, err := e.openDirectory(public)
			if err != nil {
				res.UnavailableMembers = append(res.UnavailableMembers, member.ID)
				continue
			}
			info, err := file.Stat()
			_ = file.Close()
			if err != nil || !info.IsDir() || workspaceRootIdentity(info) != e.memberRoots[member.ID] {
				res.UnavailableMembers = append(res.UnavailableMembers, member.ID)
			}
		}
	}
	res.OK = true
	res.ErrorCode = ""
	res.Truncated = res.Truncated || truncated
	return res
}
func (e *LocalExplorer) search(ctx context.Context, req protocol.WorkspaceExplorerRequest, name string) protocol.WorkspaceExplorerResult {
	budget := protocol.WorkspaceSearchVisited
	return e.searchWithBudget(ctx, req, name, &budget, protocol.WorkspaceSearchHits, e.entryReplyBudget(req), 0)
}
func (e *LocalExplorer) searchWithBudget(ctx context.Context, req protocol.WorkspaceExplorerRequest, name string, budget *int, hitLimit, byteLimit, depthOffset int) protocol.WorkspaceExplorerResult {
	res := explorerFailure(req, "unavailable")
	if strings.TrimSpace(req.Filter) == "" || req.Cursor != "" {
		res.ErrorCode = "invalid"
		return res
	}
	ctx = workspaceIgnoreRequest(ctx)
	bytes := 0
	policy := newWorkspaceIgnorePolicy(e)
	policy.scope = name
	defer policy.close()
	var walk func(string, int, *workspaceIgnoreRules) error
	walk = func(dir string, depth int, trackedOnly *workspaceIgnoreRules) error {
		var rows []protocol.WorkspaceEntry
		var cut bool
		var err error
		if trackedOnly != nil {
			rows, cut, err = e.readTrackedDirectory(ctx, dir, budget, trackedOnly)
		} else {
			rows, _, cut, err = e.readDirectory(ctx, dir, budget)
		}
		if err != nil {
			return err
		}
		res.Truncated = res.Truncated || cut
		rules, ignored := policy.annotate(ctx, dir, rows)
		for i, row := range rows {
			if ignored[i] && (!row.Directory || rules == nil || !rules.tracked[rules.relative(row.Path)]) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !ignored[i] && strings.Contains(strings.ToLower(row.Name), strings.ToLower(req.Filter)) {
				raw, _ := json.Marshal(row)
				if len(res.Entries) >= hitLimit || bytes+len(raw) > byteLimit {
					res.Truncated = true
					return nil
				}
				bytes += len(raw)
				res.Entries = append(res.Entries, row)
			}
			if row.Directory {
				if depth >= protocol.WorkspaceSearchDepth {
					res.Truncated = true
					continue
				}
				if *budget <= 0 {
					res.Truncated = true
					return nil
				}
				next := trackedOnly
				if row.Repository || row.MemberID != "" {
					next = nil
				}
				if ignored[i] {
					next = rules
				}
				if err := walk(row.Path, depth+1, next); err != nil {
					return err
				}
			}
			if len(res.Entries) >= hitLimit {
				res.Truncated = true
				return nil
			}
		}
		return nil
	}
	// Explicitly opening an ignored scope overrides its inherited ignore rule.
	if name != "." {
		rules := policy.forDirectory(ctx, name)
		if rules != nil && !rules.failed && rules.isIgnored(rules.relative(name)) {
			policy.explicit = rules
		}
	}
	if err := walk(name, depthOffset, nil); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return explorerFailure(req, "unavailable")
		}
		res.Truncated = true
	}
	res.OK = true
	res.ErrorCode = ""
	return res
}

var errExplorerStale = errors.New("workspace directory changed")

// Membership pins are captured after preparation, before any request. Missing
// or replaced members never acquire a fresh identity from a status request.
func (e *LocalExplorer) pinMembers() {
	for _, pin := range e.worktrees {
		_ = pin.git.Close()
		_ = pin.common.Close()
	}
	for _, anchor := range e.anchors {
		_ = anchor.root.Close()
	}
	e.anchors = map[string]workspaceMemberAnchor{}
	e.memberRoots = map[string]protocol.WorkspaceRoot{}
	e.gitRoots = map[string]protocol.WorkspaceRoot{}
	e.worktrees = map[string]hostWorktreePin{}
	for _, member := range e.manifest.Members {
		if !e.live() {
			return
		}
		file, err := workspaceOpenDirectory(e.root, member.Path)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.IsDir() {
			continue
		}
		folder := filepath.Join(e.folder, member.Path)
		anchor, err := os.OpenRoot(folder)
		if err != nil {
			continue
		}
		if !workspaceRootUnchanged(anchor, folder, info) {
			_ = anchor.Close()
			continue
		}
		e.anchors[member.ID] = workspaceMemberAnchor{root: anchor, folder: folder, info: info}
		e.memberRoots[member.ID] = workspaceRootIdentity(info)
		git, err := e.openDirectory(path.Join(member.Path, ".git"))
		if err != nil {
			continue
		}
		gitInfo, err := git.Stat()
		_ = git.Close()
		if err != nil {
			continue
		}
		if !gitInfo.IsDir() {
			if e.manifest.Runtime == "host" && workspaceRegularFile(gitInfo) {
				if pin, ok := e.pinHostWorktree(member.Path, info); ok {
					e.worktrees[member.ID] = pin
					e.gitRoots[member.ID] = workspaceRootIdentity(gitInfo)
				}
			}
			continue
		}
		e.gitRoots[member.ID] = workspaceRootIdentity(gitInfo)
	}
}

func (e *LocalExplorer) memberAvailable(name string) bool {
	for _, member := range e.manifest.Members {
		if member.Path != "." && name != member.Path && !strings.HasPrefix(name, member.Path+"/") {
			continue
		}
		file, err := workspaceOpenDirectory(e.root, member.Path)
		if err != nil {
			return false
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.IsDir() || workspaceRootIdentity(info) != e.memberRoots[member.ID] {
			return false
		}
	}
	return true
}

func (e *LocalExplorer) anchorFor(name string) (*os.Root, string, *workspaceMemberAnchor) {
	for _, member := range e.manifest.Members {
		if member.Path != "." && name != member.Path && !strings.HasPrefix(name, member.Path+"/") {
			continue
		}
		anchor, ok := e.anchors[member.ID]
		if !ok {
			return nil, "", nil
		}
		relative := name
		if member.Path != "." {
			relative = strings.TrimPrefix(name, member.Path)
			relative = strings.TrimPrefix(relative, "/")
			if relative == "" {
				relative = "."
			}
		}
		return anchor.root, relative, &anchor
	}
	return e.root, name, nil
}
func (e *LocalExplorer) openFile(name string) (*os.File, error) {
	root, relative, _ := e.anchorFor(name)
	if root == nil {
		return nil, os.ErrPermission
	}
	return workspaceOpenFile(root, relative)
}
func (e *LocalExplorer) openDirectory(name string) (*os.File, error) { return e.openFile(name) }

// Reserve the actual escaped response envelope and public member metadata, not
// a fixed estimate: a legal long path can expand sixfold under JSON escaping.
func (e *LocalExplorer) entryReplyBudget(req protocol.WorkspaceExplorerRequest) int {
	base := explorerFailure(req, "")
	raw, _ := json.Marshal(struct {
		Result  protocol.WorkspaceExplorerResult `json:"result"`
		Members []protocol.WorkspaceMember       `json:"members"`
		Runtime string                           `json:"runtime"`
	}{base, e.manifest.Members, e.manifest.Runtime})
	return max(0, protocol.WorkspaceExplorerReplyBytes-len(raw)-8192)
}
