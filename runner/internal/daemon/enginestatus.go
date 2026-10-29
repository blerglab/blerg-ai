package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Engine discovery after install: why each engine is or is not usable on this
// host, re-detected on every heartbeat and on demand (engine_check). Only
// facts leave the daemon (a status, where a CLI is, the PATH searched); the
// server writes the words a person reads. Nothing here reads a credential's
// value: logins are checked the same way DetectAvailable checks them.

const (
	maxSearchedPath   = 32
	maxPathEntryLen   = 512
	maxLoginPathItems = 64
	loginPathFile     = "login-path.json"
	loginShellTimeout = 10 * time.Second
	versionTimeout    = 5 * time.Second
	loginPathMarker   = "__BLERG_LOGIN_PATH__"
)

// engineMinVersions is the oldest CLI version Blerg drives, per engine. Empty
// means any version; an engine listed here is probed with `<cli> --version`
// (cached per binary) and reported "outdated" below it.
var engineMinVersions = map[string]string{}

// engineStatuses reports every known engine, Claude first, in the order the
// launch sheet lists them.
func engineStatuses(ctx context.Context) []protocol.EngineStatus {
	home := homeDir()
	out := []protocol.EngineStatus{claudeStatus(home)}
	for _, id := range engineOrder {
		out = append(out, registeredEngineStatus(ctx, engineRegistry[id], home))
	}
	return out
}

func claudeStatus(home string) protocol.EngineStatus {
	st := protocol.EngineStatus{Engine: "claude"}
	path, err := exec.LookPath(claudeEngineSpec.Binary)
	keyed := os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != ""
	switch {
	case err != nil && os.Getenv("ANTHROPIC_API_KEY") != "":
		st.Status, st.Detail = protocol.EngineAvailable, "api_key_only"
	case err != nil:
		notInstalled(&st, claudeEngineSpec.Binary, home)
	case claudeAvailable() || keyed:
		st.Status, st.FoundAt = protocol.EngineAvailable, tildePath(path, home)
	case runtime.GOOS == "darwin":
		// The login lives in the keychain, which a service cannot read: not claimed missing.
		st.Status, st.Detail, st.FoundAt = protocol.EngineAvailable, "keychain", tildePath(path, home)
	default:
		st.Status, st.FoundAt = protocol.EngineNotLoggedIn, tildePath(path, home)
	}
	return st
}

func registeredEngineStatus(ctx context.Context, spec EngineSpec, home string) protocol.EngineStatus {
	st := protocol.EngineStatus{Engine: spec.ID}
	path, err := exec.LookPath(spec.Binary)
	if err != nil {
		notInstalled(&st, spec.Binary, home)
		return st
	}
	st.FoundAt = tildePath(path, home)
	st.Status, st.Detail = engineSetupState(spec.ID, home)
	if st.Status == protocol.EngineAvailable {
		if minimum := engineMinVersions[spec.ID]; minimum != "" {
			if version := cliVersion(ctx, path); version != "" {
				st.Version = version
				if compareVersionStrings(version, minimum) < 0 {
					st.Status, st.MinVersion = protocol.EngineOutdated, minimum
				}
			}
		}
	}
	return st
}

// engineSetupState is a found CLI's login/config state, by the same files
// DetectAvailable reads (so "available" here is exactly availableEngines).
func engineSetupState(id, home string) (status, detail string) {
	if home == "" {
		return protocol.EngineNotLoggedIn, ""
	}
	switch id {
	case "codex":
		if !nonEmptyFile(filepath.Join(engineHome(home, "CODEX_HOME", ".codex"), "auth.json")) {
			return protocol.EngineNotLoggedIn, ""
		}
	case "hermes":
		if !hermesAlreadyConfigured() {
			return protocol.EngineMisconfigure, "no_provider"
		}
	case "openclaw":
		if !openclawAlreadyConfigured() {
			return protocol.EngineMisconfigure, "not_onboarded"
		}
	}
	return protocol.EngineAvailable, ""
}

func notInstalled(st *protocol.EngineStatus, binary, home string) {
	st.Status = protocol.EngineNotInstalled
	st.SearchedPath = searchedPath(home)
	if found := findOffPath(binary, home); found != "" {
		st.Detail, st.FoundAt = "found_elsewhere", tildePath(found, home)
	}
}

// searchedPath is the daemon's PATH as a person reads it: home as "~",
// unprintable entries dropped, bounded.
func searchedPath(home string) []string {
	out := []string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if len(out) == maxSearchedPath {
			break
		}
		if cleanPathEntry(dir) {
			out = append(out, tildePath(dir, home))
		}
	}
	return out
}

// offPathDirs are the folders the usual installers put an engine CLI in
// that a service's PATH lacks: nvm, a custom npm prefix, Volta, Bun, pnpm,
// asdf and mise shims, Homebrew (Apple Silicon and Linux), ~/.local/bin.
func offPathDirs(home string) []string {
	var dirs []string
	if home != "" {
		if prefix := npmPrefix(home); prefix != "" {
			dirs = append(dirs, filepath.Join(prefix, "bin"))
		}
		dirs = append(dirs, nvmBinDirs(home)...)
		for _, rel := range []string{".local/bin", "bin", ".npm-global/bin", ".volta/bin", ".bun/bin",
			".local/share/pnpm", ".asdf/shims", ".local/share/mise/shims", ".cargo/bin", ".linuxbrew/bin"} {
			dirs = append(dirs, filepath.Join(home, rel))
		}
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin")
}

// findOffPath is where binary is in a well-known install folder, or "".
func findOffPath(binary, home string) string {
	for _, dir := range offPathDirs(home) {
		candidate := filepath.Join(dir, binary)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

// nvmBinDirs lists nvm's node versions' bin folders, newest first, bounded.
func nvmBinDirs(home string) []string {
	root := os.Getenv("NVM_DIR")
	if root == "" || !filepath.IsAbs(root) {
		root = filepath.Join(home, ".nvm")
	}
	entries, err := os.ReadDir(filepath.Join(root, "versions", "node"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return compareVersionStrings(strings.TrimPrefix(names[i], "v"), strings.TrimPrefix(names[j], "v")) > 0
	})
	if len(names) > 16 {
		names = names[:16]
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(root, "versions", "node", n, "bin"))
	}
	return out
}

var npmPrefixLine = regexp.MustCompile(`(?m)^\s*prefix\s*=\s*(\S+)\s*$`)

// npmPrefix is the global prefix set in ~/.npmrc ("prefix=~/.npm-global"), or "".
func npmPrefix(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".npmrc")) //nolint:gosec // the owner's own npm config, read for one non-secret line
	if err != nil || len(raw) > 64<<10 {
		return ""
	}
	m := npmPrefixLine.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	prefix := strings.Trim(string(m[1]), `"'`)
	if strings.HasPrefix(prefix, "~/") {
		prefix = filepath.Join(home, prefix[2:])
	} else if strings.HasPrefix(prefix, "${HOME}/") || strings.HasPrefix(prefix, "$HOME/") {
		prefix = filepath.Join(home, prefix[strings.Index(prefix, "/")+1:])
	}
	if !filepath.IsAbs(prefix) {
		return ""
	}
	return filepath.Clean(prefix)
}

// tildePath writes a path under home as "~/...".
func tildePath(path, home string) string {
	if home != "" && home != "/" {
		if path == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return path
}

var pathEntryChars = regexp.MustCompile(`^[A-Za-z0-9/._+@,~-]+$`)

// cleanPathEntry admits one PATH folder: absolute, bounded, printable, no
// quoting or expansion characters (so it is safe in a service unit, a plist
// and a shell line alike), no parent references.
func cleanPathEntry(dir string) bool {
	return strings.HasPrefix(dir, "/") && len(dir) <= maxPathEntryLen && pathEntryChars.MatchString(dir) &&
		!strings.Contains("/"+dir+"/", "/../") && !strings.Contains(dir, "//")
}

// mergePath puts first's folders ahead of rest's, each once, dropping
// unclean ones, bounded to maxLoginPathItems.
func mergePath(first, rest []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{first, rest} {
		for _, dir := range list {
			dir = strings.TrimRight(dir, "/")
			if dir == "" || seen[dir] || !cleanPathEntry(dir) || len(out) == maxLoginPathItems {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

// loginShell is the owner's login shell: $SHELL, else their passwd entry,
// else /bin/sh.
func loginShell(ctx context.Context) string {
	if s := os.Getenv("SHELL"); filepath.IsAbs(s) && isExecutable(s) {
		return s
	}
	if raw, err := os.ReadFile("/etc/passwd"); err == nil {
		uid := fmt.Sprint(os.Getuid())
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Split(line, ":")
			if len(f) == 7 && f[2] == uid && filepath.IsAbs(f[6]) && isExecutable(f[6]) {
				return f[6]
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", homeDir(), "UserShell").Output(); err == nil { //nolint:gosec // fixed tool, the owner's own record
			if _, s, ok := strings.Cut(strings.TrimSpace(string(out)), "UserShell: "); ok && filepath.IsAbs(s) && isExecutable(s) {
				return s
			}
		}
	}
	return "/bin/sh"
}

func isExecutable(path string) bool {
	info, err := os.Stat(path) //nolint:gosec // a shell path from $SHELL or the passwd entry, only stat'ed
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// loginShellCommand is overridden by tests.
var loginShellCommand = func(ctx context.Context, shell string, interactive bool) ([]byte, error) {
	flags := "-lc"
	if interactive {
		flags = "-ilc"
	}
	script := fmt.Sprintf(`printf '\n%s%%s%s\n' "$PATH"`, loginPathMarker, loginPathMarker)
	cmd := exec.CommandContext(ctx, shell, flags, script) //nolint:gosec // the owner's own login shell, a fixed script
	// Only what a login shell needs: never the daemon's token or session identity.
	env := []string{"TERM=dumb"}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TMPDIR", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	cmd.Env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	cmd.Stdin = nil
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// captureLoginPath reads the owner's login-shell PATH: an interactive login
// shell first (nvm and friends are usually loaded from the rc file), then a
// plain login shell. Output around the markers (a banner, prompts) is ignored.
func captureLoginPath(ctx context.Context) ([]string, error) {
	shell := loginShell(ctx)
	var lastErr error
	for _, interactive := range []bool{true, false} {
		run, cancel := context.WithTimeout(ctx, loginShellTimeout)
		out, err := loginShellCommand(run, shell, interactive)
		cancel()
		if dirs, ok := parseMarkedPath(out); ok {
			return mergePath(dirs, nil), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("it printed no PATH")
	}
	return nil, fmt.Errorf("could not read the PATH of your login shell (%s): %w", filepath.Base(shell), lastErr)
}

func parseMarkedPath(out []byte) ([]string, bool) {
	s := string(out)
	start := strings.LastIndex(s, loginPathMarker)
	if start < 0 {
		return nil, false
	}
	before := s[:start]
	open := strings.LastIndex(before, loginPathMarker)
	if open < 0 {
		return nil, false
	}
	value := before[open+len(loginPathMarker):]
	if value == "" || len(value) > 64<<10 {
		return nil, false
	}
	return filepath.SplitList(value), true
}

type savedLoginPath struct {
	Path []string `json:"path"`
}

// redetectPath reads the login-shell PATH, puts it ahead of the daemon's own
// in this process (every later CLI lookup and session start sees it, no
// restart) and saves it in stateDir so the next start applies it again.
// It returns the folders that were not on the daemon's PATH before; saveErr
// says the PATH applies now but could not be saved (a restart loses it).
func redetectPath(ctx context.Context, stateDir string) (added []string, saveErr, err error) {
	captured, err := captureLoginPath(ctx)
	if err != nil {
		return nil, nil, err
	}
	current := filepath.SplitList(os.Getenv("PATH"))
	had := map[string]bool{}
	for _, d := range current {
		had[strings.TrimRight(d, "/")] = true
	}
	for _, d := range captured {
		if !had[d] {
			added = append(added, d)
		}
	}
	if err := os.Setenv("PATH", strings.Join(mergePath(captured, current), string(os.PathListSeparator))); err != nil {
		return nil, nil, err
	}
	return added, saveLoginPath(stateDir, captured), nil
}

// saveLoginPath writes the re-detected PATH where the next start reads it.
// The installer (install, refresh-path) rewrites the same file with what it
// captured, so whichever captured last wins.
func saveLoginPath(stateDir string, dirs []string) error {
	if stateDir == "" {
		return errors.New("the daemon has no state folder")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(savedLoginPath{Path: dirs})
	if err != nil {
		return err
	}
	tmp := filepath.Join(stateDir, loginPathFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(stateDir, loginPathFile)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ApplySavedLoginPath puts a PATH saved by Re-detect PATH ahead of the
// service's own at daemon start. A missing or unreadable file changes nothing.
func ApplySavedLoginPath(stateDir string) {
	if stateDir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, loginPathFile)) //nolint:gosec // the daemon's own state file
	if err != nil || len(raw) > 64<<10 {
		return
	}
	var saved savedLoginPath
	if json.Unmarshal(raw, &saved) != nil || len(saved.Path) == 0 {
		return
	}
	merged := mergePath(saved.Path, filepath.SplitList(os.Getenv("PATH")))
	if err := os.Setenv("PATH", strings.Join(merged, string(os.PathListSeparator))); err != nil {
		log.Printf("apply saved login PATH: %v", err)
	}
}

var cliVersionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

type versionCacheEntry struct {
	mod     time.Time
	size    int64
	version string
}

var versionCache = struct {
	sync.Mutex
	byPath map[string]versionCacheEntry
}{byPath: map[string]versionCacheEntry{}}

// cliVersion is `<path> --version`'s first dotted number, cached until the
// binary changes; "" when it cannot tell.
func cliVersion(parent context.Context, path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	versionCache.Lock()
	hit, ok := versionCache.byPath[path]
	versionCache.Unlock()
	if ok && hit.mod.Equal(info.ModTime()) && hit.size == info.Size() {
		return hit.version
	}
	ctx, cancel := context.WithTimeout(parent, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = sanitizedEnviron()
	out, _ := cmd.Output()
	version := cliVersionPattern.FindString(string(out[:min(len(out), 4096)]))
	versionCache.Lock()
	versionCache.byPath[path] = versionCacheEntry{mod: info.ModTime(), size: info.Size(), version: version}
	versionCache.Unlock()
	return version
}

// engineCheck answers an engine_check: Re-detect PATH first when asked, then
// a fresh detection.
func engineCheck(ctx context.Context, msg protocol.EngineCheck, stateDir string) protocol.EngineCheckResult {
	res := protocol.EngineCheckResult{Type: "engine_check_result", RequestID: msg.RequestID}
	if msg.RedetectPath {
		added, saveErr, err := redetectPath(ctx, stateDir)
		if err != nil {
			res.PathError = err.Error()
			log.Printf("engines: re-detect PATH: %v", err)
		} else {
			if saveErr != nil {
				res.PathSaveError = saveErr.Error()
				log.Printf("engines: re-detect PATH applied but not saved (a restart loses it): %v", saveErr)
			}
			home := homeDir()
			for _, d := range added {
				res.PathAdded = append(res.PathAdded, tildePath(d, home))
			}
			log.Printf("engines: re-detected PATH, %d folder(s) added", len(added))
		}
	}
	res.Statuses = engineStatuses(ctx)
	res.AvailableEngines = availableEngines()
	if res.AvailableEngines == nil {
		res.AvailableEngines = []string{}
	}
	return res
}

// engineCheckSender is what EngineCheckHandler answers through (the WSClient).
type engineCheckSender interface {
	Send(msg any) error
	RequestHeartbeat()
}

// engineCheckBound is the most one check may take on the daemon, below the
// server's work bound for it (engineCheckWorkBound).
const engineCheckBound = 45 * time.Second

// EngineCheckHandler answers engine_check off the read pump (reading a login
// shell can take seconds). One check runs at a time and none queue: a check
// that arrives while one runs is answered at once as Busy, so canceled or
// repeated requests can never pile up login shells. stateDir is where
// Re-detect PATH saves the PATH it read; kick (optional) asks the model
// prober for a fresh list, since an engine that just became available has
// none yet.
func EngineCheckHandler(client engineCheckSender, stateDir string, kick func()) func([]byte) {
	var mu sync.Mutex
	return func(raw []byte) {
		var msg protocol.EngineCheck
		if err := json.Unmarshal(raw, &msg); err != nil || msg.RequestID == "" {
			log.Printf("engines: bad engine_check")
			return
		}
		if !mu.TryLock() {
			_ = client.Send(protocol.EngineCheckResult{Type: "engine_check_result", RequestID: msg.RequestID, Busy: true})
			return
		}
		go func() {
			defer mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), engineCheckBound)
			defer cancel()
			res := engineCheck(ctx, msg, stateDir)
			_ = client.Send(res)
			client.RequestHeartbeat()
			if kick != nil {
				kick()
			}
		}()
	}
}
