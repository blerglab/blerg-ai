// Package skills discovers and loads Claude-style skills (SKILL.md files)
// from project, home, and enabled-plugin scopes, and resolves slash commands
// against them.
package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string // frontmatter name ("plugin:name" for plugin skills)
	Description string
	Dir         string // absolute dir containing SKILL.md
}

// FrontmatterReadLimit is how much of a SKILL.md ReadFrontmatter reads: the
// frontmatter sits at the top, and discovery never needs the body.
const FrontmatterReadLimit = 64 * 1024

func parseFrontmatter(path string) (name, desc string, err error) {
	return ReadFrontmatter(path)
}

// ReadFrontmatter reads the name and description from a SKILL.md-style
// file's YAML frontmatter. It reads only a regular file (never a FIFO or
// device, which could block), at most FrontmatterReadLimit bytes of it, and
// understands just enough YAML for these two keys: plain or quoted values
// and folded/literal block scalars ("description: >-" plus indented lines).
func ReadFrontmatter(path string) (name, desc string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // path is a SKILL.md under the fixed skills roots; regular-file check above and read capped by FrontmatterReadLimit
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, FrontmatterReadLimit))
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(string(data), "---", 3)
	if len(parts) < 3 {
		return "", "", fmt.Errorf("no frontmatter in %s", path)
	}
	lines := strings.Split(parts[1], "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		if raw != strings.TrimLeft(raw, " \t") {
			continue // nested key: only top-level name/description count
		}
		line := strings.TrimSpace(raw)
		var key string
		var val string
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			key, val = "name", strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "description:"); ok {
			key, val = "description", strings.TrimSpace(v)
		} else {
			continue
		}
		if strings.HasPrefix(val, ">") || strings.HasPrefix(val, "|") {
			var block []string
			for i+1 < len(lines) && (strings.TrimSpace(lines[i+1]) == "" || lines[i+1] != strings.TrimLeft(lines[i+1], " \t")) {
				i++
				if t := strings.TrimSpace(lines[i]); t != "" {
					block = append(block, t)
				}
			}
			val = strings.Join(block, " ")
		} else {
			val = unquoteYAML(val)
		}
		if key == "name" {
			name = val
		} else {
			desc = val
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("no name in %s", path)
	}
	return name, desc, nil
}

// unquoteYAML strips one level of matching single or double quotes.
func unquoteYAML(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		if s[0] == '"' {
			if u, err := strconv.Unquote(s); err == nil {
				return u
			}
		}
		return s[1 : len(s)-1]
	}
	return s
}

func scanDir(root, prefix string) []Skill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		name, desc, err := parseFrontmatter(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, Skill{Name: prefix + name, Description: desc, Dir: dir})
	}
	return out
}

func enabledPlugins(projectDir string) []string {
	data, err := os.ReadFile(filepath.Join(projectDir, ".claude/settings.json")) //nolint:gosec // fixed name .claude/settings.json under the session's own project dir
	if err != nil {
		return nil
	}
	var s struct {
		EnabledPlugins []string `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return s.EnabledPlugins
}

// pluginVersionDir returns the lexically-highest version dir for a plugin,
// searching all marketplaces under the cache root. The cache holds multiple
// versions side by side; exactly one may load.
func pluginVersionDir(homeDir, plugin string) string {
	cache := filepath.Join(homeDir, ".claude/plugins/cache")
	markets, err := os.ReadDir(cache)
	if err != nil {
		return ""
	}
	best := ""
	bestVersion := ""
	for _, m := range markets {
		versions, err := os.ReadDir(filepath.Join(cache, m.Name(), plugin))
		if err != nil {
			continue
		}
		var names []string
		for _, v := range versions {
			if v.IsDir() {
				names = append(names, v.Name())
			}
		}
		sort.Slice(names, func(i, j int) bool { return compareVersions(names[i], names[j]) < 0 })
		if len(names) > 0 && (best == "" || compareVersions(names[len(names)-1], bestVersion) > 0) {
			bestVersion = names[len(names)-1]
			best = filepath.Join(cache, m.Name(), plugin, bestVersion)
		}
	}
	return best
}

// compareVersions compares dotted version strings numerically per component
// ("10.0.0" > "9.9.9"); non-numeric components fall back to string compare.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		ai, aerr := strconv.Atoi(av)
		bi, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
		default:
			if av != bv {
				if av < bv {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}

// Discover scans, in order: project skills, home skills, enabled plugins
// (one version each). First name wins (project > home > plugin).
func Discover(projectDir, homeDir string) ([]Skill, error) {
	var out []Skill
	seen := map[string]bool{}
	add := func(list []Skill) {
		for _, s := range list {
			if !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
	}
	add(scanDir(filepath.Join(projectDir, ".claude/skills"), ""))
	add(scanDir(filepath.Join(homeDir, ".claude/skills"), ""))
	for _, plugin := range enabledPlugins(projectDir) {
		vdir := pluginVersionDir(homeDir, plugin)
		if vdir == "" {
			continue
		}
		add(scanDir(filepath.Join(vdir, "skills"), plugin+":"))
	}
	return out, nil
}

// PromptList renders "- name: description" lines for the system prompt.
func PromptList(list []Skill) string {
	var b strings.Builder
	for _, s := range list {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return b.String()
}

// Load returns the full SKILL.md content and the skill dir.
func Load(list []Skill, name string) (string, string, error) {
	for _, s := range list {
		if s.Name == name {
			data, err := os.ReadFile(filepath.Join(s.Dir, "SKILL.md"))
			if err != nil {
				return "", "", err
			}
			return string(data), s.Dir, nil
		}
	}
	return "", "", fmt.Errorf("unknown skill: %s", name)
}

// ResolveSlash resolves "/name args" against the list. If the SKILL.md
// contains "$ARGUMENTS" the placeholder is substituted; otherwise an
// "**Arguments:** ..." line is appended (most SKILL.md files parse arguments
// from prose and have no placeholder). ok=false when no skill matches.
func ResolveSlash(list []Skill, input string) (string, bool, error) {
	if !strings.HasPrefix(input, "/") {
		return "", false, nil
	}
	fields := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(fields) == 0 {
		return "", false, nil
	}
	cmd := fields[0]
	args := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(input, "/"+cmd), " "))
	for _, s := range list {
		// match bare name or plugin-suffixed name
		if s.Name == cmd || strings.HasSuffix(s.Name, ":"+cmd) {
			content, _, err := Load(list, s.Name)
			if err != nil {
				return "", false, err
			}
			if strings.Contains(content, "$ARGUMENTS") {
				return strings.ReplaceAll(content, "$ARGUMENTS", args), true, nil
			}
			if args != "" {
				content += "\n\n**Arguments:** " + args
			}
			return content, true, nil
		}
	}
	return "", false, nil
}
