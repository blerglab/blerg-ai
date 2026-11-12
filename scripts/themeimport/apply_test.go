package main

import (
	"os"
	"strings"
	"testing"
)

const fixtureTokensCSS = `:root {
  --basalt: #000000;
}
` + themesStart + `
stale content that must be replaced
` + themesEnd + `

/* Session identity palette */
:root {
  --session-color-blue: #56b4e9;
}
` + sessionsStart + `
stale session content
` + sessionsEnd + `
`

func TestGenerateTokensCSSReplacesOnlyTheMarkedSections(t *testing.T) {
	out, err := generateTokensCSS(fixtureTokensCSS)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--basalt: #000000;") {
		t.Error("content outside the markers was not preserved")
	}
	if strings.Contains(out, "stale content that must be replaced") {
		t.Error("stale theme-section content was not replaced")
	}
	if strings.Contains(out, "stale session content") {
		t.Error("stale session-section content was not replaced")
	}
	for _, id := range []string{"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		if !strings.Contains(out, `data-theme="`+id+`"`) {
			t.Errorf("generated output missing theme block for %s", id)
		}
	}
}

func TestGenerateTokensCSSIsDeterministic(t *testing.T) {
	a, err := generateTokensCSS(fixtureTokensCSS)
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateTokensCSS(fixtureTokensCSS)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("generateTokensCSS is not deterministic across runs")
	}
}

func TestGenerateTokensCSSIsIdempotent(t *testing.T) {
	once, err := generateTokensCSS(fixtureTokensCSS)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := generateTokensCSS(once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Error("regenerating already-generated output changed it — the splice is not idempotent")
	}
}

func TestGenerateTokensCSSErrorsWithoutMarkers(t *testing.T) {
	if _, err := generateTokensCSS("no markers here"); err == nil {
		t.Error("expected an error when the sentinel markers are missing")
	}
}

// TestBrandTokensCSSHasNoDrift is the review's own ask: a test that regenerates from the schemes
// and compares the result with the committed brand/tokens.css, so the two can never silently
// drift apart — splicing a bundled theme's block by hand is no longer possible without this test
// catching it on the next run.
func TestBrandTokensCSSHasNoDrift(t *testing.T) {
	committed := readRepoFile(t, "../../brand/tokens.css")
	regenerated, err := generateTokensCSS(committed)
	if err != nil {
		t.Fatal(err)
	}
	if regenerated != committed {
		t.Error("brand/tokens.css has drifted from what the importer would generate — run " +
			"`GOWORK=off go run . apply ../..` and commit the result")
	}
}

func TestBrandTokensCSSCopiesMatch(t *testing.T) {
	brand := readRepoFile(t, "../../brand/tokens.css")
	for _, copy := range []string{"../../packages/chat/src/theme/brand.css", "../../core/internal/landing/static/tokens.css"} {
		if got := readRepoFile(t, copy); got != brand {
			t.Errorf("%s is not byte-identical to brand/tokens.css", copy)
		}
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
