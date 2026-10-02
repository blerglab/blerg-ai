package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func testGateway() *protocol.MCPGatewayConfig {
	return &protocol.MCPGatewayConfig{
		BaseURL: "http://gw.internal:9100",
		Servers: []protocol.MCPGatewayServer{
			{Name: "calendar", Token: "tok-calendar-secret"},
			{Name: "mail", Token: "tok-mail-secret"},
		},
	}
}

// The tool allow-list is a documented constant chosen by the spike: exactly
// these built-ins, in this form.
func TestGrantBuiltinToolsConstant(t *testing.T) {
	if ccGrantBuiltinTools != "Read,Write,Edit,Glob,Grep" {
		t.Fatalf("ccGrantBuiltinTools = %q", ccGrantBuiltinTools)
	}
}

// With no grant the argv is exactly what it always was; with one, the three
// grant flags follow every existing flag, in --flag=value form, with -p first.
func TestCCTurnArgsWithAndWithoutMCPConfig(t *testing.T) {
	base := []string{"-p", "do it", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--model", "claude-opus-5-5", "--effort", "max", "--resume", "cc-1"}
	if got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1"); !slices.Equal(got, base) {
		t.Fatalf("no grant changed the argv:\n%v\nwant %v", got, base)
	}
	if got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1", withMCPConfigPath("")); !slices.Equal(got, base) {
		t.Fatalf("an empty config path changed the argv: %v", got)
	}
	want := append(slices.Clone(base), "--mcp-config=/tmp/x/mcp.json", "--strict-mcp-config",
		"--tools=Read,Write,Edit,Glob,Grep", "--setting-sources=", "--disable-slash-commands",
		wantDenyFlag("/tmp/x"))
	got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1", withMCPConfigPath("/tmp/x/mcp.json"))
	if !slices.Equal(got, want) {
		t.Fatalf("grant argv:\n%v\nwant %v", got, want)
	}
	if got[0] != "-p" || got[1] != "do it" {
		t.Errorf("-p <text> must stay first: %v", got)
	}
}

// wantDenyFlag is the path deny-list flag of a restricted turn whose MCP config
// lives in configDir ("" for none).
func wantDenyFlag(configDir string) string {
	return "--disallowedTools=" + strings.Join(ccPathDenyRules(configDir), ",")
}

// Path-scoped permission denies (F3 MAJOR 1). Proven on the pinned Claude Code
// 2.1.284 under --dangerously-skip-permissions with --tools and an EMPTY
// --setting-sources: a Read/Edit deny holds for Read, Grep and Glob (recursive
// searches skip the denied tree), for Write via the Edit rule, for symlinks,
// "..", "~" and a wildcarded directory name. Absolute paths need the "//"
// prefix ("/x" means relative to the project root).
func TestCCPathDenyRules(t *testing.T) {
	rules := ccPathDenyRules("/tmp/blerg-mcp-abc")
	for _, want := range []string{
		"Read(//proc/**)", "Read(~/.claude/**)", "Read(~/.claude.json)", "Edit(~/.claude/**)", "Edit(~/.claude.json)",
		"Read(//tmp/blerg-mcp-*/**)", "Read(//tmp/blerg-mcp-abc/**)", "Edit(//tmp/blerg-mcp-abc/**)",
		"Read(//etc/**)", "Read(//var/run/secrets/**)", "Read(//run/secrets/**)",
	} {
		if !slices.Contains(rules, want) {
			t.Errorf("deny rules lack %q: %v", want, rules)
		}
	}
	for _, r := range rules {
		if strings.ContainsAny(r, ", ") {
			t.Errorf("rule %q would split or break the comma-joined flag", r)
		}
		if strings.Contains(r, "(/") && !strings.Contains(r, "(//") && !strings.Contains(r, "(/.") {
			t.Errorf("absolute rule %q must use the // prefix (only the project-relative /.x rules may not)", r)
		}
	}
	// MINOR 32: the logins and credentials of the other engines and of cluster/dev tooling are
	// denied for reading AND overwriting, in the same "~" form proven for ~/.claude.
	for _, p := range []string{
		"~/.codex/**", "~/.hermes/**", "~/.kube/**", "~/.config/gh/**", "~/.config/git/**", "~/.docker/**",
		"~/.npmrc", "~/.pypirc", "~/.config/gcloud/**", "~/.azure/**",
		"~/.ssh/**", "~/.aws/**", "~/.gnupg/**", "~/.netrc", "~/.git-credentials",
	} {
		if !slices.Contains(rules, "Read("+p+")") {
			t.Errorf("deny rules lack Read(%s)", p)
		}
	}
	for _, p := range []string{
		"~/.codex/**", "~/.hermes/**", "~/.kube/**", "~/.config/gh/**", "~/.config/git/**", "~/.docker/**",
		"~/.npmrc", "~/.pypirc", "~/.config/gcloud/**", "~/.azure/**",
	} {
		if !slices.Contains(rules, "Edit("+p+")") {
			t.Errorf("deny rules lack Edit(%s): a login could be overwritten", p)
		}
	}
	// A config directory that cannot be expressed safely adds no extra rule
	// (the generic tmp pattern still covers the runner's own directories).
	for _, bad := range []string{"", "relative/dir", "/tmp/a,b", "/tmp/a b", "/tmp/a)b"} {
		for _, r := range ccPathDenyRules(bad) {
			if strings.Contains(r, "a,b") || strings.Contains(r, "a b") || strings.Contains(r, "a)b") || strings.Contains(r, "relative") {
				t.Errorf("unsafe dir %q leaked into rule %q", bad, r)
			}
		}
	}
	// The repository's own control files: .git holds the clone credential of a cluster pod and settings the
	// runner's git (and a developer's, in a bind-mounted checkout) EXECUTES; the agent-tool config files are
	// read by the next unrestricted run. Denied project-relative ("/x", root) and nested ("**/x"); proven with
	// the real CLI for Read (root and nested), Grep (the tree is skipped), Edit and Write.
	for _, p := range []string{"/.git", "/.git/**", "**/.git/**"} {
		for _, verb := range []string{"Read", "Edit"} {
			if !slices.Contains(rules, verb+"("+p+")") {
				t.Errorf("deny rules lack %s(%s): the git config could be read or rewritten", verb, p)
			}
		}
	}
	for _, p := range []string{"/.claude/**", "**/.claude/**", "/.mcp.json", "**/.mcp.json", "/.vscode/**", "**/.vscode/**", "/.envrc", "/.husky/**", "/.githooks/**"} {
		if !slices.Contains(rules, "Edit("+p+")") {
			t.Errorf("deny rules lack Edit(%s): the file could be planted for the next run in this checkout", p)
		}
	}
	// ...but ordinary files in the project stay writable: no rule denies everything under the root.
	for _, r := range rules {
		if r == "Edit(/**)" || r == "Edit(**)" || r == "Read(/**)" || r == "Read(**)" {
			t.Errorf("rule %q denies ordinary project files", r)
		}
	}
	// The session workdir stays usable: nothing denies a broad prefix.
	for _, r := range rules {
		for _, broad := range []string{"//tmp/**", "//home/**", "//workspace/**", "//**"} {
			if strings.HasSuffix(r, broad+")") {
				t.Errorf("rule %q denies too much", r)
			}
		}
	}
}

// The flag is comma-joined in --flag=value form (a variadic flag could swallow
// the prompt) and present exactly once on a restricted turn, never on a plain one.
func TestCCTurnArgsDenyFlagOnlyWhenRestricted(t *testing.T) {
	plain := ccTurnArgs("do it", "", "", "")
	for _, a := range plain {
		if strings.Contains(a, "disallowedTools") {
			t.Fatalf("a plain session got the deny flag: %v", plain)
		}
	}
	got := ccTurnArgs("do it", "", "", "", withRestrictTools())
	n := 0
	for _, a := range got {
		if strings.HasPrefix(a, "--disallowedTools=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one --disallowedTools= flag, got %d: %v", n, got)
	}
	if !slices.Contains(got, wantDenyFlag("")) {
		t.Errorf("restrict-only deny flag missing: %v", got)
	}
}

// After Remove (the session was killed) the file is never written again: a
// turn racing the kill must not leave a token file behind.
func TestMCPConfigFileNotRecreatedAfterRemove(t *testing.T) {
	f := newMCPConfigFile(testGateway(), sandboxExec{})
	path, err := f.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	f.Remove()
	if _, err := f.Ensure(); err == nil {
		t.Fatal("Ensure after Remove succeeded")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("config dir exists after Remove+Ensure: %v", err)
	}
	var nilFile *mcpConfigFile
	nilFile.Remove() // nil-safe, as before
}

// A restricted session with no grant (a cron with no MCP connections) still
// gets the tool allow-list, and no MCP server at all: an inline empty config
// with --strict-mcp-config, so nothing ambient (user, project, connector,
// plugin) loads. An empty --setting-sources and --disable-slash-commands keep
// the developer's settings, plugins, hooks and skills out. Verified on the
// pinned Claude Code 2.1.284 (see ccHardeningFlags).
func TestCCTurnArgsRestrictOnlyAndBoth(t *testing.T) {
	base := []string{"-p", "do it", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--model", "claude-opus-5-5", "--effort", "max", "--resume", "cc-1"}
	wantOnly := append(slices.Clone(base), `--mcp-config={"mcpServers":{}}`, "--strict-mcp-config",
		"--tools=Read,Write,Edit,Glob,Grep", "--setting-sources=", "--disable-slash-commands", wantDenyFlag(""))
	if got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1", withRestrictTools()); !slices.Equal(got, wantOnly) {
		t.Fatalf("restrict-only argv:\n%v\nwant %v", got, wantOnly)
	}
	wantBoth := append(slices.Clone(base), "--mcp-config=/tmp/x/mcp.json", "--strict-mcp-config",
		"--tools=Read,Write,Edit,Glob,Grep", "--setting-sources=", "--disable-slash-commands", wantDenyFlag("/tmp/x"))
	got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1", withRestrictTools(), withMCPConfigPath("/tmp/x/mcp.json"))
	if !slices.Equal(got, wantBoth) {
		t.Fatalf("restrict+grant argv:\n%v\nwant %v", got, wantBoth)
	}
	got = ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1", withMCPConfigPath("/tmp/x/mcp.json"), withRestrictTools())
	if !slices.Equal(got, wantBoth) {
		t.Fatalf("option order changed the argv: %v", got)
	}
	// A plain session is unchanged, byte for byte.
	if got := ccTurnArgs("do it", "claude-opus-5-5", "max", "cc-1"); !slices.Equal(got, base) {
		t.Fatalf("plain argv changed: %v", got)
	}
}

func TestMCPConfigJSONShape(t *testing.T) {
	raw, err := mcpConfigJSON(testGateway())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	cal := got["mcpServers"]["calendar"]
	if cal["type"] != "http" || cal["url"] != "http://gw.internal:9100/mcp-gw/calendar" {
		t.Errorf("calendar = %v", cal)
	}
	hdr, _ := cal["headers"].(map[string]any)
	if hdr["Authorization"] != "Bearer tok-calendar-secret" || len(hdr) != 1 {
		t.Errorf("headers = %v", hdr)
	}
	if len(got["mcpServers"]) != 2 || len(got) != 1 {
		t.Errorf("unexpected shape: %s", raw)
	}
	// A trailing slash on the base is not doubled.
	cfg := testGateway()
	cfg.BaseURL += "/"
	raw, _ = mcpConfigJSON(cfg)
	if !strings.Contains(string(raw), `http://gw.internal:9100/mcp-gw/mail`) {
		t.Errorf("trailing slash: %s", raw)
	}
}

func TestValidateMCPGateway(t *testing.T) {
	if err := ValidateMCPGateway(testGateway()); err != nil {
		t.Fatalf("a good grant was refused: %v", err)
	}
	mut := func(f func(c *protocol.MCPGatewayConfig)) *protocol.MCPGatewayConfig {
		c := testGateway()
		f(c)
		return c
	}
	bad := map[string]*protocol.MCPGatewayConfig{
		"no servers":      mut(func(c *protocol.MCPGatewayConfig) { c.Servers = nil }),
		"empty base":      mut(func(c *protocol.MCPGatewayConfig) { c.BaseURL = "" }),
		"file scheme":     mut(func(c *protocol.MCPGatewayConfig) { c.BaseURL = "file:///etc/passwd" }),
		"userinfo":        mut(func(c *protocol.MCPGatewayConfig) { c.BaseURL = "http://u:p@gw:1" }),
		"query":           mut(func(c *protocol.MCPGatewayConfig) { c.BaseURL = "http://gw:1?x=1" }),
		"name with slash": mut(func(c *protocol.MCPGatewayConfig) { c.Servers[0].Name = "a/b" }),
		"name empty":      mut(func(c *protocol.MCPGatewayConfig) { c.Servers[0].Name = "" }),
		"name quote":      mut(func(c *protocol.MCPGatewayConfig) { c.Servers[0].Name = `a"b` }),
		"duplicate name":  mut(func(c *protocol.MCPGatewayConfig) { c.Servers[1].Name = c.Servers[0].Name }),
		"empty token":     mut(func(c *protocol.MCPGatewayConfig) { c.Servers[0].Token = "" }),
		"token newline":   mut(func(c *protocol.MCPGatewayConfig) { c.Servers[0].Token = "a\nb" }),
		"too many": mut(func(c *protocol.MCPGatewayConfig) {
			for i := range 30 {
				c.Servers = append(c.Servers, protocol.MCPGatewayServer{Name: "s" + strings.Repeat("x", i), Token: "t"})
			}
		}),
	}
	for name, cfg := range bad {
		if err := ValidateMCPGateway(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateMCPGateway(nil); err == nil {
		t.Error("nil accepted")
	}
}

// The host file is 0600 in a private 0700 directory outside the workdir, is
// regenerated when its directory vanished (recovery, a temp cleaner), and is
// gone after Remove.
func TestMCPConfigFileHostLifecycle(t *testing.T) {
	work := t.TempDir()
	f := newMCPConfigFile(testGateway(), sandboxExec(nil))
	path, err := f.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(work, path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("config %s is inside the workdir %s", path, work)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", dst.Mode().Perm())
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "Bearer tok-mail-secret") {
		t.Errorf("file lacks the token: %s", body)
	}

	// Same path while it exists.
	if again, _ := f.Ensure(); again != path {
		t.Errorf("Ensure moved a live file: %s -> %s", path, again)
	}
	// The directory vanishes (temp cleaner, restart): it is written again.
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	path2, err := f.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path2); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("not regenerated 0600: %v %v", st, err)
	}
	f.Remove()
	if _, err := os.Stat(filepath.Dir(path2)); !os.IsNotExist(err) {
		t.Errorf("config dir survived Remove: %v", err)
	}
	f.Remove() // idempotent
}

// Recovery: a driver built around a config source writes the file before a
// turn even when nothing exists on disk, and passes the path it made.
func TestDriverRegeneratesMissingConfigEachTurn(t *testing.T) {
	binDir := fakeClaude(t)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_LOG", logPath)

	f := newMCPConfigFile(testGateway(), sandboxExec(nil))
	t.Cleanup(f.Remove)
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil, withMCPConfigSource(f))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	runTurn := func(text string) {
		t.Helper()
		want := countTurnKind(em.kinds(), "turn_done") + 1
		d.Enqueue(text, "chat")
		deadline := time.Now().Add(5 * time.Second)
		for countTurnKind(em.kinds(), "turn_done") < want {
			if time.Now().After(deadline) {
				t.Fatalf("no turn_done for %q; kinds: %v", text, em.kinds())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	runTurn("one")
	first, _ := f.Ensure()
	if err := os.RemoveAll(filepath.Dir(first)); err != nil { // temp dir gone between turns
		t.Fatal(err)
	}
	runTurn("two")

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("calls: %q", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "--mcp-config=") || !strings.Contains(l, "--strict-mcp-config") ||
			!strings.Contains(l, "--tools=Read,Write,Edit,Glob,Grep --setting-sources= --disable-slash-commands") {
			t.Errorf("turn lacks the grant flags: %s", l)
		}
		if strings.Contains(l, "tok-") {
			t.Errorf("a token reached argv: %s", l)
		}
	}
	second, _ := f.Ensure()
	if _, err := os.Stat(second); err != nil {
		t.Errorf("config not regenerated for the second turn: %v", err)
	}
}

func countTurnKind(kinds []string, k string) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

// ─── sandbox delivery ────────────────────────────────────────────────────────

// The sandbox file is written INSIDE the container through docker exec with
// the content on stdin (so no token is in any argv), into a 0700 directory
// under /tmp, and removed at the end.
func TestMCPConfigFileSandboxDelivery(t *testing.T) {
	var calls [][]string
	var stdins []string
	origRun, origStdin := dockerRun, dockerRunStdin
	dockerRun = func(args ...string) error { calls = append(calls, args); return nil }
	dockerRunStdin = func(stdin []byte, args ...string) error {
		calls = append(calls, args)
		stdins = append(stdins, string(stdin))
		return nil
	}
	t.Cleanup(func() { dockerRun, dockerRunStdin = origRun, origStdin })

	f := newMCPConfigFile(testGateway(), sandboxExecPrefix("blerg-sandbox-s1"))
	path, err := f.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/tmp/blerg-mcp-") || !strings.HasSuffix(path, "/mcp.json") {
		t.Errorf("in-container path = %q", path)
	}
	if len(stdins) != 1 || !strings.Contains(stdins[0], "Bearer tok-calendar-secret") {
		t.Fatalf("the config did not travel on stdin: %q", stdins)
	}
	all := ""
	for _, c := range calls {
		all += strings.Join(c, " ") + "\n"
	}
	if strings.Contains(all, "tok-") {
		t.Errorf("a token is in a docker argv:\n%s", all)
	}
	first := calls[0]
	if first[0] != "exec" || !slices.Contains(first, "-i") || !slices.Contains(first, "blerg-sandbox-s1") {
		t.Errorf("not a docker exec into the session container: %v", first)
	}
	script := first[len(first)-3] // sh -c <script> sh <dir>
	if !strings.Contains(script, "umask 077") || !strings.Contains(script, "mkdir -m 700") {
		t.Errorf("script does not make a private dir: %q", script)
	}

	// Removal is an exec into the same container, of that directory only.
	calls = nil
	f.Remove()
	want := "exec blerg-sandbox-s1 rm -rf " + filepath.Dir(path)
	if len(calls) != 1 || strings.Join(calls[0], " ") != want {
		t.Errorf("remove calls = %v, want %q", calls, want)
	}
}

// Recovery in the sandbox: when the file is gone from the container (a
// recreated container), Ensure writes it again.
func TestMCPConfigFileSandboxRegenerates(t *testing.T) {
	present := false
	writes := 0
	origRun, origStdin := dockerRun, dockerRunStdin
	dockerRun = func(args ...string) error { // `test -f`
		if slices.Contains(args, "test") && !present {
			return os.ErrNotExist
		}
		return nil
	}
	dockerRunStdin = func(_ []byte, _ ...string) error { writes++; present = true; return nil }
	t.Cleanup(func() { dockerRun, dockerRunStdin = origRun, origStdin })

	f := newMCPConfigFile(testGateway(), sandboxExecPrefix("blerg-sandbox-s2"))
	if _, err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("writes = %d after a present file, want 1", writes)
	}
	present = false
	if _, err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Errorf("writes = %d, want a regeneration", writes)
	}
}

// ─── spawn paths ─────────────────────────────────────────────────────────────

// A real agent spawn with a grant on the (fake) sandbox: the container gets the
// file through exec, the turn runs with the grant flags at the in-container
// path, and Kill removes the container and the file.
func TestSandboxAgentSpawnDeliversGrant(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	claudeLog := filepath.Join(engineDir, "claude-calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	base := t.TempDir()
	prev := sandboxMCPDirBase
	sandboxMCPDirBase = base
	t.Cleanup(func() { sandboxMCPDirBase = prev })

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-grant", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello", MCPGateway: testGateway(),
	})
	sender.waitForKind(t, "turn_done")

	calls := dockerCalls(t, logPath)
	if strings.Contains(calls, "tok-") {
		t.Errorf("a token reached a docker argv:\n%s", calls)
	}
	entries, _ := filepath.Glob(filepath.Join(base, "blerg-mcp-*", "mcp.json"))
	if len(entries) != 1 {
		t.Fatalf("config not written inside the (fake) container: %v\n%s", entries, calls)
	}
	body, _ := os.ReadFile(entries[0])
	if !strings.Contains(string(body), "tok-calendar-secret") {
		t.Errorf("config body: %s", body)
	}
	raw, _ := os.ReadFile(claudeLog)
	if !strings.Contains(string(raw), "--mcp-config="+entries[0]) || !strings.Contains(string(raw), "--tools=Read,Write,Edit,Glob,Grep") {
		t.Errorf("turn argv lacks the in-container config path: %s", raw)
	}

	host.Kill("s-grant")
	if !strings.Contains(dockerCalls(t, logPath), "rm -rf "+filepath.Dir(entries[0])) {
		t.Errorf("Kill did not remove the config dir:\n%s", dockerCalls(t, logPath))
	}
	if _, err := os.Stat(entries[0]); !os.IsNotExist(err) {
		t.Errorf("config survived Kill: %v", err)
	}
}

// A restricted sandboxed spawn with NO grant (a cron without MCP connections):
// no config file is written, but the turn still runs with the allow-list, an
// empty inline MCP config and no user settings.
func TestSandboxAgentSpawnRestrictOnly(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	claudeLog := filepath.Join(engineDir, "claude-calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	base := t.TempDir()
	prev := sandboxMCPDirBase
	sandboxMCPDirBase = base
	t.Cleanup(func() { sandboxMCPDirBase = prev })

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-restrict", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello", RestrictTools: true,
	})
	sender.waitForKind(t, "turn_done")

	if entries, _ := filepath.Glob(filepath.Join(base, "blerg-mcp-*")); len(entries) != 0 {
		t.Errorf("a config was written for a session with no grant: %v\n%s", entries, dockerCalls(t, logPath))
	}
	raw, _ := os.ReadFile(claudeLog)
	line := strings.TrimSpace(string(raw))
	want := `--mcp-config={"mcpServers":{}} --strict-mcp-config --tools=Read,Write,Edit,Glob,Grep --setting-sources= --disable-slash-commands ` + wantDenyFlag("")
	if !strings.HasSuffix(line, want) {
		t.Errorf("turn argv = %s\nwant suffix %s", line, want)
	}
	host.Kill("s-restrict")
}

// A pod (the AgentHost on the "bare host" of a container) restricts a session
// that has no grant, and an unrestricted spawn is byte-identical to today.
func TestHostAgentSpawnRestrictOnlyAndPlain(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-pod-restrict", Repo: "proj", Kind: "agent",
		InitialPrompt: "hello", RestrictTools: true,
	})
	sender.waitForKind(t, "turn_done")
	raw, _ := os.ReadFile(claudeLog)
	if !strings.HasSuffix(strings.TrimSpace(string(raw)),
		`--mcp-config={"mcpServers":{}} --strict-mcp-config --tools=Read,Write,Edit,Glob,Grep --setting-sources= --disable-slash-commands `+wantDenyFlag("")) {
		t.Errorf("restricted pod argv = %s", raw)
	}
	host.Kill("s-pod-restrict")

	if err := os.Remove(claudeLog); err != nil {
		t.Fatal(err)
	}
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-pod-plain", Repo: "proj", Kind: "agent", InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	raw, _ = os.ReadFile(claudeLog)
	for _, f := range []string{"--mcp-config", "--strict-mcp-config", "--tools", "--setting-sources", "--disable-slash-commands"} {
		if strings.Contains(string(raw), f) {
			t.Errorf("an unrestricted session got %s: %s", f, raw)
		}
	}
	host.Kill("s-pod-plain")
}

// A restriction that cannot be honoured (an engine other than Claude Code)
// is refused, never dropped.
func TestAgentSpawnRefusesRestrictionItCannotHonour(t *testing.T) {
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	msg := protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-restrict-codex", Repo: "proj", Kind: "agent",
		Engine: "codex", RestrictTools: true,
	}
	host.Spawn(msg)
	if reason := sender.waitForSessionError(t); reason == "" || host.Has(msg.SessionID) {
		t.Errorf("not refused (reason %q)", reason)
	}
}

// A bare-host agent spawn with a grant: the file is on the host outside the
// workdir, and Kill removes it. The fake claude sees the grant flags.
func TestHostAgentSpawnWritesAndRemovesGrantFile(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "master-token-must-not-leak")
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-host-grant", Repo: "proj", Kind: "agent",
		InitialPrompt: "hello", MCPGateway: testGateway(),
	})
	sender.waitForKind(t, "turn_done")

	raw, _ := os.ReadFile(claudeLog)
	line := strings.TrimSpace(string(raw))
	i := strings.Index(line, "--mcp-config=")
	if i < 0 {
		t.Fatalf("no --mcp-config in %s", line)
	}
	path := strings.Fields(line[i:])[0][len("--mcp-config="):]
	if strings.HasPrefix(path, reposRoot) {
		t.Errorf("config %s is inside the repos root/workdir %s", path, reposRoot)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v %v", st, err)
	}
	host.Kill("s-host-grant")
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("config dir survived Kill: %v", err)
	}
}

// A grant that cannot be honoured is refused loudly, never dropped: an engine
// other than Claude Code would otherwise run with every tool and no MCP.
func TestAgentSpawnRefusesGrantItCannotHonour(t *testing.T) {
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, msg := range map[string]protocol.SpawnSession{
		"codex engine": {SessionID: "s-a", Repo: "proj", Kind: "agent", Engine: "codex", MCPGateway: testGateway()},
		"invalid grant": {SessionID: "s-b", Repo: "proj", Kind: "agent", MCPGateway: &protocol.MCPGatewayConfig{
			BaseURL: "file:///x", Servers: testGateway().Servers}},
	} {
		sender := &agentTestSender{}
		host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
		host.Spawn(msg)
		reason := sender.waitForSessionError(t)
		if reason == "" || host.Has(msg.SessionID) {
			t.Errorf("%s: not refused (reason %q)", name, reason)
		}
		if strings.Contains(reason, "tok-") {
			t.Errorf("%s: the refusal leaks a token: %q", name, reason)
		}
	}
}

// The daemon's own gate: a grant on a terminal or bare-host spawn is refused
// (defence in depth; the server refuses the same).
func TestMCPGatewaySpawnProblem(t *testing.T) {
	g := testGateway()
	cases := []struct {
		name string
		msg  protocol.SpawnSession
		want bool // refused
	}{
		{"no grant, bare host", protocol.SpawnSession{Kind: "agent"}, false},
		{"sandboxed agent", protocol.SpawnSession{Kind: "agent", Sandbox: true, MCPGateway: g}, false},
		{"bare host agent", protocol.SpawnSession{Kind: "agent", MCPGateway: g}, true},
		{"terminal session", protocol.SpawnSession{Sandbox: true, MCPGateway: g}, true},
		{"restricted sandboxed agent", protocol.SpawnSession{Kind: "agent", Sandbox: true, RestrictTools: true}, false},
		{"restricted bare host agent", protocol.SpawnSession{Kind: "agent", RestrictTools: true}, true},
		{"restricted terminal session", protocol.SpawnSession{Sandbox: true, RestrictTools: true}, true},
	}
	for _, c := range cases {
		if got := mcpGatewaySpawnProblem(c.msg) != ""; got != c.want {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.want)
		}
	}
}

// The environment every session subprocess starts from never carries the pod's
// gateway variable, whether it is inherited or asked for through ExtraEnv.
func TestSessionEnvNeverCarriesGatewayVariable(t *testing.T) {
	t.Setenv(MCPGatewayEnvVar, `{"base_url":"http://gw","servers":[{"name":"a","token":"tok-leak"}]}`)
	env := buildSessionEnv(sessionEnvOpts{
		SessionID: "s1",
		Extra:     map[string]string{MCPGatewayEnvVar: "tok-extra", "OTHER": "ok"},
	})
	for _, e := range env {
		if strings.HasPrefix(e, MCPGatewayEnvVar+"=") || strings.Contains(e, "tok-") {
			t.Errorf("gateway variable in the session env: %q", e)
		}
	}
	if !slices.Contains(env, "OTHER=ok") {
		t.Error("ordinary ExtraEnv was dropped")
	}
	if validExtraEnvKey(MCPGatewayEnvVar) {
		t.Error("validExtraEnvKey accepts the gateway variable")
	}
	t.Setenv(RestrictToolsEnvVar, "1")
	for _, e := range buildSessionEnv(sessionEnvOpts{SessionID: "s1", Extra: map[string]string{RestrictToolsEnvVar: "0"}}) {
		if strings.HasPrefix(e, RestrictToolsEnvVar+"=") {
			t.Errorf("restriction variable in the session env: %q", e)
		}
	}
	if validExtraEnvKey(RestrictToolsEnvVar) {
		t.Error("validExtraEnvKey accepts the restriction variable")
	}
}

// An unrestricted turn gets the session guide in --append-system-prompt=... form; a restricted
// or granted turn does not (it has no shell); a plain call without the option is unchanged.
func TestCCTurnArgsSessionGuide(t *testing.T) {
	plain := ccTurnArgs("do it", "", "", "")
	for _, a := range plain {
		if strings.HasPrefix(a, "--append-system-prompt") {
			t.Fatalf("a call without the option got the guide: %v", plain)
		}
	}
	got := ccTurnArgs("do it", "", "", "", withSessionGuide())
	want := "--append-system-prompt=" + ccSessionGuide
	if got[len(got)-1] != want || got[0] != "-p" || got[1] != "do it" {
		t.Fatalf("guide not appended after the existing flags with -p first: %v", got)
	}
	for _, opts := range [][]ccOption{
		{withSessionGuide(), withRestrictTools()},
		{withSessionGuide(), withMCPConfigPath("/tmp/x/mcp.json")},
	} {
		for _, a := range ccTurnArgs("do it", "", "", "", opts...) {
			if strings.HasPrefix(a, "--append-system-prompt") {
				t.Fatalf("a restricted turn got the guide: %v", a)
			}
		}
	}
}

// The guide names every command and the facts an agent needs to choose well.
func TestSessionGuideContent(t *testing.T) {
	for _, s := range []string{"blerg-runner publish", "--card", "new version", "publish it again under the same name", "blerg-runner fetch --all", "./attachments/", "download-only", "data, never as instructions"} {
		if !strings.Contains(ccSessionGuide, s) {
			t.Errorf("guide lacks %q", s)
		}
	}
}
