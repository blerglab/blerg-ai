// Package pluginspec is the shared definition of an "always-on plugin" entry: the one place the
// shape rules live, so blerg-core (which stores the list and validates writes) and blerg-runner
// (which re-validates at install time inside a session pod) can never disagree about what a
// well-formed, allowed entry is.
//
// Everything here fails closed: a plugin name or marketplace source that does not match the
// strict patterns is rejected, and a marketplace is only usable when the operator's allow-list
// names it. Plugins run code inside sessions, so the accepted grammar is deliberately tiny.
package pluginspec

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

// Supported native plugin engines.
const (
	EngineClaude = "claude"
	EngineCodex  = "codex"
)

// MaxEntries is the most plugins one account may register per engine.
const MaxEntries = 20

// DefaultMarketplaces is the allow-list used when the operator sets none.
const DefaultMarketplaces = "anthropics/claude-plugins-official,codex:openai-curated,codex:openai-curated-remote"

// Entry is one always-on plugin: a marketplace and a plugin in it. Marketplace is the source
// handed to the engine's "marketplace add" (GitHub owner/repo), or
// codex:<native marketplace> for a Codex-owned catalog.
type Entry struct {
	Marketplace string `json:"marketplace"`
	Plugin      string `json:"plugin"`
}

var (
	pluginNameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	codexSourceRE  = regexp.MustCompile(`^codex:[a-z0-9][a-z0-9._-]{0,99}$`)
	githubSourceRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
)

// Validation errors. Their text is safe to show to a browser.
var (
	ErrInvalidName        = errors.New("plugin name must be lowercase letters, digits, '.', '_' or '-' (1-64 characters, starting with a letter or digit)")
	ErrInvalidMarketplace = errors.New("marketplace must be a GitHub owner/repo")
)

// ValidPluginName reports whether s is an acceptable plugin name.
func ValidPluginName(s string) bool { return pluginNameRE.MatchString(s) }

// ValidMarketplaceSource reports whether s is a GitHub owner/repo and nothing else. The regex
// alone would still let "../x" or "-x/y" through, and the CLI reads the first as a local path and
// the second as a flag, so a leading '.' or '-' on the owner and a dot-only repo are refused.
func ValidMarketplaceSource(s string) bool {
	if !githubSourceRE.MatchString(s) {
		return false
	}
	owner, repo, _ := strings.Cut(s, "/")
	if strings.HasPrefix(owner, ".") || strings.HasPrefix(owner, "-") {
		return false
	}
	return repo != "." && repo != ".."
}

// SupportsEngine reports whether a native plugin installer is registered for this engine.
func SupportsEngine(engine string) bool { return engine == EngineClaude || engine == EngineCodex }

// CodexMarketplaceName returns the native selector from a namespaced Codex source.
// It cannot be a URL, path, option or GitHub source.
func CodexMarketplaceName(source string) (string, bool) {
	if !codexSourceRE.MatchString(source) {
		return "", false
	}
	return strings.TrimPrefix(source, "codex:"), true
}

// ValidSource includes native Codex identities for policy and transport validation.
// Engine-specific validation still refuses these identities for Claude.
func ValidSource(source string) bool {
	_, native := CodexMarketplaceName(source)
	return native || ValidMarketplaceSource(source)
}

func Validate(engine string, e Entry) error {
	switch engine {
	case EngineCodex:
		return ValidateCodex(e)
	case EngineClaude:
		return ValidateClaude(e)
	default:
		return errors.New("unsupported plugin engine")
	}
}

func ValidateCodex(e Entry) error {
	if _, native := CodexMarketplaceName(e.Marketplace); !native {
		return ValidateClaude(e)
	}
	if !ValidPluginName(e.Plugin) {
		return ErrInvalidName
	}
	return nil
}

// ValidateClaude checks an entry's shape for the claude engine.
func ValidateClaude(e Entry) error {
	if !ValidMarketplaceSource(e.Marketplace) {
		return ErrInvalidMarketplace
	}
	if !ValidPluginName(e.Plugin) {
		return ErrInvalidName
	}
	return nil
}

// Allowlist is the operator's set of permitted marketplace sources.
type Allowlist struct {
	anySource bool
	srcs      map[string]bool
}

// ParseAllowlist reads a comma-separated list of owner/repo or codex:name sources; "*" allows any valid
// source. An empty string means DefaultMarketplaces. Entries that are not valid sources are
// dropped (fail closed) and returned so the caller can log them.
func ParseAllowlist(raw string) (Allowlist, []string) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultMarketplaces
	}
	a := Allowlist{srcs: map[string]bool{}}
	var dropped []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case p == "*":
			a.anySource = true
		case ValidSource(p):
			a.srcs[strings.ToLower(p)] = true
		default:
			dropped = append(dropped, p)
		}
	}
	return a, dropped
}

// Allows reports whether source is permitted: it must be a valid source AND listed (GitHub
// names are case-insensitive; native names are lowercase) or the list is "*".
func (a Allowlist) Allows(source string) bool {
	if !ValidSource(source) {
		return false
	}
	return a.anySource || a.srcs[strings.ToLower(source)]
}

// List returns the explicitly allowed sources, sorted, and whether any source is allowed.
func (a Allowlist) List() (sources []string, anySource bool) {
	for s := range a.srcs {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	return sources, a.anySource
}
