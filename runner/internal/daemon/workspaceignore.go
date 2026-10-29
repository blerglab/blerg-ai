package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Used only when Git cannot supply ignore rules. Browsing never prunes these.
var workspaceHeavyDirectories = map[string]bool{
	"node_modules": true, ".git": true, ".hg": true, ".svn": true,
	".venv": true, "venv": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, ".cache": true, ".next": true, ".nuxt": true,
	".turbo": true, ".gradle": true, "target": true, "bower_components": true,
}

const workspaceIgnoreTimeout = time.Second

type workspaceIgnoreRules struct {
	root     string
	pin      *pinnedGitDir
	snapshot *workspaceStatusSnapshot
	ignored  map[string]bool
	tracked  map[string]bool // tracked ignored files and routes to independent roots
	children map[string][]string
	failed   bool
}
type workspaceIgnorePolicy struct {
	explorer *LocalExplorer
	rules    map[string]*workspaceIgnoreRules
	budget   *workspaceIgnoreBudget
	scope    string
	explicit *workspaceIgnoreRules
	roots    []string
}

// Shared across every member of a local or virtual request. Filesystem walking
// does not consume this allowance; only time spent preparing Git ignore data does.
type workspaceIgnoreBudget struct{ remaining time.Duration }
type workspaceIgnoreBudgetKey struct{}

func workspaceIgnoreRequest(ctx context.Context) context.Context {
	if ctx.Value(workspaceIgnoreBudgetKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, workspaceIgnoreBudgetKey{}, &workspaceIgnoreBudget{remaining: workspaceIgnoreTimeout})
}

func newWorkspaceIgnorePolicy(e *LocalExplorer) *workspaceIgnorePolicy {
	p := &workspaceIgnorePolicy{explorer: e, rules: map[string]*workspaceIgnoreRules{}}
	for _, member := range e.manifest.Members {
		p.roots = append(p.roots, member.Path)
	}
	// Previously browsed discoveries remain independent roots even when an outer
	// ignore rule hides their ancestors. Discovery never grants membership.
	e.mu.Lock()
	for root := range e.discoveries {
		p.roots = append(p.roots, root)
	}
	e.mu.Unlock()
	return p
}
func (p *workspaceIgnorePolicy) close() {
	for _, rules := range p.rules {
		if rules.snapshot != nil {
			rules.snapshot.close()
		}
		if rules.pin != nil {
			rules.pin.close()
		}
	}
}
func (p *workspaceIgnorePolicy) forDirectory(ctx context.Context, dir string) *workspaceIgnoreRules {
	// Prefer the innermost prepared member. Folder sessions also discover in-tree
	// repositories; never follow a request-time gitdir pointer outside these roots.
	root, id := "", ""
	for _, member := range p.explorer.manifest.Members {
		if (member.Path == "." || dir == member.Path || strings.HasPrefix(dir, member.Path+"/")) && len(member.Path) > len(root) {
			root, id = member.Path, member.ID
		}
	}
	// A legacy non-Git root member must not shadow discovered repositories.
	if p.explorer.manifest.Runtime == "host" {
		for candidate := dir; ; candidate = path.Dir(candidate) {
			file, err := p.explorer.openDirectory(path.Join(candidate, ".git"))
			if err == nil {
				info, err := file.Stat()
				_ = file.Close()
				if err == nil && info.IsDir() {
					if len(candidate) >= len(root) || root == "." {
						root = candidate
						id = ""
						for _, member := range p.explorer.manifest.Members {
							if member.Path == root {
								id = member.ID
								break
							}
						}
					}
					break
				}
			}
			if candidate == "." {
				break
			}
		}
	}
	if root == "" {
		return nil
	}
	if rules, ok := p.rules[root]; ok {
		return rules
	}
	rules := &workspaceIgnoreRules{root: root, failed: true}
	p.rules[root] = rules
	defer p.addRootRoutes(rules)
	// Leave time for the bounded filesystem fallback within Explore's deadline.
	if p.budget == nil {
		budget, ok := ctx.Value(workspaceIgnoreBudgetKey{}).(*workspaceIgnoreBudget)
		if !ok || budget == nil {
			budget = &workspaceIgnoreBudget{remaining: workspaceIgnoreTimeout}
		}
		p.budget = budget
	}
	if p.budget.remaining <= 0 {
		return rules
	}
	start := time.Now()
	gitCtx, cancel := context.WithTimeout(ctx, p.budget.remaining)
	defer cancel()
	defer func() { p.budget.remaining -= time.Since(start) }()
	if err := rules.prepare(gitCtx, p.explorer, id); err != nil {
		p.explorer.diagnoseStatus(ctx, "search ignore fallback", err, "")
	}
	return rules
}

// Retain just the next components leading to independent roots. The normal
// tracked-only reader confines and validates these opens, including on fallback.
func (p *workspaceIgnorePolicy) addRootRoutes(r *workspaceIgnoreRules) {
	if r.tracked == nil {
		r.tracked = map[string]bool{}
	}
	if r.children == nil {
		r.children = map[string][]string{}
	}
	for _, root := range p.roots {
		if root == r.root || r.root != "." && !strings.HasPrefix(root, r.root+"/") {
			continue
		}
		for name := r.relative(root); name != "."; name = path.Dir(name) {
			if r.tracked[name] {
				continue
			}
			r.tracked[name] = true
			parent := path.Dir(name)
			r.children[parent] = append(r.children[parent], name)
			sort.Strings(r.children[parent])
		}
	}
}
func (r *workspaceIgnoreRules) prepare(ctx context.Context, e *LocalExplorer, id string) error {
	file, err := e.openDirectory(r.root)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return err
	}
	if id != "" && workspaceRootIdentity(info) != e.memberRoots[id] {
		return os.ErrPermission
	}
	pin, ok := pinGitDir(filepath.Join(e.folder, r.root), info)
	if !ok {
		return os.ErrPermission
	}
	r.pin = pin
	git, err := e.openFile(path.Join(r.root, ".git"))
	if err != nil {
		return err
	}
	gitInfo, err := git.Stat()
	_ = git.Close()
	if err != nil {
		return err
	}
	if id != "" && workspaceRootIdentity(gitInfo) != e.gitRoots[id] {
		return os.ErrPermission
	}
	worktree, isWorktree := e.worktrees[id]
	objectPin := e.gitPins[id]
	if objectPin.GitRoot.Inode != 0 && (!validWorkspaceObjectPin(objectPin) || workspaceRootIdentity(gitInfo) != objectPin.GitRoot) {
		return os.ErrPermission
	}
	// Fingerprint only ignore inputs. Walking the object store here would make
	// preparation scale with repository history even though Git reads no objects.
	guard := func() (string, bool) {
		primary, relative, _ := e.anchorFor(path.Join(r.root, ".git"))
		common, commonRelative := primary, relative
		if isWorktree {
			anchor, rel, _ := e.anchorFor(r.root)
			if !worktree.publicMatches(anchor, rel) || !workspaceRootUnchanged(worktree.git, worktree.gitPath, worktree.gitInfo) || !workspaceRootUnchanged(worktree.common, worktree.commonPath, worktree.commonInfo) {
				return "", false
			}
			primary, relative, common, commonRelative = worktree.git, ".", worktree.common, "."
		} else {
			file, err := e.openDirectory(path.Join(r.root, ".git"))
			if err != nil {
				return "", false
			}
			info, err := file.Stat()
			_ = file.Close()
			if err != nil || !info.IsDir() || !os.SameFile(gitInfo, info) {
				return "", false
			}
		}
		var result strings.Builder
		for _, input := range []struct {
			root *os.Root
			name string
		}{
			{primary, path.Join(relative, "HEAD")}, {primary, path.Join(relative, "index")},
			{common, path.Join(commonRelative, "config")}, {common, path.Join(commonRelative, "info/exclude")},
		} {
			if ctx.Err() != nil {
				return "", false
			}
			file, err := workspaceOpenFile(input.root, input.name)
			if errors.Is(err, os.ErrNotExist) {
				result.WriteString("missing;")
				continue
			}
			if err != nil {
				return "", false
			}
			info, err := file.Stat()
			_ = file.Close()
			if err != nil || !workspaceRegularFile(info) {
				return "", false
			}
			result.WriteString(workspaceMetadataFingerprint(info))
			result.WriteByte(';')
		}
		return result.String(), true
	}
	fingerprint, safe := guard()
	if !safe {
		return os.ErrPermission
	}
	snapshot, err := e.statusSnapshotUsing(ctx, r.root, worktree, isWorktree, runtime.GOOS, true)
	if err != nil {
		return err
	}
	r.snapshot = snapshot
	// Status snapshots deliberately omit configuration. Copy only ignore data;
	// repository config (includes, filters, hooks) never reaches the Git reader.
	common, relative, _ := e.anchorFor(path.Join(r.root, ".git"))
	if isWorktree {
		common, relative = worktree.common, "."
	}
	if err := os.Mkdir(filepath.Join(snapshot.directory, "info"), 0700); err != nil {
		return err
	}
	reserve := func(bytes int64, entries int) error {
		if snapshot.ledger == nil {
			return nil
		}
		usage := sessionGitUsage{metadata: bytes, entries: entries}
		if err := snapshot.ledger.reserve(ctx, usage); err != nil {
			return err
		}
		snapshot.reservation.metadata += bytes
		snapshot.reservation.entries += entries
		return nil
	}
	if err := reserve(0, 1); err != nil {
		return err
	}
	// The shared copier caps metadata at 256 MiB; allow at most 2 MiB of
	// additional ignore data, charged to the runtime snapshot ledger.
	budget := &metadataBudget{entries: 4, bytes: 254 << 20, reserve: reserve}
	if err := copyStatusMetadataFile(ctx, common, path.Join(relative, "info/exclude"), filepath.Join(snapshot.directory, "info/exclude"), false, budget); err != nil {
		return err
	}
	if e.manifest.Runtime == "host" {
		if err := copyWorkspaceUserExcludes(ctx, snapshot.directory, budget, pin); err != nil {
			return err
		}
	}
	out := &cappedOutput{max: 8 << 20, strict: true}
	options := r.options(out)
	if err := hostgit.Run(ctx, options, "ls-files", "--cached", "-z"); err != nil || out.over {
		return errors.Join(err, errors.New("tracked ignore index unavailable"))
	}
	// Keep the bounded raw index until ignore discovery is complete. Only paths
	// beneath ignored entries need the tracked-only map; source trees do not.
	cached := out.buf.String()
	// After capturing tracked paths, omit the private index for ignore discovery.
	// This lets Git collapse ignored directories even when they contain tracked
	// files; the retained index still makes those files win during search.
	if err := os.Remove(filepath.Join(snapshot.directory, "index")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Collapse ignored directories up front: the bounded walk below never needs
	// a per-directory Git process and never enumerates ignored untracked children.
	out = &cappedOutput{max: 8 << 20, strict: true}
	if err := hostgit.Run(ctx, r.options(out), "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z", "--", "./"); err != nil || out.over {
		return errors.Join(err, errors.New("ignored paths unavailable"))
	}
	r.ignored = map[string]bool{}
	for name := range strings.SplitSeq(out.buf.String(), "\x00") {
		name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
		if clean, ok := explorerPath(name); ok && name != "" && clean == name {
			r.ignored[name] = true
		}
	}
	tracked := map[string]bool{}
	count := 0
	for name := range strings.SplitSeq(cached, "\x00") {
		if name == "" {
			continue
		}
		count++
		if count > 200000 {
			return errors.New("tracked ignore index exceeds entry limit")
		}
		if count%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if !r.isIgnored(name) {
			continue
		}
		if clean, ok := explorerPath(name); !ok || clean != name {
			continue
		}
		for name != "." && !tracked[name] {
			if len(tracked) >= 200000 {
				return errors.New("tracked ignore index exceeds entry limit")
			}
			tracked[name] = true
			name = path.Dir(name)
		}
	}
	after, valid := guard()
	if !valid || fingerprint != after || !e.memberAvailable(r.root) || !e.valid() {
		return os.ErrPermission
	}
	r.children = map[string][]string{}
	for name := range tracked {
		r.children[path.Dir(name)] = append(r.children[path.Dir(name)], name)
	}
	for parent := range r.children {
		sort.Strings(r.children[parent])
	}
	r.tracked = tracked
	r.failed = false
	return nil
}
func (r *workspaceIgnoreRules) options(out io.Writer) hostgit.Options {
	options := r.snapshot.options(r.pin, out)
	// Ignore snapshots deliberately have no object-store descriptor.
	options.ExtraFiles = []*os.File{r.pin.dir}
	for i, value := range options.Env {
		if strings.HasPrefix(value, "GIT_OBJECT_DIRECTORY=") {
			options.Env[i] = "GIT_OBJECT_DIRECTORY=" + filepath.Join(r.snapshot.directory, "objects")
		}
	}
	options.Timeout = workspaceIgnoreTimeout
	options.Config = append(options.Config, "core.excludesFile="+filepath.Join(r.snapshot.directory, "user-exclude"))
	return options
}

// Read only the user's ignore setting through hardened Git. Global configuration
// is consulted by `config`, never by the repository reader. Git handles includes
// and tilde expansion. GIT_CONFIG_GLOBAL explicitly selects the host config;
// other inherited GIT_* settings cannot inject overrides.
//
//nolint:gosec // G304/G703: reads only the trusted user's excludes path; writes use a private mode-0700 snapshot and fixed filename.
func copyWorkspaceUserExcludes(ctx context.Context, directory string, budget *metadataBudget, pin *pinnedGitDir) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	excludes := filepath.Join(xdg, "git/ignore")
	configs := []string{filepath.Join(xdg, "git/config"), filepath.Join(home, ".gitconfig")}
	if override, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		configs = []string{override}
	}
	for _, config := range configs {
		out := &cappedOutput{max: 8192, strict: true}
		err := hostgit.Run(ctx, hostgit.Options{Dir: pin.cwd, DirFile: pin.dir, ExtraFiles: []*os.File{pin.dir}, SafeDirectory: pin.resolved, Ceiling: filepath.Dir(pin.resolved), MemoryLimit: true, LimitKB: 256 << 10, Timeout: workspaceIgnoreTimeout, Env: []string{"HOME=" + home}, Stdout: out}, "config", "--file", config, "--includes", "--path", "--null", "--get", "core.excludesFile")
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			continue
		}
		if err != nil || out.over {
			return errors.Join(err, errors.New("user ignore configuration unavailable"))
		}
		value := strings.TrimSuffix(out.buf.String(), "\x00")
		excludes = value
	}
	if excludes == "" {
		return nil
	}
	// Git resolves relative exclude paths from the repository working directory.
	if !filepath.IsAbs(excludes) {
		excludes = filepath.Join(pin.resolved, excludes)
	}
	file, err := os.OpenFile(excludes, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("user excludes are not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("user excludes exceed read limit")
	}
	budget.bytes += int64(len(raw))
	budget.entries--
	if budget.bytes > 256<<20 || budget.entries < 0 {
		return errors.New("ignore data exceed read limit")
	}
	if err := budget.reserve(int64(len(raw)), 1); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "user-exclude"), raw, 0600)
}
func (r *workspaceIgnoreRules) relative(name string) string {
	if name == r.root {
		return "."
	}
	if r.root == "." {
		return name
	}
	return strings.TrimPrefix(name, r.root+"/")
}
func (r *workspaceIgnoreRules) isIgnored(name string) bool {
	for name != "." {
		if r.ignored[name] {
			return true
		}
		name = path.Dir(name)
	}
	return false
}
func (p *workspaceIgnorePolicy) annotate(ctx context.Context, dir string, rows []protocol.WorkspaceEntry) (*workspaceIgnoreRules, []bool) {
	rules := p.forDirectory(ctx, dir)
	ignored := make([]bool, len(rows))
	for i := range rows {
		row := &rows[i]
		// Chosen members and discovered repositories are independent search roots.
		if row.Repository || row.MemberID != "" {
			continue
		}
		if rules != nil && !rules.failed {
			name := rules.relative(row.Path)
			ignored[i] = rules != p.explicit && name != "." && rules.isIgnored(name) && (row.Directory || !rules.tracked[name])
			continue
		}
		relative := row.Path
		scope := p.scope
		// A nested independent root also resets inherited fallback exclusions.
		if rules != nil && (scope == "." || strings.HasPrefix(rules.root, scope+"/")) {
			scope = rules.root
		}
		if scope != "." {
			relative = strings.TrimPrefix(relative, scope+"/")
		}
		components := strings.Split(relative, "/")
		for index, component := range components {
			if workspaceHeavyDirectories[component] && (row.Directory || index < len(components)-1) {
				ignored[i] = true
				break
			}
		}
	}
	return rules, ignored
}

// An ignored directory is never enumerated. When the index names tracked
// descendants, open only those children through the same pinned file policy.
func (e *LocalExplorer) readTrackedDirectory(ctx context.Context, dir string, budget *int, rules *workspaceIgnoreRules) ([]protocol.WorkspaceEntry, bool, error) {
	if !e.memberAvailable(dir) {
		return nil, false, os.ErrPermission
	}
	listing, err := workspaceOpenListing(e, dir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = listing.close() }()
	before, err := listing.file.Stat()
	if err != nil || !before.IsDir() {
		return nil, false, os.ErrPermission
	}
	truncated := false
	var rows []protocol.WorkspaceEntry
	relative := rules.relative(dir)
	for _, name := range rules.children[relative] {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if *budget <= 0 {
			truncated = true
			break
		}
		*budget--
		child := path.Join(rules.root, name)
		if _, ok := explorerPath(child); !ok || !utf8.ValidString(child) {
			continue
		}
		file, err := listing.openChild(path.Base(child))
		if err != nil {
			continue
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.IsDir() && (!workspaceRegularFile(info) || workspaceSecretFile(info.Name())) {
			continue
		}
		row := protocol.WorkspaceEntry{Name: path.Base(child), Path: child, Directory: info.IsDir()}
		if !row.Directory {
			row.Size = info.Size()
		}
		e.decorateWorkspaceEntry(&row, info)
		rows = append(rows, row)
	}
	if listing.validate != nil && !listing.validate() {
		return nil, false, errExplorerStale
	}
	after, err := listing.file.Stat()
	if err != nil || !os.SameFile(before, after) || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(after) {
		return nil, false, errExplorerStale
	}
	named, err := e.openDirectory(dir)
	if err != nil {
		return nil, false, err
	}
	info, err := named.Stat()
	_ = named.Close()
	if err != nil || !os.SameFile(before, info) || workspaceMetadataFingerprint(before) != workspaceMetadataFingerprint(info) || !e.memberAvailable(dir) {
		return nil, false, errExplorerStale
	}
	return rows, truncated, nil
}
