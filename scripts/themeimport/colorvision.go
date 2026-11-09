package main

import "math"

// visionMatrices are the same Machado full-severity color-vision-deficiency simulation matrices,
// and visionDeltaEMin the same ΔE76 floor, that
// runner/frontend/src/test/colorVision.ts uses to grade session-color separation. Keeping both
// sides in sync is the whole point of porting them here rather than approximating.
var visionMatrices = map[string][9]float64{
	"plain":        {1, 0, 0, 0, 1, 0, 0, 0, 1},
	"protanopia":   {.152286, 1.052583, -.204868, .114503, .786281, .099216, -.003882, -.048116, 1.051998},
	"deuteranopia": {.367322, .860646, -.227968, .280085, .672501, .047413, -.01182, .04294, .968881},
	"tritanopia":   {1.255528, -.076749, -.178779, -.078411, .930809, .147602, .004733, .691367, .3039},
}

// simulatedLab runs hex through a color-vision-deficiency simulation matrix and converts the
// result to CIE L*a*b*, mirroring colorVision.ts's simulatedLab exactly (same D65 XYZ matrix and
// clamp-then-cube-root path) so a ΔE comparison here predicts what that browser test measures.
func simulatedLab(hex string, matrix [9]float64) [3]float64 {
	r, g, b := parseHex(hex)
	rgb := [3]float64{linearize(float64(r)), linearize(float64(g)), linearize(float64(b))}
	clamp01 := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	sim := [3]float64{
		clamp01(matrix[0]*rgb[0] + matrix[1]*rgb[1] + matrix[2]*rgb[2]),
		clamp01(matrix[3]*rgb[0] + matrix[4]*rgb[1] + matrix[5]*rgb[2]),
		clamp01(matrix[6]*rgb[0] + matrix[7]*rgb[1] + matrix[8]*rgb[2]),
	}
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	x := f((.4124564*sim[0] + .3575761*sim[1] + .1804375*sim[2]) / .95047)
	y := f(.2126729*sim[0] + .7151522*sim[1] + .072175*sim[2])
	z := f((.0193339*sim[0] + .119192*sim[1] + .9503041*sim[2]) / 1.08883)
	return [3]float64{116*y - 16, 500 * (x - y), 200 * (y - z)}
}

// deltaE is the Euclidean (ΔE76) distance between two Lab triples.
func deltaE(a, b [3]float64) float64 {
	dl, da, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return math.Sqrt(dl*dl + da*da + db*db)
}

// minDeltaE returns the smallest ΔE between hex a and b across every vision simulation in
// visionMatrices — the binding constraint, since a pair only separates if every simulated viewer
// can tell them apart.
func minDeltaE(a, b string) float64 {
	worst := math.Inf(1)
	for _, matrix := range visionMatrices {
		if d := deltaE(simulatedLab(a, matrix), simulatedLab(b, matrix)); d < worst {
			worst = d
		}
	}
	return worst
}
