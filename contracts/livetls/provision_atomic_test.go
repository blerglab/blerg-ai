package livetls

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionRecoversInterruptedStaging(t *testing.T) {
	root := t.TempDir()
	abandoned := filepath.Join(root, "authority", ".provision-interrupted")
	if err := os.MkdirAll(abandoned, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "key.pem"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Provision(root); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(root, "authority", "bundle")
	for _, name := range []string{"identity.json", "key.pem", "ca.pem"} {
		if _, err := os.Stat(filepath.Join(authority, name)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(authority, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Provision(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(authority, "ca.pem"))
	if err != nil || string(before) != string(after) {
		t.Fatal("authority changed on renewal")
	}
	if _, err := Load(filepath.Join(root, "core"), "core"); err != nil {
		t.Fatal(err)
	}
}
