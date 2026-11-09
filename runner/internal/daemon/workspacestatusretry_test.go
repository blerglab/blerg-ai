package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataTreeMaintenanceRetry(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "persistent"}[persistent], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			attempts := 0
			budget := &metadataBudget{entries: 200000}
			_, safe := metadataTreeWithOpen(t.Context(), root, ".git", nil, true, budget, func(root *os.Root, name string) (*os.File, error) {
				if name == ".git" {
					attempts++
				}
				f, err := workspaceOpenDirectory(root, name)
				if err == nil && name == ".git/objects" && (persistent || attempts == 1) {
					// A writer changes the parent after its fingerprint was captured.
					done := make(chan error, 1)
					go func() {
						done <- os.WriteFile(filepath.Join(dir, ".git", "maintenance"), []byte("busy"), 0600)
					}()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(filepath.Join(dir, ".git", "maintenance")); err != nil {
						t.Fatal(err)
					}
					// Force a distinct timestamp even on filesystems with coarse clocks.
					stamp := time.Unix(1000000000+int64(attempts), 0)
					if err := os.Chtimes(filepath.Join(dir, ".git"), stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
				return f, err
			})
			if safe == persistent || attempts != 2 {
				t.Fatalf("safe=%v attempts=%d reason=%s", safe, attempts, budget.reason)
			}
			if persistent && budget.reason != "metadata_changed" {
				t.Fatalf("reason=%s", budget.reason)
			}
		})
	}
}

func TestMetadataTreeRefusalDoesNotRetry(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	attempts := 0
	budget := &metadataBudget{entries: 200000}
	_, safe := metadataTreeWithOpen(t.Context(), root, ".", nil, true, budget, func(*os.Root, string) (*os.File, error) {
		attempts++
		return nil, os.ErrPermission
	})
	if safe || attempts != 1 || budget.reason != "metadata_read_refused" {
		t.Fatalf("safe=%v attempts=%d reason=%s", safe, attempts, budget.reason)
	}
}
