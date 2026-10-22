package protocol

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

// The plugin browser (card #144): the Capabilities tab's "+ Add" lists every plugin of the
// marketplaces the session's engine knows, read by the session's daemon from the engine's own
// files. Nothing here installs: an Install goes through the existing owner-approved path
// (POST /api/sessions/{id}/plugins → ApplySessionPlugins).

// ReadPluginCatalog asks the session's daemon for the plugins of the marketplaces its engine knows.
// AddMarketplace, when set, first adds that marketplace source (a GitHub owner/repo that passed
// ValidMarketplaceSource and the operator allow-list) to the daemon's own plugin workshop with a
// fixed `claude plugin marketplace add <source>`, then reads the catalog.
type ReadPluginCatalog struct {
	Type           string `json:"type"`
	RequestID      string `json:"request_id"`
	SessionID      string `json:"session_id"`
	AddMarketplace string `json:"add_marketplace,omitempty"`
}

// CatalogMarketplace is one marketplace the engine knows. Source is its GitHub owner/repo when it
// has one (the only kind Blerg installs from), "" otherwise.
type CatalogMarketplace struct {
	Name        string `json:"name"`
	Source      string `json:"source,omitempty"`
	Description string `json:"description,omitempty"`
}

// CatalogPlugin is one plugin in a marketplace's manifest. Installed: the person has it installed
// for this session's project (user scope, or project/local scope for this workspace), or the
// session loaded it through Blerg. Enabled: it is switched on for this session.
type CatalogPlugin struct {
	Name        string `json:"name"`
	Marketplace string `json:"marketplace"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Author      string `json:"author,omitempty"`
	Category    string `json:"category,omitempty"`
	Installed   bool   `json:"installed,omitempty"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// PluginCatalogResult answers ReadPluginCatalog. ErrorCode: "unsupported" (not a host Claude Code
// session, or no catalog on this daemon), "not_allowed" (the marketplace to add is not allowed
// here), "add_failed" (the marketplace could not be added), "busy", "unavailable".
type PluginCatalogResult struct {
	Type         string               `json:"type"`
	RequestID    string               `json:"request_id"`
	SessionID    string               `json:"session_id"`
	OK           bool                 `json:"ok"`
	ErrorCode    string               `json:"error_code,omitempty"`
	Marketplaces []CatalogMarketplace `json:"marketplaces,omitempty"`
	Plugins      []CatalogPlugin      `json:"plugins,omitempty"`
	// Truncated: there were more plugins (or marketplaces) than the bounds allow.
	Truncated bool `json:"truncated,omitempty"`
}

// The bounds of a catalog, enforced by the daemon when it reads the engine's files and again by the
// server on the reply.
const (
	CatalogMaxMarketplaces   = 32
	CatalogMaxPlugins        = 2000
	CatalogMaxDescription    = 400 // runes
	CatalogMaxShort          = 80  // runes: version, author, category
	CatalogMaxMarketplaceLen = 100 // bytes: a marketplace name (ValidCatalogMarketplaceName)
	// CatalogResultMaxFrame is the largest plugin_catalog_result frame the server reads.
	CatalogResultMaxFrame = 4 << 20
	// CatalogEncodedBudget is where the daemon stops adding plugins (JSON-encoded, escapes
	// included), well under CatalogResultMaxFrame; the rest is reported as truncated.
	CatalogEncodedBudget = 3 << 20
)

var catalogMarketplaceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidCatalogMarketplaceName is the marketplace name grammar (the "@name" in plugin@name).
func ValidCatalogMarketplaceName(s string) bool { return catalogMarketplaceNameRE.MatchString(s) }

// CatalogText reduces s to plain single-line text of at most limit runes: invalid UTF-8 and control
// characters become spaces, runs of space collapse, and an over-long text ends with "…". It is
// rendered as text, never markup.
func CatalogText(s string, limit int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, " ")
	}
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue // format characters (bidi overrides, zero-width) are dropped
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\u2028' || r == '\u2029' {
			space = b.Len() > 0
			continue
		}
		if n >= limit {
			b.WriteString("…")
			return b.String()
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
			if n >= limit {
				b.WriteString("…")
				return b.String()
			}
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// SanitizePluginCatalog keeps only well-formed entries within the bounds: marketplace names that
// pass the grammar, sources that are GitHub owner/repo, plugin names that pass
// pluginspec.ValidPluginName and belong to a listed marketplace, and bounded plain text. Duplicates
// (marketplace + plugin) are dropped. It returns the clean lists and whether anything was cut.
func SanitizePluginCatalog(markets []CatalogMarketplace, plugins []CatalogPlugin) ([]CatalogMarketplace, []CatalogPlugin, bool) {
	cut := false
	outM := make([]CatalogMarketplace, 0, min(len(markets), CatalogMaxMarketplaces))
	known := map[string]bool{}
	for _, m := range markets {
		if !ValidCatalogMarketplaceName(m.Name) || known[m.Name] {
			cut = true
			continue
		}
		if len(outM) >= CatalogMaxMarketplaces {
			cut = true
			continue
		}
		src := m.Source
		if src != "" && !pluginspec.ValidMarketplaceSource(src) {
			src = ""
		}
		known[m.Name] = true
		outM = append(outM, CatalogMarketplace{Name: m.Name, Source: src, Description: CatalogText(m.Description, CatalogMaxDescription)})
	}
	outP := make([]CatalogPlugin, 0, min(len(plugins), CatalogMaxPlugins))
	seen := map[string]bool{}
	for _, p := range plugins {
		key := p.Marketplace + "\x00" + p.Name
		if !pluginspec.ValidPluginName(p.Name) || !known[p.Marketplace] || seen[key] {
			cut = true
			continue
		}
		if len(outP) >= CatalogMaxPlugins {
			cut = true
			continue
		}
		seen[key] = true
		outP = append(outP, CatalogPlugin{
			Name: p.Name, Marketplace: p.Marketplace,
			Description: CatalogText(p.Description, CatalogMaxDescription),
			Version:     CatalogText(p.Version, CatalogMaxShort),
			Author:      CatalogText(p.Author, CatalogMaxShort),
			Category:    CatalogText(p.Category, CatalogMaxShort),
			Installed:   p.Installed, Enabled: p.Enabled,
		})
	}
	return outM, outP, cut
}

// MarketplaceSourceFromInput turns what a person typed as a marketplace into the GitHub owner/repo
// Blerg installs from: an owner/repo as is, or an https URL of a GitHub repository
// (https://github.com/owner/repo, optionally ending in .git or /). Anything else — another
// scheme, another host, credentials, a query, a path below the repository — is refused ("" false).
func MarketplaceSourceFromInput(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 300 {
		return "", false
	}
	if pluginspec.ValidMarketplaceSource(s) {
		return s, true
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	if !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
		return "", false
	}
	p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	p = strings.TrimSuffix(p, ".git")
	if !pluginspec.ValidMarketplaceSource(p) {
		return "", false
	}
	return p, true
}

// MCPSignIn asks the session's daemon to start the engine's own OAuth sign-in for one of the
// session's MCP servers. The daemon asks the engine (Claude Code's stream-json control request
// mcp_authenticate) and returns the authorization URL it made; the engine listens for the callback
// on the session's machine and keeps the token itself. Nothing secret passes through Blerg.
type MCPSignIn struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	Server    string `json:"server"`
}

// MCPSignInResult answers MCPSignIn. AuthURL is an https URL the engine produced (empty when the
// server needs no action). ErrorCode: "unsupported" (not a host Claude Code session),
// "not_found" (no such server needing sign-in), "connector" (a claude.ai connector: connected on
// claude.ai instead), "untrusted" (defined by the workspace, a plugin or another source the
// session could write: signed in to from a terminal), "refused" (the engine could not start a
// sign-in), "busy", "unavailable".
type MCPSignInResult struct {
	Type               string `json:"type"`
	RequestID          string `json:"request_id"`
	SessionID          string `json:"session_id"`
	OK                 bool   `json:"ok"`
	ErrorCode          string `json:"error_code,omitempty"`
	AuthURL            string `json:"auth_url,omitempty"`
	RequiresUserAction bool   `json:"requires_user_action,omitempty"`
	CallbackExpected   bool   `json:"callback_expected,omitempty"`
	// Source is where the server was defined ("user": the person's own settings). Sign-in is only
	// started for such a server; any other is "untrusted" and signed in to from a terminal.
	Source string `json:"source,omitempty"`
}

// MCPSignInMaxURL bounds an authorization URL.
const MCPSignInMaxURL = 4096

// ValidMCPServerName accepts a server name as the live MCP state carries it: valid UTF-8, no
// control characters, 1-200 bytes.
func ValidMCPServerName(s string) bool {
	if s == "" || len(s) > 200 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// ValidAuthURL accepts only an absolute https URL with a host and no credentials, within
// MCPSignInMaxURL bytes and without control characters or spaces.
func ValidAuthURL(s string) bool {
	if s == "" || len(s) > MCPSignInMaxURL || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return u.Hostname() != ""
}

// BudgetPluginCatalog keeps plugins, in order, while their JSON encoding (as the frame will carry
// it, HTML escapes included) stays within budget bytes; cut reports that some were left out.
func BudgetPluginCatalog(plugins []CatalogPlugin, budget int) (kept []CatalogPlugin, cut bool) {
	used := 0
	for i, p := range plugins {
		raw, err := json.Marshal(p)
		if err != nil {
			return plugins[:i], true
		}
		if used+len(raw)+1 > budget {
			return plugins[:i], true
		}
		used += len(raw) + 1
	}
	return plugins, false
}

// ReservedEngineHomeEnv are the variables that say where an engine's own configuration lives (the
// person's "user scope"). A session's custom variables may not set them: pointing them at a
// directory the session can write would turn a session-written MCP server into a "user" one that
// Blerg offers a one-click sign-in for.
var ReservedEngineHomeEnv = []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"}

// IsReservedEngineHomeEnv reports whether k is one of ReservedEngineHomeEnv.
func IsReservedEngineHomeEnv(k string) bool {
	for _, r := range ReservedEngineHomeEnv {
		if k == r {
			return true
		}
	}
	return false
}
