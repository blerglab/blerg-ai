package main

import "testing"

func TestContrastKnownPairs(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"#ffffff", "#000000", 21},
		{"#000000", "#ffffff", 21},
		{"#ffffff", "#ffffff", 1},
	}
	for _, c := range cases {
		if got := contrast(c.a, c.b); abs(got-c.want) > 0.05 {
			t.Errorf("contrast(%s,%s) = %.3f, want %.3f", c.a, c.b, got, c.want)
		}
	}
}

func TestParseHexRoundTrip(t *testing.T) {
	for _, hex := range []string{"#1e1e2e", "#ffffff", "#000000", "#89b4fa"} {
		r, g, b := parseHex(hex)
		if got := formatHex(r, g, b); got != hex {
			t.Errorf("round-trip %s -> %s", hex, got)
		}
	}
}

func TestEnsureContrastMeetsMinimumAgainstWorstSurface(t *testing.T) {
	surfaces := []string{"#1e1e2e", "#181825", "#313244"}
	// A muted, low-contrast ink that fails 4.5:1 against all three surfaces.
	fixed := ensureContrast("#45475a", surfaces, 4.5)
	for _, s := range surfaces {
		if c := contrast(fixed, s); c < 4.5-0.01 {
			t.Errorf("ensureContrast result %s on %s: %.2f < 4.5", fixed, s, c)
		}
	}
}

func TestEnsureContrastPreservesHueFamily(t *testing.T) {
	// A saturated accent that already passes should be returned unchanged.
	surfaces := []string{"#1e1e2e"}
	in := "#f38ba8"
	if got := ensureContrast(in, surfaces, 3); got != in {
		t.Errorf("already-passing color was changed: %s -> %s", in, got)
	}
}

func TestEnsureContrastNeverExceedsValidHex(t *testing.T) {
	// A near-white ink against a near-white surface: pushed to black rather than overflowing.
	fixed := ensureContrast("#f8f8f2", []string{"#fafafa"}, 4.5)
	r, g, b := parseHex(fixed)
	if r > 255 || g > 255 || b > 255 {
		t.Fatalf("invalid channel values in %s", fixed)
	}
	if contrast(fixed, "#fafafa") < 4.5-0.01 {
		t.Errorf("still fails contrast: %s", fixed)
	}
}

func TestPickByHuePrefersClosestAmongCandidates(t *testing.T) {
	// Tokyo Night's base16 port has no true red (BLG-474 review): base08/09 are pale
	// lavender-grays, but base0F (#F7768E) is an actual red/pink. pickByHue must find it.
	candidates := []string{"#C0CAF5", "#A9B1D6", "#0DB9D7", "#9ECE6A", "#B4F9F8", "#2AC3DE", "#BB9AF7", "#F7768E"}
	if got := pickByHue(candidates, 0); got != "#F7768E" {
		t.Errorf("pickByHue(red) = %s, want #F7768E", got)
	}
}

func TestPickByHueWrapsAroundZero(t *testing.T) {
	// A hue of 350 and a target of 10 are 20 degrees apart, not 340.
	candidates := []string{"#ff1a00", "#00ff00"} // hue ~5, hue 120
	if got := pickByHue(candidates, 350); got != "#ff1a00" {
		t.Errorf("pickByHue(350) = %s, want #ff1a00", got)
	}
}

func TestPickReadableFgChoosesHigherContrastExtreme(t *testing.T) {
	if got := pickReadableFg("#1a1a1a"); got != "#ffffff" {
		t.Errorf("pickReadableFg(dark) = %s, want white", got)
	}
	if got := pickReadableFg("#fafafa"); got != "#000000" {
		t.Errorf("pickReadableFg(light) = %s, want black", got)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
