package hostgit

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This must run natively: Linux /dev/fd symlinks would hide the old Darwin failure.
func TestDarwinPinnedDirectory(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var options []Options
	var expected []string
	for range 2 {
		dir := repoWithHistory(t)
		held, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held.Close() })
		moved := dir + "-held"
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(moved) })
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		physical, err := filepath.EvalSymlinks(moved)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, physical)
		options = append(options, Options{Dir: "/dev/fd/" + strconv.FormatUint(uint64(held.Fd()), 10), DirFile: held, ExtraFiles: []*os.File{held}, MemoryLimit: true, LimitKB: 256 << 10, Timeout: 2 * time.Second})
	}
	var workers sync.WaitGroup
	for i, o := range options {
		workers.Go(func() {
			for range 5 {
				got, err := Output(t.Context(), o, "rev-parse", "--show-toplevel")
				if err != nil || got != expected[i] {
					t.Errorf("descriptor cwd lost under parallel launches/replacement: got=%q want=%q err=%v", got, expected[i], err)
					return
				}
			}
		})
	}
	workers.Wait()
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatalf("daemon cwd changed: before=%q after=%q err=%v", before, after, err)
	}
}

func TestDarwinPinnedDirectoryRefusesNonDirectory(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, err = Output(t.Context(), Options{DirFile: file, MemoryLimit: true}, "--version")
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("invalid cwd reached Git: %v", err)
	}
}
