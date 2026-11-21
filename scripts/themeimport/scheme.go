// Command themeimport maps a Base16 or Base24 scheme (tinted-theming's YAML format) onto Blerg's
// brand tokens, applying WCAG contrast fixes where a slot would fail: text at least 4.5:1, chart
// marks at least 3:1, each against every app surface. See README.md for the token mapping and the
// license this command itself requires from a bundled scheme before importing it.
package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Scheme is a parsed Base16/Base24 palette (tinted-theming schemes repo format): a flat
// `base00`..`base0F` (Base16) or `base00`..`base17` (Base24) map of hex colors plus metadata.
type Scheme struct {
	System  string // "base16" or "base24"
	Name    string
	Author  string
	Variant string // "dark" or "light"
	Palette map[string]string
}

var paletteLine = regexp.MustCompile(`^\s*(base[0-9A-Fa-f]{2}):\s*"?(#[0-9a-fA-F]{6})"?`)
var metaLine = regexp.MustCompile(`^(system|name|author|variant):\s*"?([^"#]*)"?`)

// parseScheme reads a tinted-theming scheme file's bytes. It is a minimal line-oriented reader for
// this one flat shape (metadata keys, then an indented `palette:` map of hex strings) rather than a
// general YAML parser: every scheme this importer bundles comes from the same generator and never
// uses YAML features beyond that shape.
func parseScheme(raw []byte) (Scheme, error) {
	sch := Scheme{Palette: map[string]string{}}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := metaLine.FindStringSubmatch(line); m != nil {
			value := strings.TrimSpace(m[2])
			switch m[1] {
			case "system":
				sch.System = value
			case "name":
				sch.Name = value
			case "author":
				sch.Author = value
			case "variant":
				sch.Variant = value
			}
			continue
		}
		if m := paletteLine.FindStringSubmatch(line); m != nil {
			sch.Palette[strings.ToLower(m[1])] = strings.ToLower(m[2])
		}
	}
	required := []string{"base00", "base01", "base02", "base03", "base04", "base05", "base06", "base07",
		"base08", "base09", "base0a", "base0b", "base0c", "base0d", "base0e", "base0f"}
	if sch.System == "base24" {
		required = append(required, "base10", "base11", "base12", "base13", "base14", "base15", "base16", "base17")
	}
	for _, key := range required {
		if sch.Palette[key] == "" {
			return Scheme{}, fmt.Errorf("themeimport: scheme %q missing %s", sch.Name, key)
		}
	}
	return sch, nil
}

// slot resolves a base16/24 key case-insensitively (the YAML uses base0A, lookups use base0a).
func (s Scheme) slot(key string) string {
	return s.Palette[strings.ToLower(key)]
}
