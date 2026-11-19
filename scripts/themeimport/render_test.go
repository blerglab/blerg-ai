package main

import (
	"strings"
	"testing"
)

func TestRenderBlockShapeAndOrder(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	block := renderBlock("fixture-id", tokens)
	if !strings.HasPrefix(block, `:root[data-theme="fixture-id"] {`+"\n") {
		t.Fatalf("unexpected header: %q", block[:60])
	}
	if !strings.HasSuffix(strings.TrimRight(block, "\n"), "}") {
		t.Fatalf("unexpected footer: %q", block[len(block)-10:])
	}
	for key := range tokens {
		decl := key + ": " + tokens[key] + ";"
		if !strings.Contains(block, decl) {
			t.Errorf("missing declaration %q in rendered block", decl)
		}
	}
}

func TestRenderBlockIsValidCSSLineShape(t *testing.T) {
	tokens := mapTokens(fixtureScheme(t))
	block := renderBlock("fixture-id", tokens)
	for _, line := range strings.Split(strings.TrimSpace(block), "\n")[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "}" || trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, ";") {
			t.Errorf("declaration line missing trailing semicolon: %q", line)
		}
	}
}
