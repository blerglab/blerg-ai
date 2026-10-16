package pluginsinstall

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

// InstallCodex uses Codex's native plugin installer. The caller supplies an isolated,
// credential-free CODEX_HOME and validates returned paths before loading any plugin.
func InstallCodex(ctx context.Context, o Options, entries []pluginspec.Entry) Result {
	if o.Bin == "" {
		o.Bin = "codex"
	}
	p := newInstaller(o)
	ctx, cancel := context.WithTimeout(ctx, p.total)
	defer cancel()
	res := Result{Total: len(entries), Names: map[string]string{}, Paths: map[string]string{}}
	allowed := []pluginspec.Entry{}
	markets := map[string]bool{}
	for _, e := range entries {
		if pluginspec.ValidateClaude(e) != nil || !p.allow.Allows(e.Marketplace) {
			res.Skipped = append(res.Skipped, sanitizeName(e.Plugin))
			continue
		}
		key := strings.ToLower(e.Marketplace)
		if !markets[key] {
			if _, err := p.step(ctx, "marketplace add "+e.Marketplace, "plugin", "marketplace", "add", e.Marketplace); err != nil {
				res.Failed = append(res.Failed, e.Plugin)
				res.MarketplaceFailed = true
				continue
			}
			markets[key] = true
		}
		allowed = append(allowed, e)
	}
	if len(allowed) == 0 {
		return res
	}
	out, err := p.step(ctx, "marketplace list", "plugin", "marketplace", "list", "--json")
	var list struct {
		Marketplaces []struct {
			Name   string `json:"name"`
			Source struct {
				Type   string `json:"sourceType"`
				Source string `json:"source"`
			} `json:"marketplaceSource"`
		} `json:"marketplaces"`
	}
	if err != nil || decodeCodexJSON(out, &list) != nil {
		res.MarketplaceFailed = true
		for _, e := range allowed {
			res.Failed = append(res.Failed, e.Plugin)
		}
		return res
	}
	for _, m := range list.Marketplaces {
		source := codexGitHubSource(m.Source.Source)
		if m.Source.Type == "git" && ValidMarketplaceName(m.Name) && markets[strings.ToLower(source)] {
			res.Names[strings.ToLower(source)] = m.Name
		}
	}
	for _, e := range allowed {
		name := res.Names[strings.ToLower(e.Marketplace)]
		if name == "" {
			res.Failed = append(res.Failed, e.Plugin)
			res.MarketplaceFailed = true
			continue
		}
		id := e.Plugin + "@" + name
		out, err := p.step(ctx, "install "+id, "plugin", "add", id, "--json")
		var installed struct {
			ID          string `json:"pluginId"`
			Name        string `json:"name"`
			Marketplace string `json:"marketplaceName"`
			Path        string `json:"installedPath"`
		}
		if err != nil || decodeCodexJSON(out, &installed) != nil || installed.ID != id || installed.Name != e.Plugin || installed.Marketplace != name || installed.Path == "" {
			res.Failed = append(res.Failed, e.Plugin)
			continue
		}
		res.Installed++
		res.Entries = append(res.Entries, e)
		res.Paths[id] = installed.Path
	}
	return res
}

func decodeCodexJSON(out string, v any) error {
	if at := strings.Index(out, "{"); at >= 0 {
		out = out[at:]
	}
	return json.Unmarshal([]byte(strings.TrimSpace(out)), v)
}
func codexGitHubSource(raw string) string {
	if pluginspec.ValidMarketplaceSource(raw) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	source := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if !pluginspec.ValidMarketplaceSource(source) {
		return ""
	}
	return source
}
