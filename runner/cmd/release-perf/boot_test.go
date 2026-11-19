package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootEvidenceRequiresThreeTrials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "boot.json")
	for _, text := range []string{`{}`, `{"status":"complete","boot":{"samplesMs":[1,2]}}`, `{"status":"complete","boot":{"samplesMs":[1,2,0]}}`} {
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if validateBootOutput(p) == nil {
			t.Fatal("accepted incomplete boot samples")
		}
	}
	if err := os.WriteFile(p, []byte(`{"status":"complete","boot":{"samplesMs":[10,20,30]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateBootOutput(p); err != nil {
		t.Fatal(err)
	}
}
func TestHistoricalSourceRequiresAllServiceModules(t *testing.T) {
	if got, err := selectBuildRoot("fixture", ""); err != nil || got != "fixture" {
		t.Fatal(got, err)
	}
	d := t.TempDir()
	if _, err := selectBuildRoot("fixture", d); err == nil {
		t.Fatal("accepted absent modules")
	}
	for _, module := range []string{"core", "board", "runner"} {
		p := filepath.Join(d, module)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "go.mod"), []byte("module fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := selectBuildRoot("fixture", d); err != nil || got != d {
		t.Fatal(got, err)
	}
}
