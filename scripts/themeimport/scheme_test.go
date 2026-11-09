package main

import "testing"

const fixtureYAML = `system: "base16"
name: "Fixture Scheme"
author: "Test Author"
variant: "dark"
palette:
  base00: "#1e1e2e" # base
  base01: "#181825" # mantle
  base02: "#313244" # surface0
  base03: "#45475a" # surface1
  base04: "#585b70" # surface2
  base05: "#cdd6f4" # text
  base06: "#f5e0dc" # rosewater
  base07: "#b4befe" # lavender
  base08: "#f38ba8" # red
  base09: "#fab387" # peach
  base0A: "#f9e2af" # yellow
  base0B: "#a6e3a1" # green
  base0C: "#94e2d5" # teal
  base0D: "#89b4fa" # blue
  base0E: "#cba6f7" # mauve
  base0F: "#f2cdcd" # flamingo
`

func TestParseSchemeBase16(t *testing.T) {
	sch, err := parseScheme([]byte(fixtureYAML))
	if err != nil {
		t.Fatal(err)
	}
	if sch.Name != "Fixture Scheme" || sch.Author != "Test Author" || sch.Variant != "dark" {
		t.Fatalf("metadata: %+v", sch)
	}
	if sch.System != "base16" {
		t.Fatalf("system: %q", sch.System)
	}
	if len(sch.Palette) != 16 {
		t.Fatalf("palette size: %d", len(sch.Palette))
	}
	if sch.slot("base00") != "#1e1e2e" || sch.slot("base0F") != "#f2cdcd" {
		t.Fatalf("palette values: %+v", sch.Palette)
	}
}

func TestParseSchemeRejectsIncompletePalette(t *testing.T) {
	_, err := parseScheme([]byte(`system: "base16"
name: "Broken"
variant: "dark"
palette:
  base00: "#1e1e2e"
`))
	if err == nil {
		t.Fatal("expected an error for a short palette")
	}
}

func TestParseSchemeBase24HasExtendedSlots(t *testing.T) {
	sch, err := parseScheme([]byte(`system: "base24"
name: "Fixture24"
variant: "dark"
palette:
  base00: "#000000"
  base01: "#000000"
  base02: "#000000"
  base03: "#000000"
  base04: "#000000"
  base05: "#000000"
  base06: "#000000"
  base07: "#000000"
  base08: "#000000"
  base09: "#000000"
  base0A: "#000000"
  base0B: "#000000"
  base0C: "#000000"
  base0D: "#000000"
  base0E: "#000000"
  base0F: "#000000"
  base10: "#111111"
  base11: "#222222"
  base12: "#333333"
  base13: "#444444"
  base14: "#555555"
  base15: "#666666"
  base16: "#777777"
  base17: "#888888"
`))
	if err != nil {
		t.Fatal(err)
	}
	if sch.slot("base17") != "#888888" {
		t.Fatalf("base24 extended slot missing: %+v", sch.Palette)
	}
}
