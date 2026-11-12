package main

import (
	"math"
	"sort"
)

// sessionColorNames is the fixed key order the "Session identity palette" section of
// brand/tokens.css uses for its twelve stable workspace swatches.
var sessionColorNames = []string{"blue", "orange", "purple", "teal", "silver", "indigo", "gold",
	"coral", "cyan", "violet", "lime", "rose"}

// testOrder matches SESSION_COLORS (runner/frontend/src/lib/sessionColors.ts, minus "None") — the
// fixed order SessionColor.browser.test.tsx walks when it checks that keyboard/visual neighbors
// (one and four steps apart in that list) stay separated under every color-vision simulation.
var testOrder = []string{"gold", "rose", "blue", "lime", "purple", "orange", "silver", "violet",
	"teal", "indigo", "coral", "cyan"}

type colorPair struct {
	a, b string
	min  float64
}

// sessionColorPairs is every pair SessionColor.browser.test.tsx actually grades for separation:
// the four scheme-derived swatches against each other (every bundled and default theme's bar,
// ≥5 ΔE76 under each color-vision simulation — the colorblind/high-contrast presets' stronger
// ≥15 bar lives in their own hand-tuned block, not here), plus the step-1 and step-4 neighbors in
// testOrder (≥10, every theme). A pair reachable both ways keeps the stricter of the two bars.
func sessionColorPairs() []colorPair {
	bars := map[string]float64{}
	key := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "|" + b
	}
	add := func(a, b string, min float64) {
		k := key(a, b)
		if cur, ok := bars[k]; !ok || min > cur {
			bars[k] = min
		}
	}
	base := []string{"blue", "orange", "purple", "teal"}
	for i := 0; i < len(base); i++ {
		for j := i + 1; j < len(base); j++ {
			add(base[i], base[j], 5)
		}
	}
	n := len(testOrder)
	for i := 0; i < n; i++ {
		for _, step := range []int{1, 4} {
			add(testOrder[i], testOrder[(i+step)%n], 10)
		}
	}
	return splitPairKeys(bars)
}

// splitPairKeys turns the "a|b" -> min map sessionColorPairs builds back into colorPair values.
func splitPairKeys(bars map[string]float64) []colorPair {
	pairs := make([]colorPair, 0, len(bars))
	for k, min := range bars {
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				pairs = append(pairs, colorPair{a: k[:i], b: k[i+1:], min: min})
				break
			}
		}
	}
	return pairs
}

// sessionColors derives the twelve named session swatches from the scheme's own accent hues
// instead of reusing the hand-tuned defaults (brand/tokens.css's base :root block), which were
// tuned against that one palette's surfaces and are not guaranteed to clear contrast against an
// arbitrary bundled scheme's own. The first four reuse the scheme's actual blue/orange/purple/
// cyan("teal") accents; the rest are hue-rotated from blue in fixed 30° steps at the same
// saturation/lightness, giving eight more named, distinct swatches. Every one is nudged to at
// least 4.5:1 against every surface (including the "waiting" sweep's brightest blend of --blaze
// over that surface — BLG-474 review round 5) — the accessibility-preset bar every bundled-scheme
// role now shares — and then, where a pair would still be indistinguishable to a simulated
// color-blind viewer (BLG-474 review round 6: Nord's blue/purple measured ΔE 3.96 under
// protanopia), repaired until every pair SessionColor.browser.test.tsx checks clears its bar.
func sessionColors(s Scheme) map[string]string {
	basalt, scree, scree2 := s.slot("base00"), s.slot("base01"), s.slot("base02")
	surfaces := []string{basalt, scree, scree2}
	blaze := mapTokens(s)["--blaze"]
	allSurfaces := append([]string{}, surfaces...)
	for _, surf := range surfaces {
		allSurfaces = append(allSurfaces, blend(surf, blaze, 0.22))
	}
	mark := func(hex string) string { return ensureContrast(hex, allSurfaces, 4.5) }

	blue := s.slot("base0d")
	r, g, b := parseHex(blue)
	h, sat, l := rgbToHSL(r, g, b)

	colors := map[string]string{
		"blue":   mark(blue),
		"orange": mark(s.slot("base09")),
		"purple": mark(s.slot("base0e")),
		"teal":   mark(s.slot("base0c")),
	}
	rotated := []string{"silver", "indigo", "gold", "coral", "cyan", "violet", "lime", "rose"}
	for i, name := range rotated {
		step := float64(i+1) * 30
		nr, ng, nb := hslToRGB(mod360(h+step), sat, l)
		colors[name] = mark(formatHex(nr, ng, nb))
	}

	separateSessionColors(colors, allSurfaces)

	out := make(map[string]string, len(sessionColorNames))
	for _, name := range sessionColorNames {
		out["--session-color-"+name] = colors[name]
	}
	return out
}

// separateSessionColors repairs, in place, any pair of session swatches that fails
// SessionColor.browser.test.tsx's color-vision-deficiency separation bar (sessionColorPairs), by
// nudging each offending swatch's lightness — and, only if that alone can't clear the bar, its
// hue within a modest range of where it started, so it stays recognizably the same color family —
// across a few rounds of coordinate ascent. It never gives up the 4.5:1 contrast every swatch
// already holds against allSurfaces.
func separateSessionColors(colors map[string]string, allSurfaces []string) {
	constraints := map[string][]colorPair{}
	for _, p := range sessionColorPairs() {
		constraints[p.a] = append(constraints[p.a], p)
		constraints[p.b] = append(constraints[p.b], p)
	}
	type anchor struct{ hue, sat float64 }
	anchors := map[string]anchor{}
	// sessionColorNames gives a fixed processing order — map iteration order is randomized in Go,
	// and this is a coordinate-ascent repair, so an unordered walk would make the generated output
	// nondeterministic across runs (TestGenerateTokensCSSIsDeterministic).
	names := append([]string{}, sessionColorNames...)
	for _, name := range names {
		r, g, b := parseHex(colors[name])
		hh, ss, _ := rgbToHSL(r, g, b)
		anchors[name] = anchor{hh, ss}
	}

	score := func(name, hex string) float64 {
		worst := math.Inf(1)
		for _, p := range constraints[name] {
			other := p.a
			if other == name {
				other = p.b
			}
			if d := minDeltaE(hex, colors[other]) - p.min; d < worst {
				worst = d
			}
		}
		return worst
	}

	tryCandidate := func(name, candidate string, bestScore *float64, bestHex *string) {
		if worstContrast(candidate, allSurfaces) < 4.5-0.01 {
			return
		}
		if sc := score(name, candidate); sc > *bestScore {
			*bestScore, *bestHex = sc, candidate
		}
	}

	for round := 0; round < 8; round++ {
		improved := false
		for _, name := range names {
			if len(constraints[name]) == 0 {
				continue
			}
			bestHex := colors[name]
			bestScore := score(name, bestHex)
			if bestScore >= 0 {
				continue
			}
			a := anchors[name]
			// Phase 1: lightness only, hue untouched — preserves the swatch's hue family.
			for lt := 0.04; lt <= 0.96; lt += 0.01 {
				nr, ng, nb := hslToRGB(a.hue, a.sat, lt)
				tryCandidate(name, formatHex(nr, ng, nb), &bestScore, &bestHex)
			}
			// Phase 2: lightness alone couldn't clear the bar — allow a wider hue nudge, and more
			// saturation (a muted base16 accent gives a vision simulation less a*/b* room to keep
			// hues apart), too.
			if bestScore < 0 {
				for _, sat := range []float64{a.sat, math.Min(1, a.sat+0.3), 1} {
					for dh := -60.0; dh <= 60.0; dh += 3 {
						hh := mod360(a.hue + dh)
						for lt := 0.04; lt <= 0.96; lt += 0.02 {
							nr, ng, nb := hslToRGB(hh, sat, lt)
							tryCandidate(name, formatHex(nr, ng, nb), &bestScore, &bestHex)
						}
					}
				}
			}
			if bestHex != colors[name] {
				colors[name] = bestHex
				improved = true
			}
		}
		if !improved {
			break
		}
	}
	// Coordinate ascent can plateau at a local optimum where one node's fix regresses another's
	// margin. A second outside-in sweep — worst violation first each round, every still-violating
	// name tried (not just the single worst, so one stubborn pair never stalls the rest), each
	// still re-scored against the latest state of every other color — resolves the handful of
	// remaining near-misses that a fixed left-to-right walk can leave behind.
	for round := 0; round < 20; round++ {
		type violation struct {
			name  string
			score float64
		}
		var worst []violation
		for _, name := range names {
			if len(constraints[name]) == 0 {
				continue
			}
			if sc := score(name, colors[name]); sc < 0 {
				worst = append(worst, violation{name, sc})
			}
		}
		if len(worst) == 0 {
			break
		}
		sort.Slice(worst, func(i, j int) bool { return worst[i].score < worst[j].score })
		improved := false
		for _, v := range worst {
			a := anchors[v.name]
			bestHex, bestScore := colors[v.name], score(v.name, colors[v.name])
			for _, sat := range []float64{a.sat, math.Min(1, a.sat+0.3), 1} {
				for dh := -90.0; dh <= 90.0; dh += 2 {
					hh := mod360(a.hue + dh)
					for lt := 0.02; lt <= 0.98; lt += 0.01 {
						nr, ng, nb := hslToRGB(hh, sat, lt)
						tryCandidate(v.name, formatHex(nr, ng, nb), &bestScore, &bestHex)
					}
				}
			}
			if bestHex != colors[v.name] {
				colors[v.name] = bestHex
				improved = true
			}
		}
		if !improved {
			break
		}
	}
}

func mod360(h float64) float64 {
	for h < 0 {
		h += 360
	}
	for h >= 360 {
		h -= 360
	}
	return h
}
