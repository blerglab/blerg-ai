package daemon

// Commit lookup: a commit chip's hovercard and its on-demand diff, read from the session's own
// workspace by the daemon that runs it. The server asks (read_commit) only for a session the
// person may read; the daemon answers from the workspace the live launch is bound to, never from a
// path in the request. The one value from the request that reaches git is the sha, accepted only as
// 7 to 64 lowercase hex digits and resolved with rev-parse --verify --end-of-options <sha>^{commit};
// every later git call names the full sha rev-parse printed.
//
// A sandboxed session's clone is written by the session's container, so host Git never runs in
// it (sandboxclone.go). Its commits come back to the person's repository on wip/<id> at each
// turn's end, so a sandboxed session's commits are read there: the repository the daemon
// recorded for the clone. A commit made since the last turn ended is not there yet and reads as
// not found.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// commitReadTimeout bounds one read (every git call of it together).
var commitReadTimeout = 5 * time.Second

// commitReads bounds the reads in flight on this daemon (git processes and their reply buffers);
// commitReadsPerSession keeps one session from taking them all. A read over either is answered
// "busy" at once.
var (
	commitReads           = make(chan struct{}, 3)
	commitReadsPerSession = 2
	commitReadsMu         sync.Mutex
	commitReadsBySession  = map[string]int{}
)

// The most git output read for a summary and for its file list; past it, the rest is dropped.
const (
	commitSummaryMaxBytes = 64 << 10
	commitNumstatMaxBytes = 256 << 10
)

var errCommitOutput = errors.New("git output exceeds the commit limit")

// cappedOutput keeps at most max bytes. strict: one byte more fails the write (git stops at once,
// the read is "too large"); else the rest is dropped and over is set.
type cappedOutput struct {
	buf    bytes.Buffer
	max    int
	strict bool
	over   bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if len(p) <= room {
		w.buf.Write(p)
		return len(p), nil
	}
	w.over = true
	if w.strict {
		return 0, errCommitOutput
	}
	if room > 0 {
		w.buf.Write(p[:room])
	}
	return len(p), nil
}

// commitReadDir is where a session's commits are read: its workspace, or for a sandboxed session
// the repository its clone was made from (see the top of this file). trusted: that repository.
// A workspace anywhere under the sandbox clone root that is not this session's recognized clone
// with a valid record is refused: host git never runs in a container-written .git.
func commitReadDir(sessionID string, binding workspaceFileBinding) (dir *pinnedGitDir, trusted, ok bool) {
	resolved, err := filepath.EvalSymlinks(binding.root)
	if err != nil {
		return nil, false, false
	}
	if underSandboxCloneRoot(binding.root) || underSandboxCloneRoot(resolved) {
		if !isSandboxClone(binding.root, sessionID) && !isSandboxClone(resolved, sessionID) {
			return nil, false, false
		}
		c, err := loadSandboxClone(sessionID)
		if err != nil {
			return nil, false, false
		}
		dir, ok = pinGitDir(c.Repo, nil)
		return dir, true, ok
	}
	dir, ok = pinGitDir(binding.root, binding.info)
	return dir, false, ok
}

// underSandboxCloneRoot: path is the sandbox clone root or anything under it (raw or resolved).
func underSandboxCloneRoot(path string) bool {
	root := sandboxCloneRoot()
	if root == "" {
		return false
	}
	roots := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, r)
	}
	clean := filepath.Clean(path)
	for _, r := range roots {
		if clean == r || strings.HasPrefix(clean, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// readWorkspaceCommit reads sha's summary (patch false) or its diff (patch true). It never logs
// content, paths or git's diagnostics.
func readWorkspaceCommit(parent context.Context, sessionID string, binding workspaceFileBinding, sha string, patch bool) protocol.CommitResult {
	fail := func(code string) protocol.CommitResult { return protocol.CommitResult{ErrorCode: code} }
	if !protocol.ValidCommitLookupSHA(sha) {
		return fail("invalid")
	}
	if binding.live == nil || binding.info == nil || !binding.live() {
		return fail("unavailable")
	}
	dir, trusted, ok := commitReadDir(sessionID, binding)
	if !ok {
		return fail("unavailable")
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail("unavailable")
	}
	full, code := resolveCommit(ctx, dir, sha)
	if code != "" {
		return fail(code)
	}
	var res protocol.CommitResult
	if patch {
		res = readCommitPatch(ctx, dir, full)
	} else {
		res = readCommitSummary(ctx, dir, full)
	}
	// An engine end or replacement during the read must not answer for the session as it is now.
	if ctx.Err() != nil || !binding.live() {
		return fail("unavailable")
	}
	return res
}

// readChildMerged answers whether wip/<child> has work beyond its recorded clone base merged into the parent's branch (plan nudges:
// a finished child can be discarded). child is a session id; nothing else from the request
// reaches git.
func readChildMerged(parent context.Context, sessionID string, binding workspaceFileBinding, child string) protocol.CommitResult {
	return readChildMergedBase(parent, sessionID, binding, child, "", "")
}
func readChildMergedBase(parent context.Context, sessionID string, binding workspaceFileBinding, child, base, target string) protocol.CommitResult {
	fail := func(code string) protocol.CommitResult { return protocol.CommitResult{ErrorCode: code} }
	if !protocol.ValidChildBranchID(child) {
		return fail("invalid")
	}
	if binding.live == nil || binding.info == nil || !binding.live() {
		return fail("unavailable")
	}
	dir, trusted, ok := commitReadDir(sessionID, binding)
	if !ok {
		return fail("unavailable")
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail("unavailable")
	}
	tip, code := resolveRef(ctx, dir, "refs/heads/wip/"+child)
	if code != "" {
		return fail(code)
	}
	if base == "" {
		clone, err := loadSandboxClone(child)
		if err != nil {
			return fail("unavailable")
		}
		base = clone.Base
	} else if !protocol.ValidSessionOriginCommit(base) {
		return fail("invalid")
	}
	res := protocol.CommitResult{OK: true}
	if tip == base {
		return res
	}
	worktrees := &cappedOutput{max: 64 << 10, strict: true}
	if err := dir.git(ctx, worktrees, "worktree", "list", "--porcelain"); err != nil {
		return fail("unavailable")
	}
	for _, line := range strings.Split(worktrees.buf.String(), "\n") {
		if line == "branch refs/heads/wip/"+child {
			return res
		}
	}
	if _, code := resolveRef(ctx, dir, "refs/heads/wip/"+child+".incoming"); code != "not_found" {
		return res
	}
	if target == "" {
		target = "HEAD"
		if trusted {
			target = "refs/heads/wip/" + sessionID
		}
	}
	head, code := resolveRef(ctx, dir, target)
	if code != "" {
		return fail(code)
	}
	err := dir.git(ctx, io.Discard, "merge-base", "--is-ancestor", "--end-of-options", tip, head)
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil || !binding.live():
		return fail("unavailable")
	case err == nil:
		res.Merged = true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		res.Merged = false
	default:
		return fail("unavailable")
	}
	return res
}

// resolveRef is a fixed ref's commit: "not_found" when there is none.
func resolveRef(ctx context.Context, dir *pinnedGitDir, ref string) (string, string) {
	out := &cappedOutput{max: 100, strict: true}
	err := dir.git(ctx, out, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if ctx.Err() != nil {
		return "", "unavailable"
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && out.buf.Len() == 0 {
			return "", "not_found"
		}
		return "", "unavailable"
	}
	full := strings.TrimSpace(out.buf.String())
	if !protocol.ValidSessionOriginCommit(full) {
		return "", "unavailable"
	}
	return full, ""
}

func resolveCommit(ctx context.Context, dir *pinnedGitDir, sha string) (string, string) {
	out := &cappedOutput{max: 100, strict: true}
	err := dir.git(ctx, out, "rev-parse", "--verify", "--quiet", "--end-of-options", sha+"^{commit}")
	if ctx.Err() != nil {
		return "", "unavailable"
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && out.buf.Len() == 0 {
			return "", "not_found" // unknown, ambiguous, or not a commit
		}
		return "", "unavailable" // not a repository, or git failed
	}
	full := strings.TrimSpace(out.buf.String())
	if !protocol.ValidSessionOriginCommit(full) || !strings.HasPrefix(full, sha) {
		return "", "unavailable"
	}
	return full, ""
}

// The diff options every read carries: what the repository's own configuration could change
// (colors, an external diff, text conversion, prefixes, a relative diff, notes, signatures) set
// back, and a merge shown against its first parent.
var commitDiffArgs = []string{
	"--no-color", "--no-ext-diff", "--no-textconv", "--no-notes", "--no-show-signature", "--no-relative", "--no-mailmap",
	"--submodule=short", "-O/dev/null", "--encoding=UTF-8", "--diff-merges=first-parent", "-M", "-l1000",
	"--src-prefix=a/", "--dst-prefix=b/",
}

func readCommitSummary(ctx context.Context, dir *pinnedGitDir, full string) protocol.CommitResult {
	head := &cappedOutput{max: commitSummaryMaxBytes}
	if err := dir.git(ctx, head, "show", "-s", "--no-color", "--no-notes", "--no-show-signature", "--no-mailmap", "--encoding=UTF-8",
		"--format=%H%x00%P%x00%an%x00%aI%x00%s%x00%b", "--end-of-options", full); err != nil {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	parts := strings.SplitN(head.buf.String(), "\x00", 6)
	for len(parts) < 6 {
		parts = append(parts, "")
	}
	if parts[0] != full {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	info := &protocol.CommitInfo{SHA: full, Short: full[:7], Parents: []string{}, Files: []protocol.CommitFile{}}
	for _, p := range strings.Fields(parts[1]) {
		if protocol.ValidSessionOriginCommit(p) && len(info.Parents) < protocol.CommitParentsMax {
			info.Parents = append(info.Parents, p)
		}
	}
	info.Author, _ = capText(parts[2], protocol.CommitAuthorMaxBytes)
	if date := strings.TrimSpace(parts[3]); len(date) <= 64 {
		if _, err := time.Parse(time.RFC3339, date); err == nil {
			info.AuthorDate = date
		}
	}
	info.Subject, _ = capText(strings.TrimSpace(parts[4]), protocol.CommitSubjectMaxBytes)
	body, cut := capText(strings.TrimRight(parts[5], "\n"), protocol.CommitBodyMaxBytes)
	info.Body, info.BodyTruncated = body, cut || head.over

	stat := &cappedOutput{max: commitNumstatMaxBytes}
	args := append(append([]string{"show", "--numstat", "-z", "--format="}, commitDiffArgs...), "--end-of-options", full)
	if err := dir.git(ctx, stat, args...); err != nil {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	info.Files, info.Adds, info.Dels, info.FilesTruncated = parseNumstat(stat.buf.Bytes(), stat.over)
	return protocol.CommitResult{OK: true, Commit: info}
}

// parseNumstat reads `git show --numstat -z`: "adds\tdels\tpath\0", or for a rename
// "adds\tdels\t\0old\0new\0"; "-" counts are a binary file. over: the output was cut, so its last
// entry may be partial and is dropped.
func parseNumstat(raw []byte, over bool) (files []protocol.CommitFile, adds, dels int, truncated bool) {
	files = []protocol.CommitFile{}
	tokens := strings.Split(string(raw), "\x00")
	if over {
		truncated = true
		if len(tokens) > 0 {
			tokens = tokens[:len(tokens)-1]
		}
	}
	for i := 0; i < len(tokens); i++ {
		tok := strings.TrimLeft(tokens[i], "\n")
		if tok == "" {
			continue
		}
		fields := strings.SplitN(tok, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		f := protocol.CommitFile{Path: fields[2]}
		if f.Path == "" { // a rename: the old and new paths follow
			if i+2 >= len(tokens) {
				truncated = true
				break
			}
			f.OldPath, f.Path = tokens[i+1], tokens[i+2]
			i += 2
		}
		if fields[0] == "-" || fields[1] == "-" {
			f.Binary = true
		} else {
			a, errA := strconv.Atoi(fields[0])
			d, errD := strconv.Atoi(fields[1])
			if errA != nil || errD != nil || a < 0 || d < 0 {
				continue
			}
			f.Adds, f.Dels = a, d
			adds += a
			dels += d
		}
		if len(files) >= protocol.CommitFilesMax {
			truncated = true
			continue // the totals still count it
		}
		var cut bool
		f.Path, cut = capText(f.Path, protocol.CommitPathMaxBytes)
		f.OldPath, _ = capText(f.OldPath, protocol.CommitPathMaxBytes)
		if f.Path == "" || cut {
			truncated = true
			continue
		}
		files = append(files, f)
	}
	return files, adds, dels, truncated
}

func readCommitPatch(ctx context.Context, dir *pinnedGitDir, full string) protocol.CommitResult {
	out := &cappedOutput{max: protocol.CommitPatchMaxBytes, strict: true}
	args := append(append([]string{"show", "--patch",
		"--format=commit %H%nAuthor: %an <%ae>%nDate:   %aD%n%n%w(0,4,4)%B"}, commitDiffArgs...), "--end-of-options", full)
	err := dir.git(ctx, out, args...)
	if out.over {
		return protocol.CommitResult{ErrorCode: "too_large"}
	}
	if err != nil {
		return protocol.CommitResult{ErrorCode: "unavailable"}
	}
	return protocol.CommitResult{OK: true, PatchB64: base64.StdEncoding.EncodeToString(out.buf.Bytes())}
}

// capText is s cut to at most limit bytes on a rune boundary, invalid UTF-8 replaced; cut: it was longer.
func capText(s string, limit int) (string, bool) {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}

func acquireCommitRead(sessionID string) (func(), bool) {
	commitReadsMu.Lock()
	if commitReadsBySession[sessionID] >= commitReadsPerSession {
		commitReadsMu.Unlock()
		return nil, false
	}
	select {
	case commitReads <- struct{}{}:
	default:
		commitReadsMu.Unlock()
		return nil, false
	}
	commitReadsBySession[sessionID]++
	commitReadsMu.Unlock()
	return func() {
		commitReadsMu.Lock()
		if commitReadsBySession[sessionID]--; commitReadsBySession[sessionID] <= 0 {
			delete(commitReadsBySession, sessionID)
		}
		commitReadsMu.Unlock()
		<-commitReads
	}, true
}

func queueCommitRead(ctx context.Context, sender Sender, req protocol.ReadCommit, binding workspaceFileBinding) {
	queueCommitReadUsing(ctx, sender, req, func(ctx context.Context) protocol.CommitResult {
		if req.MergedChild != "" {
			return readChildMerged(ctx, req.SessionID, binding, req.MergedChild)
		}
		return readWorkspaceCommit(ctx, req.SessionID, binding, req.SHA, req.Patch)
	})
}

func queueCommitReadUsing(ctx context.Context, sender Sender, req protocol.ReadCommit, read func(context.Context) protocol.CommitResult) {
	if !validSessionOriginID(req.RequestID) || !validSessionOriginID(req.SessionID) {
		return
	}
	reply := func(res protocol.CommitResult) {
		res.WorkspaceGitRef = req.WorkspaceGitRef
		if res.Commit != nil {
			res.Commit.WorkspaceGitRef = req.WorkspaceGitRef
		}
		res.Type, res.RequestID, res.SessionID = "commit_result", req.RequestID, req.SessionID
		outcome := res.ErrorCode
		if res.OK {
			outcome = "ok"
		}
		log.Printf("commit_read session=%q patch=%t outcome=%s", req.SessionID, req.Patch, outcome)
		if bounded, ok := sender.(interface {
			SendCommitResult(protocol.CommitResult) error
		}); ok {
			_ = bounded.SendCommitResult(res)
		} else {
			_ = sender.Send(res)
		}
	}
	release, ok := acquireCommitRead(req.SessionID)
	if !ok {
		go reply(protocol.CommitResult{ErrorCode: "busy"})
		return
	}
	go func() {
		defer release()
		bounded, cancel := context.WithTimeout(ctx, commitReadTimeout)
		defer cancel()
		reply(read(bounded))
	}()
}

// ReadCommit answers read_commit from an existing launch, without preparing an engine.
func (h *AgentHost) ReadCommit(ctx context.Context, req protocol.ReadCommit) {
	queueCommitReadUsing(ctx, h.sender, req, func(ctx context.Context) protocol.CommitResult {
		if result, handled := h.explorer.readGit(ctx, req, false); handled {
			return result
		}
		return readLegacyGit(ctx, req, false, h.workspaceFileBinding(req.SessionID))
	})
}

func (m *Manager) handleReadCommit(raw []byte) {
	var req protocol.ReadCommit
	if len(raw) > 1024 || json.Unmarshal(raw, &req) != nil {
		return
	}
	if m.agents != nil && m.agents.get(req.SessionID) != nil {
		m.agents.ReadCommit(context.Background(), req)
		return
	}
	queueCommitReadUsing(context.Background(), m.client, req, func(ctx context.Context) protocol.CommitResult {
		if result, handled := m.explorer.readGit(ctx, req, false); handled {
			return result
		}
		return readLegacyGit(ctx, req, false, m.workspaceFileBinding(req.SessionID))
	})
}

// SendCommitResult sends a reply (a diff of up to 2 MB) with a bounded write, like a workspace file's.
func (c *WSClient) SendCommitResult(result protocol.CommitResult) error {
	return c.sendBoundedWorkspaceReply(result, 10*time.Second)
}
