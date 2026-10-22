package protocol

import (
	"strings"
	"testing"
)

func TestMarketplaceSourceFromInput(t *testing.T) {
	ok := map[string]string{
		"example-org/plugins":                         "example-org/plugins",
		"https://github.com/example-org/plugins":      "example-org/plugins",
		"https://github.com/example-org/plugins.git":  "example-org/plugins",
		"https://www.github.com/example-org/plugins/": "example-org/plugins",
	}
	if got, valid := MarketplaceSourceFromInput("  example-org/plugins\t"); !valid || got != "example-org/plugins" {
		t.Errorf("surrounding space: %q %t", got, valid)
	}
	for in, want := range ok {
		if got, valid := MarketplaceSourceFromInput(in); !valid || got != want {
			t.Errorf("%q → %q %t", in, got, valid)
		}
	}
	for _, bad := range []string{"", "http://github.com/a/b", "https://gitlab.com/a/b", "https://user:pw@github.com/a/b", "https://github.com/a/b?x=1",
		"https://github.com/a/b/tree/main", "https://github.com/../x", "https://github.com/a/..", "https://github.com:443/a/b", "https://github.com//a/b", "https://github.com/a%2Fb/c", "https://github.com/a/b#x", "HTTPS://github.com/-a/b", "git@github.com:a/b.git", "file:///etc", "../x", "-x/y", "a/b c", "/local/path", strings.Repeat("a", 301)} {
		if got, valid := MarketplaceSourceFromInput(bad); valid {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestSanitizePluginCatalogBoundsAndValidates(t *testing.T) {
	markets := []CatalogMarketplace{{Name: "good", Source: "a/b"}, {Name: "-bad"}, {Name: "good"}, {Name: "nosrc", Source: "https://x"}}
	plugins := []CatalogPlugin{
		{Name: "p1", Marketplace: "good", Description: "<b>x</b>\x1b[31m\nline"},
		{Name: "P2", Marketplace: "good"},
		{Name: "p3", Marketplace: "unknown"},
		{Name: "p1", Marketplace: "good"},
		{Name: "p4", Marketplace: "nosrc", Version: strings.Repeat("9", 200)},
	}
	m, p, cut := SanitizePluginCatalog(markets, plugins)
	if !cut || len(m) != 2 || m[1].Source != "" || len(p) != 2 {
		t.Fatalf("m=%+v p=%+v cut=%t", m, p, cut)
	}
	if p[0].Description != "<b>x</b> [31m line" {
		t.Fatalf("description %q", p[0].Description)
	}
	if n := len([]rune(p[1].Version)); n != CatalogMaxShort+1 {
		t.Fatalf("version runes %d", n)
	}
	many := make([]CatalogPlugin, CatalogMaxPlugins+3)
	for i := range many {
		many[i] = CatalogPlugin{Name: "p" + strings.Repeat("x", i%50) + string(rune('a'+i%26)) + "-" + itoa(i), Marketplace: "good"}
	}
	if _, p, cut := SanitizePluginCatalog(markets[:1], many); !cut || len(p) != CatalogMaxPlugins {
		t.Fatalf("plugins %d cut %t", len(p), cut)
	}
}

func itoa(i int) string {
	s := ""
	for {
		s = string(rune('0'+i%10)) + s
		i /= 10
		if i == 0 {
			return s
		}
	}
}

func TestValidMCPServerName(t *testing.T) {
	for _, ok := range []string{"tracker", "claude.ai Google Drive", "my-server_1"} {
		if !ValidMCPServerName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", " lead", "a\nb", "a\u202eb", strings.Repeat("x", 201), "\xff"} {
		if ValidMCPServerName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
