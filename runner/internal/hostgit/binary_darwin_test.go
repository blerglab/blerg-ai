package hostgit

import (
	"os"
	"testing"
	"time"
)

func TestDarwinResolvedGitBypassesAppleShim(t *testing.T) {
	if err := Initialize(); err != nil {
		t.Fatalf("CLT/Xcode developer Git required for native acceptance: %v", err)
	}
	path, err := Binary()
	if err != nil || path == "/usr/bin/git" {
		t.Fatalf("Apple shim remained in hardened hot path: %q %v", path, err)
	}
	if err := validateDarwinGitTree(path, os.Lstat, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if again, err := Binary(); err != nil || again != path {
		t.Fatalf("cached Git changed with PATH: %q %v", again, err)
	}
	cmd, err := commandForPlatform(Options{}, "darwin", "--version")
	if err != nil || cmd.Path != path {
		t.Fatalf("hardened command lost resolved Git: %v %v", cmd, err)
	}
	if _, err := Output(t.Context(), Options{MemoryLimit: true, LimitKB: 256 << 10, Timeout: 2 * time.Second}, "--version"); err != nil {
		t.Fatal(err)
	}
	t.Logf("validated real Git=%q (Apple shim bypassed)", path)
}
