package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseHex reads a "#rrggbb" string into its three 0-255 channels.
func parseHex(hex string) (r, g, b int) {
	h := strings.TrimPrefix(strings.ToLower(hex), "#")
	v, _ := strconv.ParseUint(h, 16, 32)
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

// formatHex renders three 0-255 channels as a lowercase "#rrggbb" string, clamping out-of-range
// values so a lightness nudge can never produce an invalid custom property value.
func formatHex(r, g, b int) string {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	return fmt.Sprintf("#%02x%02x%02x", clamp(r), clamp(g), clamp(b))
}

func linearize(c float64) float64 {
	v := c / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// luminance is the WCAG relative luminance of a "#rrggbb" color.
func luminance(hex string) float64 {
	r, g, b := parseHex(hex)
	return 0.2126*linearize(float64(r)) + 0.7152*linearize(float64(g)) + 0.0722*linearize(float64(b))
}

// contrast is the WCAG contrast ratio between two "#rrggbb" colors.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// worstContrast is the smallest ratio between hex and any of surfaces — the binding constraint
// when a color must read against several backgrounds (an ink shown on basalt, scree and scree-2).
func worstContrast(hex string, surfaces []string) float64 {
	worst := math.Inf(1)
	for _, s := range surfaces {
		if c := contrast(hex, s); c < worst {
			worst = c
		}
	}
	return worst
}

// rgbToHSL converts 0-255 channels to hue in [0,360), saturation and lightness in [0,1].
func rgbToHSL(r, g, b int) (h, s, l float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	l = (max + min) / 2
	d := max - min
	if d == 0 {
		return 0, 0, l
	}
	if l > 0.5 {
		s = d / (2 - max - min)
	} else {
		s = d / (max + min)
	}
	switch max {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, l
}

func hslToRGB(h, s, l float64) (r, g, b int) {
	if s == 0 {
		v := int(math.Round(l * 255))
		return v, v, v
	}
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var rf, gf, bf float64
	switch {
	case h < 60:
		rf, gf, bf = c, x, 0
	case h < 120:
		rf, gf, bf = x, c, 0
	case h < 180:
		rf, gf, bf = 0, c, x
	case h < 240:
		rf, gf, bf = 0, x, c
	case h < 300:
		rf, gf, bf = x, 0, c
	default:
		rf, gf, bf = c, 0, x
	}
	return int(math.Round((rf + m) * 255)), int(math.Round((gf + m) * 255)), int(math.Round((bf + m) * 255))
}

// ensureContrast returns hex unchanged if it already clears minimum against every surface.
// Otherwise it nudges HSL lightness — toward black or toward white, whichever the starting
// lightness is already closer to, so a dark-on-dark ink goes darker only if that's a dead end —
// in fixed steps until the ratio against the worst surface clears the target, preserving hue and
// saturation. If lightening and darkening both plateau (an impossible target), it returns the
// best of the two directions reached.
func ensureContrast(hex string, surfaces []string, min float64) string {
	if worstContrast(hex, surfaces) >= min {
		return hex
	}
	r, g, b := parseHex(hex)
	h, s, l := rgbToHSL(r, g, b)
	// Pick the average surface luminance to decide whether this ink should go darker (surfaces
	// read light) or lighter (surfaces read dark).
	avgSurfaceLum := 0.0
	for _, surf := range surfaces {
		avgSurfaceLum += luminance(surf)
	}
	avgSurfaceLum /= float64(len(surfaces))
	selfLum := luminance(hex)
	goLighter := selfLum <= avgSurfaceLum
	step := 0.02
	best := hex
	bestRatio := worstContrast(hex, surfaces)
	l2 := l
	for i := 0; i < 50; i++ {
		if goLighter {
			l2 += step
			if l2 > 1 {
				break
			}
		} else {
			l2 -= step
			if l2 < 0 {
				break
			}
		}
		nr, ng, nb := hslToRGB(h, s, l2)
		candidate := formatHex(nr, ng, nb)
		ratio := worstContrast(candidate, surfaces)
		if ratio > bestRatio {
			bestRatio = ratio
			best = candidate
		}
		if ratio >= min {
			return candidate
		}
	}
	// One direction plateaued short of the target (an unusually weak surface set); try the other.
	l2 = l
	for i := 0; i < 50; i++ {
		if goLighter {
			l2 -= step
			if l2 < 0 {
				break
			}
		} else {
			l2 += step
			if l2 > 1 {
				break
			}
		}
		nr, ng, nb := hslToRGB(h, s, l2)
		candidate := formatHex(nr, ng, nb)
		ratio := worstContrast(candidate, surfaces)
		if ratio > bestRatio {
			bestRatio = ratio
			best = candidate
		}
		if ratio >= min {
			return candidate
		}
	}
	return best
}

// hueOf returns a color's hue in [0,360).
func hueOf(hex string) float64 {
	r, g, b := parseHex(hex)
	h, _, _ := rgbToHSL(r, g, b)
	return h
}

// hueDistance is the circular distance in degrees between two hues (never more than 180).
func hueDistance(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// pickByHue returns whichever candidate's hue is closest to target — a scheme's own role
// assignment (BLG-474 review: Tokyo Night's base16 port has no true red in base08, its "red"
// slot, but does have one, #F7768E, misassigned to base0F) is not always trustworthy; searching
// every accent slot by actual hue finds the color that reads as the intended role regardless of
// which slot the porter put it in.
func pickByHue(candidates []string, target float64) string {
	best := candidates[0]
	bestDist := math.Inf(1)
	for _, c := range candidates {
		if d := hueDistance(hueOf(c), target); d < bestDist {
			bestDist = d
			best = c
		}
	}
	return best
}

// pickReadableFg returns whichever of pure black or white has higher contrast against bg —
// the same choice --button-fg makes in the accessibility presets (brand/tokens.css), generalized
// here so a bundled scheme's own accent always has a legible foreground painted on it.
func pickReadableFg(bg string) string {
	if contrast("#000000", bg) >= contrast("#ffffff", bg) {
		return "#000000"
	}
	return "#ffffff"
}
