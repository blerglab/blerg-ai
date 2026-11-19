package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

// A workspace whose first repository sits at "." keeps it there. Others join in
// the reserved folder: a link to the checkout on a host, a clone in a sandbox's
// allocation. The first repository's Git never sees the folder, through a marked
// entry in its info/exclude, which is why nothing here is ever committed.
//
// That entry is a courtesy to the agent's own Git. It lives in a file the agent
// can write, so sandbox settlement never trusts it; see reservedPaths.

const (
	reservedExcludeMarker = "# Added by Blerg for repositories added to this workspace."
	reservedExcludeOwner  = "# Blerg folder "
	reservedExcludeLine   = protocol.WorkspaceReservedFolder + "/"
	reservedExcludeLimit  = 1 << 20
)

// workspaceReservedRetired is a reserved path still on disk after its member left.
// Settlement keeps excluding it until it is gone, so a retained clone never
// reaches the first repository's history.
type workspaceReservedRetired struct {
	Path     string                 `json:"path"`
	Target   string                 `json:"target,omitempty"`  // a host link's checkout
	Identity protocol.WorkspaceRoot `json:"identity,omitzero"` // a sandbox clone's directory
	// What deleting a sandbox clone must prove again: the agent can reach it until
	// it is gone, so the check that allowed the removal does not carry over.
	Delivered    string `json:"delivered,omitempty"`
	RefsDigest   string `json:"refs_digest,omitempty"`
	DiscardLocal string `json:"discard_local,omitempty"`
}

// reservedPaths is the trusted list of what the first repository must not absorb.
func reservedPaths(state workspacePrivateManifest) []string {
	var paths []string
	for _, member := range state.Launch.Members {
		if protocol.ReservedMemberPath(member.Path) {
			paths = append(paths, member.Path)
		}
	}
	for _, retired := range state.ReservedRetired {
		paths = append(paths, retired.Path)
	}
	return paths
}

// claimReservedFolder makes the folder ready for a name. Something there that is
// not one of ours belongs to the person or the agent, so the change is refused.
func claimReservedFolder(root string, known []string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Lstat(protocol.WorkspaceReservedFolder)
	if os.IsNotExist(err) {
		return dir.Mkdir(protocol.WorkspaceReservedFolder, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errWorkspaceMemberCollision
	}
	folder, err := dir.Open(protocol.WorkspaceReservedFolder)
	if err != nil {
		return err
	}
	defer func() { _ = folder.Close() }()
	entries, err := folder.ReadDir(protocol.MaxWorkspaceMembers + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	ours := map[string]bool{}
	for _, name := range known {
		ours[path.Base(name)] = true
	}
	for _, entry := range entries {
		if !ours[entry.Name()] {
			return errWorkspaceMemberCollision
		}
	}
	return nil
}

// releaseReservedFolder removes the folder once it is empty, and its Git entry
// with it. A folder still holding something keeps both.
func releaseReservedFolder(root, gitDir string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Lstat(protocol.WorkspaceReservedFolder)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case !info.IsDir():
		return nil
	default:
		if dir.Remove(protocol.WorkspaceReservedFolder) != nil {
			return nil // not empty: another member still lives here
		}
	}
	return removeReservedExclude(gitDir, root)
}

// linkReservedMember gives the agent its path to a host checkout. The same link
// from an interrupted attempt is reused; anything else there is refused.
func linkReservedMember(root, member, target string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.FromSlash(member)
	info, err := dir.Lstat(name)
	if os.IsNotExist(err) {
		return dir.Symlink(target, name)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if existing, readErr := dir.Readlink(name); readErr == nil && existing == target {
			return nil
		}
	}
	return errWorkspaceMemberCollision
}

// unlinkReservedMember removes only the link this workspace made.
func unlinkReservedMember(root, member, target string) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.FromSlash(member)
	existing, err := dir.Readlink(name)
	if err != nil || existing != target {
		return nil // gone, replaced or not a link: not ours to remove
	}
	return dir.Remove(name)
}

func openReservedExclude(gitDir string) (*os.Root, string, error) {
	dir, err := os.OpenRoot(gitDir)
	if err != nil {
		return nil, "", err
	}
	name := filepath.Join("info", "exclude")
	info, err := dir.Lstat("info")
	if err == nil && !info.IsDir() {
		err = errors.New("info is not a directory")
	} else if os.IsNotExist(err) {
		err = dir.Mkdir("info", 0o755)
	}
	if err != nil {
		_ = dir.Close()
		return nil, "", err
	}
	raw, err := dir.Lstat(name)
	if err == nil && !raw.Mode().IsRegular() {
		_ = dir.Close()
		return nil, "", errors.New("info/exclude is not a regular file")
	}
	if err == nil && raw.Size() > reservedExcludeLimit {
		_ = dir.Close()
		return nil, "", errors.New("info/exclude is too large")
	}
	return dir, name, nil
}

func replaceReservedExclude(dir *os.Root, name, text string) error {
	temporary := name + ".blerg"
	if err := dir.WriteFile(temporary, []byte(text), 0o644); err != nil {
		return err
	}
	if err := dir.Rename(temporary, name); err != nil {
		_ = dir.Remove(temporary)
		return err
	}
	return nil
}

// reservedExcludeLock orders the read and rewrite of a shared info/exclude between
// workspaces of this daemon; a sandbox's copy is its own.
var reservedExcludeLock sync.Mutex

func reservedExcludeOwnerLine(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return reservedExcludeOwner + hex.EncodeToString(sum[:8])
}

// reservedExcludeBlock finds the marked entry: the marker, one line per folder
// that relies on it, then the entry. lines keep their newlines.
func reservedExcludeBlock(lines []string) (start, entry int, ok bool) {
	for i := range lines {
		if strings.TrimRight(lines[i], "\n") != reservedExcludeMarker {
			continue
		}
		next := i + 1
		for next < len(lines) && strings.HasPrefix(lines[next], reservedExcludeOwner) {
			next++
		}
		if next < len(lines) && strings.TrimRight(lines[next], "\n") == reservedExcludeLine {
			return i, next, true
		}
	}
	return 0, 0, false
}

// addReservedExclude lists the folder once. gitDir is the common directory: Git
// reads info/exclude from there for every linked worktree, so one entry serves
// them all and each root is recorded as relying on it. An entry the person
// already wrote is left alone, and stays theirs.
func addReservedExclude(gitDir, root string) error {
	reservedExcludeLock.Lock()
	defer reservedExcludeLock.Unlock()
	dir, name, err := openReservedExclude(gitDir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	raw, err := dir.ReadFile(name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(raw)
	owner := reservedExcludeOwnerLine(root)
	lines := strings.SplitAfter(text, "\n")
	if start, entry, ok := reservedExcludeBlock(lines); ok {
		for _, line := range lines[start+1 : entry] {
			if strings.TrimRight(line, "\n") == owner {
				return nil
			}
		}
		return replaceReservedExclude(dir, name, strings.Join(lines[:entry], "")+owner+"\n"+strings.Join(lines[entry:], ""))
	}
	for _, line := range strings.Split(text, "\n") {
		switch strings.TrimSpace(line) {
		case reservedExcludeLine, "/" + reservedExcludeLine, protocol.WorkspaceReservedFolder, "/" + protocol.WorkspaceReservedFolder:
			return nil
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return replaceReservedExclude(dir, name, text+reservedExcludeMarker+"\n"+owner+"\n"+reservedExcludeLine+"\n")
}

// removeReservedExclude ends this root's claim. The entry goes with the last claim
// and only if it carries Blerg's marker.
func removeReservedExclude(gitDir, root string) error {
	reservedExcludeLock.Lock()
	defer reservedExcludeLock.Unlock()
	dir, name, err := openReservedExclude(gitDir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	raw, err := dir.ReadFile(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(raw), "\n")
	start, entry, ok := reservedExcludeBlock(lines)
	if !ok {
		return nil
	}
	owner := reservedExcludeOwnerLine(root)
	var others []string
	found := false
	for _, line := range lines[start+1 : entry] {
		if strings.TrimRight(line, "\n") == owner {
			found = true
			continue
		}
		others = append(others, line)
	}
	switch {
	case !found:
		return nil
	case len(others) > 0:
		kept := append(append(append([]string{}, lines[:start+1]...), others...), lines[entry:]...)
		return replaceReservedExclude(dir, name, strings.Join(kept, ""))
	}
	return replaceReservedExclude(dir, name, strings.Join(append(lines[:start:start], lines[entry+1:]...), ""))
}

// rootGitDir is where the root member's exclude lives: the proven common
// directory on a host, the clone's own .git in a sandbox allocation.
func rootGitDir(state workspacePrivateManifest) (string, bool) {
	if !state.Host {
		for _, member := range state.Launch.Members {
			if member.Path == "." {
				return filepath.Join(state.Root, ".git"), true
			}
		}
		return "", false
	}
	for _, source := range state.Sources {
		if source.Member.Path == "." {
			return source.CommonPath, true
		}
	}
	return "", false
}

// placeReservedMember runs with writers fenced, before the receipt. Everything it
// does is undone by undoReservedAddition if the change never commits.
func (w *LocalWorkspace) placeReservedMember(source WorkspaceSource) error {
	gitDir, ok := rootGitDir(w.state)
	if !ok {
		return refuseMembership(protocol.MembershipRefusePrepare, errors.New("reserved path without a root member"))
	}
	collision := func(err error) error {
		if errors.Is(err, errWorkspaceMemberCollision) {
			return refuseMembership(protocol.MembershipRefuseCollision, err)
		}
		return refuseMembership(protocol.MembershipRefusePrepare, err)
	}
	if err := claimReservedFolder(w.state.Root, append(reservedPaths(w.state), source.Member.Path)); err != nil {
		return collision(err)
	}
	if err := linkReservedMember(w.state.Root, source.Member.Path, source.Path); err != nil {
		return collision(err)
	}
	if err := addReservedExclude(gitDir, w.state.Root); err != nil {
		return collision(err)
	}
	return nil
}

// undoReservedAddition restores a host folder after a change that never committed.
func (w *LocalWorkspace) undoReservedAddition(j *workspaceMembershipJournal) error {
	if j == nil || j.Kind != "add" || j.Source == nil || !protocol.ReservedMemberPath(j.Source.Member.Path) {
		return nil
	}
	if err := unlinkReservedMember(w.state.Root, j.Source.Member.Path, j.Source.Path); err != nil {
		return err
	}
	gitDir, ok := rootGitDir(w.state)
	if !ok {
		return nil
	}
	return releaseReservedFolder(w.state.Root, gitDir)
}

// retireReserved clears paths left by members that are gone. It runs fenced, on a
// host or through the storage worker; a failure keeps the record, and with it the
// exclusion, for the next change to retry.
func (w *LocalWorkspace) retireReserved(ctx context.Context) error {
	if len(w.state.ReservedRetired) == 0 {
		return nil
	}
	if w.state.Host {
		gitDir, ok := rootGitDir(w.state)
		if !ok {
			return nil
		}
		for _, retired := range w.state.ReservedRetired {
			if err := unlinkReservedMember(w.state.Root, retired.Path, retired.Target); err != nil {
				return err
			}
		}
		if err := releaseReservedFolder(w.state.Root, gitDir); err != nil {
			return err
		}
	} else {
		if w.storage == nil || w.runtime == nil {
			return nil // a startup settle has no worker; the next change retries
		}
		if err := w.save(); err != nil {
			return err
		}
		if err := w.beginWorker(ctx); err != nil {
			return err
		}
		if _, err := w.runWorker(ctx, workspaceWorkerRequest{Action: "membership-retire", State: w.state, Record: w.record}); err != nil {
			return err
		}
	}
	var kept []workspaceReservedRetired // nil when empty, as the signed record reads back
	for _, retired := range w.state.ReservedRetired {
		if !w.state.Host && retired.Identity == (protocol.WorkspaceRoot{}) {
			kept = append(kept, retired)
		}
	}
	w.state.ReservedRetired = kept
	return w.save()
}

// retireSandboxReserved is the worker's half: writers are frozen, and each path is
// removed only if it is still the directory recorded when its member left and
// everything in it is still proved delivered. A clone that fails stays, with its
// record and its exclusion.
func retireSandboxReserved(ctx context.Context, state workspacePrivateManifest) error {
	for _, retired := range state.ReservedRetired {
		if !protocol.ReservedMemberPath(retired.Path) {
			return errors.New("invalid reserved path")
		}
		if retired.Identity == (protocol.WorkspaceRoot{}) {
			continue
		}
		clone := filepath.Join(state.Root, filepath.FromSlash(retired.Path))
		identity, err := workspaceDirectoryIdentity(clone)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || identity != retired.Identity {
			return errors.New("reserved clone replaced")
		}
		if err := proveCloneDelivered(ctx, clone, retired.Delivered, retired.RefsDigest, workbranch.Effective(state.Session, state.WorkBranch)); err != nil {
			return err
		}
		local, err := proveCloneDisposable(ctx, clone)
		if err != nil {
			return err
		}
		if local.Count > 0 && local.Digest != retired.DiscardLocal {
			return errors.New("reserved clone has local files nobody confirmed")
		}
		if err := os.RemoveAll(clone); err != nil {
			return err
		}
	}
	gitDir, ok := rootGitDir(state)
	if !ok {
		return nil
	}
	return releaseReservedFolder(state.Root, gitDir)
}

// proveCloneDelivered shows every commit, ref and tracked or untracked file of a
// clone reached its destination. lastSynced and refsDigest are the delivered state.
func proveCloneDelivered(ctx context.Context, clone, lastSynced, refsDigest, branch string) error {
	if err := checkSandboxCloneTreeContext(ctx, clone); err != nil {
		return err
	}
	if head, ok := readCloneRef(clone, "refs/heads/"+branch); ok && head != lastSynced {
		return errors.New("member has undelivered commits")
	}
	refs, err := workspaceCloneRefs(ctx, clone)
	if err != nil {
		return err
	}
	if refsDigest == "" || workspaceRefsDigest(refs, "refs/heads/"+branch) != refsDigest {
		return errors.New("member has refs that were not delivered")
	}
	status, err := cloneGit(ctx, clone, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("member has uncommitted files")
	}
	return nil
}

func cloneGit(ctx context.Context, clone string, args ...string) (string, error) {
	return hostgit.Output(ctx, hostgit.Options{Dir: clone, Timeout: sandboxGitTimeout, Untrusted: true, Config: []string{"core.excludesFile=/dev/null"}}, append([]string{"--work-tree=."}, args...)...)
}

// proveCloneDisposable covers what Git status leaves out, for a clone about to be
// deleted. Index flags and populated submodules hide work from status, so they
// fail. Ignored files are returned for the person to decide on.
func proveCloneDisposable(ctx context.Context, clone string) (protocol.WorkspaceLocalFiles, error) {
	var local protocol.WorkspaceLocalFiles
	index, err := cloneGit(ctx, clone, "ls-files", "-v", "-s", "-z")
	if err != nil {
		return local, err
	}
	for _, line := range strings.Split(index, "\x00") {
		if line == "" {
			continue
		}
		tag, rest, _ := strings.Cut(line, " ")
		if tag != "H" {
			return local, errors.New("member hides files from Git")
		}
		mode, entry, _ := strings.Cut(rest, " ")
		if _, name, ok := strings.Cut(entry, "\t"); ok && mode == "160000" {
			if inner, err := os.ReadDir(filepath.Join(clone, filepath.FromSlash(name))); err == nil && len(inner) > 0 {
				return local, errors.New("member has a populated submodule")
			}
		}
	}
	listed, err := cloneGit(ctx, clone, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory", "-z")
	if err != nil {
		return local, err
	}
	sum := sha256.New()
	for _, name := range strings.Split(listed, "\x00") {
		if name == "" {
			continue
		}
		local.Count++
		if len(local.Names) < protocol.MaxWorkspaceLocalNames {
			local.Names = append(local.Names, name)
		}
		err := filepath.WalkDir(filepath.Join(clone, filepath.FromSlash(strings.TrimSuffix(name, "/"))), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(clone, p)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(sum, "%s\x00%o\x00%d\x00%d\x00", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
			return err
		})
		if err != nil {
			return local, err
		}
	}
	if local.Count > 0 {
		local.Digest = hex.EncodeToString(sum.Sum(nil))
	}
	return local, nil
}
