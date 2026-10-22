package landing_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The landing page serves a copy of brand/tokens.css (go:embed cannot reach outside the module), so
// the two must stay byte-identical: brand/tokens.css is the source of truth.
func TestEmbeddedTokensMatchBrand(t *testing.T) {
	brand, err := os.ReadFile(filepath.Join("..", "..", "..", "brand", "tokens.css"))
	if err != nil {
		t.Fatalf("read brand/tokens.css: %v", err)
	}
	embedded, err := os.ReadFile(filepath.Join("static", "tokens.css"))
	if err != nil {
		t.Fatalf("read static/tokens.css: %v", err)
	}
	if !bytes.Equal(brand, embedded) {
		t.Fatal("core/internal/landing/static/tokens.css differs from brand/tokens.css: copy brand/tokens.css over it")
	}
	// The selected-state family is in every palette block: dark default, light media query,
	// [data-theme="light"] and [data-theme="dark"].
	for _, tok := range []string{"--selected-border:", "--selected-bg:", "--selected-text:"} {
		if n := strings.Count(string(brand), tok); n != 4 {
			t.Errorf("brand/tokens.css declares %s %d times, want 4 (one per palette block)", tok, n)
		}
	}
}
