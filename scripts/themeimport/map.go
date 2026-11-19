package main

import "math"

// mapTokens maps a Base16/Base24 scheme onto every custom property Blerg's own
// :root[data-theme="dark"]/"light" blocks declare (brand/tokens.css), applying the base16 "role"
// convention — base00 is always the default background and base05 the default foreground,
// regardless of variant; base08-0F are the accent slots, nominally red/orange/yellow/green/cyan/
// blue/purple/brown but not always honestly assigned (see pickByHue below) — then nudging every
// slot that fails WCAG for its use to at least 4.5:1 against every surface it can actually be
// shown on. BLG-474 review (round 2): a bundled scheme is a theme someone picks for everyday use,
// not a lesser tier exempt from AA, so this now holds every role to the same bar the three
// accessibility presets hold themselves to — not just "primary/secondary text", but every accent,
// status, chart, terminal and button-on-accent pairing the apps actually render.
func mapTokens(s Scheme) map[string]string {
	basalt := s.slot("base00")
	scree := s.slot("base01")
	scree2 := s.slot("base02")
	fogDim0 := s.slot("base04")
	surfaces := []string{basalt, scree, scree2}

	text := func(hex string) string { return ensureContrast(hex, surfaces, 4.5) }

	accentSlots := []string{s.slot("base08"), s.slot("base09"), s.slot("base0a"), s.slot("base0b"),
		s.slot("base0c"), s.slot("base0d"), s.slot("base0e"), s.slot("base0f")}
	// Pick by actual hue, not slot position: a port's base08 ("red") is sometimes a pale gray
	// reused for its own editor's syntax highlighting (Tokyo Night's base16 port has no true red
	// or orange in base08/base09 — review finding 5). Searching every accent slot for the hue
	// closest to the intended role finds the real red/orange/yellow even when it was misassigned.
	red := pickByHue(accentSlots, 0)
	orange := pickByHue(accentSlots, 30)
	yellow := pickByHue(accentSlots, 50)
	green := s.slot("base0b")
	cyan := s.slot("base0c")
	blue := s.slot("base0d")
	purple := s.slot("base0e")
	brown := s.slot("base0f")

	// Every accent that stands in for a status/role color is contrast-fixed as text up front: it
	// is read as a word (a status label), a glyph, a chart mark or a button fill's foreground
	// counterpart, never shown as a large wash where sub-AA would be tolerable.
	blaze := text(orange)
	lichen := text(green)
	amber := text(yellow)
	danger := text(red)
	batch := text(blue)
	fogDim := text(fogDim0)
	stone := ensureContrast(s.slot("base03"), surfaces, 3)
	// chart-idle and terminal-bright-black both want a muted-but-legible ink, not --stone itself
	// (which only needs the lower 3:1 border bar and must stay closer to true border-gray).
	mutedText := text(s.slot("base03"))

	// chalk and selected-text are also shown over a translucent tinted fill (--selected-bg at
	// alpha 0x29, --blaze-soft at 0x33), never just the flat surface: fold the actual alpha
	// composite of each fill over each surface into the set of backgrounds they must clear 4.5:1
	// against, the same check the landing contrast test runs.
	var fillSurfaces []string
	for _, fill := range []struct {
		color string
		alpha float64
	}{{batch, float64(0x29) / 255}, {blaze, float64(0x33) / 255}} {
		for _, surf := range surfaces {
			fillSurfaces = append(fillSurfaces, blend(fill.color, surf, 1-fill.alpha))
		}
	}
	textOverFill := func(hex string) string {
		return ensureContrast(hex, append(append([]string{}, surfaces...), fillSurfaces...), 4.5)
	}

	chalk := textOverFill(s.slot("base05"))
	// A secondary ink distinct from chalk: blended a quarter of the way toward the surface, then
	// re-fixed — so it reads dimmer than the primary text but still clears AA on its own.
	fog := text(blend(chalk, scree, 0.25))

	// --fill-muted: the background of a "selected" or "filled" tile shown over the app surface —
	// core's settings nav, runner's CapabilitiesPanel, board's chip.type, chat's --chat-button-bg
	// — always painted with --chalk or --fog text on top. BLG-474 review round 3: left unset, this
	// falls back to --stone, which is only ever contrast-checked against the app surfaces as a
	// border (3:1), never against the text actually shown on it; Tokyo Night's --stone measured
	// ~1.5:1 against --fog there, nearly invisible. Start from --stone (so it still reads as a
	// muted, related fill, not an arbitrary color) and nudge it against the two inks it's actually
	// shown with, the same "nudge an ink against a fixed ink" trick --button-fg uses.
	fillMuted := ensureContrast(stone, []string{fog, chalk}, 4.5)

	// --button-fg: the foreground painted on a solid --blaze fill (a primary button, the settings
	// knob). The accessibility presets never use a fixed white here either, for the same reason
	// (BLG-474 review finding 3: a fixed #fff measured as low as 1.70:1 on some --blaze values).
	// Picking the higher-contrast extreme and then, if that still falls short, nudging --blaze
	// itself away from it (ensureContrast can nudge an "ink" against a fixed "surface" either way)
	// guarantees the pair always clears 4.5:1 without the mapper needing to special-case a scheme.
	buttonFg := pickReadableFg(blaze)
	if contrast(buttonFg, blaze) < 4.5 {
		blaze = ensureContrast(blaze, []string{buttonFg}, 4.5)
	}

	seriesChartColors := map[string]string{
		"tests": blaze, "build": amber, "git": lichen,
		"read": text(cyan), "edit": text(purple), "other": text(brown),
		"model": text(lighten(cyan, -0.1)), "waiting": text(lighten(yellow, -0.1)),
		"idle": mutedText,
	}
	// --series-fg is painted on all three of --chart-tests/-build/-git (blaze/amber/lichen),
	// already contrast-fixed text-weight colors that cluster in a similar lightness band: pick
	// whichever of black/white wins the worst case across all three, same rule as --button-fg.
	seriesFg := "#000000"
	worstBlack := math.Inf(1)
	worstWhite := math.Inf(1)
	for _, key := range []string{"tests", "build", "git"} {
		worstBlack = math.Min(worstBlack, contrast("#000000", seriesChartColors[key]))
		worstWhite = math.Min(worstWhite, contrast("#ffffff", seriesChartColors[key]))
	}
	if worstWhite > worstBlack {
		seriesFg = "#ffffff"
	}

	colorScheme := "dark"
	if s.Variant == "light" {
		colorScheme = "light"
	}

	tokens := map[string]string{
		"color-scheme": colorScheme,

		"--terminal-black":          scree,
		"--terminal-red":            text(red),
		"--terminal-green":          text(green),
		"--terminal-yellow":         text(yellow),
		"--terminal-blue":           text(blue),
		"--terminal-magenta":        text(purple),
		"--terminal-cyan":           text(cyan),
		"--terminal-white":          chalk,
		"--terminal-bright-black":   mutedText,
		"--terminal-bright-red":     text(lighten(red, 0.12)),
		"--terminal-bright-green":   text(lighten(green, 0.12)),
		"--terminal-bright-yellow":  text(lighten(yellow, 0.12)),
		"--terminal-bright-blue":    text(lighten(blue, 0.12)),
		"--terminal-bright-magenta": text(lighten(purple, 0.12)),
		"--terminal-bright-cyan":    text(lighten(cyan, 0.12)),
		"--terminal-bright-white":   lighten(chalk, 0.08),

		// Base16's own UI-styling convention for code highlighting (base16/styling.md): keyword =
		// purple/magenta, string = green, number = orange, comment = the muted slot, title/class =
		// blue, type = cyan, variable = orange, builtin = cyan.
		"--code-keyword":  text(purple),
		"--code-string":   text(green),
		"--code-number":   text(orange),
		"--code-comment":  text(s.slot("base03")),
		"--code-title":    text(blue),
		"--code-type":     text(cyan),
		"--code-variable": text(orange),
		"--code-builtin":  text(cyan),

		"--basalt":     basalt,
		"--scree":      scree,
		"--scree-2":    scree2,
		"--stone":      stone,
		"--fog":        fog,
		"--fog-dim":    fogDim,
		"--chalk":      chalk,
		"--fill-muted": fillMuted,

		"--blaze":      blaze,
		"--blaze-soft": blaze + "33",
		"--lichen":     lichen,
		"--amber":      amber,
		"--danger":     danger,
		"--batch":      batch,
		"--button-fg":  buttonFg,

		"--selected-border": batch,
		"--selected-bg":     batch + "29",
		"--selected-text":   textOverFill(blue),

		"--diff-add": text(lichen),
		"--diff-del": text(danger),

		"--primary-white-fg":  buttonFg,
		"--runner-primary-fg": buttonFg,
		"--chat-primary-fg":   buttonFg,
		"--knob-fg":           buttonFg,
		"--board-diff-add":    text(lichen),
		"--board-diff-del":    text(danger),

		"--si-series-1": blaze,
		"--si-series-2": amber,
		"--si-series-3": lichen,
		"--series-fg":   seriesFg,

		"--chart-tests":   seriesChartColors["tests"],
		"--chart-build":   seriesChartColors["build"],
		"--chart-git":     seriesChartColors["git"],
		"--chart-read":    seriesChartColors["read"],
		"--chart-edit":    seriesChartColors["edit"],
		"--chart-other":   seriesChartColors["other"],
		"--chart-model":   seriesChartColors["model"],
		"--chart-waiting": seriesChartColors["waiting"],
		"--chart-idle":    seriesChartColors["idle"],
	}
	return tokens
}

// blend mixes a toward b by t in [0,1] (0 = a, 1 = b) in sRGB channel space, rounding each channel
// (not truncating) to match the alpha-composite math the landing contrast test itself uses.
func blend(a, b string, t float64) string {
	ar, ag, ab := parseHex(a)
	br, bg, bb := parseHex(b)
	mix := func(x, y int) int { return int(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return formatHex(mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// lighten nudges a color's HSL lightness by delta (negative darkens), preserving hue/saturation.
func lighten(hex string, delta float64) string {
	r, g, b := parseHex(hex)
	h, s, l := rgbToHSL(r, g, b)
	l += delta
	if l < 0 {
		l = 0
	}
	if l > 1 {
		l = 1
	}
	nr, ng, nb := hslToRGB(h, s, l)
	return formatHex(nr, ng, nb)
}
