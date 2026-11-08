package main

import (
	"strings"
	"testing"
)

func TestBundledSchemesLoadAndParse(t *testing.T) {
	for _, b := range BundledThemes {
		sch, err := loadBundled(b)
		if err != nil {
			t.Errorf("%s: %v", b.ID, err)
			continue
		}
		if sch.Name == "" {
			t.Errorf("%s: scheme has no name", b.ID)
		}
		if b.License == "" || b.Source == "" || b.PaletteOwner == "" {
			t.Errorf("%s: missing license, source or palette-owner record", b.ID)
		}
	}
}

// TestBundledSchemesVendorARealLicenseNotice proves every bundled scheme ships the actual LICENSE
// text next to it (MIT requires keeping the notice), not just a claim in manifest.go — the BLG-474
// review's "secondary-source corroboration doesn't count" standard: each vendored file must
// contain a real copyright line and MIT's own permission grant text.
func TestBundledSchemesVendorARealLicenseNotice(t *testing.T) {
	for _, b := range BundledThemes {
		text, err := loadLicense(b)
		if err != nil {
			t.Fatalf("%s: %v", b.ID, err)
		}
		if !strings.Contains(text, "Copyright") {
			t.Errorf("%s: %s has no Copyright line", b.ID, b.LicenseFile)
		}
		if !strings.Contains(text, "Permission is hereby granted") {
			t.Errorf("%s: %s is not the full MIT license text", b.ID, b.LicenseFile)
		}
	}
}

// TestTintedThemingSchemesLicenseIsVendored proves the aggregator repo every scheme's raw hex
// values came from (https://github.com/tinted-theming/schemes) has its own LICENSE vendored too,
// independent of which bundled scheme's own LicenseFile a reader happens to check.
func TestTintedThemingSchemesLicenseIsVendored(t *testing.T) {
	raw, err := schemeFiles.ReadFile("schemes/LICENSE-tinted-theming-schemes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Tinted Theming") || !strings.Contains(string(raw), "Permission is hereby granted") {
		t.Fatal("schemes/LICENSE-tinted-theming-schemes.txt is not the full tinted-theming/schemes license")
	}
}

func TestTokyoNightCreditsTheRealPortAuthor(t *testing.T) {
	for _, b := range BundledThemes {
		if b.ID != "tokyo-night" {
			continue
		}
		if !strings.Contains(b.PortBy, "Michaël Ball") {
			t.Errorf("tokyo-night PortBy = %q, want it to credit Michaël Ball (the actual base16 porter, not enkia)", b.PortBy)
		}
		if !strings.Contains(b.PaletteOwner, "Enkia") {
			t.Errorf("tokyo-night PaletteOwner = %q, want it to credit Enkia (the original palette designer)", b.PaletteOwner)
		}
	}
}

func TestGruvboxIsNotBundled(t *testing.T) {
	for _, b := range BundledThemes {
		if strings.Contains(strings.ToLower(b.ID), "gruvbox") {
			t.Fatalf("gruvbox-dark is bundled (%s) but no vendoring-grade LICENSE text was reachable for it (BLG-474 review)", b.ID)
		}
	}
}

func TestBundledSchemesMapToWCAGSafeTokens(t *testing.T) {
	for _, b := range BundledThemes {
		sch, err := loadBundled(b)
		if err != nil {
			t.Fatalf("%s: %v", b.ID, err)
		}
		tokens := mapTokens(sch)
		surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
		for _, key := range requiredKeys {
			if tokens[key] == "" {
				t.Errorf("%s: missing %s", b.ID, key)
			}
		}
		for _, key := range extendedInks {
			if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
				t.Errorf("%s: %s = %s fails 4.5:1 against a surface: %.2f", b.ID, key, tokens[key], c)
			}
		}
		for _, key := range []string{"--terminal-red", "--terminal-green", "--terminal-yellow", "--terminal-blue",
			"--terminal-magenta", "--terminal-cyan", "--terminal-bright-black", "--terminal-bright-red",
			"--terminal-bright-green", "--terminal-bright-yellow", "--terminal-bright-blue",
			"--terminal-bright-magenta", "--terminal-bright-cyan"} {
			if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
				t.Errorf("%s: %s = %s fails 4.5:1 against a surface: %.2f", b.ID, key, tokens[key], c)
			}
		}
		if c := worstContrast(tokens["--stone"], surfaces); c < 3-0.01 {
			t.Errorf("%s: --stone = %s fails 3:1 against a surface: %.2f", b.ID, tokens["--stone"], c)
		}
		if c := contrast(tokens["--button-fg"], tokens["--blaze"]); c < 4.5-0.01 {
			t.Errorf("%s: --button-fg %s on --blaze %s: %.2f < 4.5", b.ID, tokens["--button-fg"], tokens["--blaze"], c)
		}
		// The selected tile in core's settings nav (and anything else sharing these tokens)
		// paints --chalk/--fog text over a --fill-muted fill.
		for _, ink := range []string{"--chalk", "--fog"} {
			if c := contrast(tokens[ink], tokens["--fill-muted"]); c < 4.5-0.01 {
				t.Errorf("%s: %s on --fill-muted %s: %.2f < 4.5", b.ID, ink, tokens["--fill-muted"], c)
			}
		}
		for _, series := range extendedChartSeries {
			key := "--chart-" + series
			if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
				t.Errorf("%s: %s = %s fails 4.5:1 against a surface: %.2f", b.ID, key, tokens[key], c)
			}
		}
	}
}

func TestBundledThemeIDsAreUniqueAndKebabCase(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range BundledThemes {
		if seen[b.ID] {
			t.Errorf("duplicate id %s", b.ID)
		}
		seen[b.ID] = true
		for _, c := range b.ID {
			if !(c >= 'a' && c <= 'z') && c != '-' {
				t.Errorf("%s: id is not kebab-case", b.ID)
			}
		}
	}
}
