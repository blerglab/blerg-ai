package main

import "testing"

func fixtureScheme(t *testing.T) Scheme {
	t.Helper()
	sch, err := parseScheme([]byte(fixtureYAML))
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// requiredKeys are every custom property the existing :root[data-theme="dark"]/"light" blocks in
// brand/tokens.css declare — the shape a bundled scheme's own block must match so every surface
// that reads a token actually gets this theme's value instead of silently falling back to the
// unthemed default.
var requiredKeys = []string{
	"color-scheme",
	"--terminal-black", "--terminal-red", "--terminal-green", "--terminal-yellow", "--terminal-blue",
	"--terminal-magenta", "--terminal-cyan", "--terminal-white",
	"--terminal-bright-black", "--terminal-bright-red", "--terminal-bright-green", "--terminal-bright-yellow",
	"--terminal-bright-blue", "--terminal-bright-magenta", "--terminal-bright-cyan", "--terminal-bright-white",
	"--code-keyword", "--code-string", "--code-number", "--code-comment", "--code-title", "--code-type",
	"--code-variable", "--code-builtin",
	"--basalt", "--scree", "--scree-2", "--stone", "--fog", "--fog-dim", "--chalk",
	"--blaze", "--blaze-soft", "--lichen", "--amber", "--danger", "--batch", "--button-fg", "--fill-muted",
	"--selected-border", "--selected-bg", "--selected-text",
	"--diff-add", "--diff-del",
	"--primary-white-fg", "--runner-primary-fg", "--chat-primary-fg", "--knob-fg",
	"--board-diff-add", "--board-diff-del",
	"--si-series-1", "--si-series-2", "--si-series-3", "--series-fg",
	"--chart-tests", "--chart-build", "--chart-git", "--chart-read", "--chart-edit", "--chart-other",
	"--chart-model", "--chart-waiting", "--chart-idle",
}

// extendedInks are every ink BLG-474's review requires at 4.5:1 against basalt/scree/scree-2 — the
// same "accessibility preset" battery core/internal/landing/theme_test.go's TestThemeAAContrast
// runs for colorblind-dark/light and high-contrast. A bundled scheme is a theme someone picks for
// everyday use, not a lesser "aesthetic" tier exempt from AA (the review's finding 3: the earlier
// legacyBucket exemption let --primary-white-fg/--knob-fg stay a hard-coded #fff that measured as
// low as 1.70:1 against --blaze).
var extendedInks = []string{"--chalk", "--fog", "--fog-dim", "--blaze", "--lichen", "--amber",
	"--danger", "--batch", "--selected-text", "--diff-add", "--diff-del",
	"--code-keyword", "--code-string", "--code-number", "--code-comment", "--code-title", "--code-type",
	"--code-variable", "--code-builtin"}

// extendedChartSeries are the 9 chart tokens TestThemeVisualContrast checks at 4.5:1 against every
// surface for the accessibility-preset bucket.
var extendedChartSeries = []string{"tests", "build", "git", "read", "edit", "other", "model", "waiting", "idle"}

func TestMapTokensDeclaresEveryRequiredKey(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	for _, key := range requiredKeys {
		if _, ok := tokens[key]; !ok {
			t.Errorf("missing token %s", key)
		}
	}
}

func TestMapTokensTextInksClearAAAgainstEverySurface(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
	for _, key := range extendedInks {
		if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
			t.Errorf("%s = %s fails 4.5:1 against a surface: %.2f", key, tokens[key], c)
		}
	}
	// ANSI black/white double as backgrounds (the existing dark/light blocks exempt them from the
	// same check for the same reason); the rest of the 16, including bright-black, must clear
	// 4.5:1 too (the accessibility-preset bar, not the legacy dark/light exemption).
	for _, key := range []string{"--terminal-red", "--terminal-green", "--terminal-yellow", "--terminal-blue",
		"--terminal-magenta", "--terminal-cyan", "--terminal-bright-black", "--terminal-bright-red",
		"--terminal-bright-green", "--terminal-bright-yellow", "--terminal-bright-blue",
		"--terminal-bright-magenta", "--terminal-bright-cyan"} {
		if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
			t.Errorf("%s = %s fails 4.5:1 against a surface: %.2f", key, tokens[key], c)
		}
	}
	if c := worstContrast(tokens["--stone"], surfaces); c < 3-0.01 {
		t.Errorf("--stone = %s fails 3:1 against a surface: %.2f", tokens["--stone"], c)
	}
}

func TestMapTokensButtonFgClearsAAAgainstBlaze(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	if c := contrast(tokens["--button-fg"], tokens["--blaze"]); c < 4.5-0.01 {
		t.Errorf("--button-fg %s on --blaze %s: %.2f < 4.5", tokens["--button-fg"], tokens["--blaze"], c)
	}
	// The accessibility presets redirect these three to --button-fg instead of a fixed #fff
	// (BLG-474 review finding 3: a fixed white measured as low as 1.70:1 on some blaze values).
	for _, key := range []string{"--primary-white-fg", "--runner-primary-fg", "--chat-primary-fg", "--knob-fg"} {
		if tokens[key] != tokens["--button-fg"] {
			t.Errorf("%s = %s, want --button-fg (%s)", key, tokens[key], tokens["--button-fg"])
		}
	}
}

// TestMapTokensFillMutedClearsAAAgainstTextShownOnIt covers the selected-tile pattern (core's
// settings nav, runner's CapabilitiesPanel, board's chip.type, chat's --chat-button-bg): --fog
// title/description text painted over a --fill-muted background. BLG-474 review round 3: Tokyo
// Night's --fill-muted (inherited from the unthemed default, var(--stone)) measured ~1.5:1
// against --fog there.
func TestMapTokensFillMutedClearsAAAgainstTextShownOnIt(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	for _, ink := range []string{"--chalk", "--fog"} {
		if c := contrast(tokens[ink], tokens["--fill-muted"]); c < 4.5-0.01 {
			t.Errorf("%s on --fill-muted %s: %.2f < 4.5", ink, tokens["--fill-muted"], c)
		}
	}
}

func TestMapTokensChartSeriesClearAAAgainstEverySurface(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
	for _, series := range extendedChartSeries {
		key := "--chart-" + series
		if c := worstContrast(tokens[key], surfaces); c < 4.5-0.01 {
			t.Errorf("%s = %s fails 4.5:1 against a surface: %.2f", key, tokens[key], c)
		}
	}
}

func TestMapTokensSeriesFgClearsAAAgainstNamedSeries(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	for _, series := range []string{"--chart-tests", "--chart-build", "--chart-git"} {
		if c := contrast(tokens["--series-fg"], tokens[series]); c < 4.5-0.01 {
			t.Errorf("--series-fg %s on %s %s: %.2f < 4.5", tokens["--series-fg"], series, tokens[series], c)
		}
	}
}

func TestMapTokensVariantSetsColorScheme(t *testing.T) {
	dark := fixtureScheme(t)
	if got := mapTokens(dark)["color-scheme"]; got != "dark" {
		t.Errorf("color-scheme = %q, want dark", got)
	}
	light := dark
	light.Variant = "light"
	if got := mapTokens(light)["color-scheme"]; got != "light" {
		t.Errorf("color-scheme = %q, want light", got)
	}
}

// TestMapTokensChalkAndSelectedTextClearTranslucentFills mirrors the landing contrast test's
// alpha-composite check (core/internal/landing/theme_test.go's TestThemeVisualContrast): chalk and
// selected-text are shown over --selected-bg/--blaze-soft, translucent fills over a surface, not
// just the flat surface.
func TestMapTokensChalkAndSelectedTextClearTranslucentFills(t *testing.T) {
	for _, b := range BundledThemes {
		sch, err := loadBundled(b)
		if err != nil {
			t.Fatal(err)
		}
		tokens := mapTokens(sch)
		surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
		for _, fill := range []struct {
			color string
			alpha float64
		}{{tokens["--selected-bg"], float64(0x29) / 255}, {tokens["--blaze-soft"], float64(0x33) / 255}} {
			opaque := fill.color[:7]
			for _, surf := range surfaces {
				mixed := blend(opaque, surf, 1-fill.alpha)
				for _, ink := range []string{"--chalk", "--selected-text"} {
					if c := contrast(tokens[ink], mixed); c < 4.5-0.01 {
						t.Errorf("%s: %s on %s over %s: %.2f < 4.5", b.ID, ink, fill.color, surf, c)
					}
				}
			}
		}
	}
}

func TestMapTokensEveryValueIsASolidHexOrColorScheme(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	for key, value := range tokens {
		if key == "color-scheme" {
			continue
		}
		if key == "--blaze-soft" || key == "--selected-bg" {
			if len(value) != 9 || value[0] != '#' {
				t.Errorf("%s = %q is not an 8-digit hex with alpha", key, value)
			}
			continue
		}
		if len(value) != 7 || value[0] != '#' {
			t.Errorf("%s = %q is not a solid 6-digit hex", key, value)
		}
	}
}
