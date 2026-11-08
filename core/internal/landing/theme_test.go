package landing_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func themeBlock(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	out := map[string]string{}
	blocks := regexp.MustCompile(regexp.QuoteMeta(selector)+` \{([^}]+)\}`).FindAllStringSubmatch(css, -1)
	if len(blocks) == 0 {
		t.Fatalf("missing palette %s", selector)
	}
	for _, block := range blocks {
		for _, match := range regexp.MustCompile(`(--[\w-]+):\s*([^;]+);`).FindAllStringSubmatch(block[1], -1) {
			out[match[1]] = strings.TrimSpace(match[2])
		}
	}
	return out
}
func luminance(hex string) float64 {
	value, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	channel := func(shift uint) float64 {
		v := float64((value>>shift)&255) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(16) + 0.7152*channel(8) + 0.0722*channel(0)
}
func contrast(a, b string) float64 {
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

func TestThemeAAContrast(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast", "catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		t.Run(name, func(t *testing.T) {
			palette := themeBlock(t, css, `:root[data-theme="`+name+`"]`)
			inks := []string{"--chalk", "--fog"}
			// The defaults retain their historical colors. Check their primary and
			// secondary text; the accessibility presets also guarantee small status,
			// muted, diff and syntax text. Legacy exceptions are documented in themes.md.
			if name != "dark" && name != "light" {
				inks = append(inks, "--fog-dim", "--blaze", "--lichen", "--amber", "--danger", "--batch", "--selected-text", "--diff-add", "--diff-del", "--code-keyword", "--code-string", "--code-number", "--code-comment", "--code-title", "--code-type", "--code-variable", "--code-builtin")
			}
			if name != "dark" && name != "light" {
				if ratio := contrast(palette["--button-fg"], palette["--blaze"]); ratio < 4.5 {
					t.Errorf("button text contrast %.2f", ratio)
				}
				for _, surface := range []string{"--basalt", "--scree", "--scree-2"} {
					if ratio := contrast(palette["--stone"], palette[surface]); ratio < 3 {
						t.Errorf("control border on %s: %.2f", surface, ratio)
					}
				}
			}
			// The selected tile in core's settings nav (and anything else sharing these tokens:
			// runner's CapabilitiesPanel, board's chip.type, chat's --chat-button-bg) paints --fog
			// title/description text over a --fill-muted fill. BLG-474 review round 3/4: Tokyo
			// Night's own --stone measured ~1.5:1 against --fog there, nearly invisible, because
			// --fill-muted had never been checked against the text shown on it — and the same gap
			// existed in colorblind-light (~1.2:1), an accessibility preset, so this applies to
			// every theme, not just the bundled schemes: a token someone else's component reads
			// unconditionally must hold for whichever theme is active. --fill-muted and --chalk
			// aren't always declared in a theme's own block (dark/light inherit both from the base
			// :root), so resolve each against the merged base+override palette, the same way
			// TestThemeVisualContrast does below.
			{
				merged := themeBlock(t, css, ":root")
				for k, v := range palette {
					merged[k] = v
				}
				fillMuted := resolveToken(merged, "--fill-muted")
				if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(fillMuted) {
					t.Fatalf("missing solid --fill-muted (got %q)", fillMuted)
				}
				for _, ink := range []string{"--chalk", "--fog"} {
					color := resolveToken(merged, ink)
					if ratio := contrast(color, fillMuted); ratio < 4.5 {
						t.Errorf("selected-tile %s (%s) on --fill-muted %s: %.2f < 4.5", ink, color, fillMuted, ratio)
					}
				}
			}
			for _, ink := range inks {
				for _, surface := range []string{"--basalt", "--scree", "--scree-2"} {
					if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(palette[ink]) {
						t.Fatalf("missing solid ink %s", ink)
					}
					if ratio := contrast(palette[ink], palette[surface]); ratio < 4.5 {
						t.Errorf("%s on %s: %.2f:1, want WCAG AA 4.5:1", ink, surface, ratio)
					}
				}
			}
		})
	}
}

func TestThemeCopiesAndConsumers(t *testing.T) {
	brand, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"static/tokens.css", "../../../packages/chat/src/theme/brand.css"} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != string(brand) {
			t.Errorf("%s must match brand/tokens.css", path)
		}
	}
	for _, path := range []string{"../../../runner/frontend/src/index.css", "../../../board/web/src/styles.css"} {
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), `@import "../../../brand/tokens.css"`) {
			t.Errorf("%s must import shared tokens", path)
		}
	}
}

// Snapshot of the pre-feature defaults: palette consolidation must not repaint them.
func TestDefaultThemePaletteRemainsUnchanged(t *testing.T) {
	raw, err := os.ReadFile("default_themes_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var defaults map[string]map[string]string
	if err := json.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range defaults {
		actual := themeBlock(t, string(css), ":root")
		for key, value := range themeBlock(t, string(css), `:root[data-theme="`+name+`"]`) {
			actual[key] = value
		}
		for key := range actual {
			actual[key] = resolveToken(actual, key)
		}
		for key, value := range expected {
			if actual[key] != value {
				t.Errorf("default %s %s changed from %s to %s", name, key, value, actual[key])
			}
		}
	}
}

func TestChartSeriesDistinct(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast"} {
		p := themeBlock(t, string(raw), `:root[data-theme="`+name+`"]`)
		// Default hues are frozen legacy values; their small minimum detects
		// near-duplicate edits without promising an accessibility redesign.
		minimum := 5.0
		if strings.HasPrefix(name, "colorblind-") || name == "high-contrast" {
			minimum = 15
		}
		p0 := themeBlock(t, string(raw), ":root")
		for k, v := range p {
			p0[k] = v
		}
		modes := map[string][9]float64{"plain": {1, 0, 0, 0, 1, 0, 0, 0, 1}}
		if minimum == 15 {
			for mode, matrix := range visionMatrices {
				modes[mode] = matrix
			}
		}
		keys := []string{"tests", "build", "git", "read", "edit", "other", "model", "waiting", "idle"}
		for _, key := range keys {
			if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(resolveToken(p0, "--chart-"+key)) {
				t.Fatalf("%s missing solid chart ink %s", name, key)
			}
		}
		for mode, matrix := range modes {
			for i, left := range keys {
				for _, right := range keys[i+1:] {
					a, b := simulatedLab(resolveToken(p0, "--chart-"+left), matrix), simulatedLab(resolveToken(p0, "--chart-"+right), matrix)
					d := labDistance(a, b)
					if d < minimum {
						t.Errorf("%s %s %s/%s delta E %.2f < %.0f", name, mode, left, right, d, minimum)
					}
				}
			}
		}
	}
}

var visionMatrices = map[string][9]float64{
	"protanopia":   {.152286, 1.052583, -.204868, .114503, .786281, .099216, -.003882, -.048116, 1.051998},
	"deuteranopia": {.367322, .860646, -.227968, .280085, .672501, .047413, -.01182, .04294, .968881},
	"tritanopia":   {1.255528, -.076749, -.178779, -.078411, .930809, .147602, .004733, .691367, .3039},
}

func resolveToken(p map[string]string, key string) string {
	value := p[key]
	for strings.HasPrefix(value, "var(") {
		value = p[strings.TrimSuffix(strings.TrimPrefix(value, "var("), ")")]
	}
	return value
}
func labDistance(a, b [3]float64) float64 {
	sum := 0.0
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// Machado et al. full-severity linear RGB transforms, followed by CIE Lab ΔE76.
func simulatedLab(hex string, matrix [9]float64) [3]float64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	var rgb [3]float64
	for i, shift := range []uint{16, 8, 0} {
		v := float64((n>>shift)&255) / 255
		if v <= .04045 {
			rgb[i] = v / 12.92
		} else {
			rgb[i] = math.Pow((v+.055)/1.055, 2.4)
		}
	}
	var sim [3]float64
	for i := 0; i < 3; i++ {
		sim[i] = math.Max(0, math.Min(1, matrix[i*3]*rgb[0]+matrix[i*3+1]*rgb[1]+matrix[i*3+2]*rgb[2]))
	}
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	x := f((.4124564*sim[0] + .3575761*sim[1] + .1804375*sim[2]) / .95047)
	y := f(.2126729*sim[0] + .7151522*sim[1] + .072175*sim[2])
	z := f((.0193339*sim[0] + .119192*sim[1] + .9503041*sim[2]) / 1.08883)
	return [3]float64{116*y - 16, 500 * (x - y), 200 * (y - z)}
}
func TestColorBlindRequiredPairs(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"colorblind-dark", "colorblind-light"} {
		p := themeBlock(t, string(raw), `:root[data-theme="`+name+`"]`)
		for mode, m := range map[string][9]float64{
			"protanopia":   {.152286, 1.052583, -.204868, .114503, .786281, .099216, -.003882, -.048116, 1.051998},
			"deuteranopia": {.367322, .860646, -.227968, .280085, .672501, .047413, -.01182, .04294, .968881},
			"tritanopia":   {1.255528, -.076749, -.178779, -.078411, .930809, .147602, .004733, .691367, .3039},
		} {
			for _, pair := range [][2]string{{"--amber", "--danger"}, {"--batch", "--code-keyword"}, {"--session-color-blue", "--session-color-purple"}, {"--diff-add", "--diff-del"}} {
				a, b := simulatedLab(p[pair[0]], m), simulatedLab(p[pair[1]], m)
				d := 0.0
				for i := range a {
					delta := a[i] - b[i]
					d += delta * delta
				}
				if math.Sqrt(d) < 15 {
					t.Errorf("%s %s %v ΔE %.2f < 15", name, mode, pair, math.Sqrt(d))
				}
			}
		}
	}
}

func TestThemeComponentColorsUseTokens(t *testing.T) {
	for _, path := range []string{"../../../runner/frontend/src/components/ActivityInsights.css", "../../../runner/frontend/src/components/SessionInsights.css", "../../../runner/frontend/src/components/UsageMeters.css",
		"../../../runner/frontend/src/components/CapabilitiesPanel.css",
		"../../../runner/frontend/src/components/PermissionsModal.css",
		"../../../runner/frontend/src/components/SessionRail.css",
		"../../../packages/settings/settings.css",
		"../../../packages/chat/src/components/chat.css",
		"../../../packages/chat/src/components/diff.css",
		"../../../packages/chat/src/components/markup/markup.css",
		"../../../packages/chat/src/components/review/pdf.css", "../../../core/web/src/styles.css", "../../../board/web/src/styles.css", "../../../runner/frontend/src/index.css"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")
		// PDF paper deliberately remains white; other PDF UI colors use tokens.
		if strings.HasSuffix(path, "/review/pdf.css") {
			text = regexp.MustCompile(`(?s)\.pdf-page \{[^}]+\}`).ReplaceAllStringFunc(text, func(block string) string { return strings.ReplaceAll(block, "background: #fff;", "") })
		}
		// A token with a fallback still follows the theme. Remove balanced var()
		// calls, but keep literals in the surrounding declaration visible.
		for start := strings.Index(text, "var("); start >= 0; start = strings.Index(text, "var(") {
			end, depth := start+4, 1
			for end < len(text) && depth > 0 {
				if text[end] == '(' {
					depth++
				}
				if text[end] == ')' {
					depth--
				}
				end++
			}
			text = text[:start] + "TOKEN" + text[end:]
		}
		if regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|rgba?\(\s*\d`).MatchString(text) {
			t.Errorf("%s contains a hardcoded color", path)
		}
	}
	raw, err := os.ReadFile("../../../board/web/src/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "#b9d49a") || strings.Contains(string(raw), "#e2a3a3") {
		t.Fatal("board diff inks must use shared tokens")
	}
}

func TestThemeVisualContrast(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast", "catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		p := themeBlock(t, css, ":root")
		for k, v := range themeBlock(t, css, `:root[data-theme="`+name+`"]`) {
			p[k] = v
		}
		var resolve func(string) string
		resolve = func(k string) string {
			v := p[k]
			if strings.HasPrefix(v, "var(") {
				return resolve(strings.TrimSuffix(strings.TrimPrefix(v, "var("), ")"))
			}
			return v
		}
		check := func(ink, bg string, minimum float64) {
			t.Helper()
			if r := contrast(resolve(ink), resolve(bg)); r < minimum {
				t.Errorf("%s %s on %s %.2f < %.1f", name, ink, bg, r, minimum)
			}
		}
		if name != "dark" && name != "light" {
			check("--button-fg", "--blaze", 4.5)
			for _, series := range []string{"tests", "build", "git", "read", "edit", "other", "model", "waiting", "idle"} {
				for _, bg := range []string{"--basalt", "--scree", "--scree-2"} {
					check("--chart-"+series, bg, 4.5)
				}
			}
		} // Default button and chart inks are frozen against main, including legacy contrast.
		for _, ink := range []string{"blue", "orange", "purple", "teal"} {
			minimum := 4.5
			if name == "dark" || name == "light" {
				minimum = 3
			} // Historical default glyph colors.
			for _, bg := range []string{"--basalt", "--scree", "--scree-2"} {
				check("--session-color-"+ink, bg, minimum)
			}
		}
		for _, series := range []string{"tests", "build", "git"} {
			if name == "dark" || name == "light" {
				continue
			}
			check("--series-fg", "--chart-"+series, 4.5)
		}
		for k := range p {
			if strings.HasPrefix(k, "--terminal-") {
				if k == "--terminal-bright-black" && (name == "dark" || name == "light") {
					continue
				} // Historical muted ink exception.
				// ANSI black and white also serve as backgrounds; test those as a pair.
				if k == "--terminal-black" || k == "--terminal-white" || k == "--terminal-bright-white" {
					continue
				}
				for _, bg := range []string{"--basalt", "--scree", "--scree-2"} {
					check(k, bg, 4.5)
				}
			}
		}
		check("--terminal-black", "--terminal-white", 4.5)
		for _, fill := range []string{"--selected-bg", "--blaze-soft"} {
			rgba := p[fill]
			if len(rgba) != 9 {
				t.Fatalf("%s %s must have explicit alpha", name, fill)
			}
			n, _ := strconv.ParseUint(rgba[1:], 16, 32)
			alpha := float64(n&255) / 255
			for _, bg := range []string{"--basalt", "--scree", "--scree-2"} {
				base, _ := strconv.ParseUint(resolve(bg)[1:], 16, 32)
				mixed := uint64(0)
				for _, shift := range []uint{16, 8, 0} {
					c := math.Round(float64((n>>(shift+8))&255)*alpha + float64((base>>shift)&255)*(1-alpha))
					mixed |= uint64(c) << shift
				}
				hex := "#" + fmt.Sprintf("%06x", mixed)
				for _, ink := range []string{"--chalk", "--selected-text"} {
					if r := contrast(resolve(ink), hex); r < 4.5 {
						t.Errorf("%s %s on %s over %s %.2f", name, ink, fill, bg, r)
					}
				}
			}
		}
	}
}

func TestThemeBootstrapPerOrigin(t *testing.T) {
	for _, app := range []string{"core/web", "board/web", "runner/frontend"} {
		raw, err := os.ReadFile("../../../" + app + "/index.html")
		if err != nil {
			t.Fatal(err)
		}
		html := string(raw)
		bootstrap := -1
		if match := regexp.MustCompile(`localStorage\.getItem\(['"]blerg-theme['"]\)`).FindStringIndex(html); match != nil {
			bootstrap = match[0]
		}
		module := strings.Index(html, `type="module"`)
		if bootstrap < 0 || module < bootstrap {
			t.Errorf("%s needs local theme bootstrap before app load", app)
		}
		for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast", "catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
			if !regexp.MustCompile(`['"]` + regexp.QuoteMeta(name) + `['"]`).MatchString(html) {
				t.Errorf("%s bootstrap omits %s", app, name)
			}
		}
		if app == "core/web" && strings.Index(html, `rel="stylesheet"`) < bootstrap {
			t.Error("core theme bootstrap must run before stylesheet paint")
		}
	}
}

// hslToHex matches the CSS hsl(h s% l%) formula (CSS Color 3) — hue in degrees, saturation and
// lightness as fractions.
func hslToHex(h, s, l float64) string {
	h = math.Mod(h, 360) / 360
	var r, g, b float64
	if s == 0 {
		r, g, b = l, l, l
	} else {
		var q float64
		if l < 0.5 {
			q = l * (1 + s)
		} else {
			q = l + s - l*s
		}
		p := 2*l - q
		hue := func(t float64) float64 {
			if t < 0 {
				t++
			}
			if t > 1 {
				t--
			}
			switch {
			case t < 1.0/6:
				return p + (q-p)*6*t
			case t < 1.0/2:
				return q
			case t < 2.0/3:
				return p + (q-p)*(2.0/3-t)*6
			default:
				return p
			}
		}
		r, g, b = hue(h+1.0/3), hue(h), hue(h-1.0/3)
	}
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(r*255)), int(math.Round(g*255)), int(math.Round(b*255)))
}

// Runner's SessionTiles.css and board's App.tsx BoardIcon paint a hash-derived-hue tile at a fixed
// hsl(h, S%, L%) — the same lightness in every theme, by design (card #473 review). Their label ink
// must clear 4.5:1 against every hue that formula can produce, in every theme.
func TestTileLabelContrast(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	formulas := []struct {
		name string
		s, l float64
	}{
		{"runner SessionTiles.css hsl(h 32% 30%)", 0.32, 0.30},
		{"board BoardIcon hsl(h 45% 28%)", 0.45, 0.28},
	}
	for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast", "catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		p := themeBlock(t, css, ":root")
		for k, v := range themeBlock(t, css, `:root[data-theme="`+name+`"]`) {
			p[k] = v
		}
		ink := resolveToken(p, "--tile-label-fg")
		if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(ink) {
			t.Fatalf("%s: --tile-label-fg is not a solid hex color: %q", name, ink)
		}
		for _, f := range formulas {
			for h := 0; h < 360; h += 2 {
				bg := hslToHex(float64(h), f.s, f.l)
				if r := contrast(ink, bg); r < 4.5 {
					t.Errorf("%s: --tile-label-fg on %s at hue %d (%s): %.2f < 4.5", name, f.name, h, bg, r)
				}
			}
		}
	}
}

// Saved terminal history (runner TerminalHistory.tsx, via terminalRenderer.ts's historyTheme) reads
// --history-bg/-fg/-cursor/-ansi-* from the DOM; they must actually follow the theme, not stay a
// fixed dark palette regardless (review finding: this was previously true only by accident — the
// token existed in every theme by inheriting one fixed value, not by being themed).
func TestHistoryTokensFollowTheme(t *testing.T) {
	raw, err := os.ReadFile("../../../brand/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	resolved := map[string]string{}
	for _, name := range []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast", "catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		p := themeBlock(t, css, ":root")
		for k, v := range themeBlock(t, css, `:root[data-theme="`+name+`"]`) {
			p[k] = v
		}
		bg := resolveToken(p, "--history-bg")
		fg := resolveToken(p, "--history-fg")
		if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(bg) {
			t.Fatalf("%s: --history-bg does not resolve to a solid color: %q", name, bg)
		}
		if r := contrast(fg, bg); r < 4.5 {
			t.Errorf("%s: --history-fg on --history-bg: %.2f < 4.5", name, r)
		}
		resolved[name] = bg
	}
	if resolved["dark"] == resolved["light"] {
		t.Errorf("--history-bg must differ between dark and light (got %s for both) — it is not following the theme", resolved["dark"])
	}
}
