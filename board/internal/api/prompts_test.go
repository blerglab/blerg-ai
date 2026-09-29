package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/prompts"
)

// New wires up the embedded prompt defaults with no DB or auth backend
// touched — API.New only stores those, it doesn't dial them.
func TestNewLoadsDefaultPrompts(t *testing.T) {
	a := api.New(nil, nil, nil)
	if a.Prompts == nil {
		t.Fatal("API.New: Prompts is nil, want embedded defaults loaded")
	}
}

func TestLoadPromptsAppliesOverrideDir(t *testing.T) {
	a := api.New(nil, nil, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("custom reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.LoadPrompts(dir); err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	out, err := a.Prompts.Render("reviewer", prompts.Data{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "custom reviewer" {
		t.Errorf("override not applied: got %q", out)
	}
}

func TestLoadPromptsRefusesBadOverride(t *testing.T) {
	a := api.New(nil, nil, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.md"), []byte("{{.NotAField}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := a.Prompts
	if err := a.LoadPrompts(dir); err == nil {
		t.Fatal("LoadPrompts with an unknown field reference: expected error, got nil")
	}
	if a.Prompts != before {
		t.Fatal("LoadPrompts: Prompts was replaced despite a load error — server would boot with a broken brief")
	}
}
