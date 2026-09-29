package pluginspec

import "testing"

func TestValidPluginName(t *testing.T) {
	good := []string{"superpowers", "frontend-design", "a", "a.b_c-d", "0abc"}
	bad := []string{"", "Superpowers", "-x", ".x", "_x", "a b", "a/b", "a@b", "x;y", "$(x)", "é"}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	bad = append(bad, string(long))
	for _, s := range good {
		if !ValidPluginName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if ValidPluginName(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestValidMarketplaceSource(t *testing.T) {
	good := []string{"anthropics/claude-plugins-official", "a/b", "some.org/repo_x", "o/.github"}
	bad := []string{
		"", "owner", "/repo", "owner/", "a/b/c", "https://github.com/a/b", "git@github.com:a/b", "file:///x",
		"./x/y", "../x", "../..", ".x/y", "-x/y", "a/..", "a/.", "a/b#ref", "a/b@ref", "a b/c", "http://a/b", "~/x",
	}
	for _, s := range good {
		if !ValidMarketplaceSource(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if ValidMarketplaceSource(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestAllowlist(t *testing.T) {
	def, dropped := ParseAllowlist("")
	if len(dropped) != 0 || !def.Allows("anthropics/claude-plugins-official") || !def.Allows("Anthropics/Claude-Plugins-Official") {
		t.Fatal("default allow-list must allow the official marketplace, case-insensitively")
	}
	if def.Allows("evil/plugins") {
		t.Fatal("default allow-list must not allow others")
	}
	a, dropped := ParseAllowlist(" a/b , https://x/y ,,c/d")
	if len(dropped) != 1 || dropped[0] != "https://x/y" {
		t.Fatalf("dropped = %v", dropped)
	}
	if !a.Allows("a/b") || !a.Allows("c/d") || a.Allows("anthropics/claude-plugins-official") {
		t.Fatal("explicit list replaces the default")
	}
	star, _ := ParseAllowlist("*")
	if !star.Allows("any/thing") || star.Allows("../x") || star.Allows("file:///etc") {
		t.Fatal("* allows any VALID source only")
	}
}
