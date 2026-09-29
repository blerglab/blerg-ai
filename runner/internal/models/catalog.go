package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultCatalogURL is the public Claude Code model catalog — the list
// Claude Code's own /model picker is built from.
const DefaultCatalogURL = "https://downloads.claude.ai/model-catalog/v1/catalog.json"

// DefaultRefreshInterval is how often a running server re-fetches the catalog.
const DefaultRefreshInterval = 6 * time.Hour

const (
	// maxBodyBytes caps a catalog download; today's is ~135 KB.
	maxBodyBytes = 1 << 20
	// fetchTimeout bounds one fetch end to end.
	fetchTimeout = 5 * time.Second
	// maxModels caps how many models one catalog may contribute.
	maxModels = 64
	// maxNameLen / maxDescriptionLen cap the display strings (runes).
	maxNameLen        = 64
	maxDescriptionLen = 200
	// catalogSurface is the catalog's Claude Code surface.
	catalogSurface = "cc"
)

// rawCatalog is the subset of catalog.json this package reads. Everything in
// it is untrusted: the file comes off the network and is only used to populate
// a picker, so each field is validated in ParseCatalog before use.
type rawCatalog struct {
	Surfaces map[string]struct {
		ModelSelectorConfig []struct {
			ID     string     `json:"id"`
			Models []rawModel `json:"models"`
		} `json:"model_selector_config"`
	} `json:"surfaces"`
}

type rawModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Section     string   `json:"section"`
	OfferedOn   []string `json:"offered_on"`
	Runtime     struct {
		EffortLevels  []string `json:"effort_levels"`
		DefaultEffort string   `json:"default_effort"`
	} `json:"runtime"`
}

// ParseCatalog extracts the Claude Code picker from a catalog.json document.
// Models not offered on first_party (the Anthropic API/subscription this
// runner's engines use) are left out; an absent offered_on is taken as
// "no restriction". Invalid entries are dropped rather than failing the whole
// catalog. The result is main-section models first, then overflow, each in
// catalog order. It is an error for nothing usable to remain.
func ParseCatalog(data []byte) ([]Model, error) {
	var raw rawCatalog
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	surface, ok := raw.Surfaces[catalogSurface]
	if !ok || len(surface.ModelSelectorConfig) == 0 {
		return nil, errors.New("catalog: no cc model selector")
	}
	cfg := surface.ModelSelectorConfig[0]
	for _, c := range surface.ModelSelectorConfig {
		if c.ID == catalogSurface {
			cfg = c
			break
		}
	}

	var main, overflow []Model
	seen := map[string]bool{}
	for _, rm := range cfg.Models {
		if len(main)+len(overflow) >= maxModels {
			break
		}
		if !ValidCatalogID(rm.ID) || seen[rm.ID] {
			continue
		}
		if rm.OfferedOn != nil && !contains(rm.OfferedOn, "first_party") {
			continue
		}
		seen[rm.ID] = true
		m := Model{
			ID:          rm.ID,
			Name:        clean(rm.Name, maxNameLen),
			Description: clean(rm.Description, maxDescriptionLen),
			Section:     "overflow",
			Efforts:     canonicalEfforts(rm.Runtime.EffortLevels),
		}
		if m.Name == "" {
			m.Name = m.ID
		}
		m.DefaultEffort = pickDefaultEffort(m.Efforts, rm.Runtime.DefaultEffort)
		if rm.Section == "main" {
			m.Section = "main"
			main = append(main, m)
		} else {
			overflow = append(overflow, m)
		}
	}
	out := make([]Model, 0, len(main)+len(overflow))
	out = append(out, main...)
	out = append(out, overflow...)
	if len(out) == 0 {
		return nil, errors.New("catalog: no usable models")
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// canonicalEfforts keeps only Claude-allowlisted levels, deduplicated, in the
// allowlist's order. Never nil, so it serialises as [].
func canonicalEfforts(levels []string) []string {
	out := []string{}
	for _, e := range EffortsFor(ClaudeEngine) {
		if contains(levels, e) {
			out = append(out, e)
		}
	}
	return out
}

// pickDefaultEffort returns want when the model offers it, else "high" when
// offered, else the model's first level, else "" (no effort control at all).
func pickDefaultEffort(efforts []string, want string) string {
	if len(efforts) == 0 {
		return ""
	}
	if contains(efforts, want) {
		return want
	}
	if contains(efforts, "high") {
		return "high"
	}
	return efforts[0]
}

// clean strips control characters and caps s at limit runes.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) { // Cf: bidi overrides, zero-width chars
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

// Builtin is the compiled-in picker: what ParseCatalog gives for the catalog
// as of this build (TestBuiltinMatchesFixture keeps the two in step). Used
// until a fetch succeeds, and for good when fetching is disabled. A fresh
// copy each call, so no caller can mutate the shared list.
func Builtin() []Model {
	all := []string{"low", "medium", "high", "xhigh", "max"}
	noX := []string{"low", "medium", "high", "max"}
	mk := func(id, name, desc, section string, efforts []string, def string) Model {
		e := append([]string{}, efforts...)
		return Model{ID: id, Name: name, Description: desc, Section: section, Efforts: e, DefaultEffort: def}
	}
	return []Model{
		mk("claude-opus-5-5", "Opus 5.5", "Most capable for ambitious work", "main", all, "medium"),
		mk("claude-sonnet-5", "Sonnet 5", "Most efficient for everyday tasks", "main", all, "high"),
		mk("claude-fable-5-1", "Fable 5.1", "For your toughest challenges", "main", all, "high"),
		mk("claude-haiku-4-5-20251001", "Haiku 4.5", "Fastest for quick answers", "main", nil, ""),
		mk("claude-opus-5", "Opus 5", "", "overflow", all, "high"),
		mk("claude-fable-5", "Fable 5", "", "overflow", all, "high"),
		mk("claude-opus-4-8", "Opus 4.8", "", "overflow", all, "high"),
		mk("claude-opus-4-7", "Opus 4.7", "", "overflow", all, "xhigh"),
		mk("claude-opus-4-6", "Opus 4.6", "", "overflow", noX, "high"),
		mk("claude-sonnet-4-6", "Sonnet 4.6", "", "overflow", noX, "high"),
	}
}

// Catalog is the Claude engine's model Source: it keeps the last good copy of
// the published catalog in memory. Safe for concurrent use.
type Catalog struct {
	url     string
	client  *http.Client
	timeout time.Duration

	mu        sync.RWMutex
	models    []Model // nil until a fetch succeeds
	fetchedAt time.Time
}

// NewCatalog returns a catalog that fetches from url. An empty url disables
// fetching: the catalog then always serves Builtin() (air-gapped installs).
func NewCatalog(url string) *Catalog {
	return &Catalog{url: url, client: &http.Client{CheckRedirect: catalogRedirectPolicy}, timeout: fetchTimeout}
}

// maxCatalogRedirects bounds the redirects one fetch follows.
const maxCatalogRedirects = 3

// catalogRedirectPolicy follows a redirect only to the same host and only
// over https (or plain http on a loopback host, the test-server exception
// checkCatalogURL makes too): a catalog is untrusted input, and a redirect
// must not be able to move the fetch somewhere else.
func catalogRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxCatalogRedirects {
		return fmt.Errorf("catalog: too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("catalog: redirect to another host (%s) refused", req.URL.Host)
	}
	return checkCatalogURL(req.URL)
}

// checkCatalogURL requires https. Plain http is allowed only for a loopback
// host — local test servers — never for a real catalog.
func checkCatalogURL(u *url.URL) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("catalog: %s URL refused — the catalog must be fetched over https", u.Scheme)
}

// Snapshot returns the current picker: the last good fetch, or Builtin() when
// there has never been one. The returned slices are copies.
func (c *Catalog) Snapshot() List {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.models == nil {
		return List{Models: Builtin(), Source: SourceBuiltin}
	}
	out := make([]Model, len(c.models))
	for i, m := range c.models {
		m.Efforts = append([]string{}, m.Efforts...)
		out[i] = m
	}
	t := c.fetchedAt
	return List{Models: out, Source: SourceLive, FetchedAt: &t}
}

// Models implements Source. The catalog is published centrally, so daemonID
// plays no part.
func (c *Catalog) Models(context.Context, string) (List, error) { return c.Snapshot(), nil }

// BuiltinClaudeSource serves Builtin() — the Claude source when no catalog is
// configured at all.
var BuiltinClaudeSource Source = SourceFunc(func(context.Context, string) (List, error) {
	return List{Models: Builtin(), Source: SourceBuiltin}, nil
})

// Refresh fetches the catalog once. On success it replaces the in-memory
// copy; on any failure it keeps whatever it had and returns the error.
func (c *Catalog) Refresh(ctx context.Context) error {
	if c.url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	if err := checkCatalogURL(req.URL); err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxBodyBytes {
		return fmt.Errorf("catalog: body larger than %d bytes", maxBodyBytes)
	}
	ms, err := ParseCatalog(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.models = ms
	c.fetchedAt = time.Now().UTC()
	c.mu.Unlock()
	return nil
}

// Run refreshes now and then every interval until ctx ends. Failures are
// logged; the last good copy (or Builtin) keeps being served.
func (c *Catalog) Run(ctx context.Context, interval time.Duration) {
	if c.url == "" {
		log.Printf("models: catalog fetching disabled — serving the built-in model list")
		return
	}
	refresh := func() {
		if err := c.Refresh(ctx); err != nil {
			log.Printf("models: catalog refresh from %s failed (keeping last good copy): %v", c.url, err)
		}
	}
	refresh()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}
