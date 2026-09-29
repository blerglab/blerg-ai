package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allRoles = []string{"worker", "reviewer", "specreview", "discuss", "board", "bootstrap"}

func TestLoadDefaults(t *testing.T) {
	store, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	data := Data{
		Card:  CardInfo{Number: 42, Title: "fix the thing", Body: "it's broken"},
		Board: BoardInfo{Name: "blerg-board"},
		PR:    "https://github.com/example/example/pull/1",
		Extra: "please hurry",
	}
	for _, role := range allRoles {
		out, err := store.Render(role, data)
		if err != nil {
			t.Fatalf("Render(%q): %v", role, err)
		}
		if !strings.Contains(out, "blerg-board") {
			t.Errorf("Render(%q): expected board name in output, got %q", role, out)
		}
	}
}

func TestRenderUnknownRole(t *testing.T) {
	store, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := store.Render("nonexistent", Data{}); err == nil {
		t.Fatal("Render(\"nonexistent\"): expected error, got nil")
	}
}

func TestLoadOverrideDirReplacesRole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("custom reviewer brief for {{.Board.Name}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(dir): %v", err)
	}
	out, err := store.Render("reviewer", Data{Board: BoardInfo{Name: "blerg-board"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "custom reviewer brief for blerg-board" {
		t.Errorf("override not applied: got %q", out)
	}
	// every other role should still fall back to the embedded default
	out, err = store.Render("worker", Data{Board: BoardInfo{Name: "blerg-board"}, Card: CardInfo{Number: 1}})
	if err != nil {
		t.Fatalf("Render(worker): %v", err)
	}
	if strings.Contains(out, "custom reviewer brief") {
		t.Errorf("override for reviewer leaked into worker: %q", out)
	}
}

func TestLoadOverrideParseErrorRefusesToBoot(t *testing.T) {
	dir := t.TempDir()
	// unbalanced action — invalid template syntax
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("broken {{.Card.Title"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load(dir) with a malformed override: expected error, got nil")
	}
}

func TestLoadOverrideUnknownFieldRefusesToBoot(t *testing.T) {
	dir := t.TempDir()
	// valid template syntax, but references a field that doesn't exist on Data
	if err := os.WriteFile(filepath.Join(dir, "worker.md"), []byte("{{.NotAField}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load(dir) with an unknown field reference: expected error, got nil")
	}
}

func TestLoadOverrideDirMissingFileFallsBackToDefault(t *testing.T) {
	dir := t.TempDir() // no files placed
	store, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(dir): %v", err)
	}
	if _, err := store.Render("worker", Data{Board: BoardInfo{Name: "blerg-board"}, Card: CardInfo{Number: 1}}); err != nil {
		t.Fatalf("Render(worker): %v", err)
	}
}
