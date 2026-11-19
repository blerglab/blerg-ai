package main

import "testing"

var sessionKeys = []string{"--session-color-blue", "--session-color-orange", "--session-color-purple",
	"--session-color-teal", "--session-color-silver", "--session-color-indigo", "--session-color-gold",
	"--session-color-coral", "--session-color-cyan", "--session-color-violet", "--session-color-lime",
	"--session-color-rose"}

func TestSessionColorsDeclaresEveryKey(t *testing.T) {
	colors := sessionColors(fixtureScheme(t))
	for _, key := range sessionKeys {
		if colors[key] == "" {
			t.Errorf("missing %s", key)
		}
	}
}

func TestSessionColorsClearMinimumAgainstEverySurface(t *testing.T) {
	sch := fixtureScheme(t)
	tokens := mapTokens(sch)
	surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
	colors := sessionColors(sch)
	for _, key := range sessionKeys {
		if c := worstContrast(colors[key], surfaces); c < 4.5-0.01 {
			t.Errorf("%s = %s fails 4.5:1 against a surface: %.2f", key, colors[key], c)
		}
	}
}

func TestBundledSchemesSessionColorsClearMinimum(t *testing.T) {
	for _, b := range BundledThemes {
		sch, err := loadBundled(b)
		if err != nil {
			t.Fatalf("%s: %v", b.ID, err)
		}
		tokens := mapTokens(sch)
		surfaces := []string{tokens["--basalt"], tokens["--scree"], tokens["--scree-2"]}
		colors := sessionColors(sch)
		for _, key := range sessionKeys {
			if c := worstContrast(colors[key], surfaces); c < 4.5-0.01 {
				t.Errorf("%s: %s = %s fails 4.5:1 against a surface: %.2f", b.ID, key, colors[key], c)
			}
		}
	}
}
