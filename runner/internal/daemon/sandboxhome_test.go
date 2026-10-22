package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// claudeCredFixture is a Claude Code credentials file: the login (fake tokens) and an MCP server's
// token beside it, as the person's own file holds.
func claudeCredFixture(access, refresh string, expires time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max","futureKey":"x"},"mcpOAuth":{"fixture-server":{"accessToken":"mcp-fixture-token"}}}`,
		access, refresh, expires.UnixMilli())
}

func codexAuthFixture(access, refresh, account string, last time.Time) string {
	return fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id-fixture","access_token":%q,"refresh_token":%q,"account_id":%q,"extra":"x"},"last_refresh":%q,"unknown":"x"}`,
		access, refresh, account, last.UTC().Format(time.RFC3339Nano))
}

// fakeJWT is an unsigned JWT-shaped token with an exp claim (a fixture, not a credential).
func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

// The secrets a person's configuration may hold beside the logins: none may reach a sandbox.
var configSecrets = []string{"settings-env-secret", "settings-helper-secret", "settings-hook-secret", "settings-status-secret", "codex-shellenv-secret", "codex-bearer-secret", "codex-mcp-secret", "codex-provider-secret", "codex-notify-secret", "mcp-secret", "mcp-fixture-token", "host-refresh", "codex-refresh", "/home/person/app"}

// sandboxHomeFixture is a person's home with every engine's login and configuration (and secrets
// in that configuration), and a state directory for the daemon.
func sandboxHomeFixture(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv(DaemonStateDirEnv, filepath.Join(t.TempDir(), "state"))
	files := map[string]string{
		".claude/.credentials.json": claudeCredFixture("host-access", "host-refresh", time.Now().Add(time.Hour)),
		".claude/settings.json": `{"permissions":{"allow":["Bash(ls)"]},"env":{"GITHUB_TOKEN":"settings-env-secret"},"apiKeyHelper":"echo settings-helper-secret",` +
			`"statusLine":{"type":"command","command":"TOKEN=settings-status-secret status"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"curl -H 'X: settings-hook-secret' x"}]}]}}`,
		".claude/CLAUDE.md":         "person's instructions",
		".claude/skills/s/SKILL.md": "skill",
		".claude.json": `{"oauthAccount":{"emailAddress":"person@example.com"},"hasCompletedOnboarding":true,"githubRepoPaths":{"x":["/home/person/app"]},` +
			`"mcpServers":{"secret":{"command":"x","env":{"TOKEN":"mcp-secret"}}},` +
			`"projects":{"/workspace":{"hasTrustDialogAccepted":true,"mcpServers":{"p":{}}},"/home/person/app":{"allowedTools":["Bash"]}}}`,
		".codex/auth.json": codexAuthFixture("codex-access", "codex-refresh", "acct-1", time.Now().Add(-time.Hour)),
		".codex/config.toml": "model = \"fixture\"\nnotify = [\"codex-notify-secret\"]\nmodel_provider = \"corp\"\n" +
			"[mcp_servers.gh]\nurl = \"https://mcp.example.com\"\nbearer_token = \"codex-mcp-secret\"\n" +
			"[model_providers.corp]\nname = \"Corp\"\nbase_url = \"https://llm.example.com\"\nhttp_headers = { Authorization = \"codex-provider-secret\" }\nexperimental_bearer_token = \"codex-bearer-secret\"\n" +
			"[shell_environment_policy]\nset = { API_TOKEN = \"codex-shellenv-secret\" }\n",
		".hermes/config.yaml":  "model: {}\n",
		".hermes/.env":         "KEY=fixture\n",
		".hermes/other-config": "not mounted\n",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(home, name), body)
	}
	return home
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// allFiles is every file under dir, by path, with its content.
func allFiles(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			raw, _ := os.ReadFile(p)
			out[strings.TrimPrefix(p, dir)] = string(raw)
		}
		return nil
	})
	return out
}

// A sandbox home holds the current access token and nothing that renews it, the configuration
// without its secrets, and ~/.claude.json reduced to the account state; and is private.
func TestPrepareSandboxHomeCopiesOnlyWhatAuthenticates(t *testing.T) {
	home := sandboxHomeFixture(t)
	source := filepath.Join(home, ".codex")
	sb, err := prepareSandboxHome("s-claude", home, source, sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	cred := readJSONFile(t, filepath.Join(sb, "claude", claudeCredentialsN))
	o, _ := cred["claudeAiOauth"].(map[string]any)
	if len(cred) != 1 || o["accessToken"] != "host-access" || o["refreshToken"] != sandboxNoRefresh || o["futureKey"] != nil {
		t.Errorf("claude login copy = %v", cred)
	}
	cj := readJSONFile(t, filepath.Join(sb, "claude.json"))
	for k := range cj {
		if k != "oauthAccount" && k != "hasCompletedOnboarding" && k != "projects" {
			t.Errorf("~/.claude.json copy kept %q", k)
		}
	}
	if fmt.Sprint(cj["projects"]) != "map[/workspace:map[hasTrustDialogAccepted:true]]" {
		t.Errorf("projects = %v", cj["projects"])
	}
	settings := readJSONFile(t, filepath.Join(sb, "claude-settings.json"))
	if settings["env"] != nil || settings["apiKeyHelper"] != nil || settings["hooks"] != nil || settings["statusLine"] != nil || settings["permissions"] == nil {
		t.Errorf("settings copy = %v", settings)
	}
	if _, err := os.Stat(filepath.Join(sb, "codex")); err == nil {
		t.Error("a Claude session got a Codex home")
	}

	sb, err = prepareSandboxHome("s-codex", home, source, sandboxEnginesFor("codex", ""))
	if err != nil {
		t.Fatal(err)
	}
	auth := readJSONFile(t, filepath.Join(sb, "codex", codexAuthN))
	tok, _ := auth["tokens"].(map[string]any)
	if tok["access_token"] != "codex-access" || tok["refresh_token"] != sandboxNoRefresh || tok["extra"] != nil || auth["unknown"] != nil {
		t.Errorf("codex login copy = %v", auth)
	}
	cfg, _ := os.ReadFile(filepath.Join(sb, "codex-config.toml"))
	if !strings.Contains(string(cfg), "fixture") || !strings.Contains(string(cfg), "llm.example.com") {
		t.Errorf("codex config lost its model or provider: %s", cfg)
	}
	if _, err := os.Stat(filepath.Join(sb, "claude")); err == nil {
		t.Error("a Codex session got a Claude home")
	}
	for path, body := range allFiles(filepath.Dir(sb)) {
		for _, secret := range configSecrets {
			if strings.Contains(body, secret) {
				t.Errorf("%s holds %q", path, secret)
			}
		}
	}
	if info, err := os.Stat(sb); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the home is not private: %v", err)
	}
}

// The configuration goes in read-only; the person's engine directories, login files and unsanitized
// settings are never mounted.
func TestSandboxHomeMountsConfigurationReadOnly(t *testing.T) {
	home := sandboxHomeFixture(t)
	sb := "/state/sandbox-homes/s1"
	claude := " " + strings.Join(sandboxHomeMountArgs(sb, home, filepath.Join(home, ".codex"), "", sandboxEnginesFor("", "")), " ") + " "
	codex := " " + strings.Join(sandboxHomeMountArgs(sb, home, filepath.Join(home, ".codex"), "", sandboxEnginesFor("codex", "")), " ") + " "
	hermes := " " + strings.Join(sandboxHomeMountArgs(sb, home, filepath.Join(home, ".codex"), "", sandboxEnginesFor("hermes", "")), " ") + " "
	for args, wants := range map[string][]string{
		claude: {
			"type=bind,src=" + sb + "/claude,dst=/home/agent/.claude ",
			"src=" + sb + "/claude-settings.json,dst=/home/agent/.claude/settings.json,readonly",
			"src=" + sb + "/claude-settings.local.json,dst=/home/agent/.claude/settings.local.json,readonly",
			"src=" + home + "/.claude/CLAUDE.md,dst=/home/agent/.claude/CLAUDE.md,readonly",
			"src=" + home + "/.claude/skills,dst=/home/agent/.claude/skills,readonly",
			"src=" + sb + "/empty,dst=/home/agent/.claude/hooks,readonly",
			"src=" + sb + "/claude.json,dst=/home/agent/.claude.json,readonly",
		},
		codex: {
			"type=bind,src=" + sb + "/codex,dst=/home/agent/.codex ",
			"src=" + sb + "/codex-config.toml,dst=/home/agent/.codex/config.toml,readonly",
			"src=" + sb + "/empty,dst=/home/agent/.codex/skills,readonly",
		},
		hermes: {
			"src=" + home + "/.hermes/config.yaml,dst=/home/agent/.hermes/config.yaml,readonly",
			"src=" + home + "/.hermes/.env,dst=/home/agent/.hermes/.env,readonly",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(args, want) {
				t.Errorf("missing %q in%s", want, args)
			}
		}
		for _, bad := range []string{"src=" + home + "/.claude,", "src=" + home + "/.claude.json", "src=" + home + "/.codex,", "/.credentials.json", "/auth.json", "src=" + home + "/.claude/settings.json", "src=" + home + "/.codex/config.toml", "other-config"} {
			if strings.Contains(args, bad) {
				t.Errorf("must not contain %q:%s", bad, args)
			}
		}
		for _, m := range strings.Split(args, "--mount ") {
			if strings.Contains(m, "src="+home) && !strings.Contains(m, ",readonly") {
				t.Errorf("a mount of the person's files is writable: %s", m)
			}
		}
	}
	if strings.Contains(claude, "/home/agent/.hermes") || strings.Contains(claude, "/home/agent/.codex") || strings.Contains(hermes, "/home/agent/.claude") {
		t.Error("a session got another engine's login")
	}
}

// Whatever a sandbox writes as its login (another account, extra keys, a forged id_token), nothing
// of it reaches the host, and the next refresh replaces it with the host's current login.
func TestASandboxWrittenLoginNeverReachesTheHost(t *testing.T) {
	home := sandboxHomeFixture(t)
	source := filepath.Join(home, ".codex")
	before := allFiles(home)
	sbClaude, err := prepareSandboxHome("s-forge-claude", home, source, sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	sbCodex, err := prepareSandboxHome("s-forge-codex", home, source, sandboxEnginesFor("codex", ""))
	if err != nil {
		t.Fatal(err)
	}
	forgedClaude := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"OTHER-ACCOUNT","refreshToken":"OTHER-REFRESH","expiresAt":%d,"scopes":["user:profile"],"apiBaseUrl":"http://evil.example"},"mcpOAuth":{"planted":{}}}`, time.Now().Add(30*24*time.Hour).UnixMilli())
	forgedCodex := `{"auth_mode":"apikey","OPENAI_API_KEY":"planted","tokens":{"id_token":"forged.unsigned.jwt","access_token":"OTHER-ACCOUNT","refresh_token":"OTHER-REFRESH","account_id":"acct-1","extra":"x"},"last_refresh":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	writeFile(t, filepath.Join(sbClaude, "claude", claudeCredentialsN), forgedClaude)
	writeFile(t, filepath.Join(sbCodex, "codex", codexAuthN), forgedCodex)
	for _, id := range []string{"s-forge-claude", "s-forge-codex"} {
		registerSandbox(id, dockerContainerName(id))
		t.Cleanup(func() { unregisterSandbox(id) })
	}
	refreshLiveSandboxLogins(time.Now())
	releaseSandboxHome("s-forge-claude")
	releaseSandboxHome("s-forge-codex")
	after := allFiles(home)
	for path, was := range before {
		if after[path] != was {
			t.Errorf("%s changed on the host", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files appeared on the host: %d -> %d", len(before), len(after))
	}
}

// The refresh replaces a forged copy with the host's current login, and picks up a login the host
// engine refreshed; only registered sandboxes are refreshed.
func TestSandboxLoginRefreshFollowsTheHost(t *testing.T) {
	home := sandboxHomeFixture(t)
	source := filepath.Join(home, ".codex")
	sb, err := prepareSandboxHome("s-follow", home, source, sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := prepareSandboxHome("s-orphan", home, source, sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	registerSandbox("s-follow", dockerContainerName("s-follow"))
	t.Cleanup(func() { unregisterSandbox("s-follow") })
	privPath := filepath.Join(sb, "claude", claudeCredentialsN)
	writeFile(t, privPath, `{"claudeAiOauth":{"accessToken":"forged"}}`)
	writeFile(t, filepath.Join(home, ".claude", claudeCredentialsN), claudeCredFixture("host-access-2", "host-refresh-2", time.Now().Add(2*time.Hour)))
	refreshLiveSandboxLogins(time.Now())
	if o, _ := readJSONFile(t, privPath)["claudeAiOauth"].(map[string]any); o["accessToken"] != "host-access-2" || o["refreshToken"] != sandboxNoRefresh {
		t.Fatalf("sandbox login = %v", o)
	}
	if o, _ := readJSONFile(t, filepath.Join(orphan, "claude", claudeCredentialsN))["claudeAiOauth"].(map[string]any); o["accessToken"] != "host-access" {
		t.Fatalf("a home no sandbox uses was refreshed: %v", o)
	}
}

// An expired host login is said to the session once (also when it expired before the session could
// be told), and again only after a fresh login expires.
func TestSandboxSessionIsToldWhenTheHostLoginExpired(t *testing.T) {
	home := sandboxHomeFixture(t)
	writeFile(t, filepath.Join(home, ".claude", claudeCredentialsN), claudeCredFixture("old", "r", time.Now().Add(-time.Minute)))
	writeFile(t, filepath.Join(home, ".codex", codexAuthN), codexAuthFixture(fakeJWT(time.Now().Add(-time.Minute)), "r", "acct-1", time.Now()))
	if _, err := prepareSandboxHome("s-expired", home, filepath.Join(home, ".codex"), sandboxEnginesFor("", "")); err != nil {
		t.Fatal(err)
	}
	registerSandbox("s-expired", dockerContainerName("s-expired"))
	t.Cleanup(func() { unregisterSandbox("s-expired"); releaseSandboxHome("s-expired") })
	var mu sync.Mutex
	var notes []string
	setSandboxHomeNotifier("s-expired", func(text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() })
	refreshLiveSandboxLogins(time.Now())
	if len(notes) != 1 || !strings.Contains(notes[0], "Claude sign-in on this machine needs refreshing") {
		t.Fatalf("notes = %q", notes)
	}
	writeFile(t, filepath.Join(home, ".claude", claudeCredentialsN), claudeCredFixture("new", "r", time.Now().Add(time.Hour)))
	refreshLiveSandboxLogins(time.Now())
	refreshLiveSandboxLogins(time.Now().Add(2 * time.Hour))
	if len(notes) != 2 {
		t.Fatalf("notes = %q", notes)
	}
	// A Codex session with an expired access token hears it too.
	if _, err := prepareSandboxHome("s-expired-codex", home, filepath.Join(home, ".codex"), sandboxEnginesFor("codex", "")); err != nil {
		t.Fatal(err)
	}
	var codexNotes []string
	setSandboxHomeNotifier("s-expired-codex", func(text string) { codexNotes = append(codexNotes, text) })
	t.Cleanup(func() { releaseSandboxHome("s-expired-codex") })
	if len(codexNotes) != 1 || !strings.Contains(codexNotes[0], "Codex sign-in") {
		t.Fatalf("codex notes = %q", codexNotes)
	}
}

// FIFOs the session leaves where its login copy and its mount points are block nothing: they are
// never opened.
func TestSandboxHomeFIFOsBlockNothing(t *testing.T) {
	home := sandboxHomeFixture(t)
	sb, err := prepareSandboxHome("s-fifo", home, filepath.Join(home, ".codex"), sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(sb, "claude", claudeCredentialsN), filepath.Join(sb, "claude", "settings.json"), filepath.Join(sb, "claude", "CLAUDE.md")} {
		_ = os.Remove(p)
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registerSandbox("s-fifo", dockerContainerName("s-fifo"))
	t.Cleanup(func() { unregisterSandbox("s-fifo") })
	done := make(chan error, 1)
	go func() {
		refreshLiveSandboxLogins(time.Now())
		_, err := prepareSandboxHome("s-fifo", home, filepath.Join(home, ".codex"), sandboxEnginesFor("", ""))
		releaseSandboxHome("s-fifo")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a FIFO in the sandbox home blocked the daemon")
	}
	// The host side is read non-blocking too: a FIFO in the person's own login path does not block.
	hostCred := filepath.Join(home, ".claude", claudeCredentialsN)
	_ = os.Remove(hostCred)
	if err := syscall.Mkfifo(hostCred, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() { claudeSandboxLogin(hostCred); done <- nil }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a FIFO at the host login blocked the daemon")
	}
}

// Homes a crash left behind (no registered sandbox, no container, no use) are removed; others kept.
func TestSweepOrphanSandboxHomes(t *testing.T) {
	home := sandboxHomeFixture(t)
	for _, id := range []string{"s-orphan", "s-registered", "s-running", "s-recorded"} {
		if _, err := prepareSandboxHome(id, home, "", sandboxEnginesFor("", "")); err != nil {
			t.Fatal(err)
		}
	}
	registerSandbox("s-registered", dockerContainerName("s-registered"))
	t.Cleanup(func() { unregisterSandbox("s-registered") })
	prev := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) { return []byte(dockerContainerName("s-running") + "\n"), nil }
	t.Cleanup(func() { dockerOutput = prev })
	old := time.Now().Add(-time.Hour)
	for _, id := range []string{"s-orphan", "s-registered", "s-running", "s-recorded"} {
		dir, _ := sandboxHomePath(id)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	sweepOrphanSandboxHomes(func(id string) bool { return id == "s-recorded" })
	for id, want := range map[string]bool{"s-orphan": false, "s-registered": true, "s-running": true, "s-recorded": true} {
		dir, _ := sandboxHomePath(id)
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", id, err == nil, want)
		}
	}
}

// A session that is starting (its home made, its container not registered yet) keeps its home
// through a sweep; so does any home touched within the grace period. Only an old, unclaimed home goes.
func TestSweepNeverTakesAStartingSessionsHome(t *testing.T) {
	home := sandboxHomeFixture(t)
	prev := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() { dockerOutput = prev })
	markSandboxHomeStarting("s-starting")
	if _, err := prepareSandboxHome("s-starting", home, "", sandboxEnginesFor("", "")); err != nil {
		t.Fatal(err)
	}
	dir, _ := sandboxHomePath("s-starting")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	sweepOrphanSandboxHomes(nil)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the sweep took the home of a session that is starting")
	}
	clearSandboxHomeStarting("s-starting")
	// Not starting any more, but touched just now: kept.
	if err := os.Chtimes(dir, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	sweepOrphanSandboxHomes(nil)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the sweep took a home touched within the grace period")
	}
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	sweepOrphanSandboxHomes(nil)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("an old unclaimed home was kept")
	}
}

// A sandbox's Codex copy says it was just refreshed, so Codex never tries its (placeholder) refresh
// token before the access token itself expires, however long ago the host last refreshed.
func TestCodexSandboxLoginLooksFreshlyRefreshed(t *testing.T) {
	home := sandboxHomeFixture(t)
	writeFile(t, filepath.Join(home, ".codex", codexAuthN), codexAuthFixture("codex-access", "codex-refresh", "acct-1", time.Now().Add(-30*24*time.Hour)))
	sb, err := prepareSandboxHome("s-codex-fresh", home, filepath.Join(home, ".codex"), sandboxEnginesFor("codex", ""))
	if err != nil {
		t.Fatal(err)
	}
	last, _ := readJSONFile(t, filepath.Join(sb, "codex", codexAuthN))["last_refresh"].(string)
	at, err := time.Parse(time.RFC3339Nano, last)
	if err != nil || time.Since(at) > time.Minute {
		t.Fatalf("last_refresh = %q", last)
	}
}

// A sandboxed Codex session with an MCP grant gets its private CODEX_HOME's config.toml through the
// sandbox allowlist; the host-only (unsandboxed) grant home keeps the person's settings as before.
func TestSandboxedCodexGrantHomeHasNoConfigSecrets(t *testing.T) {
	home := sandboxHomeFixture(t)
	source := filepath.Join(home, ".codex")
	sandboxed, err := prepareCodexInteractiveHomeFor(t.Context(), source, sandboxWorkdir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sandboxed) })
	raw, _ := os.ReadFile(filepath.Join(sandboxed, "config.toml"))
	for _, secret := range configSecrets {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the sandboxed grant home's config.toml holds %q", secret)
		}
	}
	if !strings.Contains(string(raw), "fixture") || !strings.Contains(string(raw), "untrusted") {
		t.Errorf("the grant home lost its model or its project trust: %s", raw)
	}
	host, err := prepareCodexInteractiveHomeFor(t.Context(), source, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(host) })
	if raw, _ := os.ReadFile(filepath.Join(host, "config.toml")); !strings.Contains(string(raw), "codex-notify-secret") {
		t.Errorf("an unsandboxed grant home changed: %s", raw)
	}
}

// The session's home goes when the session does.
func TestReleaseSandboxHome(t *testing.T) {
	home := sandboxHomeFixture(t)
	sb, err := prepareSandboxHome("s-release", home, filepath.Join(home, ".codex"), sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	releaseSandboxHome("s-release")
	if _, err := os.Stat(sb); !os.IsNotExist(err) {
		t.Fatalf("the sandbox home outlived its session: %v", err)
	}
}

// A sandboxed terminal brought back after a restart resumes from the transcript in its own home.
func TestSandboxTranscriptExists(t *testing.T) {
	home := sandboxHomeFixture(t)
	sb, err := prepareSandboxHome("s-term", home, "", sandboxEnginesFor("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if sandboxTranscriptExists("s-term") {
		t.Fatal("no transcript yet")
	}
	writeFile(t, filepath.Join(sb, "claude", "projects", "-workspace", "s-term.jsonl"), "{}\n")
	if !sandboxTranscriptExists("s-term") {
		t.Fatal("the transcript in the session's home was not found")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// labelTestContainers labels every container this test starts with a label of its own, and removes
// at the end only the containers that carry it: never another session's container.
func labelTestContainers(t *testing.T) string {
	t.Helper()
	label := "blerg-test=" + ccUUID()
	prev := sandboxTestLabels
	sandboxTestLabels = append(append([]string{}, prev...), label)
	t.Cleanup(func() {
		sandboxTestLabels = prev
		removeTestContainers(label)
	})
	return label
}

// releaseTestSandbox forgets a test session's sandbox in the daemon (registry, sandbox home). Its
// container is removed by its test label (labelTestContainers), never by name.
func releaseTestSandbox(id string) {
	unregisterSandbox(id)
	releaseSandboxHome(id)
}

// startProbeSandbox starts a real sandbox for one engine over the fixture home.
func startProbeSandbox(t *testing.T, engine string) (id, name string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	id = "home-probe-" + ccUUID()
	clone, err := createSandboxClone(repo, id, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { discardSandboxClone(clone, id) })
	name, err = startSandboxContainer(id, clone, nil, sandboxCredential{}, SandboxOptions{Engine: engine})
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	t.Cleanup(func() { releaseTestSandbox(id) })
	if out, err := exec.Command("docker", "inspect", "-f", `{{index .Config.Labels "blerg-test"}}`, name).Output(); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("the test container carries no test label: %v", err)
	}
	// Readable by the container's user whatever the uid mapping: fixtures, not real logins.
	sb, _ := sandboxHomePath(id)
	_ = filepath.Walk(sb, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			mode := os.FileMode(0o644)
			if info.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(p, mode)
		}
		return nil
	})
	return id, name
}

// End to end with a real container (BLERG_SANDBOX_CHILD_TEST=1 and a built sandbox image), for each
// engine: every write to its settings, hooks, plugins, skills and MCP configuration fails; no
// secret of the person's configuration is readable inside; the login copy has no usable refresh
// token, and a fake engine runs a turn with its access token; the person's files are unchanged.
func TestSandboxHomeEscapeProbesInDocker(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	home := sandboxHomeFixture(t)
	labelTestContainers(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			mode := os.FileMode(0o644)
			if info.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(p, mode)
		}
		return nil
	})
	before := allFiles(home)
	secrets := strings.Join(configSecrets, "|")
	common := `set -u
fail=0
escaped() { echo "ESCAPED: $1"; fail=1; }
H=/home/agent
# (Of ~/.hermes only what Blerg mounts: the rest is the image's own Hermes install.)
if grep -rIqE '` + secrets + `' $H/.claude $H/.claude.json $H/.codex $H/.hermes/config.yaml $H/.hermes/.env /proc/self/environ 2>/dev/null; then echo "SECRET: $(grep -rIlE '` + secrets + `' $H/.claude $H/.claude.json $H/.codex $H/.hermes/config.yaml $H/.hermes/.env 2>/dev/null)"; fail=1; fi
`
	probes := map[string]string{
		"claude": common + `
echo '{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch /tmp/pwned"}]}]}}' > $H/.claude/settings.json 2>/dev/null && escaped claude-settings
echo '{}' > $H/.claude/settings.local.json 2>/dev/null && escaped claude-settings-local
mkdir -p $H/.claude/hooks/x 2>/dev/null && escaped claude-hooks
touch $H/.claude/skills/planted $H/.claude/agents/planted.md $H/.claude/plugins/planted $H/.claude/commands/planted.md 2>/dev/null && escaped claude-dirs
echo planted >> $H/.claude/CLAUDE.md 2>/dev/null && escaped claude-md
echo '{"mcpServers":{"x":{"command":"evil"}}}' > $H/.claude.json 2>/dev/null && escaped claude-json
mv $H/.claude/settings.json $H/.claude/moved 2>/dev/null && escaped claude-settings-move
test -e $H/.codex/config.toml && escaped codex-visible
grep -qs KEY=fixture $H/.hermes/.env && escaped hermes-visible
grep -q "person's instructions" $H/.claude/CLAUDE.md || { echo "no CLAUDE.md"; fail=1; }
grep -q Bash $H/.claude/settings.json || { echo "no settings"; fail=1; }
# A fake engine's turn: it authenticates with the access token it was given, and cannot renew it.
python3 -c 'import json,os,sys; o=json.load(open(os.path.expanduser("~/.claude/.credentials.json")))["claudeAiOauth"]; sys.exit(0 if o["accessToken"]=="host-access" and o["refreshToken"]=="` + sandboxNoRefresh + `" else 1)' && echo "TURN OK" || { echo "fake turn failed"; fail=1; }
printf '%s' '{"claudeAiOauth":{"accessToken":"OTHER","refreshToken":"OTHER","expiresAt":9999999999999}}' > $H/.claude/.credentials.json || { echo "login not writable"; fail=1; }
exit $fail`,
		"codex": common + `
echo 'notify = ["evil"]' >> $H/.codex/config.toml 2>/dev/null && escaped codex-config
touch $H/.codex/skills/planted $H/.codex/plugins/planted $H/.codex/rules/planted 2>/dev/null && escaped codex-dirs
test -e $H/.claude/.credentials.json && escaped claude-visible
grep -q fixture $H/.codex/config.toml || { echo "no codex config"; fail=1; }
python3 -c 'import json,os,sys; t=json.load(open(os.path.expanduser("~/.codex/auth.json")))["tokens"]; sys.exit(0 if t["access_token"]=="codex-access" and t["refresh_token"]=="` + sandboxNoRefresh + `" else 1)' && echo "TURN OK" || { echo "fake turn failed"; fail=1; }
printf '%s' '{"tokens":{"id_token":"forged.unsigned.jwt","access_token":"OTHER","refresh_token":"OTHER","account_id":"acct-1"},"last_refresh":"2099-01-01T00:00:00Z"}' > $H/.codex/auth.json || { echo "login not writable"; fail=1; }
exit $fail`,
		"hermes": `set -u
fail=0
escaped() { echo "ESCAPED: $1"; fail=1; }
H=/home/agent
echo 'x: 1' >> $H/.hermes/config.yaml 2>/dev/null && escaped hermes-config
echo 'X=1' >> $H/.hermes/.env 2>/dev/null && escaped hermes-env
grep -q fixture $H/.hermes/.env || { echo "no hermes login"; fail=1; }
test -e $H/.hermes/other-config && { echo "the rest of ~/.hermes is mounted"; fail=1; }
test -e $H/.claude/.credentials.json && escaped claude-visible
test -e $H/.codex/auth.json && escaped codex-visible
exit $fail`,
	}
	for _, engine := range []string{"claude", "codex", "hermes"} {
		id, name := startProbeSandbox(t, engine)
		out, err := exec.Command("docker", "exec", name, "sh", "-c", probes[engine]).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: a probe escaped the sandbox home: %v: %s", engine, err, out)
		}
		if engine != "hermes" && !strings.Contains(string(out), "TURN OK") {
			t.Fatalf("%s: the fake engine did not run its turn: %s", engine, out)
		}
		refreshLiveSandboxLogins(time.Now())
		removeSandboxContainer(id)
		if sb, _ := sandboxHomePath(id); func() bool { _, err := os.Stat(sb); return err == nil }() {
			t.Errorf("%s: the sandbox home outlived its session", engine)
		}
	}
	// The grant path: a Codex session with an MCP grant reads its private CODEX_HOME's config.toml.
	grant, err := prepareCodexInteractiveHomeFor(t.Context(), filepath.Join(home, ".codex"), sandboxWorkdir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(grant) })
	_ = os.Chmod(grant, 0o755)
	_ = os.Chmod(filepath.Join(grant, "config.toml"), 0o644)
	repo := filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	gid := "home-grant-" + ccUUID()
	clone, err := createSandboxClone(repo, gid, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { discardSandboxClone(clone, gid) })
	gname, err := startSandboxContainer(gid, clone, nil, sandboxCredential{}, SandboxOptions{Engine: "codex", CodexHome: grant, CodexSourceHome: filepath.Join(home, ".codex")})
	if err != nil {
		t.Fatalf("grant sandbox: %v", err)
	}
	out, err := exec.Command("docker", "exec", "-e", "CODEX_HOME="+grant, gname, "sh", "-c",
		`grep -IqE '`+secrets+`' "$CODEX_HOME/config.toml" "$CODEX_HOME"/*/config.toml 2>/dev/null && { echo "SECRET in the grant home"; exit 1; }; grep -q fixture "$CODEX_HOME/config.toml" || { echo "no grant config"; exit 1; }`).CombinedOutput()
	removeSandboxContainer(gid)
	if err != nil {
		t.Fatalf("grant path: %v: %s", err, out)
	}

	after := allFiles(home)
	for path, was := range before {
		if after[path] != was {
			t.Errorf("%s changed on the host: %q", path, after[path])
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared on the host", path)
		}
	}
}

// A sandboxed session that plants a link where its grant home's config.toml goes (a reused plugin
// snapshot is writable from the container) never gets the daemon to write through it: the file is
// renamed into place. Raced on purpose: a link is planted over and over while the home is rebuilt.
func TestGrantHomeConfigNeverWritesThroughAPlantedLink(t *testing.T) {
	home := sandboxHomeFixture(t)
	source := filepath.Join(home, ".codex")
	grant := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	writeFile(t, victim, "untouched")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		link := filepath.Join(grant, "config.toml")
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(link)
			_ = os.Symlink(victim, link)
		}
	}()
	for i := 0; i < 3000; i++ {
		_ = sanitizeCodexInteractiveHomeFor(t.Context(), grant, source, sandboxWorkdir, true)
	}
	close(stop)
	wg.Wait()
	if raw, _ := os.ReadFile(victim); string(raw) != "untouched" {
		t.Fatalf("the daemon wrote through a planted link: %q", raw)
	}
}

// A FIFO in a reused plugin snapshot fails the start; it does not hang it.
func TestGrantHomePluginFIFOFailsFast(t *testing.T) {
	home := sandboxHomeFixture(t)
	grant := t.TempDir()
	dir := filepath.Join(grant, "plugins", "cache", "m", "p", "1.0.0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".mcp.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- sanitizeCodexInteractiveHomeFor(t.Context(), grant, filepath.Join(home, ".codex"), sandboxWorkdir, true)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO in the plugin snapshot was accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a FIFO in the plugin snapshot hung the start")
	}
	// A plugin file that is a link to elsewhere in the snapshot is refused too, and its target kept.
	if err := os.Remove(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(grant, "keep.json"), `{"keep":true}`)
	if err := os.Symlink(filepath.Join(grant, "keep.json"), filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatal(err)
	}
	if err := sanitizeCodexInteractiveHomeFor(t.Context(), grant, filepath.Join(home, ".codex"), sandboxWorkdir, true); err == nil {
		t.Fatal("a linked plugin file was accepted")
	}
	if raw, _ := os.ReadFile(filepath.Join(grant, "keep.json")); string(raw) != `{"keep":true}` {
		t.Fatalf("the link's target was rewritten: %s", raw)
	}
}
