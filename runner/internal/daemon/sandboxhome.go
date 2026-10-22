package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Sandbox homes. A Local sandbox session never sees the person's own engine directories
// (~/.claude, ~/.claude.json, ~/.codex, ~/.hermes): a hook, a setting, a plugin or a skill it could
// write there would run on the host, as the person, the next time an unsandboxed engine started.
// Each sandboxed session gets a home of its own instead, kept by the daemon at
// <state>/sandbox-homes/<session-id>/, with only the engine the session runs:
//
//   - claude/ (or codex/) is the container's ~/.claude (~/.codex), writable for the engine's own
//     transcripts and caches. The daemon writes the login into it: the current access token, with
//     the refresh token replaced by an inert placeholder, so the session can neither rotate nor
//     keep the person's login.
//   - The engine's settings are copies with secrets left out by allowlist (Claude's settings without
//     their env block or credential helpers, Codex's config.toml without MCP servers, plugins or
//     provider headers, ~/.claude.json reduced to the account state the login check needs),
//     mounted read-only. Instructions, skills, agents, commands, hooks and plugins are the person's
//     own, mounted read-only. A missing directory is an empty read-only placeholder, so a session
//     cannot create its own.
//   - A Hermes session gets ~/.hermes/config.yaml and .env (its provider login), read-only.
//
// Nothing in a sandbox home is ever read back by the daemon or copied to the host. While the
// session runs, the daemon re-copies the host's current login in every sandboxLoginInterval (a
// login the host engine refreshed reaches the session that way). When the host's login has expired
// and the host engine has not refreshed it, the session is told its sign-in needs refreshing on
// this machine.
//
// The top level of a sandbox home (the sanitized copies, the placeholders) is never mounted
// writable.

const sandboxHomeDirName = "sandbox-homes"

// The engine paths inside the container (the sandbox image's HOME is /home/agent).
const (
	sandboxAgentHome   = "/home/agent"
	sandboxClaudeDir   = sandboxAgentHome + "/.claude"
	sandboxClaudeJSON  = sandboxAgentHome + "/.claude.json"
	sandboxCodexDir    = sandboxAgentHome + "/.codex"
	sandboxHermesDir   = sandboxAgentHome + "/.hermes"
	claudeCredentialsN = ".credentials.json"
	codexAuthN         = "auth.json"
	// sandboxNoRefresh stands in for a refresh token in a sandbox's login copy.
	sandboxNoRefresh = "blerg-sandbox-no-refresh"
)

// sandboxConfigEntry is one configuration file or directory of an engine, mounted read-only. dir:
// a directory (an empty placeholder when the person has none). sanitize: a file the daemon copies
// through a secret-stripping allowlist (always mounted, "{}" or empty when the person has none);
// otherwise the person's file, mounted only when it exists.
type sandboxConfigEntry struct {
	name     string
	dir      bool
	sanitize func(raw []byte) []byte
}

// claudeSandboxConfig is what Claude Code reads as user configuration under ~/.claude.
var claudeSandboxConfig = []sandboxConfigEntry{
	{name: "settings.json", sanitize: sanitizeClaudeSettings},
	{name: "settings.local.json", sanitize: sanitizeClaudeSettings},
	{name: "CLAUDE.md"},
	{name: "keybindings.json"},
	{name: "agents", dir: true},
	{name: "commands", dir: true},
	{name: "hooks", dir: true},
	{name: "output-styles", dir: true},
	{name: "personas", dir: true},
	{name: "plugins", dir: true},
	{name: "skills", dir: true},
}

// codexSandboxConfig is what Codex reads as user configuration under $CODEX_HOME.
var codexSandboxConfig = []sandboxConfigEntry{
	{name: "config.toml", sanitize: sanitizeCodexConfig},
	{name: "AGENTS.md"},
	{name: "AGENTS.override.md"},
	{name: "hooks.json"},
	{name: "hooks", dir: true},
	{name: "skills", dir: true},
	{name: "plugins", dir: true},
	{name: "rules", dir: true},
	{name: "prompts", dir: true},
	{name: "marketplaces", dir: true},
}

// hermesSandboxConfig: Hermes's configuration and provider keys (its login), read-only. The rest of
// the image's ~/.hermes is its own install (the hermes-agent checkout), which must not be replaced.
var hermesSandboxConfig = []string{"config.yaml", ".env"}

// sandboxEngines says whose login and configuration a sandbox gets.
type sandboxEngines struct{ claude, codex, hermes bool }

// sandboxEnginesFor is the one engine a sandboxed session runs (restrictedEngine, when set, wins):
// "" is Claude; an engine without a sandbox login (OpenClaw) or one this daemon does not know gets
// nothing.
func sandboxEnginesFor(engine, restrictedEngine string) sandboxEngines {
	if restrictedEngine != "" {
		engine = restrictedEngine
	}
	switch engine {
	case "", "claude":
		return sandboxEngines{claude: true}
	case "codex":
		return sandboxEngines{codex: true}
	case "hermes":
		return sandboxEngines{hermes: true}
	}
	return sandboxEngines{}
}

// sandboxHomePath is the daemon's home for a sandboxed session; ok is false without a state
// directory or for an id that is not a plain name.
func sandboxHomePath(sessionID string) (string, bool) {
	if !sandboxCloneIDPattern.MatchString(sessionID) {
		return "", false
	}
	state := DaemonStateDir()
	if state == "" {
		return "", false
	}
	return filepath.Join(state, sandboxHomeDirName, sessionID), true
}

// prepareSandboxHome creates (or, for a recreated container, refreshes) a session's sandbox home
// and returns its path. Every directory is the daemon's own and closed to other users. Nothing in
// it is read: mount points the session could have replaced are removed and made again, and the
// login and configuration copies are written from the host's files.
func prepareSandboxHome(sessionID, home, codexSource string, engines sandboxEngines) (string, error) {
	dir, ok := sandboxHomePath(sessionID)
	if !ok {
		return "", errors.New("the sandbox home needs the daemon's state directory")
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if _, err := secureAgentRecordsDir(d, true); err != nil {
			return "", fmt.Errorf("sandbox home: %w", err)
		}
	}
	if err := remake(filepath.Join(dir, "empty"), true); err != nil {
		return "", err
	}
	if engines.claude {
		if err := prepareEngineDir(dir, "claude", claudeSandboxConfig, filepath.Join(home, ".claude")); err != nil {
			return "", err
		}
		if err := writeDaemonFile(filepath.Join(dir, "claude.json"), claudeJSONForSandbox(filepath.Join(home, ".claude.json"))); err != nil {
			return "", err
		}
	}
	if engines.codex {
		if err := prepareEngineDir(dir, "codex", codexSandboxConfig, codexSource); err != nil {
			return "", err
		}
	}
	refreshSandboxLogins(sessionID, dir, home, codexSource, time.Now())
	return dir, nil
}

// prepareEngineDir makes an engine's private directory, its configuration mount points, and the
// sanitized copies of its settings (at the home's top level, never mounted writable). A mount point
// is always removed and made again: one the session replaced in an earlier life of its container (a
// link, a FIFO) is never read and never followed by docker.
func prepareEngineDir(sbHome, engine string, entries []sandboxConfigEntry, source string) error {
	dir := filepath.Join(sbHome, engine)
	if _, err := secureAgentRecordsDir(dir, true); err != nil {
		return fmt.Errorf("sandbox home: %w", err)
	}
	for _, e := range entries {
		if e.sanitize != nil {
			var raw []byte
			if source != "" {
				raw, _ = readBounded(filepath.Join(source, e.name), configMax)
			}
			if err := writeDaemonFile(filepath.Join(sbHome, sanitizedName(engine, e.name)), e.sanitize(raw)); err != nil {
				return err
			}
		} else if !e.dir && !hostEntryExists(source, e) {
			continue
		}
		if err := remake(filepath.Join(dir, e.name), e.dir); err != nil {
			return err
		}
	}
	return nil
}

func sanitizedName(engine, name string) string { return engine + "-" + name }

// remake removes path, whatever it is, and makes it an empty directory (dir) or an empty file, owned
// by the daemon. Nothing at path is opened or read.
func remake(path string, dir bool) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("sandbox home: %w", err)
	}
	if dir {
		return os.Mkdir(path, 0o700)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a mount point inside the daemon's own sandbox home
	if err != nil {
		return fmt.Errorf("sandbox home: %w", err)
	}
	return f.Close()
}

// writeDaemonFile replaces a file at the home's top level (never mounted writable), 0600.
func writeDaemonFile(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := writeFileSynced(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sandbox home: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sandbox home: %w", err)
	}
	return nil
}

// hostEntryExists reports whether the person has e under source (following their own links).
func hostEntryExists(source string, e sandboxConfigEntry) bool {
	if source == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(source, e.name))
	if err != nil {
		return false
	}
	return info.IsDir() == e.dir
}

const (
	configMax     = 4 << 20
	claudeJSONMax = 32 << 20 // it keeps a history of every project
	credentialMax = 1 << 20
)

// readBounded reads a regular file (following links: these are the person's own paths, never a
// sandbox's) of at most limit bytes. Opened non-blocking and checked on the handle, so a FIFO put in
// a file's place never blocks the read.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|nonBlockFlag, 0) //nolint:gosec // the person's own engine files, read to copy into their sandbox
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("not a bounded regular file")
	}
	return raw, nil
}

// ─── Configuration copies ──────────────────────────────────────────────────────────────────────

// claudeSettingsKeys are the settings a sandbox's Claude Code gets: behavior, not credentials. Left
// out among others: env (tokens), apiKeyHelper, awsCredentialExport, awsAuthRefresh,
// otelHeadersHelper, MCP server switches.
//
// Hooks and the status line are left out too: their commands are free text that can carry a token
// (a header in a curl, a variable set inline), and a sandboxed session runs without the person's
// hooks (Blerg's own progress hook comes in through the session's --settings).
var claudeSettingsKeys = map[string]bool{
	"permissions": true, "model": true, "enabledPlugins": true,
	"outputStyle": true, "includeCoAuthoredBy": true, "cleanupPeriodDays": true, "alwaysThinkingEnabled": true,
	"spinnerTipsEnabled": true, "language": true, "theme": true, "sandbox": true,
}

// sanitizeClaudeSettings keeps only claudeSettingsKeys; anything that does not parse is "{}".
func sanitizeClaudeSettings(raw []byte) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return []byte("{}")
	}
	out := map[string]json.RawMessage{}
	for k, v := range doc {
		if claudeSettingsKeys[k] {
			out[k] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// codexConfigKeys are the config.toml settings a sandbox's Codex gets. Left out: MCP servers,
// plugins and marketplaces, projects, shell environment policy, notify commands, and anything else
// not listed. Model providers keep only their identity and endpoint (no headers or query params).
var codexConfigKeys = map[string]bool{
	"model": true, "model_provider": true, "model_reasoning_effort": true, "model_reasoning_summary": true,
	"model_verbosity": true, "model_context_window": true, "model_auto_compact_token_limit": true,
	"approval_policy": true, "sandbox_mode": true, "sandbox_workspace_write": true, "profile": true,
	"features": true, "tools": true, "web_search": true, "project_doc_max_bytes": true,
	"hide_agent_reasoning": true, "show_raw_agent_reasoning": true, "disable_response_storage": true,
	"preferred_auth_method": true, "forced_login_method": true, "forced_chatgpt_workspace_id": true,
	"cli_auth_credentials_store": true, "file_opener": true, "tui": true,
}

var codexProviderKeys = map[string]bool{"name": true, "base_url": true, "env_key": true, "wire_api": true, "requires_openai_auth": true}

// sanitizeCodexConfig keeps only codexConfigKeys (and, per profile, the same keys); anything that
// does not parse is an empty file.
func sanitizeCodexConfig(raw []byte) []byte {
	var doc map[string]any
	if len(raw) == 0 || toml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	keep := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			if codexConfigKeys[k] {
				out[k] = v
			}
		}
		return out
	}
	out := keep(doc)
	if providers, ok := doc["model_providers"].(map[string]any); ok {
		clean := map[string]any{}
		for name, p := range providers {
			if pm, ok := p.(map[string]any); ok {
				c := map[string]any{}
				for k, v := range pm {
					if codexProviderKeys[k] {
						c[k] = v
					}
				}
				clean[name] = c
			}
		}
		out["model_providers"] = clean
	}
	if profiles, ok := doc["profiles"].(map[string]any); ok {
		clean := map[string]any{}
		for name, p := range profiles {
			if pm, ok := p.(map[string]any); ok {
				clean[name] = keep(pm)
			}
		}
		out["profiles"] = clean
	}
	b, err := toml.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// claudeJSONForSandbox is the session's read-only ~/.claude.json: only the account state Claude
// Code's login check needs, and /workspace's folder trust. Everything else (MCP servers, other
// projects, repository paths, caches, any key added later) is left out.
func claudeJSONForSandbox(src string) []byte {
	raw, err := readBounded(src, claudeJSONMax)
	if err != nil {
		return []byte("{}")
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return []byte("{}")
	}
	out := map[string]any{}
	for _, k := range []string{"oauthAccount", "userID", "hasCompletedOnboarding", "lastOnboardingVersion", "primaryApiKey"} {
		if v, ok := doc[k]; ok {
			out[k] = v
		}
	}
	var projects map[string]struct {
		Trust *bool `json:"hasTrustDialogAccepted"`
	}
	if json.Unmarshal(doc["projects"], &projects) == nil {
		if p, ok := projects[sandboxWorkdir]; ok && p.Trust != nil {
			out["projects"] = map[string]any{sandboxWorkdir: map[string]any{"hasTrustDialogAccepted": *p.Trust}}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ─── Mounts ────────────────────────────────────────────────────────────────────────────────────

// bindMountRO is sandboxBindMount, read-only.
func bindMountRO(source, target string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"type=bind", "src=" + source, "dst=" + target, "readonly"})
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// sandboxHomeMountArgs are the `--mount` arguments of a sandbox home: the engine's private
// directory, writable, with its configuration read-only on top. codexSource is the person's Codex
// directory; codexAt, when set, is a second in-container path the Codex directory is also mounted at
// (the host path a private CODEX_HOME's links point to).
func sandboxHomeMountArgs(sbHome, home, codexSource, codexAt string, engines sandboxEngines) []string {
	if sbHome == "" {
		return nil
	}
	var args []string
	mount := func(spec string) { args = append(args, "--mount", spec) }
	config := func(engine, source, inside string, entries []sandboxConfigEntry) {
		for _, e := range entries {
			switch {
			case e.sanitize != nil:
				mount(bindMountRO(filepath.Join(sbHome, sanitizedName(engine, e.name)), inside+"/"+e.name))
			case hostEntryExists(source, e):
				mount(bindMountRO(filepath.Join(source, e.name), inside+"/"+e.name))
			case e.dir:
				mount(bindMountRO(filepath.Join(sbHome, "empty"), inside+"/"+e.name))
			}
		}
	}
	if engines.claude {
		mount(sandboxBindMount(filepath.Join(sbHome, "claude"), sandboxClaudeDir))
		config("claude", filepath.Join(home, ".claude"), sandboxClaudeDir, claudeSandboxConfig)
		mount(bindMountRO(filepath.Join(sbHome, "claude.json"), sandboxClaudeJSON))
	}
	if engines.codex {
		for _, at := range []string{sandboxCodexDir, codexAt} {
			if at == "" || !filepath.IsAbs(at) {
				continue
			}
			mount(sandboxBindMount(filepath.Join(sbHome, "codex"), at))
			config("codex", codexSource, at, codexSandboxConfig)
		}
	}
	if engines.hermes && home != "" {
		for _, name := range hermesSandboxConfig {
			p := filepath.Join(home, ".hermes", name)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				mount(bindMountRO(p, sandboxHermesDir+"/"+name))
			}
		}
	}
	return args
}

// ─── Lifecycle ─────────────────────────────────────────────────────────────────────────────────

// sandboxHomeState is the daemon's in-memory state of one live sandbox home: whether the session
// was told its sign-in needs refreshing, and how to tell it. Each home has its own lock; none is
// held across another home's I/O, and nothing under it reads a file the session can write.
type sandboxHomeState struct {
	mu    sync.Mutex
	noted map[string]bool   // engine -> the session was told the host login has expired
	note  func(text string) // tells the session (nil: nobody to tell; logged instead)
}

var sandboxHomes sync.Map // session id -> *sandboxHomeState

func sandboxHomeFor(sessionID string) *sandboxHomeState {
	v, _ := sandboxHomes.LoadOrStore(sessionID, &sandboxHomeState{noted: map[string]bool{}})
	h, _ := v.(*sandboxHomeState)
	return h
}

// setSandboxHomeNotifier gives a session's home a way to tell the session about its sign-in.
// A sign-in that had already expired when the home was made is said now.
func setSandboxHomeNotifier(sessionID string, note func(text string)) {
	h := sandboxHomeFor(sessionID)
	h.mu.Lock()
	h.note = note
	var pending []string
	for _, engine := range []string{"claude", "codex"} {
		if h.noted[engine] {
			pending = append(pending, sandboxSignInNote(engine))
		}
	}
	h.mu.Unlock()
	for _, text := range pending {
		note(text)
	}
}

// releaseSandboxHome removes a session's home. Nothing in it is read or copied anywhere.
func releaseSandboxHome(sessionID string) {
	sandboxHomes.Delete(sessionID)
	dir, ok := sandboxHomePath(sessionID)
	if !ok {
		return
	}
	if _, err := os.Lstat(dir); err != nil {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("sandbox home %s: could not be removed: %v", sessionID, err)
	}
}

// sandboxHomesStarting holds the sessions whose home is being made and whose container is not
// registered yet (prepareSandboxHome until startSandboxContainer registers it or gives up): the
// sweep never takes their home.
var sandboxHomesStarting sync.Map // session id -> time the start began

// sandboxHomeSweepGrace: a home touched more recently than this is never swept, whatever else says
// (a start the daemon cannot see yet, a Docker that did not answer `docker ps`).
var sandboxHomeSweepGrace = 10 * time.Minute

func markSandboxHomeStarting(sessionID string)  { sandboxHomesStarting.Store(sessionID, time.Now()) }
func clearSandboxHomeStarting(sessionID string) { sandboxHomesStarting.Delete(sessionID) }

// sweepOrphanSandboxHomes removes the homes a crash or a failed removal left behind: no registered
// sandbox, no running container of that name, no start in progress, not touched within
// sandboxHomeSweepGrace, and keep says the daemon has no use for it (a live session, or a sandboxed
// terminal's recovery record whose transcript is in it).
func sweepOrphanSandboxHomes(keep func(sessionID string) bool) {
	state := DaemonStateDir()
	if state == "" {
		return
	}
	root := filepath.Join(state, sandboxHomeDirName)
	if exists, err := secureAgentRecordsDir(root, false); err != nil || !exists {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	running := map[string]bool{}
	for _, name := range listSandboxContainers() {
		running[strings.TrimPrefix(name, sandboxContainerPrefix)] = true
	}
	for _, e := range entries {
		id := e.Name()
		if !sandboxCloneIDPattern.MatchString(id) {
			continue
		}
		if _, registered := sandboxContainer(id); registered || running[id] || (keep != nil && keep(id)) {
			continue
		}
		if _, starting := sandboxHomesStarting.Load(id); starting {
			continue
		}
		if info, err := os.Lstat(filepath.Join(root, id)); err != nil || time.Since(info.ModTime()) < sandboxHomeSweepGrace {
			continue
		}
		log.Printf("sandbox home %s: removing a home no session uses", id)
		releaseSandboxHome(id)
	}
}

// sandboxTranscriptExists reports whether a sandboxed session's own home holds its Claude
// transcript (a terminal sandbox brought back after a restart resumes from it). Only looked up,
// never opened.
func sandboxTranscriptExists(sessionID string) bool {
	dir, ok := sandboxHomePath(sessionID)
	if !ok {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(dir, "claude", "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return false
	}
	for _, m := range matches {
		if info, err := os.Lstat(m); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ─── The login copies ──────────────────────────────────────────────────────────────────────────

// sandboxLoginInterval is how often the host's current login is copied into every live sandbox.
var sandboxLoginInterval = 15 * time.Second

var sandboxLoginOnce sync.Once

// ensureSandboxLoginRefresh starts the daemon's login-copy loop, once.
func ensureSandboxLoginRefresh() {
	sandboxLoginOnce.Do(func() {
		go func() {
			t := time.NewTicker(sandboxLoginInterval)
			defer t.Stop()
			for range t.C {
				refreshLiveSandboxLogins(time.Now())
			}
		}()
	})
}

// refreshLiveSandboxLogins copies the host's current login into the home of every sandbox the
// daemon has registered. A home without one (a crash's leftover) gets nothing.
func refreshLiveSandboxLogins(now time.Time) {
	home := homeDir()
	codexSource := engineHome(home, "CODEX_HOME", ".codex")
	sandboxRegistry.Range(func(k, _ any) bool {
		id, _ := k.(string)
		if dir, ok := sandboxHomePath(id); ok {
			if _, err := os.Lstat(dir); err == nil {
				refreshSandboxLogins(id, dir, home, codexSource, now)
			}
		}
		return true
	})
}

// sandboxLogin is one engine's login as a sandbox gets it.
type sandboxLogin struct {
	engine, dir, name string
	data              []byte    // the copy to write; nil: the host has no usable login
	expires           time.Time // when the host's access token expires (zero: unknown)
}

// refreshSandboxLogins writes the host's current login into one sandbox home, for the engines whose
// directory the home has. The sandbox's copy is never read: it is replaced (a new file renamed over
// it). An expired host login is said once per expiry.
func refreshSandboxLogins(sessionID, sbHome, home, codexSource string, now time.Time) {
	var logins []sandboxLogin
	if home != "" {
		data, exp := claudeSandboxLogin(filepath.Join(home, ".claude", claudeCredentialsN))
		logins = append(logins, sandboxLogin{engine: "claude", dir: filepath.Join(sbHome, "claude"), name: claudeCredentialsN, data: data, expires: exp})
	}
	if codexSource != "" {
		data, exp := codexSandboxLogin(filepath.Join(codexSource, codexAuthN))
		logins = append(logins, sandboxLogin{engine: "codex", dir: filepath.Join(sbHome, "codex"), name: codexAuthN, data: data, expires: exp})
	}
	h := sandboxHomeFor(sessionID)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range logins {
		if info, err := os.Lstat(l.dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue // this session does not run that engine
		}
		if l.data == nil {
			continue
		}
		// Written every time, whatever the session did to its copy: it is never read to find out.
		if err := replaceInDir(l.dir, l.name, l.data); err != nil {
			log.Printf("sandbox home %s: the %s login could not be copied in: %v", sessionID, l.engine, err)
			continue
		}
		expired := !l.expires.IsZero() && !now.Before(l.expires)
		if expired && !h.noted[l.engine] {
			text := sandboxSignInNote(l.engine)
			log.Printf("sandbox home %s: %s", sessionID, text)
			if h.note != nil {
				h.note(text)
			}
		}
		h.noted[l.engine] = expired
	}
}

// sandboxSignInNote is what a session is told when the host's login has expired.
func sandboxSignInNote(engine string) string {
	name, cmd := "Claude", "claude"
	if engine == "codex" {
		name, cmd = "Codex", "codex"
	}
	return "The " + name + " sign-in on this machine needs refreshing: a sandboxed session gets only the current sign-in, never the means to renew it. " +
		"Run " + cmd + " once on this machine (outside the sandbox), or sign in again there; the session picks the new sign-in up within a minute."
}

// replaceInDir puts data at dir/name (0600) by writing a new file in dir and renaming it over the
// old one: whatever the session left at that name is replaced, never opened.
func replaceInDir(dir, name string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmp := fmt.Sprintf(".%s.blerg-%d.tmp", name, time.Now().UnixNano())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// credentialToken is a plausible token: printable, no spaces, bounded.
func credentialToken(s string) bool {
	if s == "" || len(s) > 16<<10 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

// claudeSandboxLogin is a Claude Code credentials file for a sandbox, from the host's: the access
// token and the fields Claude Code needs to start with it, the refresh token an inert placeholder,
// and nothing else (no MCP server tokens). nil when the host has no usable Claude login.
func claudeSandboxLogin(hostPath string) ([]byte, time.Time) {
	raw, err := readBounded(hostPath, credentialMax)
	if err != nil {
		return nil, time.Time{}
	}
	var doc struct {
		OAuth *struct {
			AccessToken      string   `json:"accessToken"`
			ExpiresAt        int64    `json:"expiresAt"`
			Scopes           []string `json:"scopes"`
			SubscriptionType *string  `json:"subscriptionType"`
			RateLimitTier    *string  `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.OAuth == nil || !credentialToken(doc.OAuth.AccessToken) || doc.OAuth.ExpiresAt <= 0 {
		return nil, time.Time{}
	}
	o := map[string]any{
		"accessToken":  doc.OAuth.AccessToken,
		"refreshToken": sandboxNoRefresh,
		"expiresAt":    doc.OAuth.ExpiresAt,
		"scopes":       doc.OAuth.Scopes,
	}
	if doc.OAuth.Scopes == nil {
		o["scopes"] = []string{}
	}
	if doc.OAuth.SubscriptionType != nil {
		o["subscriptionType"] = *doc.OAuth.SubscriptionType
	}
	if doc.OAuth.RateLimitTier != nil {
		o["rateLimitTier"] = *doc.OAuth.RateLimitTier
	}
	b, err := json.Marshal(map[string]any{"claudeAiOauth": o})
	if err != nil {
		return nil, time.Time{}
	}
	return b, time.UnixMilli(doc.OAuth.ExpiresAt)
}

// codexSandboxLogin is a Codex auth.json for a sandbox, from the host's: an API key as it is (that
// is the whole login), or the ChatGPT tokens with the refresh token an inert placeholder. Unknown
// keys are left out. The expiry is the access token's own (its JWT exp), when it says.
func codexSandboxLogin(hostPath string) ([]byte, time.Time) {
	raw, err := readBounded(hostPath, credentialMax)
	if err != nil {
		return nil, time.Time{}
	}
	var doc struct {
		AuthMode    *string `json:"auth_mode"`
		APIKey      *string `json:"OPENAI_API_KEY"`
		LastRefresh *string `json:"last_refresh"`
		Tokens      *struct {
			IDToken     string  `json:"id_token"`
			AccessToken string  `json:"access_token"`
			AccountID   *string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, time.Time{}
	}
	out := map[string]any{"OPENAI_API_KEY": nil}
	if doc.AuthMode != nil {
		out["auth_mode"] = *doc.AuthMode
	}
	if doc.APIKey != nil {
		if !credentialToken(*doc.APIKey) {
			return nil, time.Time{}
		}
		out["OPENAI_API_KEY"] = *doc.APIKey
	}
	var exp time.Time
	if t := doc.Tokens; t != nil {
		if !credentialToken(t.IDToken) || !credentialToken(t.AccessToken) {
			return nil, time.Time{}
		}
		tokens := map[string]any{"id_token": t.IDToken, "access_token": t.AccessToken, "refresh_token": sandboxNoRefresh, "account_id": nil}
		if t.AccountID != nil {
			tokens["account_id"] = *t.AccountID
		}
		out["tokens"] = tokens
		exp = jwtExpiry(t.AccessToken)
	} else if doc.APIKey == nil {
		return nil, time.Time{}
	}
	// Codex renews its tokens on its own once last_refresh is about eight days old, whatever the
	// access token's expiry; with the placeholder that renewal fails and the session stops. The copy
	// says "just refreshed" (it is rewritten every few seconds), so the session uses its access token
	// until that expires; the note about an expired sign-in follows the token's own expiry.
	if doc.Tokens != nil || doc.LastRefresh != nil {
		out["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, time.Time{}
	}
	return b, exp
}

// jwtExpiry is a JWT's exp claim, unverified (it only decides when to tell the person); zero when
// the token does not say.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if dec.Decode(&claims) != nil {
		return time.Time{}
	}
	n, err := claims.Exp.Int64()
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
