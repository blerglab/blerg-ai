package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type workspaceStatusDiagnostic func(stage string, err error, stderr string)
type workspaceStatusDiagnosticKey struct{}

func (e *LocalExplorer) diagnoseStatus(ctx context.Context, stage string, err error, stderr string) {
	diagnostic := e.statusDiagnostic
	if sink, ok := ctx.Value(workspaceStatusDiagnosticKey{}).(workspaceStatusDiagnostic); ok {
		diagnostic = sink
	}
	if diagnostic != nil {
		diagnostic(stage, err, stderr)
	}
}

// status is invoked in the same runtime as traversal. The daemon never calls
// this on a sandbox clone: that adapter must invoke its in-container helper.
func (e *LocalExplorer) status(ctx context.Context, req protocol.WorkspaceExplorerRequest, name string) protocol.WorkspaceExplorerResult {
	res := explorerFailure(req, "unavailable")
	stderr := &cappedOutput{max: 4 << 10}
	fail := func(stage string, err error) protocol.WorkspaceExplorerResult {
		if err == nil {
			err = errors.New("status safety check failed")
		}
		e.diagnoseStatus(ctx, stage, err, stderr.buf.String())
		return res
	}
	memberID := ""
	for _, m := range e.manifest.Members {
		if m.Path == name && (req.MemberID == "" || req.MemberID == m.ID) {
			memberID = m.ID
			break
		}
	}
	if memberID == "" && (e.manifest.Runtime != "host" || req.MemberID != "") {
		res.ErrorCode = "forbidden"
		return res
	}
	e.mu.Lock()
	if e.statusBusy {
		e.mu.Unlock()
		res.ErrorCode = "busy"
		return res
	}
	e.statusBusy = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.statusBusy = false; e.mu.Unlock() }()
	file, err := e.openDirectory(name)
	if err != nil {
		return fail("open member", err)
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil || !info.IsDir() {
		return fail("stat member", err)
	}
	p, ok := pinGitDir(filepath.Join(e.folder, name), info)
	if !ok {
		return fail("pin Git cwd", os.ErrPermission)
	}
	defer p.close()
	p.memoryLimitKB = 256 << 10
	// Accept an in-tree .git directory, or a registered host worktree whose
	// external metadata descriptors were pinned at preparation. Request-time
	// pointers never establish new authority.
	gitDir, err := e.openDirectory(path.Join(name, ".git"))
	if err != nil {
		return fail("open Git metadata", err)
	}
	gitInfo, err := gitDir.Stat()
	_ = gitDir.Close()
	worktree, isWorktree := e.worktrees[memberID]
	if err != nil || (!gitInfo.IsDir() && !isWorktree) {
		return fail("stat Git metadata/worktree pin", err)
	}
	if memberID != "" {
		if workspaceRootIdentity(info) != e.memberRoots[memberID] || workspaceRootIdentity(gitInfo) != e.gitRoots[memberID] {
			return fail("member/Git identity", os.ErrPermission)
		}
	} else {
		e.mu.Lock()
		discovery, ok := e.discoveries[name]
		e.mu.Unlock()
		if !ok || workspaceRootIdentity(info) != discovery.root || workspaceRootIdentity(gitInfo) != discovery.git {
			return fail("discovered identity", os.ErrPermission)
		}
	}
	pin, pinned := e.gitPins[memberID]
	if pinned && (workspaceRootIdentity(gitInfo) != pin.GitRoot || !validWorkspaceObjectPin(pin)) {
		return fail("object pin", os.ErrPermission)
	}
	guard := func() (string, bool) {
		if isWorktree {
			public, relative, _ := e.anchorFor(name)
			if public == nil || !worktree.publicMatches(public, relative) {
				return "", false
			}
			return worktree.check(ctx)
		}
		return e.statusMetadata(ctx, path.Join(name, ".git"), pin.Alternates)
	}
	fingerprint, safe := guard()
	if !safe {
		return fail("metadata guard", os.ErrPermission)
	}
	// Cached counts may be stale for five seconds, but switched Git metadata is
	// never accepted merely because there is a cached answer.
	e.mu.Lock()
	cached, cachedOK := e.cache[name]
	e.mu.Unlock()
	if cachedOK && cached.fingerprint == fingerprint && time.Now().Before(cached.until) {
		cached.result.RequestID = req.RequestID
		return cached.result
	}
	out := &cappedOutput{max: protocol.WorkspaceExplorerReplyBytes, strict: true}
	snapshot, err := e.statusSnapshot(ctx, name, worktree, isWorktree)
	if err != nil {
		return fail("private snapshot", err)
	}
	defer snapshot.close()
	options := snapshot.options(p, out)
	options.Stderr = stderr
	err = hostgit.Run(ctx, options, "--no-optional-locks", "status", "--porcelain=v2", "-z", "--branch", "--no-renames", "--untracked-files=normal", "--ignore-submodules=all")
	if err != nil || out.over {
		return fail("Git status", err)
	}
	status, err := parseWorkspaceStatus(out.buf.String())
	if err != nil {
		return fail("parse Git status", err)
	}
	if snapshot.ledger != nil {
		if err := snapshot.ledger.reserve(ctx, sessionGitUsage{}); err != nil {
			return fail("snapshot ledger", err)
		}
	}
	// Do not deliver status from switched metadata or a moved member root.
	final, err := e.openDirectory(path.Join(name, ".git"))
	if err != nil {
		return fail("reopen Git metadata", err)
	}
	finalInfo, err := final.Stat()
	_ = final.Close()
	finalFingerprint, safe := guard()
	if err != nil || !os.SameFile(gitInfo, finalInfo) || (pinned && !validWorkspaceObjectPin(pin)) || !safe || finalFingerprint != fingerprint {
		return fail("final metadata guard", err)
	}
	status.Path = req.Path
	status.MemberID = memberID
	res.OK = true
	res.ErrorCode = ""
	res.Status = &status
	res.Truncated = status.Partial
	e.mu.Lock()
	e.cache[name] = explorerStatusCache{result: res, until: time.Now().Add(5 * time.Second), fingerprint: fingerprint}
	e.mu.Unlock()
	return res
}

type metadataBudget struct {
	reserve func(int64, int) error
	entries int
	bytes   int64
}

func (e *LocalExplorer) statusMetadata(ctx context.Context, dir, alternates string) (string, bool) {
	root, relative, _ := e.anchorFor(dir)
	if root == nil {
		return "", false
	}
	allowed := map[string]string{}
	if alternates != "" {
		allowed[path.Join(relative, "objects/info/alternates")] = alternates
	}
	return metadataTree(ctx, root, relative, allowed, "", &metadataBudget{entries: 200000})
}
func metadataTree(ctx context.Context, root *os.Root, dir string, allowed map[string]string, skip string, budget *metadataBudget) (string, bool) {
	hash := sha256.New()
	seen := map[string]bool{}
	var walk func(string, int) bool
	walk = func(name string, depth int) bool {
		if ctx.Err() != nil || depth > 32 {
			return false
		}
		file, err := workspaceOpenDirectory(root, name)
		if err != nil {
			return false
		}
		defer func() { _ = file.Close() }()
		before, err := file.Stat()
		if err != nil || !before.IsDir() {
			return false
		}
		var entries []os.DirEntry
		for {
			batch, err := file.ReadDir(128)
			budget.entries -= len(batch)
			if budget.entries < 0 {
				return false
			}
			entries = append(entries, batch...)
			if err != nil {
				if err != io.EOF {
					return false
				}
				break
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, ent := range entries {
			if ctx.Err() != nil {
				return false
			}
			child := path.Join(name, ent.Name())
			if skip != "" && child == skip {
				continue
			}
			expected, allow := allowed[child]
			switch ent.Name() {
			case "commondir", "gitdir", "alternates", "http-alternates":
				if !allow {
					return false
				}
			}
			f, err := workspaceOpenFile(root, child)
			if err != nil {
				return false
			}
			st, err := f.Stat()
			if err == nil && allow {
				if !workspaceRegularFile(st) {
					_ = f.Close()
					return false
				}
				raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
				if readErr != nil || len(raw) > 4096 || string(raw) != expected {
					_ = f.Close()
					return false
				}
				seen[child] = true
			}
			_ = f.Close()
			if err != nil {
				return false
			}
			_, _ = hash.Write([]byte(child + "\x00" + workspaceMetadataFingerprint(st) + "\x00"))
			if st.IsDir() {
				if !walk(child, depth+1) {
					return false
				}
			} else {
				if !workspaceRegularFile(st) {
					return false
				}
				budget.bytes += st.Size()
				if budget.bytes > 256<<20 {
					return false
				}
			}
		}
		after, err := file.Stat()
		if err != nil || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(after) {
			return false
		}
		_, _ = hash.Write([]byte(name + "\x00" + workspaceMetadataFingerprint(after) + "\x00"))
		return true
	}
	safe := walk(dir, 0)
	for name := range allowed {
		if !seen[name] {
			safe = false
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), safe
}

// Porcelain v2 -z has fixed ASCII prefixes and opaque binary-delimited paths.
// Never split a filename on whitespace, interpolate it into argv, or treat Git
// text as markup. Invalid records fail closed instead of implying clean.
func parseWorkspaceStatus(raw string) (protocol.WorkspaceStatus, error) {
	result := protocol.WorkspaceStatus{Paths: []string{}}
	seen := map[string]bool{}
	replyBytes := 0
	if raw != "" && !strings.HasSuffix(raw, "\x00") {
		return result, errors.New("unterminated status")
	}
	for _, record := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		if strings.HasPrefix(record, "# branch.head ") {
			branch := strings.TrimPrefix(record, "# branch.head ")
			if !utf8.ValidString(branch) || len(branch) > 1024 {
				return result, errors.New("invalid branch status")
			}
			result.Branch = branch
			continue
		}
		if strings.HasPrefix(record, "# ") {
			continue
		}
		var filename string
		code := "M"
		untracked := false
		switch {
		case strings.HasPrefix(record, "1 "):
			fields := strings.SplitN(record, " ", 9)
			if len(fields) != 9 {
				return result, errors.New("invalid tracked status")
			}
			filename = fields[8]
			xy := fields[1]
			if len(xy) != 2 || strings.ContainsAny(xy, "\x00\n") || strings.Trim(xy, ".MADRCUT") != "" {
				return result, errors.New("invalid tracked status code")
			}
			switch {
			case strings.Contains(xy, "U"):
				code = "C"
			case strings.Contains(xy, "D"):
				code = "D"
			case strings.Contains(xy, "A"):
				code = "A"
			case strings.Contains(xy, "T"):
				code = "T"
			case strings.Contains(xy, "R"):
				code = "R"
			}

		case strings.HasPrefix(record, "u "):
			fields := strings.SplitN(record, " ", 11)
			if len(fields) != 11 {
				return result, errors.New("invalid unmerged status")
			}
			filename = fields[10]
			code = "C"
		case strings.HasPrefix(record, "? "):
			filename = record[2:]
			untracked = true
			code = "U"
		case strings.HasPrefix(record, "! "):
			continue
		default:
			return result, errors.New("unsupported status record")
		}
		directory := untracked && strings.HasSuffix(filename, "/")
		filename = strings.TrimSuffix(filename, "/")
		if filename == "" || !utf8.ValidString(filename) || len(filename) > 4096 || path.Clean(filename) != filename || path.IsAbs(filename) || filename == ".." || strings.HasPrefix(filename, "../") || strings.Contains(filename, "\\") || (!directory && !workspacePublicPath(filename)) || (directory && !workspacePublicDirectoryPath(filename)) {
			continue
		}
		if seen[filename] {
			continue
		}
		seen[filename] = true
		if len(result.Paths) >= protocol.WorkspaceSearchVisited {
			result.Partial = true
			break
		}
		file := protocol.WorkspacePathStatus{Path: filename, Code: code, Directory: directory}
		encodedPath, _ := json.Marshal(filename)
		encodedFile, _ := json.Marshal(file)
		replyBytes += len(encodedPath) + len(encodedFile) + 2
		if replyBytes > protocol.WorkspaceExplorerReplyBytes*3/4 {
			result.Partial = true
			break
		}
		result.Paths = append(result.Paths, filename)
		result.Files = append(result.Files, file)
		if untracked {
			result.Untracked++
		} else {
			result.Changed++
		}
	}
	return result, nil
}

func validWorkspaceObjectPin(pin WorkspaceGitPin) bool {
	target := strings.TrimSuffix(pin.Alternates, "\n")
	info, err := os.Lstat(target)
	return err == nil && info.IsDir() && workspaceRootIdentity(info) == pin.ObjectRoot
}
