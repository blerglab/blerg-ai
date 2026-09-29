package daemon

// Capabilities: what a session has loaded — skills, plugins, MCP servers,
// slash commands, tools, subagents — reported as one engine-neutral
// agent_event of kind "capabilities" (protocol.CapabilitiesPayload). Claude
// Code reports it itself in its stream-json `system/init` line; the native
// loop knows its own lists; other engines may declare a best-effort
// EngineSpec.Capabilities source. Everything read here is untrusted and goes
// through protocol.SanitizeCapabilities before it is emitted.

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// CapabilityContext is what an engine's Capabilities source may look at.
type CapabilityContext struct {
	WorkDir string
	HomeDir string
	// Sandboxed: the engine runs in a sandbox container, which sees only the
	// workspace and the mounted config dirs — a host directory that is not
	// mounted there says nothing about the session.
	Sandboxed bool
}

// capEmitter emits a capabilities event only when it differs from the last
// one this session emitted, so an engine that re-reports on every turn
// (Claude Code in -p mode re-inits per turn) doesn't fill the transcript.
type capEmitter struct {
	mu   sync.Mutex
	last [32]byte
	set  bool
}

// emit sanitizes p and emits it unless it is unchanged. Reports whether it
// emitted.
func (c *capEmitter) emit(em agent.Emitter, p protocol.CapabilitiesPayload) bool {
	p = protocol.SanitizeCapabilities(p)
	raw, err := json.Marshal(p)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(raw)
	c.mu.Lock()
	if c.set && sum == c.last {
		c.mu.Unlock()
		return false
	}
	c.last, c.set = sum, true
	c.mu.Unlock()
	em.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: protocol.CapabilitiesKind, Payload: p})
	return true
}

// ── Claude Code's system/init ───────────────────────────────────────────────

// ccInitMaxList bounds how many entries of any init list are even looked at;
// SanitizeCapabilities caps what is kept.
const ccInitMaxList = 2000

// ccInit is the subset of Claude Code's init line we read. Every field is
// decoded leniently (see strList/objList) so one odd value doesn't lose the
// rest. MCP servers are read as name/status/source ONLY: their config —
// command, args, env, headers, URLs — is never decoded, let alone forwarded.
type ccInit struct {
	Cwd            json.RawMessage `json:"cwd"`
	Model          json.RawMessage `json:"model"`
	PermissionMode json.RawMessage `json:"permissionMode"`
	Version        json.RawMessage `json:"claude_code_version"`
	Tools          json.RawMessage `json:"tools"`
	MCPServers     json.RawMessage `json:"mcp_servers"`
	SlashCommands  json.RawMessage `json:"slash_commands"`
	Skills         json.RawMessage `json:"skills"`
	Plugins        json.RawMessage `json:"plugins"`
	Agents         json.RawMessage `json:"agents"`
}

func rawStr(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// strList decodes a JSON array keeping only its string elements; anything
// else (a non-array, numbers, objects) is skipped.
func strList(raw json.RawMessage) []string {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	if len(arr) > ccInitMaxList {
		arr = arr[:ccInitMaxList]
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s := rawStr(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// objList decodes a JSON array of objects, returning for each only the
// string values of the wanted keys. Other keys are never decoded.
func objList(raw json.RawMessage, keys ...string) []map[string]string {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	if len(arr) > ccInitMaxList {
		arr = arr[:ccInitMaxList]
	}
	var out []map[string]string
	for _, e := range arr {
		var obj map[string]json.RawMessage
		if json.Unmarshal(e, &obj) != nil {
			continue
		}
		m := map[string]string{}
		for _, k := range keys {
			if v, ok := obj[k]; ok {
				m[k] = rawStr(v)
			}
		}
		out = append(out, m)
	}
	return out
}

// mcpToolName mirrors how Claude Code names an MCP server's tools
// ("mcp__<server>__<tool>", the server name with anything outside
// [A-Za-z0-9_-] replaced by "_"), so tools can be counted per server.
var mcpNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func mcpToolPrefix(server string) string {
	return "mcp__" + mcpNameUnsafe.ReplaceAllString(server, "_") + "__"
}

// mcpStatuses are the server states Claude Code reports; anything else is
// shown as "unknown" rather than passed through.
var mcpStatuses = map[string]bool{"connected": true, "failed": true, "needs-auth": true, "pending": true, "disabled": true}

// pluginVersion matches a version-shaped path component.
var pluginVersion = regexp.MustCompile(`^v?\d+(\.\d+){0,3}([-+][0-9A-Za-z.]{1,20})?$`)

// displayPath renders a working directory without leaking the host layout:
// "~/…" under home (its last three components at most), otherwise its last
// two components.
func displayPath(p, home string) string {
	p = filepath.Clean(p)
	if p == "." || p == "" || !filepath.IsAbs(p) {
		return ""
	}
	tail := func(rel string, n int) (string, bool) {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) <= n {
			return rel, false
		}
		return strings.Join(parts[len(parts)-n:], string(filepath.Separator)), true
	}
	if home != "" && home != "/" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			t, cut := tail(rel, 3)
			if cut {
				return "~/…/" + t
			}
			return "~/" + t
		}
	}
	t, cut := tail(strings.TrimPrefix(p, "/"), 2)
	if cut {
		return "…/" + t
	}
	return "/" + t
}

// skillSource says where a discovered skill lives, for display.
func skillSource(s skills.Skill, workDir, home string) string {
	if strings.Contains(s.Name, ":") {
		return "plugin"
	}
	under := func(root string) bool {
		if root == "" {
			return false
		}
		rel, err := filepath.Rel(root, s.Dir)
		return err == nil && !strings.HasPrefix(rel, "..")
	}
	switch {
	case under(filepath.Join(workDir, ".claude")):
		return "project"
	case under(filepath.Join(home, ".claude")):
		return "user"
	}
	return ""
}

// claudeInitCapabilities turns one Claude Code init line into a payload.
// known are the skills the daemon discovered itself for this session
// (project, user and plugin SKILL.md files): the init lists skill names
// only, so a known one lends its description and scope. ok=false when the
// line has none of the fields this reads.
func claudeInitCapabilities(line []byte, known []skills.Skill, workDir, home string) (protocol.CapabilitiesPayload, bool) {
	var in ccInit
	if json.Unmarshal(line, &in) != nil {
		return protocol.CapabilitiesPayload{}, false
	}
	if in.Tools == nil && in.MCPServers == nil && in.Skills == nil && in.Plugins == nil &&
		in.SlashCommands == nil && in.Agents == nil {
		return protocol.CapabilitiesPayload{}, false
	}
	p := protocol.CapabilitiesPayload{
		Engine:         "claude",
		EngineName:     "Claude Code",
		Model:          rawStr(in.Model),
		Version:        rawStr(in.Version),
		Cwd:            displayPath(rawStr(in.Cwd), home),
		PermissionMode: rawStr(in.PermissionMode),
		Note:           "What Claude Code loaded when this session's latest turn started.",
	}
	tools := strList(in.Tools)

	if in.Skills != nil {
		byName := map[string]skills.Skill{}
		for _, s := range known {
			byName[s.Name] = s
		}
		g := protocol.CapabilityGroup{ID: protocol.CapGroupSkills, Label: "Skills", Items: []protocol.CapabilityItem{}}
		for _, name := range strList(in.Skills) {
			it := protocol.CapabilityItem{Name: name}
			if s, ok := byName[name]; ok {
				it.Description = s.Description
				it.Source = skillSource(s, workDir, home)
			} else if strings.Contains(name, ":") {
				it.Source = "plugin"
			}
			g.Items = append(g.Items, it)
		}
		p.Groups = append(p.Groups, g)
	}
	if in.Plugins != nil {
		g := protocol.CapabilityGroup{ID: protocol.CapGroupPlugins, Label: "Plugins", Items: []protocol.CapabilityItem{}}
		for _, pl := range objList(in.Plugins, "name", "source", "path") {
			it := protocol.CapabilityItem{Name: pl["name"], Status: "enabled"}
			// "name@marketplace": the marketplace is the source. The path is
			// used only for a version-shaped last component; never forwarded.
			if _, market, ok := strings.Cut(pl["source"], "@"); ok {
				it.Source = market
			}
			if path := pl["path"]; path == "builtin" {
				it.Source = "builtin"
			} else if v := filepath.Base(path); path != "" && pluginVersion.MatchString(v) {
				it.Detail = "v" + strings.TrimPrefix(v, "v")
			}
			g.Items = append(g.Items, it)
		}
		p.Groups = append(p.Groups, g)
	}
	if in.MCPServers != nil {
		g := protocol.CapabilityGroup{ID: protocol.CapGroupMCP, Label: "MCP servers", Items: []protocol.CapabilityItem{}}
		for _, s := range objList(in.MCPServers, "name", "status", "source") {
			status := s["status"]
			if !mcpStatuses[status] {
				status = "unknown"
			}
			it := protocol.CapabilityItem{Name: s["name"], Status: status, Source: s["source"]}
			if s["name"] != "" {
				prefix, n := mcpToolPrefix(s["name"]), 0
				for _, t := range tools {
					if strings.HasPrefix(t, prefix) {
						n++
					}
				}
				if n == 1 {
					it.Detail = "1 tool"
				} else if n > 1 || status == "connected" {
					it.Detail = strconv.Itoa(n) + " tools"
				}
			}
			g.Items = append(g.Items, it)
		}
		p.Groups = append(p.Groups, g)
	}
	if in.SlashCommands != nil {
		// Claude Code lists every skill as a slash command too; the commands
		// group holds only the rest, so nothing shows twice.
		skillSet := map[string]bool{}
		for _, s := range strList(in.Skills) {
			skillSet[s] = true
		}
		g := protocol.CapabilityGroup{ID: protocol.CapGroupCommands, Label: "Slash commands", Items: []protocol.CapabilityItem{}}
		for _, c := range strList(in.SlashCommands) {
			if !skillSet[c] {
				g.Items = append(g.Items, protocol.CapabilityItem{Name: "/" + strings.TrimPrefix(c, "/")})
			}
		}
		if len(skillSet) > 0 {
			g.Note = "Skills are listed under Skills; each is also a slash command."
		}
		p.Groups = append(p.Groups, g)
	}
	if in.Tools != nil {
		g := protocol.CapabilityGroup{ID: protocol.CapGroupTools, Label: "Tools", Items: []protocol.CapabilityItem{}}
		for _, t := range tools {
			if !strings.HasPrefix(t, "mcp__") {
				g.Items = append(g.Items, protocol.CapabilityItem{Name: t})
			}
		}
		if len(g.Items) < len(tools) {
			g.Note = "Built-in tools; MCP tools are counted under their server."
		}
		p.Groups = append(p.Groups, g)
	}
	if in.Agents != nil {
		g := protocol.CapabilityGroup{ID: protocol.CapGroupAgents, Label: "Subagents", Items: []protocol.CapabilityItem{}}
		for _, a := range strList(in.Agents) {
			g.Items = append(g.Items, protocol.CapabilityItem{Name: a})
		}
		p.Groups = append(p.Groups, g)
	}
	return p, true
}

// ── Native loop ─────────────────────────────────────────────────────────────

// nativeCapabilities is what the in-process loop has: the skills it
// discovered, its subagent types and its tool registry.
func nativeCapabilities(model string, skillList []skills.Skill, agentTypes []agent.AgentTypeDef, tools []agent.ToolDef, workDir, home string) protocol.CapabilitiesPayload {
	p := protocol.CapabilitiesPayload{
		Engine: "native", EngineName: "Blerg native loop", Model: model,
		Note: "What the session loaded when it started.",
	}
	sk := protocol.CapabilityGroup{ID: protocol.CapGroupSkills, Label: "Skills", Items: []protocol.CapabilityItem{}}
	for _, s := range skillList {
		sk.Items = append(sk.Items, protocol.CapabilityItem{Name: s.Name, Description: s.Description, Source: skillSource(s, workDir, home)})
	}
	tg := protocol.CapabilityGroup{ID: protocol.CapGroupTools, Label: "Tools", Items: []protocol.CapabilityItem{}}
	for _, t := range tools {
		tg.Items = append(tg.Items, protocol.CapabilityItem{Name: t.Name, Description: t.Description})
	}
	ag := protocol.CapabilityGroup{ID: protocol.CapGroupAgents, Label: "Subagents", Items: []protocol.CapabilityItem{}}
	for _, a := range agentTypes {
		ag.Items = append(ag.Items, protocol.CapabilityItem{Name: a.Name, Description: a.Description})
	}
	p.Groups = []protocol.CapabilityGroup{sk, tg, ag}
	return p
}

// ── Engine skill directories (best effort) ──────────────────────────────────

// Bounds for scanning an engine's skills directory.
const (
	skillScanMaxDepth = 3   // <root>/<category>/<name>/SKILL.md
	skillScanMaxDirs  = 400 // directories visited
	skillScanMaxFound = 300 // skills returned
)

// scanSkillDir lists SKILL.md skills under root, read-only and bounded: at
// most skillScanMaxDepth levels, skillScanMaxDirs directories, and
// skillScanMaxFound skills; symlinked directories are not followed; each
// SKILL.md must be a regular file, and only its frontmatter is read
// (skills.ReadFrontmatter). source(rel) names the source of the skill found
// at root-relative dir rel. Results are sorted by name.
func scanSkillDir(root string, source func(rel string) string) []protocol.CapabilityItem {
	var out []protocol.CapabilityItem
	visited := 0
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if depth > skillScanMaxDepth || visited >= skillScanMaxDirs || len(out) >= skillScanMaxFound {
			return
		}
		visited++
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		if depth > 0 {
			if name, desc, err := skills.ReadFrontmatter(filepath.Join(dir, "SKILL.md")); err == nil {
				out = append(out, protocol.CapabilityItem{Name: name, Description: desc, Source: source(rel)})
				return // a skill's own subdirectories are its resources
			}
		}
		for _, e := range entries {
			// DirEntry.IsDir is false for a symlink, so links aren't followed.
			if !e.IsDir() {
				continue
			}
			walk(filepath.Join(dir, e.Name()), filepath.Join(rel, e.Name()), depth+1)
		}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	walk(root, "", 0)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// engineHome is an engine's config dir: $<envVar> when set to an absolute
// path, else ~/<dotDir>.
func engineHome(home, envVar, dotDir string) string {
	if v := os.Getenv(envVar); v != "" && filepath.IsAbs(v) {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, dotDir)
}

// codexCapabilities lists Codex's skills directory ($CODEX_HOME/skills;
// ".system" holds the ones Codex ships). The sandbox mounts ~/.codex, so a
// sandboxed session sees the same directory.
func codexCapabilities(c CapabilityContext) []protocol.CapabilityGroup {
	dir := engineHome(c.HomeDir, "CODEX_HOME", ".codex")
	if dir == "" {
		return nil
	}
	items := scanSkillDir(filepath.Join(dir, "skills"), func(rel string) string {
		if strings.HasPrefix(rel, ".system") {
			return "builtin"
		}
		return "user"
	})
	return []protocol.CapabilityGroup{{
		ID: protocol.CapGroupSkills, Label: "Skills", Items: nonNilItems(items),
		Note: "Installed in Codex's skills directory on this machine; Codex decides which it uses.",
	}}
}

// hermesCapabilities lists Hermes's skills directory (~/.hermes/skills,
// grouped in category folders). The sandbox does not mount it, so a
// sandboxed session reports nothing rather than the host's list.
func hermesCapabilities(c CapabilityContext) []protocol.CapabilityGroup {
	if c.Sandboxed {
		return nil
	}
	if c.HomeDir == "" {
		return nil
	}
	dir := filepath.Join(c.HomeDir, ".hermes")
	items := scanSkillDir(filepath.Join(dir, "skills"), func(rel string) string {
		if cat, _, ok := strings.Cut(rel, string(filepath.Separator)); ok {
			return cat
		}
		return "user"
	})
	return []protocol.CapabilityGroup{{
		ID: protocol.CapGroupSkills, Label: "Skills", Items: nonNilItems(items),
		Note: "Installed in Hermes's skills directory on this machine; Hermes decides which it uses.",
	}}
}

func nonNilItems(items []protocol.CapabilityItem) []protocol.CapabilityItem {
	if items == nil {
		return []protocol.CapabilityItem{}
	}
	return items
}

// engineCapabilities builds an engine's payload from its Capabilities
// source; ok=false when it has none or it found nothing to report.
func engineCapabilities(spec EngineSpec, model string, c CapabilityContext) (protocol.CapabilitiesPayload, bool) {
	if spec.Capabilities == nil {
		return protocol.CapabilitiesPayload{}, false
	}
	groups := spec.Capabilities(c)
	if len(groups) == 0 {
		return protocol.CapabilitiesPayload{}, false
	}
	return protocol.CapabilitiesPayload{
		Engine: spec.ID, EngineName: spec.DisplayName, Model: model, Groups: groups,
		Note: "Read by the runner from " + spec.DisplayName + "'s own directories when the session started; " + spec.DisplayName + " does not report what it loaded.",
	}, true
}
