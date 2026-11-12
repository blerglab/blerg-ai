package main

import "embed"

//go:embed schemes/*.yaml schemes/LICENSE-*.txt
var schemeFiles embed.FS

// Bundled is one curated scheme this command ships into brand/tokens.css. License and Source
// record what was checked before bundling it (the card's own requirement): every one here was
// confirmed by fetching the actual LICENSE file text directly from the named repository — not a
// search-engine summary or a secondary source — and that text is vendored next to the scheme file
// (schemes/LICENSE-*.txt) with the real copyright notice MIT itself requires keeping. PaletteOwner
// is the original design's actual author (where that differs from whoever contributed the base16
// port to tinted-theming/schemes); PortBy is empty when the original author did their own port.
//
// Two themes were considered and left out because no vendoring-grade LICENSE text was reachable:
//   - Gruvbox (morhetz/gruvbox): the repo has no LICENSE file, only a terse "MIT/X11" mention in
//     the README with no copyright notice text to vendor — MIT itself requires keeping that notice,
//     and a different, unrelated fork's LICENSE.md (gruvbox-community/gruvbox, copyright Pavel
//     Pertsev) does not cover the original colors this import would have used.
//   - Solarized (Ethan Schoonover): only indirect, inconsistent secondary evidence was reachable,
//     no direct LICENSE file.
type Bundled struct {
	ID           string // packages/settings/theme.ts THEMES id and the brand.css data-theme value
	Name         string // shown in the theme picker
	File         string // schemes/<File>, a tinted-theming base16 YAML
	LicenseFile  string // schemes/<LicenseFile>, the vendored full LICENSE text
	License      string // SPDX-ish summary + copyright line, for the picker's own records
	PaletteOwner string // who designed the original colors
	PortBy       string // who ported it to base16, if different from PaletteOwner
	Source       string // where the palette values came from
}

// BundledThemes is every scheme this importer ships. Order here is the order they appear in the
// theme picker, after the six built-in themes.
var BundledThemes = []Bundled{
	{
		ID: "catppuccin-mocha", Name: "Catppuccin Mocha", File: "catppuccin-mocha.yaml",
		LicenseFile: "LICENSE-catppuccin.txt", License: "MIT (Copyright (c) 2021 Catppuccin)",
		PaletteOwner: "Catppuccin Org", PortBy: "Catppuccin Org (self-ported)",
		Source: "https://github.com/catppuccin/catppuccin, via https://github.com/tinted-theming/schemes/blob/spec-0.11/base16/catppuccin-mocha.yaml",
	},
	{
		ID: "catppuccin-latte", Name: "Catppuccin Latte", File: "catppuccin-latte.yaml",
		LicenseFile: "LICENSE-catppuccin.txt", License: "MIT (Copyright (c) 2021 Catppuccin)",
		PaletteOwner: "Catppuccin Org", PortBy: "Catppuccin Org (self-ported)",
		Source: "https://github.com/catppuccin/catppuccin, via https://github.com/tinted-theming/schemes/blob/spec-0.11/base16/catppuccin-latte.yaml",
	},
	{
		ID: "dracula", Name: "Dracula", File: "dracula.yaml",
		LicenseFile: "LICENSE-dracula-theme.txt", License: "MIT (Copyright (c) 2023 Dracula Theme)",
		PaletteOwner: "Dracula Theme", PortBy: "clach04 (https://github.com/clach04), base16 port contributed to tinted-theming/schemes",
		Source: "https://github.com/dracula/dracula-theme, via https://github.com/tinted-theming/schemes/blob/spec-0.11/base16/dracula.yaml",
	},
	{
		ID: "nord", Name: "Nord", File: "nord.yaml",
		LicenseFile: "LICENSE-nord.txt", License: "MIT (Copyright (c) 2016-present Sven Greb)",
		PaletteOwner: "Sven Greb (formerly published as Arctic Ice Studio)", PortBy: "Sven Greb (self-ported)",
		Source: "https://github.com/nordtheme/nord, via https://github.com/tinted-theming/schemes/blob/spec-0.11/base16/nord.yaml",
	},
	{
		// BLG-474 review (round 2): this was wrongly attributed to enkia as the porter. enkia
		// designed the original Tokyo Night VS Code theme; the base16 port actually bundled here
		// is by Michaël Ball (the YAML's own "author" field), contributed to tinted-theming/schemes
		// under that repo's MIT license. Both layers are vendored: the original palette's own
		// LICENSE (enkia) and the aggregator's (tinted-theming/schemes, LICENSE-tinted-theming-
		// schemes.txt) which covers Michaël Ball's contributed port.
		ID: "tokyo-night", Name: "Tokyo Night", File: "tokyo-night.yaml",
		LicenseFile: "LICENSE-tokyo-night-vscode-theme.txt", License: "MIT (Copyright (c) 2018-present Enkia); base16 port under tinted-theming/schemes' MIT license (Copyright (c) 2022 Tinted Theming)",
		PaletteOwner: "Enkia (original Tokyo Night VS Code theme)", PortBy: "Michaël Ball, base16 port contributed to tinted-theming/schemes",
		Source: "https://github.com/enkia/tokyo-night-vscode-theme (original palette), base16 port via https://github.com/tinted-theming/schemes/blob/spec-0.11/base16/tokyo-night-dark.yaml",
	},
}

func loadBundled(b Bundled) (Scheme, error) {
	raw, err := schemeFiles.ReadFile("schemes/" + b.File)
	if err != nil {
		return Scheme{}, err
	}
	return parseScheme(raw)
}

// loadLicense returns the vendored full LICENSE text for a bundled scheme.
func loadLicense(b Bundled) (string, error) {
	raw, err := schemeFiles.ReadFile("schemes/" + b.LicenseFile)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
