package protocol

import (
	"regexp"
	"strings"
	"unicode"
)

// CapabilitiesKind is the agent_event kind that reports what a session has
// loaded: its skills, plugins, MCP servers, slash commands, tools and
// subagents. It is engine-neutral data — a viewer renders whatever groups it
// is given, so an engine plugs in by emitting the same event — and it is
// persisted and replayed like every other agent event. Only the latest one
// describes the session; an engine re-emits it when what it loaded changes.
const CapabilitiesKind = "capabilities"

// Capability group ids. The set is closed: a group with any other id is
// dropped by SanitizeCapabilities, so a viewer can rely on it.
const (
	CapGroupSkills   = "skills"
	CapGroupPlugins  = "plugins"
	CapGroupMCP      = "mcp"
	CapGroupCommands = "commands"
	CapGroupTools    = "tools"
	CapGroupAgents   = "agents"
)

// capGroupOrder is the display order and the allowlist of group ids.
var capGroupOrder = []string{CapGroupSkills, CapGroupPlugins, CapGroupMCP, CapGroupCommands, CapGroupTools, CapGroupAgents}

// Caps on what a capabilities payload may carry. Every string is also
// stripped of control and format characters.
const (
	CapMaxItems       = 500  // per group
	CapMaxName        = 200  // runes
	CapMaxDescription = 1024 // runes
	CapMaxDetail      = 200  // runes
	CapMaxLabel       = 80   // runes: group labels, engine name
	CapMaxNote        = 400  // runes
	CapMaxToken       = 64   // runes: engine, model, version, source, status
)

// CapabilityItem is one entry of a group. Name is required; the rest is
// optional display text. Status is a short token (connected, failed,
// needs-auth, enabled, ...); Source says where the item comes from (user,
// project, plugin, builtin, or an engine's own word for it).
type CapabilityItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Status      string `json:"status,omitempty"`
	Source      string `json:"source,omitempty"`
}

// CapabilityGroup is one kind of capability. Total is the item count before
// the per-group cap, so a viewer can say "showing 500 of 812".
type CapabilityGroup struct {
	ID    string           `json:"id"`
	Label string           `json:"label"`
	Note  string           `json:"note,omitempty"`
	Total int              `json:"total,omitempty"`
	Items []CapabilityItem `json:"items"`
}

// CapabilitiesPayload is the payload of a capabilities event. Groups the
// engine does not report are absent (a viewer says "not reported"), which is
// different from a group that is present with no items (reported: none).
type CapabilitiesPayload struct {
	Engine     string `json:"engine"`
	EngineName string `json:"engine_name,omitempty"`
	Model      string `json:"model,omitempty"`
	Version    string `json:"version,omitempty"`
	// Cwd is a short display form of the engine's working directory
	// ("~/…" or its last components) — never a full host path.
	Cwd            string            `json:"cwd,omitempty"`
	PermissionMode string            `json:"permission_mode,omitempty"`
	Note           string            `json:"note,omitempty"`
	Groups         []CapabilityGroup `json:"groups"`
}

// credURL matches the userinfo of a URL ("scheme://user:pass@").
var credURL = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)

// capClean makes s safe display text: whitespace runs become one space,
// control and format characters (bidi overrides, zero-width) are removed,
// URL credentials are masked, and the result is capped at limit runes.
func capClean(s string, limit int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == unicode.ReplacementChar:
			continue
		}
		space = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if strings.Contains(out, "://") {
		out = credURL.ReplaceAllString(out, "${1}…@")
	}
	if r := []rune(out); len(r) > limit {
		out = strings.TrimSpace(string(r[:limit-1])) + "…"
	}
	return out
}

// capToken keeps s only if it is a short word-like token (letters, digits,
// and . _ - : @ + /), else "".
func capToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > CapMaxToken {
		return ""
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-:@+/", r) {
			return ""
		}
	}
	return s
}

// SanitizeCapabilities returns a copy of p that is safe to persist and show:
// unknown groups are dropped and the rest put in the fixed order (a duplicate
// id keeps its first occurrence), items without a name are dropped, counts
// and lengths are capped, and every string is cleaned. The daemon calls it
// before emitting and the server again before persisting — neither trusts
// the other end.
func SanitizeCapabilities(p CapabilitiesPayload) CapabilitiesPayload {
	out := CapabilitiesPayload{
		Engine:         capToken(p.Engine),
		EngineName:     capClean(p.EngineName, CapMaxLabel),
		Model:          capToken(p.Model),
		Version:        capToken(p.Version),
		Cwd:            capClean(p.Cwd, CapMaxDetail),
		PermissionMode: capToken(p.PermissionMode),
		Note:           capClean(p.Note, CapMaxNote),
		Groups:         []CapabilityGroup{},
	}
	byID := map[string]CapabilityGroup{}
	for _, g := range p.Groups {
		if _, dup := byID[g.ID]; !dup {
			byID[g.ID] = g
		}
	}
	for _, id := range capGroupOrder {
		g, ok := byID[id]
		if !ok {
			continue
		}
		items := make([]CapabilityItem, 0, min(len(g.Items), CapMaxItems))
		seen := map[string]bool{}
		for _, it := range g.Items {
			name := capClean(it.Name, CapMaxName)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if len(items) == CapMaxItems {
				continue // still counted in total below
			}
			items = append(items, CapabilityItem{
				Name:        name,
				Description: capClean(it.Description, CapMaxDescription),
				Detail:      capClean(it.Detail, CapMaxDetail),
				Status:      capToken(strings.ToLower(it.Status)),
				Source:      capToken(it.Source),
			})
		}
		total := len(seen)
		if g.Total > total {
			total = min(g.Total, 1_000_000)
		}
		label := capClean(g.Label, CapMaxLabel)
		if label == "" {
			label = id
		}
		ng := CapabilityGroup{ID: id, Label: label, Note: capClean(g.Note, CapMaxNote), Items: items}
		if total > len(items) {
			ng.Total = total
		}
		out.Groups = append(out.Groups, ng)
	}
	return out
}

// CapabilityCount is the number of items across every group (after caps).
func CapabilityCount(p CapabilitiesPayload) int {
	n := 0
	for _, g := range p.Groups {
		n += len(g.Items)
	}
	return n
}
