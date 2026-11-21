package audit

import (
	"strings"
	"testing"
)

const tokensFixture = `
:root {
  --chat-bg: #0f1217;
  --chat-border: #313a48;
}
:root[data-theme="light"] {
  --chat-bg: #ffffff;
}
:root[data-theme="dark"] {
  --chat-bg: #0f1217;
}
:root[data-theme="colorblind-dark"] {
  --chat-bg: #0f1217;
}
:root[data-theme="colorblind-light"] {
  --chat-bg: #ffffff;
}
:root[data-theme="high-contrast"] {
  --chat-bg: #000000;
  --chat-focus-ring: #ffff00;
}
`

func tokens(t *testing.T) map[string]bool {
	t.Helper()
	return EveryThemeTokens(map[string]string{"tokens.css": tokensFixture}, ThemeIDs)
}

func TestEveryThemeTokens(t *testing.T) {
	every := tokens(t)
	if !every["--chat-bg"] {
		t.Error("--chat-bg is declared (via base :root) in every theme, want it in the set")
	}
	if !every["--chat-border"] {
		t.Error("--chat-border is only in the base :root block, which every theme inherits, want it in the set")
	}
	if every["--chat-focus-ring"] {
		t.Error("--chat-focus-ring is only defined for high-contrast, must not be in the every-theme set")
	}
}

func TestEveryThemeTokens_NotDataThemeBlockIsNotEveryTheme(t *testing.T) {
	// :root:not([data-theme]) only applies under an OS preference AND no explicit pin — narrower
	// than the unconditional base :root block, so a token defined only there (not in base, not in
	// any explicit :root[data-theme="..."] block) must not count as defined by every theme.
	css := `
:root {
  --chat-bg: #0f1217;
}
@media (prefers-color-scheme: light) {
  :root:not([data-theme]) {
    --chat-bg: #ffffff;
    --os-light-only: #ffffff;
  }
}
:root[data-theme="light"] {
  --chat-bg: #ffffff;
}
:root[data-theme="dark"] {
  --chat-bg: #0f1217;
}
:root[data-theme="colorblind-dark"] {
  --chat-bg: #0f1217;
}
:root[data-theme="colorblind-light"] {
  --chat-bg: #ffffff;
}
:root[data-theme="high-contrast"] {
  --chat-bg: #000000;
}
`
	every := EveryThemeTokens(map[string]string{"tokens.css": css}, ThemeIDs)
	if every["--os-light-only"] {
		t.Error("a token defined only inside :root:not([data-theme]) must not count as defined by every theme")
	}
	if !every["--chat-bg"] {
		t.Error("--chat-bg is defined by every theme (base + every explicit block), want it in the set")
	}
}

func TestScanFile_HexColorInCSS(t *testing.T) {
	v := ScanFile("DockRepositories.css", `.row { color: #ff00aa; }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
	if v[0].Line != 1 {
		t.Errorf("want line 1, got %d", v[0].Line)
	}
}

func TestScanFile_HexColorInQuotedTSString(t *testing.T) {
	v := ScanFile("FileIcon.tsx", `const c = someFn() ? '#ff00aa' : '#00ff00';`, tokens(t))
	if len(v) != 2 {
		t.Fatalf("want 2 violations (one per literal), got %d: %v", len(v), v)
	}
}

func TestScanFile_IssueReferenceInQuotedDocCommentIsNotFlagged(t *testing.T) {
	v := ScanFile("pins.ts", "/** Every pin has one of three states (card #134), as a word (\"#122\" but not \"#1220\"). */", tokens(t))
	if len(v) != 0 {
		t.Fatalf("a quoted example ticket number inside a block comment is not a live literal, got %d: %v", len(v), v)
	}
}

func TestScanFile_RegexLiteralWithBacktickInCharClassDoesNotDesyncComments(t *testing.T) {
	src := "const s = x.replace(/[’‘`]/g, \"'\")\n" +
		"// a pin's state as a glyph (#134): comment after a regex literal containing a backtick\n"
	v := ScanFile("worklist.ts", src, tokens(t))
	if len(v) != 0 {
		t.Fatalf("a backtick inside a regex character class must not be mistaken for a template-string "+
			"start, which would desync comment stripping for the rest of the file, got %d: %v", len(v), v)
	}
}

func TestScanFile_IssueReferenceInCommentIsNotFlagged(t *testing.T) {
	v := ScanFile("punycode.ts", `// see card #144 and #1234 for background, not in a string`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("want 0 violations for a bare comment reference, got %d: %v", len(v), v)
	}
}

func TestScanFile_RgbFunction(t *testing.T) {
	v := ScanFile("x.css", `.a { background: rgba(0,0,0,0.5); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_NamedColorInCSS(t *testing.T) {
	v := ScanFile("x.css", `.a { color: red; }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_TransparentIsNotFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { background: transparent; border-color: transparent; }`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("transparent has no hue, it is the same in every theme by definition, got %d: %v", len(v), v)
	}
}

func TestScanFile_NamedColorAsClassIsNotFlagged(t *testing.T) {
	v := ScanFile("x.css", `.red-badge { color: var(--chat-danger); }`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("want 0 violations, a classname is not a declaration value, got %d: %v", len(v), v)
	}
}

func TestScanFile_VarFallbackUniversalTokenPasses(t *testing.T) {
	v := ScanFile("x.css", `.a { color: var(--chat-bg, #0f1217); }`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("want 0 violations: --chat-bg is defined by every theme, so the fallback is unreachable, got %d: %v", len(v), v)
	}
}

func TestScanFile_VarFallbackNonUniversalTokenFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: var(--chat-focus-ring, #ffff00); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation: --chat-focus-ring is not defined by every theme, so the literal fallback is live, got %d: %v", len(v), v)
	}
}

func TestScanFile_DataThemeAttributeFlagged(t *testing.T) {
	v := ScanFile("ActivityInsights.css", `[data-theme="high-contrast"] .badge { color: var(--chat-fg); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation for naming the theme ID directly, got %d: %v", len(v), v)
	}
}

func TestScanFile_ThemeIDStringLiteralFlagged(t *testing.T) {
	v := ScanFile("x.ts", `if (theme === 'colorblind-dark') { setClass('hc') }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_DarkLiteralOnlyFlaggedNearTheme(t *testing.T) {
	v := ScanFile("x.ts", `const mode = 'dark';`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("'dark' with no theme context on the line should not be flagged, got %d: %v", len(v), v)
	}
	v = ScanFile("x.ts", `const theme = 'dark';`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("'dark' assigned to something named theme should be flagged, got %d: %v", len(v), v)
	}
}

func TestScanFile_AllowListMarkerSuppresses(t *testing.T) {
	v := ScanFile("x.css", `.a { color: #ff00aa; } /* themecheck:allow BLG-466, owned by Insights rework, in flight */`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("an allow marker with a reason should suppress the violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_AllowListMarkerWithoutReasonIsItselfFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: #ff00aa; } /* themecheck:allow */`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation (the empty-reason marker itself), got %d: %v", len(v), v)
	}
	if !strings.Contains(v[0].Message, "no reason") {
		t.Errorf("want the violation message to say the reason is missing, got %q", v[0].Message)
	}
}

func TestScanFile_AllowFileMarkerSuppressesWholeFile(t *testing.T) {
	src := "// themecheck:allow-file BLG-472/BLG-452, sample diagram fixtures owned by the chart renderer rework, in flight\n" +
		"export const SAMPLE = `classDef a fill:#e0f2fe,stroke:#0369a1`\n"
	v := ScanFile("mermaidSamples.ts", src, tokens(t))
	if len(v) != 0 {
		t.Fatalf("a file-level marker with a reason should suppress every violation in the file, got %d: %v", len(v), v)
	}
}

func TestScanFile_AllowFileMarkerWithoutReasonIsItselfFlagged(t *testing.T) {
	src := "// themecheck:allow-file\n" +
		"export const SAMPLE = `classDef a fill:#e0f2fe`\n"
	v := ScanFile("mermaidSamples.ts", src, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation (the empty-reason file marker itself), got %d: %v", len(v), v)
	}
	if !strings.Contains(v[0].Message, "no reason") {
		t.Errorf("want the violation message to say the reason is missing, got %q", v[0].Message)
	}
}

func TestIsTestFile(t *testing.T) {
	cases := map[string]bool{
		"runner/frontend/src/components/SettingsDialog.tsx":           false,
		"runner/frontend/src/components/SettingsDialog.test.tsx":      true,
		"packages/chat/src/model/attachmentPreview.ts":                false,
		"packages/chat/src/test/setup.ts":                             true,
		"runner/frontend/src/components/SessionList.preview.test.tsx": true,
		"board/web/src/test/setup.ts":                                 true,
		"core/web/src/test/setup.ts":                                  true,
		// A directory that happens to be named "test" but isn't one of the four named harness
		// folders is not exempt (review finding 9: a bare directory-name rule is too broad — it
		// would hide real production code that happens to live under such a folder).
		"runner/frontend/src/components/test/Something.tsx": false,
	}
	for path, want := range cases {
		if got := IsTestFile(path); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsExempt(t *testing.T) {
	exempt := []string{"packages/chat/src/theme/brand.css", "packages/chat/src/theme/blerg.css", "packages/chat/src/theme/tokens.css"}
	if !IsExempt("packages/chat/src/theme/blerg.css", exempt) {
		t.Error("blerg.css must be exempt")
	}
	if IsExempt("packages/chat/src/components/chat.css", exempt) {
		t.Error("chat.css is a consumer, must not be exempt")
	}
}

// Evasion cases found in independent review (BLG-473 round 2).

func TestScanFile_OklchFunctionFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: oklch(0.7 0.15 30); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_LabFunctionFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: lab(50% 40 59.5); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_LchFunctionFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: lch(52% 72 50); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_HwbFunctionFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: hwb(30 10% 0%); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_ColorFunctionFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { color: color(srgb 1 0 0); }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(v), v)
	}
}

func TestScanFile_ColorMixWithLiteralFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { background: color-mix(in srgb, red 50%, blue); }`, tokens(t))
	if len(v) != 2 {
		t.Fatalf("want 2 violations (one per named literal), got %d: %v", len(v), v)
	}
}

func TestScanFile_ColorMixOfTokensIsNotFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { background: color-mix(in srgb, var(--chat-bg) 50%, var(--chat-border)); }`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("color-mix of two tokens has no literal, got %d: %v", len(v), v)
	}
}

func TestScanFile_NamedColorMidValueFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { border: 1px solid white; }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a named color anywhere in a declaration value must be flagged, not just as the whole value, got %d: %v", len(v), v)
	}
}

func TestScanFile_NamedColorInsideCustomPropertyNameIsNotFlagged(t *testing.T) {
	v := ScanFile("x.css", `.a { background: var(--icon-orange); border-color: var(--notfound-stone-1); }`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("a color word inside a hyphenated custom-property name is not a literal, got %d: %v", len(v), v)
	}
}

func TestScanFile_StyleObjectNamedColorFlagged(t *testing.T) {
	v := ScanFile("x.tsx", `const x = <div style={{ color: 'red' }} />`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a named color in a style object must be flagged, got %d: %v", len(v), v)
	}
}

func TestScanFile_JsxFillStrokeAttributeNamedColorFlagged(t *testing.T) {
	v := ScanFile("x.tsx", `const x = <path fill="white" stroke="black" />`, tokens(t))
	if len(v) != 2 {
		t.Fatalf("want 2 violations (fill and stroke), got %d: %v", len(v), v)
	}
}

func TestScanFile_CanvasFillStyleStrokeStyleNamedColorFlagged(t *testing.T) {
	v := ScanFile("x.ts", `ctx.fillStyle = 'red'; ctx.strokeStyle = 'blue';`, tokens(t))
	if len(v) != 2 {
		t.Fatalf("want 2 violations (fillStyle and strokeStyle), got %d: %v", len(v), v)
	}
}

func TestScanFile_NamedColorWordUnrelatedToPropertyIsNotFlagged(t *testing.T) {
	v := ScanFile("x.tsx", `const label = 'red herring'`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("a named-color word with no css-property context must not be flagged, got %d: %v", len(v), v)
	}
}

func TestScanFile_EmptyStringAfterColorPropertyIsNotFlagged(t *testing.T) {
	v := ScanFile("x.ts", `probe.style.color = ''`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("an empty string is not a named color, got %d: %v", len(v), v)
	}
}

func TestScanFile_ThemeIDComparedDirectlyWithoutThemeWordFlagged(t *testing.T) {
	v := ScanFile("x.ts", `if (mode === 'dark') { setClass('night') }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a direct comparison against a theme ID literal must be flagged even with no 'theme' on the line, got %d: %v", len(v), v)
	}
}

func TestScanFile_ThemeIDComparedOnRightFlagged(t *testing.T) {
	v := ScanFile("x.ts", `if ('light' === mode) { setClass('day') }`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a direct comparison with the literal on the left side must also be flagged, got %d: %v", len(v), v)
	}
}

func TestScanFile_DarkWordAloneStillNotFlagged(t *testing.T) {
	v := ScanFile("x.ts", `const mode = 'dark'`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("plain assignment with no theme context and no comparison must not be flagged, got %d: %v", len(v), v)
	}
}

// Independent review, round 3: two evasions a scratch file still passed on d67c08a8.

func TestScanFile_NamedColorUnderEveryColorBearingStyleKey(t *testing.T) {
	cases := []string{
		`style={{ background: 'rebeccapurple' }}`,
		`style={{ backgroundColor: 'rebeccapurple' }}`,
		`style={{ borderColor: 'rebeccapurple' }}`,
		`style={{ borderTopColor: 'rebeccapurple' }}`,
		`style={{ outlineColor: 'rebeccapurple' }}`,
		`style={{ boxShadow: 'rebeccapurple' }}`,
		`style={{ textShadow: 'rebeccapurple' }}`,
		`style={{ caretColor: 'rebeccapurple' }}`,
		`style={{ accentColor: 'rebeccapurple' }}`,
		`style={{ columnRuleColor: 'rebeccapurple' }}`,
		`<stop stopColor="rebeccapurple" />`,
		`<feFlood floodColor="rebeccapurple" />`,
	}
	for _, src := range cases {
		v := ScanFile("x.tsx", src, tokens(t))
		if len(v) != 1 {
			t.Errorf("%s: want 1 violation, got %d: %v", src, len(v), v)
		}
	}
}

func TestScanFile_ColorMixWithNamedLiteralInPlainStringConstantFlagged(t *testing.T) {
	v := ScanFile("x.ts", `const SHADOW = 'color-mix(in srgb, var(--x) 50%, white)'`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a color-mix() literal inside a plain string constant (no preceding color-ish key) must still be flagged, got %d: %v", len(v), v)
	}
}

func TestScanFile_CssDeclarationShapedStringConstantFlagged(t *testing.T) {
	v := ScanFile("x.ts", `const RULE = 'border: 1px solid tomato'`, tokens(t))
	if len(v) != 1 {
		t.Fatalf("a string whose content is itself a CSS declaration must be scanned like CSS regardless of the variable name, got %d: %v", len(v), v)
	}
}

func TestScanFile_ColorMixOfTokensInPlainStringConstantIsNotFlagged(t *testing.T) {
	v := ScanFile("x.ts", `const SHADOW = 'color-mix(in srgb, var(--a) 50%, var(--b))'`, tokens(t))
	if len(v) != 0 {
		t.Fatalf("color-mix() of two tokens, even as a plain string constant, has no literal, got %d: %v", len(v), v)
	}
}
