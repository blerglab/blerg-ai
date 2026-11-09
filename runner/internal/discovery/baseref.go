package discovery

import (
	"errors"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var oid = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// maxPackedRefsBytes bounds packed-refs: real files are a few bytes per ref: even a
// repository with tens of thousands of branches/tags stays well under this.
const maxPackedRefsBytes = 8 << 20

// maxLooseRefBytes bounds a loose ref file: a bare oid plus a newline, or a short
// "ref: refs/heads/<name>\n" indirection line. Real ones are under 80 bytes.
const maxLooseRefBytes = 1024

// validRefBranch is a shape check only (mirrors the one the server already applies to the
// same --base value before it ever reaches here, server/workspace_child.go's
// validBaseBranch): no leading '-' or '/', no empty path segment, no control characters,
// no "..", bounded length.
func validRefBranch(branch string) bool {
	if branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.Contains(branch, "..") {
		return false
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" {
			return false
		}
	}
	return true
}

// validRefName is validRefBranch applied to a full "refs/heads/<branch>" ref, used only to
// validate a loose ref file's own "ref: ..." indirection target before following it.
func validRefName(name string) bool {
	branch, ok := strings.CutPrefix(name, "refs/heads/")
	return ok && validRefBranch(branch)
}

// resolveBase is "resolve_base"'s handler (BLG-501 design §10/§11/§12/§13, after five rounds
// of Codex review plus a live e2e failure). Round 1 ran `git rev-parse`/`git rev-list`; round
// 2's review reproduced a real escape (rev-list in a partial clone can lazily fetch missing
// objects and invoke a configured remote helper), so round 2 narrowed to a single
// `git show-ref` call in a sanitized environment. Round 3's review found that show-ref itself
// still opens .idx/.pack files and honors objects/info/alternates — a ref-reading *command*
// is not the same guarantee as reading only a ref — so round 3 dropped the git subprocess
// entirely in favor of a direct loose-ref/packed-refs read. Round 4's review found the
// remaining piece: this function still went through safeDirectory/inspectRepository to
// confirm identity, and those read config and opened objects/ for a check this function
// never needed. It now calls resolveRepositoryIdentity instead — the identity-only half of
// inspectRepository (git.go), sharing its gitdir-resolution logic but reading nothing beyond
// the real git directory's existence and the common directory's metadata.
//
// §13 (this round): the live e2e found that req.Folder.Path is never a real filesystem path
// for a single-member selection. workspace_selection.go's MemberPaths deliberately collapses
// a single member's public Path to "." for display (desktop single-repo layout), decoupled
// from where it actually lives under the daemon's root — the daemon's configured root is
// routinely a folder containing *several* repositories (Main's own desktop: one root holding
// many checkouts, one picked as a workspace), so "." is only correct when the root itself is
// the repository. Every other post-bind operation (PinBound in pins.go, bindChild in
// child.go, RestoreBound in recovery.go) already knows this and recovers the real on-disk
// path from the daemon's own remembered bound selection (s.bound, keyed by LaunchGeneration)
// rather than trusting the manifest's collapsed Path. resolveBase was the one RPC that
// skipped that lookup and opened req.Folder.Path directly — for Main's desktop (root with
// several repositories, selected member collapsed to ".") that opened the *root itself*,
// which has no .git, and resolveRepositoryIdentity correctly refused it (found=false). It now
// does the same bound-selection lookup as its siblings: find the already-bound parent
// selection by req.ParentLaunchGeneration, then the real SelectionRef within it whose
// SourceIdentity matches req.Folder.SourceIdentity — never req.Folder.Path.
//
// Every refusal here used to collapse to the same wire string ("invalid_selection"), logged
// nowhere beyond that — this is what cost a live round-trip to diagnose instead of one
// journalctl line. resolveBase (and the helpers it calls) now also return a cause: a fixed,
// distinct label naming exactly which check failed, carried only in
// protocol.WorkspaceDiscoveryResult.Cause (daemon.go's own log, discovery.go) — never folded
// into ErrorCode, so the server-validated wire vocabulary (validDiscoveryResult,
// workspace_discovery.go) and every other operation's ErrorCode contract are unchanged.
//
// Nothing under objects/, info/, hooks/ or config is ever opened by any step in this
// function; reachability and history are the server's job, against the provider's own
// compare API (cluster_host_workspace.go).
func (s *Service) resolveBase(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) (sha, cause string, err error) {
	if req.Folder == nil || req.Folder.SourceIdentity == "" {
		return "", "resolve_base:no_folder_selection", errInvalid
	}
	if req.ParentLaunchGeneration == "" {
		return "", "resolve_base:no_parent_launch", errInvalid
	}
	if !validRefBranch(req.Base) {
		return "", "resolve_base:hostile_branch_shape", errInvalid
	}
	s.mu.Lock()
	parent, ok := s.bound[req.ParentLaunchGeneration]
	s.mu.Unlock()
	if !ok {
		return "", "resolve_base:unknown_parent_launch", errStale
	}
	rel, found := realMemberPath(parent, req.Folder.SourceIdentity)
	if !found {
		return "", "resolve_base:unknown_member", errStale
	}
	if err := op.visitPath(rel); err != nil {
		return "", "resolve_base:path_budget", err
	}
	dir, err := openBeneath(root.file, rel, directoryFlags)
	if err != nil {
		return "", "resolve_base:member_directory_unavailable", errUnavailable
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Stat()
	if err != nil {
		return "", "resolve_base:member_directory_stat_failed", errUnavailable
	}
	commonPath, sourceIdentity, identityCause, err := resolveRepositoryIdentity(root.file, root.name, root.tag, rel, info)
	if err != nil {
		return "", identityCause, err
	}
	if sourceIdentity != req.Folder.SourceIdentity {
		return "", "resolve_base:identity_mismatch", errStale
	}
	// commonPath is the real common git directory (handling a linked worktree's indirection
	// the same way inspectRepository always has — resolveRepositoryIdentity shares that
	// logic via resolveGitDir, git.go). readRef below never opens anything under objects/,
	// info/, hooks/ or config: only loose ref files and packed-refs, both plain data, both
	// read through openBeneath's existing symlink/escape containment.
	local, refCause, err := readRef(root.file, commonPath, "refs/heads/"+req.Base, 0)
	if err != nil {
		return "", refCause, err
	}
	if !oid.MatchString(local) {
		return "", "resolve_base:ref_not_an_oid", errInvalid
	}
	return local, "", nil
}

// realMemberPath finds the real, on-disk SelectionRef within parent's own remembered
// selection (never the manifest's own collapsed, public Path — see resolveBase's §13 comment
// above) whose SourceIdentity matches sourceIdentity, and returns its real Path.
func realMemberPath(parent boundRecord, sourceIdentity string) (rel string, found bool) {
	for _, member := range parent.refs {
		if member.SourceIdentity == sourceIdentity {
			return member.Path, true
		}
	}
	return "", false
}

// resolveRepositoryIdentity resolves rel's real git/common directory and computes the same
// SourceIdentity inspectRepository/entryFor would for the same path (git.go) — without
// reading config, HEAD, the index, or opening anything under objects/. Shares
// resolveGitDir's gitdir-resolution with inspectRepository rather than duplicating the
// parsing of a repository-controlled "gitdir:"/"commondir" file a second time.
func resolveRepositoryIdentity(root *os.File, rootName, rootTag, rel string, info os.FileInfo) (commonPath, sourceIdentity, cause string, err error) {
	_, commonPath, found, err := resolveGitDir(root, rootName, rel)
	if err != nil {
		return "", "", "resolve_base:gitdir_resolution_failed", err
	}
	if !found {
		return "", "", "resolve_base:gitdir_not_found", errInvalid
	}
	common, e := openBeneath(root, commonPath, directoryFlags)
	if e != nil {
		return "", "", "resolve_base:common_directory_unavailable", errUnavailable
	}
	defer func() { _ = common.Close() }()
	commonInfo, e := common.Stat()
	if e != nil {
		return "", "", "resolve_base:common_directory_stat_failed", errUnavailable
	}
	commonID := hashIdentity("common-v1", infoIdentity(commonInfo))
	// A fresh open (not the caller's own dir handle) matches inspectRepository's own
	// staleness check: the directory this identity names must still be the same one the
	// caller already stat'd as info.
	sourceDirectory, e := openBeneath(root, rel, directoryFlags)
	if e != nil {
		return "", "", "resolve_base:source_directory_reopen_failed", errStale
	}
	sourceInfo, e := sourceDirectory.Stat()
	if e != nil || !os.SameFile(info, sourceInfo) {
		_ = sourceDirectory.Close()
		return "", "", "resolve_base:source_directory_changed", errStale
	}
	sourceVersion := instanceIdentity(sourceDirectory, sourceInfo)
	_ = sourceDirectory.Close()
	sourceID := hashIdentity("source-v1", rootTag, rel, infoIdentity(info), sourceVersion, commonID, instanceIdentity(common, commonInfo))
	return commonPath, sourceID, "", nil
}

// readRef resolves refName to its object id, loose first (overriding packed-refs, matching
// git's own precedence), following at most one "ref: " indirection. gitDir is a root-
// relative path (repo.commonPath); every read goes through readSmall/openBeneath, which
// already refuses a symlink or a path escaping root at the kernel level (RESOLVE_BENEATH |
// RESOLVE_NO_SYMLINKS, root_unix.go).
func readRef(root *os.File, gitDir, refName string, depth int) (sha, cause string, err error) {
	if depth > 1 {
		return "", "resolve_base:ref_indirection_too_deep", errInvalid
	}
	raw, err := readSmall(root, path.Join(gitDir, refName), maxLooseRefBytes)
	if err == nil {
		line := strings.TrimSpace(string(raw))
		if oid.MatchString(line) {
			return line, "", nil
		}
		target, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			return "", "resolve_base:loose_ref_unparseable", errInvalid
		}
		target = strings.TrimSpace(target)
		if !validRefName(target) {
			return "", "resolve_base:ref_indirection_target_invalid", errInvalid
		}
		return readRef(root, gitDir, target, depth+1)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "resolve_base:loose_ref_unreadable", errInvalid
	}
	return readPackedRef(root, gitDir, refName)
}

// readPackedRef scans packed-refs for an exact match on refName. Peeled annotation lines
// ("^<oid>", the dereferenced target of an annotated tag immediately above) are skipped —
// they never name refName themselves.
func readPackedRef(root *os.File, gitDir, refName string) (sha, cause string, err error) {
	raw, err := readSmall(root, path.Join(gitDir, "packed-refs"), maxPackedRefsBytes)
	if err != nil {
		return "", "resolve_base:packed_refs_unreadable", errInvalid
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		candidateSHA, name, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if name == refName && oid.MatchString(candidateSHA) {
			return candidateSHA, "", nil
		}
	}
	return "", "resolve_base:ref_not_found", errInvalid
}
