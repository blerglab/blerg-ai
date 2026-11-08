// themecheck finds color literals and theme-ID literals that bypass the central theme tokens
// (BLG-473). Every color in every Blerg app must come from brand/tokens.css or
// packages/chat/src/theme/*.css, so a bundled or uploaded theme applies everywhere with no code
// change. A flagged line can be excused with a trailing comment:
//
//	// themecheck:allow <reason>
//
// and a whole file — one whose sample or fixture data only looks like color literals, such as a
// multi-line template literal a trailing comment would corrupt — with a comment anywhere in it:
//
//	// themecheck:allow-file <reason>
//
// The reason is required in both forms and is part of the record of why the exception exists.
package audit

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Violation is one flagged line.
type Violation struct {
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Message)
}

// ThemeIDs are the first-party theme vocabulary (packages/settings/theme.ts). "system" is not a
// stored theme value (it means "no pin"), so it is not flagged. The six after high-contrast are
// the bundled open-source schemes (BLG-474, scripts/themeimport).
var ThemeIDs = []string{"dark", "light", "colorblind-dark", "colorblind-light", "high-contrast",
	"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"}

// uniqueThemeIDs are theme IDs specific enough to flag wherever they appear as a literal, with no
// extra context required. "dark" and "light" are common English words, so they are only flagged
// near the word "theme" on the same line (case-insensitive) to avoid flooding on unrelated code.
var uniqueThemeIDs = []string{"colorblind-dark", "colorblind-light", "high-contrast",
	"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"}
var contextThemeIDs = []string{"dark", "light"}

// Submatch 1 is "-file" for a whole-file marker, "" for a single-line one; submatch 2 is the
// reason text (everything after the marker, trimmed and comment-close-stripped by the caller).
var allowMarker = regexp.MustCompile(`themecheck:allow(-file)?\b(.*)$`)

// hexColor matches a CSS hex color: 3, 4, 6 or 8 hex digits, not preceded by a word character (so
// it doesn't match the tail of an identifier) and not immediately followed by another hex/word
// character (so it doesn't match a short prefix of a longer token).
var hexColor = regexp.MustCompile(`(?:^|[^\w#])#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b`)

// color-mix() isn't listed here: color-mix(in srgb, var(--a) 50%, var(--b)) is a legitimate way to
// blend two tokens, with no literal of its own. A literal color inside it (a hex, named color, or
// one of these function forms) is still caught by the other checks, since they scan its arguments
// like any other value.
var colorFunc = regexp.MustCompile(`\b(rgba?|hsla?|oklch|oklab|lab|lch|hwb|color)\s*\(`)

// namedColors is the common subset of CSS named colors actually likely to show up as a literal
// mistake. The independent review (round 3) found "rebeccapurple" unflagged against the earlier
// curated subset — a reminder that a hand-picked list always has a gap an adversarial check finds.
// The layered context this checker already requires before treating a word as a literal (a CSS
// declaration value, a hyphen-exclusion so a custom-property name is never mistaken for one, a
// recognized color-ish property/attribute or CSS-shaped string content in TS/TSX) makes the full
// CSS Color Module 4 keyword list safe here, not just a subset chosen to limit false positives.
// "transparent" is deliberately excluded: it has no hue, so it is identical in every theme by
// definition, not a brand color choice bypassing the tokens. "currentcolor" is excluded too: it
// names the inherited color, not a literal one.
var namedColors = []string{
	"aliceblue", "antiquewhite", "aqua", "aquamarine", "azure", "beige", "bisque", "black",
	"blanchedalmond", "blue", "blueviolet", "brown", "burlywood", "cadetblue", "chartreuse",
	"chocolate", "coral", "cornflowerblue", "cornsilk", "crimson", "cyan", "darkblue", "darkcyan",
	"darkgoldenrod", "darkgray", "darkgreen", "darkgrey", "darkkhaki", "darkmagenta",
	"darkolivegreen", "darkorange", "darkorchid", "darkred", "darksalmon", "darkseagreen",
	"darkslateblue", "darkslategray", "darkslategrey", "darkturquoise", "darkviolet", "deeppink",
	"deepskyblue", "dimgray", "dimgrey", "dodgerblue", "firebrick", "floralwhite", "forestgreen",
	"fuchsia", "gainsboro", "ghostwhite", "gold", "goldenrod", "gray", "green", "greenyellow",
	"grey", "honeydew", "hotpink", "indianred", "indigo", "ivory", "khaki", "lavender",
	"lavenderblush", "lawngreen", "lemonchiffon", "lightblue", "lightcoral", "lightcyan",
	"lightgoldenrodyellow", "lightgray", "lightgreen", "lightgrey", "lightpink", "lightsalmon",
	"lightseagreen", "lightskyblue", "lightslategray", "lightslategrey", "lightsteelblue",
	"lightyellow", "lime", "limegreen", "linen", "magenta", "maroon", "mediumaquamarine",
	"mediumblue", "mediumorchid", "mediumpurple", "mediumseagreen", "mediumslateblue",
	"mediumspringgreen", "mediumturquoise", "mediumvioletred", "midnightblue", "mintcream",
	"mistyrose", "moccasin", "navajowhite", "navy", "oldlace", "olive", "olivedrab", "orange",
	"orangered", "orchid", "palegoldenrod", "palegreen", "paleturquoise", "palevioletred",
	"papayawhip", "peachpuff", "peru", "pink", "plum", "powderblue", "purple", "rebeccapurple",
	"red", "rosybrown", "royalblue", "saddlebrown", "salmon", "sandybrown", "seagreen", "seashell",
	"sienna", "silver", "skyblue", "slateblue", "slategray", "slategrey", "snow", "springgreen",
	"steelblue", "tan", "teal", "thistle", "tomato", "turquoise", "violet", "wheat", "white",
	"whitesmoke", "yellow", "yellowgreen",
}

var namedColorWord = regexp.MustCompile(`(?i)\b(` + strings.Join(namedColors, "|") + `)\b`)

// cssDeclValue captures a declaration's value: a ":" not followed by "{" (so a selector's pseudo-
// class/element colon, which is always followed by more selector or "{", doesn't match), up to the
// ";" or "}" that ends it. Matched per line: every value in this codebase's CSS lives on one line.
var cssDeclValue = regexp.MustCompile(`:\s*([^;{}]+)[;}]`)

// jsColorProp is a property/attribute name whose string value is a CSS color if it's a bare color
// word: every color-bearing style key (background, border*, outline*, shadows, caret/accent/
// column-rule, SVG fill/stroke/stop/flood), a JSX presentation attribute, or a canvas 2D context
// property. \w* covers the family (border, borderColor, borderTopColor, ...).
var jsColorProp = regexp.MustCompile(`\b(fill\w*|stroke\w*|color\w*|background\w*|border\w*|outline\w*|boxShadow|textShadow|caret\w*|accent\w*|columnRule\w*|stopColor|floodColor)\s*[:=]\s*$`)

// cssLikeStringContent reports whether a TS/TSX/JS string literal's content is itself CSS: a
// color-function call (including color-mix, which colorFunc deliberately excludes so a blend of
// two tokens isn't flagged on its own) or a "property: value" declaration shape. Triggered, the
// whole string is scanned as CSS content — not just when a known property/attribute precedes it —
// so a literal inside a plain TS constant (const SHADOW = 'color-mix(in srgb, var(--x) 50%, white)')
// is still caught.
var cssLikeStringContent = regexp.MustCompile(`\b(?:rgba?|hsla?|oklch|oklab|lab|lch|hwb|color(?:-mix)?)\s*\(|^\s*[a-zA-Z-]+\s*:\s*\S`)

var varFallback = regexp.MustCompile(`var\(\s*(--[a-zA-Z0-9-]+)\s*,\s*([^()]*(?:\([^()]*\)[^()]*)*)\)`)

var quotedString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|` + "`" + `(?:[^` + "`" + `\\]|\\.)*` + "`")

var dataThemeAttr = regexp.MustCompile(`data-theme\s*[=:]\s*["']?(` + strings.Join(ThemeIDs, "|") + `)["']?`)

// comparisonBefore/comparisonAfter detect a theme ID literal directly compared (mode === 'dark',
// 'light' !== mode), the evasion a bare "theme" on the line doesn't catch.
var comparisonBefore = regexp.MustCompile(`(===|!==|==|!=)\s*$`)
var comparisonAfter = regexp.MustCompile(`^\s*(===|!==|==|!=)`)

// HarnessFolders are the exact test-infrastructure directories exempt from the audit: fixtures,
// mocks and setup helpers that never ship in a production build. Named explicitly, repo-root-
// relative, rather than inferred from any directory literally called "test"/"tests"/"__tests__"
// (review finding 9 — that rule was broad enough to exempt real app code that happened to live
// under a folder with that name, wherever in the tree it appeared).
var HarnessFolders = []string{
	"runner/frontend/src/test/",
	"packages/chat/src/test/",
	"board/web/src/test/",
	"core/web/src/test/",
}

// IsTestFile reports whether path (repo-root-relative) is test code, excluded from the audit
// entirely: a *.test.*/*.spec.* file anywhere, or anything under a HarnessFolders entry.
func IsTestFile(path string) bool {
	base := filepath.Base(path)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	p := filepath.ToSlash(path)
	for _, folder := range HarnessFolders {
		if strings.HasPrefix(p, folder) {
			return true
		}
	}
	return false
}

// IsExempt reports whether path (slash-separated, relative to repo root) is a theme-token file:
// the audit's own definitions, not a site that must consume them.
func IsExempt(path string, exemptFiles []string) bool {
	p := filepath.ToSlash(path)
	for _, e := range exemptFiles {
		if p == e {
			return true
		}
	}
	return false
}

// EveryThemeTokens parses CSS content and returns the set of custom property names declared by
// every theme in themeIDs: present either in a plain `:root { ... }` block (which every theme
// inherits unless it overrides it) or in every `:root[data-theme="<id>"] { ... }` block.
func EveryThemeTokens(tokenFileContents map[string]string, themeIDs []string) map[string]bool {
	base := map[string]bool{}
	perTheme := map[string]map[string]bool{}
	for _, id := range themeIDs {
		perTheme[id] = map[string]bool{}
	}
	propDecl := regexp.MustCompile(`(--[a-zA-Z0-9-]+)\s*:`)
	for _, content := range tokenFileContents {
		for _, block := range findRootBlocks(content) {
			props := map[string]bool{}
			for _, m := range propDecl.FindAllStringSubmatch(block.body, -1) {
				props[m[1]] = true
			}
			if block.themeID == "" {
				for p := range props {
					base[p] = true
				}
				continue
			}
			if dst, ok := perTheme[block.themeID]; ok {
				for p := range props {
					dst[p] = true
				}
			}
		}
	}
	every := map[string]bool{}
	for p := range base {
		every[p] = true
	}
	// Fold in properties that every per-theme block defines even without a base declaration.
	for p := range perTheme[themeIDs[0]] {
		all := true
		for _, id := range themeIDs {
			if !perTheme[id][p] {
				all = false
				break
			}
		}
		if all {
			every[p] = true
		}
	}
	return every
}

type rootBlock struct {
	themeID string // "" for the plain, unqualified :root block
	body    string
}

var rootSelector = regexp.MustCompile(`:root(?:\[data-theme=["'](` + strings.Join(ThemeIDs, "|") + `)["']\])?(?::not\([^)]*\))?\s*\{`)

// findRootBlocks extracts the body of every top-level :root / :root[data-theme=...] block in css
// content, matching braces so nested color-mix()/calc() parens don't confuse it (those are
// balanced parens, not braces, so a plain brace counter is sufficient). A :root:not([data-theme])
// block (the unpinned branch of an OS-preference media query) is skipped entirely: it only applies
// under that OS preference AND no explicit pin, narrower than the unconditional base :root block,
// so it must not be treated as "every theme" the way the true base block is.
func findRootBlocks(css string) []rootBlock {
	var blocks []rootBlock
	locs := rootSelector.FindAllStringSubmatchIndex(css, -1)
	for _, loc := range locs {
		openBrace := loc[1] - 1
		depth := 1
		i := openBrace + 1
		for ; i < len(css) && depth > 0; i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if strings.Contains(css[loc[0]:loc[1]], ":not(") {
			continue
		}
		themeID := ""
		if loc[2] != -1 {
			themeID = css[loc[2]:loc[3]]
		}
		blocks = append(blocks, rootBlock{themeID: themeID, body: css[openBrace+1 : i-1]})
	}
	return blocks
}

// regexContext is the set of characters that can precede a `/` that starts a regex literal rather
// than a division operator (or nothing at all, at the start of a line/file).
const regexContext = "([{,;:=!&|?+-~*%^<>"

// stripComments returns content with the body of every // line comment and /* */ block comment
// replaced by spaces (newlines kept, so line numbers are unaffected), so a comment that happens to
// quote an example string — "a pin's state (#134)" — is never mistaken for a live literal. A
// minimal single-pass scan tracks whether we're inside a string (and which quote) or a regex
// literal, since `//`, `/*`, and a stray quote character all lose their special meaning there (a
// regex character class like /[`'"]/ can legally contain any of them).
func stripComments(content string) string {
	out := []byte(content)
	var inString byte // 0, '\'', '"', or '`'
	var lastSig byte  // last non-whitespace character seen, for the regex-vs-division guess
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString != 0 {
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == inString {
				inString = 0
				lastSig = c
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inString = c
		case '/':
			if i+1 < len(out) && out[i+1] == '/' {
				for i < len(out) && out[i] != '\n' {
					out[i] = ' '
					i++
				}
				i--
				continue
			} else if i+1 < len(out) && out[i+1] == '*' {
				out[i] = ' '
				out[i+1] = ' '
				i += 2
				for i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/') {
					if out[i] != '\n' {
						out[i] = ' '
					}
					i++
				}
				if i+1 < len(out) {
					out[i] = ' '
					out[i+1] = ' '
					i++
				}
				continue
			} else if lastSig == 0 || strings.IndexByte(regexContext, lastSig) >= 0 {
				if end := regexLiteralEnd(out, i); end > 0 {
					i = end - 1 // the outer loop's i++ lands just past the literal
					lastSig = '/'
					continue
				}
			}
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			lastSig = c
		}
	}
	return string(out)
}

// regexLiteralEnd returns the index just past a `/.../flags` regex literal starting at start (where
// out[start] == '/'), or 0 if what follows doesn't close before a newline (so it wasn't a regex
// after all — most likely a division operator, which this function leaves untouched).
func regexLiteralEnd(out []byte, start int) int {
	inClass := false
	for j := start + 1; j < len(out); j++ {
		switch out[j] {
		case '\n':
			return 0
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				j++
				for j < len(out) && out[j] >= 'a' && out[j] <= 'z' {
					j++
				}
				return j
			}
		}
	}
	return 0
}

// ScanFile returns the violations on one file's content. path is used only for messages and the
// test/extension/exemption decisions a caller has already made; ScanFile itself only scans.
func ScanFile(path, content string, everyTheme map[string]bool) []Violation {
	var out []Violation
	isCSS := strings.EqualFold(filepath.Ext(path), ".css")
	code := stripComments(content)
	lines := strings.Split(content, "\n")
	lineStart := make([]int, len(lines))
	offset := 0
	for i, l := range lines {
		lineStart[i] = offset
		offset += len(l) + 1
	}
	lineOf := func(pos int) int {
		lo, hi := 0, len(lineStart)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if lineStart[mid] <= pos {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return lo + 1
	}

	excluded := excludedSpans(code, everyTheme)
	inExcluded := func(pos int) bool {
		for _, sp := range excluded {
			if pos >= sp[0] && pos < sp[1] {
				return true
			}
		}
		return false
	}

	// Allowed scan spans: the whole file for CSS; only quoted-string contents for JS/TS (so a
	// comment mentioning "card #144" is never mistaken for a hex color).
	type span struct{ start, end int }
	var scanSpans []span
	if isCSS {
		scanSpans = []span{{0, len(code)}}
	} else {
		for _, m := range quotedString.FindAllStringIndex(code, -1) {
			scanSpans = append(scanSpans, span{m[0], m[1]})
		}
	}

	report := func(pos int, msg string) {
		if inExcluded(pos) {
			return
		}
		out = append(out, Violation{File: path, Line: lineOf(pos), Message: msg})
	}

	for _, sp := range scanSpans {
		region := code[sp.start:sp.end]
		for _, m := range hexColor.FindAllStringSubmatchIndex(region, -1) {
			start := sp.start + m[2] - 1 // position of '#'
			report(start, fmt.Sprintf("hex color literal %q: use a theme token instead", region[m[2]-1:m[3]]))
		}
		for _, m := range colorFunc.FindAllStringIndex(region, -1) {
			report(sp.start+m[0], fmt.Sprintf("color function %q: use a theme token instead", region[m[0]:m[1]]))
		}
	}
	// isHyphenated reports whether the character immediately before start or at end is a letter,
	// digit, or hyphen — i.e. pos is inside a longer identifier like --icon-orange or var(--icon-
	// orange), not a standalone color word. \b alone treats '-' as a boundary, so a custom property
	// name built from a color word would otherwise read as a literal.
	isHyphenated := func(content string, start, end int) bool {
		return (start > 0 && content[start-1] == '-') || (end < len(content) && content[end] == '-')
	}
	if isCSS {
		for _, m := range cssDeclValue.FindAllStringSubmatchIndex(code, -1) {
			value := code[m[2]:m[3]]
			for _, cm := range namedColorWord.FindAllStringSubmatchIndex(value, -1) {
				if isHyphenated(value, cm[0], cm[1]) {
					continue
				}
				pos := m[2] + cm[0]
				report(pos, fmt.Sprintf("named color %q: use a theme token instead", value[cm[0]:cm[1]]))
			}
		}
	} else {
		// TS/TSX/JS: scanning every quoted word for a color name would flood on ordinary English
		// ("a red flag", a `label` named 'red herring'), so a bare named color is only a CSS color
		// when either:
		//   (a) it's the whole value of a recognized color-ish property/attribute (style object key,
		//       JSX fill/stroke/color, a canvas 2D context's fillStyle/strokeStyle), or
		//   (b) the string's own content is itself CSS-shaped (a color-function call or a
		//       "property: value" declaration) — the whole string is then scanned like a CSS value,
		//       wherever it appears, not just after a known property name.
		for _, m := range quotedString.FindAllStringIndex(code, -1) {
			raw := code[m[0]:m[1]]
			inner := raw[1 : len(raw)-1]
			if inner == "" {
				continue
			}
			if strings.EqualFold(namedColorWord.FindString(inner), inner) && jsColorProp.MatchString(code[:m[0]]) {
				report(m[0], fmt.Sprintf("named color %q: use a theme token instead", inner))
				continue
			}
			if cssLikeStringContent.MatchString(inner) {
				for _, cm := range namedColorWord.FindAllStringSubmatchIndex(inner, -1) {
					if isHyphenated(inner, cm[0], cm[1]) {
						continue
					}
					report(m[0]+1+cm[0], fmt.Sprintf("named color %q: use a theme token instead", inner[cm[0]:cm[1]]))
				}
			}
		}
	}

	// Theme-ID literals, wherever they appear (CSS selector attribute or a quoted TS string).
	var dataThemeSpans [][2]int
	for _, m := range dataThemeAttr.FindAllStringSubmatchIndex(code, -1) {
		dataThemeSpans = append(dataThemeSpans, [2]int{m[0], m[1]})
		report(m[0], fmt.Sprintf("theme ID %q named directly: a component must not special-case a theme ID, only a token every theme defines", code[m[2]:m[3]]))
	}
	inDataThemeSpan := func(pos int) bool {
		for _, sp := range dataThemeSpans {
			if pos >= sp[0] && pos < sp[1] {
				return true
			}
		}
		return false
	}
	for _, id := range uniqueThemeIDs {
		pat := regexp.MustCompile(`['"]` + regexp.QuoteMeta(id) + `['"]`)
		for _, m := range pat.FindAllStringIndex(code, -1) {
			if inDataThemeSpan(m[0]) {
				continue
			}
			report(m[0], fmt.Sprintf("theme ID %q named directly: a component must not special-case a theme ID, only a token every theme defines", id))
		}
	}
	for _, id := range contextThemeIDs {
		pat := regexp.MustCompile(`['"]` + regexp.QuoteMeta(id) + `['"]`)
		for _, m := range pat.FindAllStringIndex(code, -1) {
			if inDataThemeSpan(m[0]) {
				continue
			}
			ln := lineOf(m[0])
			onThemeLine := ln-1 < len(lines) && strings.Contains(strings.ToLower(lines[ln-1]), "theme")
			compared := comparisonBefore.MatchString(code[:m[0]]) || comparisonAfter.MatchString(code[m[1]:])
			if onThemeLine || compared {
				report(m[0], fmt.Sprintf("theme ID %q named directly: a component must not special-case a theme ID, only a token every theme defines", id))
			}
		}
	}

	return applyAllowList(path, out, lines)
}

// excludedSpans returns byte ranges of var(--token, fallback) calls whose token is defined by
// every theme: the literal-looking fallback text inside is unreachable once the token resolves
// everywhere, so it is not a violation. A fallback naming a token not defined everywhere is left
// un-excluded, so any color literal inside it is still flagged (the fallback is live and must be
// removed per the card's rule).
func excludedSpans(content string, everyTheme map[string]bool) [][2]int {
	var spans [][2]int
	for _, m := range varFallback.FindAllStringSubmatchIndex(content, -1) {
		token := content[m[2]:m[3]]
		if everyTheme[token] {
			spans = append(spans, [2]int{m[4], m[5]})
		}
	}
	return spans
}

// markerReason extracts and cleans the reason text from an allowMarker match: trims whitespace,
// a trailing block-comment close, and a leading "-" (left over from a JSX-style {/* ... */} or a
// stray dash in the source comment style).
func markerReason(rest string) string {
	reason := strings.TrimSpace(rest)
	reason = strings.TrimSuffix(reason, "*/")
	reason = strings.TrimSpace(strings.TrimPrefix(reason, "-"))
	return strings.TrimSpace(reason)
}

// fileLevelAllow scans every line for a `themecheck:allow-file <reason>` marker. It reports
// whether one was found, and its reason (empty if the marker itself had none).
func fileLevelAllow(lines []string) (found bool, reason string) {
	for _, line := range lines {
		m := allowMarker.FindStringSubmatch(line)
		if m != nil && m[1] == "-file" {
			return true, markerReason(m[2])
		}
	}
	return false, ""
}

// applyAllowList drops violations whose line carries a non-empty `themecheck:allow <reason>`
// marker, or whose file carries a `themecheck:allow-file <reason>` marker anywhere in it,  and
// turns an empty-reason marker into its own violation (an excuse with no reason is not an excuse).
func applyAllowList(path string, violations []Violation, lines []string) []Violation {
	if fileFound, fileReason := fileLevelAllow(lines); fileFound {
		if fileReason == "" {
			return []Violation{{File: path, Line: 1, Message: "themecheck:allow-file with no reason: every exception must say why"}}
		}
		return nil
	}
	var out []Violation
	seenEmptyReason := map[int]bool{}
	for _, v := range violations {
		if v.Line-1 >= len(lines) {
			out = append(out, v)
			continue
		}
		m := allowMarker.FindStringSubmatch(lines[v.Line-1])
		if m == nil || m[1] == "-file" {
			out = append(out, v)
			continue
		}
		if reason := markerReason(m[2]); reason == "" {
			seenEmptyReason[v.Line] = true
		}
		// else: allowed, dropped
	}
	for ln := range seenEmptyReason {
		out = append(out, Violation{File: path, Line: ln, Message: "themecheck:allow with no reason: every exception must say why"})
	}
	return out
}
