package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopCredentialPrivateFileAndNoLinks(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "desktop-credential.json")
	if c, err := LoadDesktopCredential(root); err != nil || c != nil {
		t.Fatal("missing credential should permit explicit legacy configuration")
	}
	raw, _ := json.Marshal(DesktopCredential{DesktopID: "00000000-0000-4000-8000-000000000174", Token: "bdt_" + strings.Repeat("a", 64)})
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadDesktopCredential(root); err != nil || c.DesktopID == "" {
		t.Fatal("valid credential refused", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDesktopCredential(root); err == nil {
		t.Fatal("shared credential accepted")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.WriteFile(other, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, name); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDesktopCredential(root); err == nil {
		t.Fatal("linked credential accepted")
	}
}
