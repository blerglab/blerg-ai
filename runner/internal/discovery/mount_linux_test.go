//go:build linux

package discovery

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryOpenRefusesMountCrossing(t *testing.T) {
	// /proc is an existing kernel mount. No mount or container is created.
	root, err := openConfiguredRoot("/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("proc mount unavailable")
	}
	child, err := openBeneath(root, "proc", directoryFlags)
	if err == nil {
		_ = child.Close()
		t.Fatal("openat2 crossed a mount boundary")
	}
}

func TestDiscoveryNeverBlocksOnMetadataFIFO(t *testing.T) {
	root := fixtureRoot(t)
	repo := fixtureRepo(t, root, "repo", false)
	config := filepath.Join(repo, ".git", "config")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(config, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req := request("list")
	req.Path = "."
	done := make(chan protocol.WorkspaceDiscoveryResult, 1)
	go func() { done <- s.Handle(ctx, req) }()
	select {
	case res := <-done:
		if res.OK {
			for _, e := range res.Page.Entries {
				if e.Kind == "repository" {
					t.Fatal("FIFO metadata accepted")
				}
			}
		}
	case <-ctx.Done():
		t.Fatal("FIFO blocked discovery")
	}
}
