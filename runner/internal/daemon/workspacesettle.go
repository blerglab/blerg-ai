package daemon

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity, not an authentication primitive
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

const workspaceNestedRepositoryNotice = "A nested repository is kept; release it from Release storage."

// Settlement runs only in the capped copy worker after exact runtime absence.
// Git uses a new trusted gitdir/config, never the agent's config or hooks. No
// filter drivers are configured, so attributes cannot spawn agent commands.
func settleWorkspaceMember(ctx context.Context, m *workspacePrivateMember, session string, recorded ...string) (bool, error) {
	return settleWorkspaceRoot(ctx, m, nil, session, recorded...)
}

// settleWorkspaceRoot keeps the reserved paths out of a root member's snapshot.
// They come from the signed journal: the clone's own info/exclude is the agent's,
// and a path missing from this list would land in the repository as a gitlink.
func settleWorkspaceRoot(ctx context.Context, m *workspacePrivateMember, reserved []string, session string, recorded ...string) (bool, error) {
	if err := checkSandboxCloneTreeContext(ctx, m.Clone); err != nil {
		return false, err
	}
	m.Result.SyncNote = ""
	nestedRepository := false
	// Nested repositories are independent work, including ignored repositories
	// inside cache directories. Never infer disposability from their names.
	if err := filepath.WalkDir(m.Clone, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(m.Clone, path)
		if err != nil {
			return err
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		if m.Member.Path == "." && d.IsDir() && slices.Contains(reserved, rel) {
			return filepath.SkipDir
		}
		if d.Name() == ".git" {
			nestedRepository = true
			return filepath.SkipAll
		}
		return nil
	}); err != nil {
		return false, err
	}
	if nestedRepository {
		m.Result.SyncNote = workspaceNestedRepositoryNotice
		return false, nil
	}
	if err := resetCloneAlternates(m.Clone, &m.Source); err != nil {
		return false, err
	}
	refs, err := workspaceCloneRefs(ctx, m.Clone)
	if err != nil {
		return false, err
	}
	name := sandboxBranch(session)
	if len(recorded) > 0 {
		name = workbranch.Effective(session, recorded[0])
	}
	if !workbranch.Valid(name) {
		return false, errors.New("invalid session work branch")
	}
	branch := "refs/heads/" + name
	cleanRefs := m.InitialRefsDigest != "" && workspaceRefsDigest(refs, branch) == m.InitialRefsDigest
	safe, err := os.MkdirTemp("", "blerg-settle-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(safe) }()
	git := func(args ...string) (string, error) {
		argv := append([]string{"--git-dir=" + safe, "--work-tree=" + m.Clone}, args...)
		return hostgit.Output(ctx, hostgit.Options{Dir: m.Clone, Timeout: sandboxGitTimeout, Untrusted: true, Config: []string{"user.name=Blerg", "user.email=blerg@localhost", "commit.gpgsign=false", "core.excludesFile=/dev/null", "status.showUntrackedFiles=all", "diff.ignoreSubmodules=all"}}, argv...)
	}
	if _, err := hostgit.Output(ctx, hostgit.Options{Dir: safe, Timeout: sandboxGitTimeout}, "init", "-q", "--bare", safe); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(safe, "HEAD"), []byte("ref: "+branch+"\n"), 0o600); err != nil {
		return false, err
	}
	var exclude strings.Builder
	if m.Member.Path == "." {
		for _, name := range reserved {
			exclude.WriteString("/" + name + "/\n")
		}
	}
	if err := os.MkdirAll(filepath.Join(safe, "info"), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(safe, "info", "exclude"), []byte(exclude.String()), 0o600); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(safe, filepath.FromSlash(branch))), 0o700); err != nil {
		return false, err
	}
	if head, ok := readCloneRef(m.Clone, branch); ok {
		if err := os.WriteFile(filepath.Join(safe, filepath.FromSlash(branch)), []byte(head+"\n"), 0o600); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Join(safe, "objects", "info"), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(safe, "objects", "info", "alternates"), []byte(filepath.Join(m.Clone, ".git", "objects")+"\n"), 0o600); err != nil {
		return false, err
	}
	if head, ok := readCloneRef(m.Clone, branch); ok && head != "" {
		if _, err := git("read-tree", head); err != nil {
			return false, err
		}
	}
	if _, err := git("add", "-A"); err != nil {
		return false, err
	}
	if _, err := git("diff", "--cached", "--quiet"); err != nil {
		if _, err := git("commit", "-q", "--no-verify", "-m", sandboxSnapshotMessage); err != nil {
			return false, err
		}
	}
	head, err := git("rev-parse", "--verify", "HEAD")
	if err != nil {
		return false, err
	}
	if err := filepath.WalkDir(filepath.Join(safe, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Join(safe, "objects"), path)
		if err != nil {
			return err
		}
		if relative == "info/alternates" {
			return nil
		}
		target := filepath.Join(m.Clone, ".git", "objects", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return workspaceCopyFile(path, target, true)
	}); err != nil {
		return false, err
	}
	// Replace through fresh private files, never follow an agent-owned target.
	root, err := os.OpenRoot(filepath.Join(m.Clone, ".git"))
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	if err := workspaceCopyFile(filepath.Join(safe, "index"), filepath.Join(m.Clone, ".git", "index.blerg-settle"), false); err != nil {
		return false, err
	}
	if err := root.Rename("index.blerg-settle", "index"); err != nil {
		return false, err
	}
	temp := branch + ".blerg-settle"
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	_, writeErr := f.WriteString(head + "\n")
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return false, err
	}
	if err := root.Rename(temp, branch); err != nil {
		return false, err
	}
	status, err := git("status", "--porcelain", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil || status != "" {
		return false, err
	}
	if err := workspaceVerifyTreeBytes(ctx, m.Clone, head, git); err != nil {
		return false, err
	}
	// Reserved paths are not ignored work: they are other members, settled apart.
	ignored, err := git("ls-files", "--others", "--ignored", "--exclude-per-directory=.gitignore", "-z")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Split(ignored, "\x00") {
		if name == "" {
			continue
		}
		if !workspaceRegenerableCache(name) {
			return false, nil
		}
		info, err := os.Lstat(filepath.Join(m.Clone, filepath.FromSlash(name)))
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
	}
	return cleanRefs, nil
}
func workspaceCopyFile(source, target string, allowExisting bool) error {
	from, err := os.Open(source) //nolint:gosec // daemon-created safe gitdir inside the isolated worker
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // fixed relative paths under validated private worker trees; exclusive create
	if os.IsExist(err) && allowExisting {
		return nil
	}
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(to, from)
	syncErr := to.Sync()
	closeErr := to.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}
func workspaceCloneRefs(ctx context.Context, clone string) (map[string]string, error) {
	refs := map[string]string{}
	err := filepath.WalkDir(filepath.Join(clone, ".git", "refs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filepath.Join(clone, ".git"), path)
		if err != nil {
			return err
		}
		raw, ok := readCloneFile(clone, filepath.ToSlash(relative), 512)
		if !ok {
			return errors.New("ref unavailable")
		}
		refs[filepath.ToSlash(relative)] = strings.TrimSpace(raw)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if raw, ok := readCloneFile(clone, "packed-refs", 4<<20); ok {
		for _, line := range strings.Split(raw, "\n") {
			value, name, found := strings.Cut(line, " ")
			if found && strings.HasPrefix(name, "refs/") {
				if _, exists := refs[name]; !exists {
					refs[name] = value
				}
			}
		}
	}
	return refs, nil
}

func workspaceRefsDigest(refs map[string]string, branch string) string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		if name != branch {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	hash := sha256.New()
	for _, name := range names {
		_, _ = hash.Write([]byte(name + "\x00" + refs[name] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// Prove raw saved bytes rather than trusting an index stat cache, ignores or
// attribute conversions. The fixed gitdir reads only the final object tree.
func workspaceVerifyTreeBytes(ctx context.Context, clone, head string, git func(...string) (string, error)) error {
	tree, err := git("ls-tree", "-rz", "--full-tree", head)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(clone)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" {
			if err := workspaceVerifyGitlink(root, name); err != nil {
				return err
			}
			continue
		}
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return errors.New("snapshot contains unsupported tree entry")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		var h hash.Hash
		switch len(fields[2]) {
		case 40:
			h = sha1.New() //nolint:gosec // Git SHA-1 object identity, not an authentication primitive
		case 64:
			h = sha256.New()
		default:
			return errors.New("invalid snapshot object identity")
		}
		switch {
		case fields[0] == "120000" && info.Mode()&os.ModeSymlink != 0:
			value, err := root.Readlink(name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "blob %d%c%s", len(value), 0, value)
		case (fields[0] == "100644" || fields[0] == "100755") && info.Mode().IsRegular():
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "blob %d%c", info.Size(), 0)
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		default:
			return errors.New("snapshot path type changed")
		}
		if hex.EncodeToString(h.Sum(nil)) != fields[2] {
			return errors.New("snapshot does not preserve exact working file bytes; retained")
		}
	}
	return nil
}

// Read the fetched objects themselves through a fresh trusted gitdir. A matching
// ref/OID alone is insufficient if an existing source object is corrupt. Stream
// each blob against the retained checkout; never normalize or trim its bytes.
func workspaceVerifyFetchedBytes(ctx context.Context, m workspacePrivateMember, head string) error {
	safe, err := os.MkdirTemp("", "blerg-saved-tree-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(safe) }()
	if _, err := hostgit.Output(ctx, hostgit.Options{Dir: safe, Timeout: sandboxGitTimeout}, "init", "-q", "--bare", safe); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(safe, "objects", "info"), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(safe, "objects", "info", "alternates"), []byte(m.Source.Objects+"\n"), 0600); err != nil {
		return err
	}
	options := hostgit.Options{Dir: safe, Timeout: sandboxGitTimeout, Untrusted: true}
	tree, err := hostgit.Output(ctx, options, "--git-dir="+safe, "ls-tree", "-rz", "--full-tree", head)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(m.Clone)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" {
			if err := workspaceVerifyGitlink(root, name); err != nil {
				return err
			}
			continue
		}
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return errors.New("saved tree entry unsupported")
		}
		var reader io.Reader
		var file *os.File
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		switch {
		case fields[0] == "120000" && info.Mode()&os.ModeSymlink != 0:
			value, err := root.Readlink(name)
			if err != nil {
				return err
			}
			reader = strings.NewReader(value)
		case (fields[0] == "100644" || fields[0] == "100755") && info.Mode().IsRegular():
			file, err = root.Open(name)
			if err != nil {
				return err
			}
			reader = file
		default:
			return errors.New("saved tree path type changed")
		}
		compare := &workspaceByteComparer{reader: reader, file: file}
		options.Stdout = compare
		runErr := hostgit.Run(ctx, options, "--git-dir="+safe, "cat-file", "blob", fields[2])
		var extra [1]byte
		n, endErr := reader.Read(extra[:])
		if file != nil {
			_ = file.Close()
		}
		if runErr != nil || n != 0 || endErr != io.EOF {
			return errors.New("fetched tree does not hold exact saved bytes; retained")
		}
	}
	return nil
}

// Uninitialized submodules contain no working bytes. A populated submodule is
// separate work and must keep the allocation, even if its gitlink was saved.
func workspaceVerifyGitlink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("gitlink path contains local work; retained")
	}
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(1)
	if len(entries) == 0 && errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("populated submodule contains local work; retained")
}

type workspaceByteComparer struct {
	reader io.Reader // Only the retained regular file or an in-memory symlink value.
	file   *os.File
}

func (w *workspaceByteComparer) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if w.file != nil {
		closed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = w.file.Close(); close(closed) })
		defer func() {
			if !stop() {
				<-closed
			}
		}()
	}
	return w.Write(p)
}

func (w *workspaceByteComparer) Write(p []byte) (int, error) {
	buffer := make([]byte, len(p))
	if _, err := io.ReadFull(w.reader, buffer); err != nil {
		return 0, err
	}
	if !bytes.Equal(p, buffer) {
		return 0, errors.New("saved blob bytes differ")
	}
	return len(p), nil
}

// Cache names alone do not authorize deletion. Only regular ignored files
// matching these documented tool layouts may be discarded automatically.
var workspaceMypyCacheFile = regexp.MustCompile(`^[0-9]+\.[0-9]+/(?:@plugins_snapshot\.json|(?:[A-Za-z_][A-Za-z0-9_]*/)*[A-Za-z_][A-Za-z0-9_]*\.(?:data|meta)\.json)$`)
var workspaceRuffCacheFile = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$`)
var workspaceNodeCacheFile = regexp.MustCompile(`^(?:babel-loader/[0-9a-f]{32,64}\.json|webpack/[A-Za-z0-9_.-]+/(?:index|[0-9]+)\.pack(?:\.gz|\.br)?(?:\.old)?)$`)

func workspaceRegenerableCache(name string) bool {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		rest := strings.Join(parts[i+1:], "/")
		switch part {
		case "__pycache__":
			return i+2 == len(parts) && (strings.HasSuffix(rest, ".pyc") || strings.HasSuffix(rest, ".pyo"))
		case ".pytest_cache":
			return slices.Contains([]string{".gitignore", "CACHEDIR.TAG", "README.md", "v/cache/nodeids", "v/cache/lastfailed", "v/cache/stepwise"}, rest)
		case ".mypy_cache":
			return rest == ".gitignore" || rest == "CACHEDIR.TAG" || workspaceMypyCacheFile.MatchString(rest)
		case ".ruff_cache":
			return rest == ".gitignore" || rest == "CACHEDIR.TAG" || workspaceRuffCacheFile.MatchString(rest)
		case "node_modules":
			if i+1 < len(parts) && parts[i+1] == ".cache" {
				return workspaceNodeCacheFile.MatchString(strings.Join(parts[i+2:], "/"))
			}
		}
	}
	return false
}
