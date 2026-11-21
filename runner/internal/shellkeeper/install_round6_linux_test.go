//go:build linux

package shellkeeper

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFreshDaemonInstallCannotOverwriteARacingDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "daemon")
	err := installImage(destination, func() {
		if e := os.WriteFile(destination, []byte("racing daemon"), 0700); e != nil {
			t.Fatal(e)
		}
	}, false)
	if err == nil {
		t.Fatal("fresh-only install replaced a racing destination")
	}
	data, e := os.ReadFile(destination)
	if e != nil || string(data) != "racing daemon" {
		t.Fatal("racing destination changed", e)
	}
}

func TestUnsupportedPrepareReturnsStableNonRetryableCause(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := Initialize(dir); err != nil {
		t.Fatal(err)
	}
	server, err := ListenWithRuntime(t.Context(), dir, RuntimeHooks{Prepare: func() error { return ErrUnsupported }})
	if server != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported runtime lost its service exit cause", err)
	}
	server, err = Listen(t.Context(), dir)
	if err != nil {
		t.Fatal("unsupported prepare leaked keeper.lock", err)
	}
	_ = server.Close()
}
