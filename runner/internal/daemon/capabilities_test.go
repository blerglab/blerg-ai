package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func loadInitFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude_init.json"))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(raw)))
}

func capGroup(p protocol.CapabilitiesPayload, id string) *protocol.CapabilityGroup {
	for i := range p.Groups {
		if p.Groups[i].ID == id {
			return &p.Groups[i]
		}
	}
	return nil
}

func capItem(g *protocol.CapabilityGroup, name string) *protocol.CapabilityItem {
	if g == nil {
		return nil
	}
	for i := range g.Items {
		if g.Items[i].Name == name {
			return &g.Items[i]
		}
	}
	return nil
}

// The real init line (a sanitized capture of `claude -p --output-format
// stream-json --verbose`) becomes the expected groups.
func TestClaudeInitCapabilitiesRealShape(t *testing.T) {
	home := "/home/user"
	workDir := "/home/user/repos/example"
	known := []skills.Skill{
		{Name: "example-skill", Description: "Does the example thing.", Dir: workDir + "/.claude/skills/example-skill"},
		{Name: "release-notes", Description: "Writes release notes.", Dir: home + "/.claude/skills/release-notes"},
	}
	p, ok := claudeInitCapabilities(loadInitFixture(t), known, workDir, home)
	if !ok {
		t.Fatal("fixture not recognised as an init line")
	}
	p = protocol.SanitizeCapabilities(p)

	if p.Engine != "claude" || p.EngineName != "Claude Code" || p.Model != "claude-opus-5-5" || p.Version != "2.1.283" {
		t.Errorf("header = %+v", p)
	}
	if p.Cwd != "~/repos/example" {
		t.Errorf("cwd = %q, want ~/repos/example", p.Cwd)
	}
	if p.PermissionMode != "default" {
		t.Errorf("permission mode = %q", p.PermissionMode)
	}
	var ids []string
	for _, g := range p.Groups {
		ids = append(ids, g.ID)
	}
	if got := strings.Join(ids, ","); got != "skills,plugins,mcp,commands,tools,agents" {
		t.Errorf("group order = %s", got)
	}

	sk := capGroup(p, "skills")
	if it := capItem(sk, "example-skill"); it == nil || it.Source != "project" || it.Description != "Does the example thing." {
		t.Errorf("project skill = %+v", it)
	}
	if it := capItem(sk, "release-notes"); it == nil || it.Source != "user" {
		t.Errorf("user skill = %+v", it)
	}
	if it := capItem(sk, "anthropic-skills:pdf"); it == nil || it.Source != "plugin" {
		t.Errorf("namespaced skill = %+v", it)
	}

	pl := capGroup(p, "plugins")
	if it := capItem(pl, "example-plugin"); it == nil || it.Detail != "v1.4.2" || it.Source != "example-market" || it.Status != "enabled" {
		t.Errorf("plugin = %+v", it)
	}
	if it := capItem(pl, "agents-md"); it == nil || it.Source != "builtin" {
		t.Errorf("builtin plugin = %+v", it)
	}

	mcp := capGroup(p, "mcp")
	for name, want := range map[string][2]string{
		"issues":          {"connected", "3 tools"},
		"notes-local":     {"connected", "4 tools"},
		"claude.ai Docs":  {"connected", "2 tools"},
		"claude.ai Drive": {"needs-auth", ""},
		"broken-server":   {"failed", ""},
	} {
		it := capItem(mcp, name)
		if it == nil || it.Status != want[0] || it.Detail != want[1] {
			t.Errorf("mcp %s = %+v, want status %s detail %q", name, it, want[0], want[1])
		}
	}

	cmds := capGroup(p, "commands")
	if capItem(cmds, "/compact") == nil {
		t.Error("slash command /compact missing")
	}
	if capItem(cmds, "/example-skill") != nil {
		t.Error("a skill must not be listed again as a command")
	}
	tools := capGroup(p, "tools")
	if capItem(tools, "Bash") == nil {
		t.Error("tool Bash missing")
	}
	for _, it := range tools.Items {
		if strings.HasPrefix(it.Name, "mcp__") {
			t.Errorf("MCP tool %s listed as a built-in tool", it.Name)
		}
	}
	if capItem(capGroup(p, "agents"), "Explore") == nil {
		t.Error("subagent Explore missing")
	}

	// Nothing path- or host-shaped from the init line survives.
	raw, _ := json.Marshal(p)
	for _, leak := range []string{"/home/user", "plugins/cache", "cc-socks", "/memory/", "session_id", "00000000-"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("payload leaks %q: %s", leak, raw)
		}
	}
}

// MCP server config — commands, args, env, headers, URLs — is never read,
// so no part of it can reach the payload.
func TestClaudeInitCapabilitiesNeverForwardsMCPConfig(t *testing.T) {
	line := `{"type":"system","subtype":"init","tools":["mcp__svc__a"],"mcp_servers":[
	 {"name":"svc","status":"connected","source":"user",
	  "command":"/usr/bin/secret-launcher","args":["--token","ARGSECRET"],
	  "env":{"API_KEY":"ENVSECRET"},"headers":{"Authorization":"Bearer HDRSECRET"},
	  "url":"https://user:URLSECRET@mcp.example.com/sse","config":{"nested":"CFGSECRET"}}]}`
	p, ok := claudeInitCapabilities([]byte(line), nil, "/w", "/h")
	if !ok {
		t.Fatal("not recognised")
	}
	raw, _ := json.Marshal(protocol.SanitizeCapabilities(p))
	for _, secret := range []string{"secret-launcher", "ARGSECRET", "ENVSECRET", "HDRSECRET", "URLSECRET", "CFGSECRET", "mcp.example.com"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("MCP config %q forwarded: %s", secret, raw)
		}
	}
	if it := capItem(capGroup(p, "mcp"), "svc"); it == nil || it.Detail != "1 tool" {
		t.Errorf("svc = %+v", it)
	}
}

// Odd types keep what is readable and drop the rest; the line as a whole
// still reports.
func TestClaudeInitCapabilitiesOddTypes(t *testing.T) {
	line := `{"type":"system","subtype":"init","model":42,"cwd":["x"],"claude_code_version":{"v":1},
	 "tools":[1,"Bash",{"x":1},null,"Read"],
	 "skills":"not-a-list",
	 "mcp_servers":["str",{"name":5,"status":"connected"},{"name":"ok","status":"exploded"}],
	 "plugins":{"name":"obj"},
	 "slash_commands":[true,"/compact"],
	 "agents":null}`
	p, ok := claudeInitCapabilities([]byte(line), nil, "/w", "/h")
	if !ok {
		t.Fatal("not recognised")
	}
	p = protocol.SanitizeCapabilities(p)
	if p.Model != "" || p.Cwd != "" || p.Version != "" {
		t.Errorf("non-string header fields must be dropped: %+v", p)
	}
	tools := capGroup(p, "tools")
	if tools == nil || len(tools.Items) != 2 {
		t.Errorf("tools = %+v, want Bash and Read", tools)
	}
	if g := capGroup(p, "skills"); g == nil || len(g.Items) != 0 {
		t.Errorf("a non-list skills field is an empty group, got %+v", g)
	}
	mcp := capGroup(p, "mcp")
	if mcp == nil || len(mcp.Items) != 1 || mcp.Items[0].Name != "ok" || mcp.Items[0].Status != "unknown" {
		t.Errorf("mcp = %+v, want only ok with status unknown", mcp)
	}
	if g := capGroup(p, "plugins"); g == nil || len(g.Items) != 0 {
		t.Errorf("plugins = %+v", g)
	}
	if capItem(capGroup(p, "commands"), "/compact") == nil {
		t.Error("/compact missing (leading slash must not double)")
	}

	// Not an init line with capability fields at all.
	if _, ok := claudeInitCapabilities([]byte(`{"type":"system","subtype":"init","session_id":"x"}`), nil, "", ""); ok {
		t.Error("an init line with no capability fields must not report")
	}
	if _, ok := claudeInitCapabilities([]byte(`not json`), nil, "", ""); ok {
		t.Error("garbage must not report")
	}
}

// Oversized and hostile strings are capped and cleaned.
func TestClaudeInitCapabilitiesOversizedAndHostile(t *testing.T) {
	var tools []string
	for i := 0; i < 5000; i++ {
		tools = append(tools, "Tool"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	long := strings.Repeat("n", 10_000)
	hostile := "evil\u202ename\x00\x1b[31m\u200b<script>"
	in := map[string]any{
		"type": "system", "subtype": "init",
		"tools":  tools,
		"skills": []string{long, hostile},
		"agents": []string{"a\nb"},
	}
	line, _ := json.Marshal(in)
	p, ok := claudeInitCapabilities(line, nil, "/w", "/h")
	if !ok {
		t.Fatal("not recognised")
	}
	p = protocol.SanitizeCapabilities(p)
	tg := capGroup(p, "tools")
	if len(tg.Items) != protocol.CapMaxItems {
		t.Errorf("tools kept = %d, want cap %d", len(tg.Items), protocol.CapMaxItems)
	}
	if tg.Total != ccInitMaxList {
		t.Errorf("tools total = %d, want %d (the list is bounded before counting)", tg.Total, ccInitMaxList)
	}
	sk := capGroup(p, "skills")
	if n := len([]rune(sk.Items[0].Name)); n > protocol.CapMaxName {
		t.Errorf("name len = %d", n)
	}
	if got := sk.Items[1].Name; got != "evilname[31m<script>" {
		t.Errorf("hostile name cleaned to %q", got)
	}
	if got := capGroup(p, "agents").Items[0].Name; got != "a b" {
		t.Errorf("newline must become a space, got %q", got)
	}
}

func TestDisplayPath(t *testing.T) {
	for _, c := range []struct{ p, home, want string }{
		{"/home/u/repos/x", "/home/u", "~/repos/x"},
		{"/home/u/a/b/c/d", "/home/u", "~/…/b/c/d"},
		{"/home/u", "/home/u", "~"},
		{"/workspace", "/home/u", "/workspace"},
		{"/srv/data/repos/x", "/home/u", "…/repos/x"},
		{"/home/other/x", "/home/u", "…/other/x"},
		{"/opt/x", "/home/u", "/opt/x"},
		{"/home/other/y/x", "/home/u", "…/y/x"},
		{"relative/path", "/home/u", ""},
		{"", "/home/u", ""},
	} {
		if got := displayPath(c.p, c.home); got != c.want {
			t.Errorf("displayPath(%q, %q) = %q, want %q", c.p, c.home, got, c.want)
		}
	}
}

func TestCapEmitterDedupes(t *testing.T) {
	em := &collectEmitter{}
	var c capEmitter
	p := protocol.CapabilitiesPayload{Engine: "claude", Groups: []protocol.CapabilityGroup{{ID: "tools", Label: "Tools", Items: []protocol.CapabilityItem{{Name: "Bash"}}}}}
	if !c.emit(em, p) {
		t.Fatal("first report must emit")
	}
	if c.emit(em, p) {
		t.Error("an unchanged report must not emit again")
	}
	p.Groups[0].Items = append(p.Groups[0].Items, protocol.CapabilityItem{Name: "Read"})
	if !c.emit(em, p) {
		t.Error("a changed report must emit")
	}
	if n := countKind(em, protocol.CapabilitiesKind); n != 2 {
		t.Errorf("capabilities events = %d, want 2", n)
	}
}

// fakeClaudeInit is a `claude` stub whose init line is the file named by
// $FAKE_INIT until its third call, then $FAKE_INIT2.
func fakeClaudeInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
n=$(( $(cat "$FAKE_COUNT" 2>/dev/null || echo 0) + 1 ))
echo $n > "$FAKE_COUNT"
if [ "$n" -ge 3 ]; then cat "$FAKE_INIT2"; else cat "$FAKE_INIT"; fi
echo
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every -p turn re-inits; only a changed init reaches the transcript.
func TestClaudeCodeDriverEmitsCapabilitiesOnChangeOnly(t *testing.T) {
	bin := fakeClaudeInit(t)
	tmp := t.TempDir()
	init1 := filepath.Join(tmp, "init1.json")
	init2 := filepath.Join(tmp, "init2.json")
	fixture := loadInitFixture(t)
	if err := os.WriteFile(init1, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(fixture), `"status": "failed"`, `"status": "connected"`, 1)
	if changed == string(fixture) {
		t.Fatal("fixture edit did not apply")
	}
	if err := os.WriteFile(init2, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_INIT", init1)
	t.Setenv("FAKE_INIT2", init2)
	t.Setenv("FAKE_COUNT", filepath.Join(tmp, "count"))

	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	for i := 0; i < 3; i++ {
		d.Enqueue("turn", "chat")
	}
	deadline := time.After(10 * time.Second)
	for countKind(em, "turn_done") < 3 {
		select {
		case <-deadline:
			t.Fatalf("turns never finished: %v", em.kinds())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if n := countKind(em, protocol.CapabilitiesKind); n != 2 {
		t.Fatalf("capabilities events = %d, want 2 (turn 1, then turn 3's change): %v", n, em.kinds())
	}
	em.mu.Lock()
	var last protocol.CapabilitiesPayload
	for _, ev := range em.events {
		if ev.Kind == protocol.CapabilitiesKind {
			last = ev.Payload.(protocol.CapabilitiesPayload)
		}
	}
	em.mu.Unlock()
	if it := capItem(capGroup(last, "mcp"), "broken-server"); it == nil || it.Status != "connected" {
		t.Errorf("latest report must carry the change: %+v", it)
	}
}

func writeSkill(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHermesCapabilitiesScansCategories(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".hermes", "skills")
	writeSkill(t, filepath.Join(root, "productivity", "airtable"), "---\nname: airtable\ndescription: Airtable via curl.\nmetadata:\n  name: nested-ignored\n---\nbody\n")
	writeSkill(t, filepath.Join(root, "top"), "---\nname: \"top\"\ndescription: >-\n  Folded\n  description.\n---\n")
	writeSkill(t, filepath.Join(root, "broken"), "no frontmatter")
	// A skill's own subfolders are resources, not more skills.
	writeSkill(t, filepath.Join(root, "top", "templates", "inner"), "---\nname: inner\n---\n")
	// A FIFO where SKILL.md should be must not block the scan.
	fifoDir := filepath.Join(root, "fifo")
	_ = os.MkdirAll(fifoDir, 0o755)
	_ = mkfifo(filepath.Join(fifoDir, "SKILL.md"))
	// Symlinked directories are not followed.
	outside := t.TempDir()
	writeSkill(t, filepath.Join(outside, "linked"), "---\nname: linked\n---\n")
	_ = os.Symlink(filepath.Join(outside, "linked"), filepath.Join(root, "linked"))

	done := make(chan []protocol.CapabilityGroup, 1)
	go func() { done <- hermesCapabilities(CapabilityContext{HomeDir: home}) }()
	var groups []protocol.CapabilityGroup
	select {
	case groups = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan blocked")
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	var names []string
	for _, it := range groups[0].Items {
		names = append(names, it.Name+"@"+it.Source)
	}
	if got := strings.Join(names, ","); got != "airtable@productivity,top@user" {
		t.Errorf("skills = %s", got)
	}
	if d := groups[0].Items[1].Description; d != "Folded description." {
		t.Errorf("folded description = %q", d)
	}

	// A sandboxed session does not see ~/.hermes/skills.
	if g := hermesCapabilities(CapabilityContext{HomeDir: home, Sandboxed: true}); g != nil {
		t.Errorf("sandboxed = %+v, want nil", g)
	}
}

func TestCodexCapabilitiesSystemIsBuiltin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	root := filepath.Join(home, ".codex", "skills")
	writeSkill(t, filepath.Join(root, ".system", "imagegen"), "---\nname: \"imagegen\"\ndescription: \"Generate images.\"\n---\n")
	writeSkill(t, filepath.Join(root, "mine"), "---\nname: mine\ndescription: Mine.\n---\n")
	groups := codexCapabilities(CapabilityContext{HomeDir: home})
	if len(groups) != 1 || len(groups[0].Items) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if it := capItem(&groups[0], "imagegen"); it == nil || it.Source != "builtin" || it.Description != "Generate images." {
		t.Errorf("imagegen = %+v", it)
	}
	if it := capItem(&groups[0], "mine"); it == nil || it.Source != "user" {
		t.Errorf("mine = %+v", it)
	}
	// No directory: an empty (reported) group, not an error.
	if g := codexCapabilities(CapabilityContext{HomeDir: t.TempDir()}); len(g) != 1 || len(g[0].Items) != 0 {
		t.Errorf("empty = %+v", g)
	}
}

func TestEngineCapabilitiesOnlyWhereDeclared(t *testing.T) {
	if _, ok := engineCapabilities(engineRegistry["openclaw"], "", CapabilityContext{HomeDir: t.TempDir()}); ok {
		t.Error("OpenClaw declares no source and must report nothing")
	}
	home := t.TempDir()
	writeSkill(t, filepath.Join(home, ".hermes", "skills", "x"), "---\nname: x\n---\n")
	p, ok := engineCapabilities(engineRegistry["hermes"], "m", CapabilityContext{HomeDir: home})
	if !ok || p.Engine != "hermes" || p.EngineName != "Hermes" || p.Note == "" {
		t.Errorf("hermes = %+v ok=%v", p, ok)
	}
}

func TestNativeCapabilities(t *testing.T) {
	work := "/w"
	p := nativeCapabilities("claude-sonnet-5",
		[]skills.Skill{{Name: "s", Description: "d", Dir: "/w/.claude/skills/s"}},
		[]agent.AgentTypeDef{{Name: "explore", Description: "Explores."}},
		[]agent.ToolDef{{Name: "read_file", Description: "Reads."}}, work, "/h")
	p = protocol.SanitizeCapabilities(p)
	if p.Engine != "native" || len(p.Groups) != 3 {
		t.Fatalf("payload = %+v", p)
	}
	if it := capItem(capGroup(p, "skills"), "s"); it == nil || it.Source != "project" {
		t.Errorf("skill = %+v", it)
	}
	if capItem(capGroup(p, "tools"), "read_file") == nil || capItem(capGroup(p, "agents"), "explore") == nil {
		t.Errorf("groups = %+v", p.Groups)
	}
}

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
