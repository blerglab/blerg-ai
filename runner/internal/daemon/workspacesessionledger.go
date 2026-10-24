package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type sessionGitUsage struct {
	metadata, objects int64
	entries           int
}

// The same session allowance covers live clone metadata and private copies.
// P2 owns it after Suspend has canceled/joined P4 (including snapshot removal).
// While a production reader owns P2's lifecycle lock, its helper reserves here
// against fresh measurements of ALL members, never a fresh per-copy allowance.
// Untrusted checkout writes remain bounded by P2's enforced volume quotas; a
// source change during measurement/copy is revalidated before any delivery.
type workspaceSessionLedger struct {
	mu      sync.Mutex
	used    sessionGitUsage // outstanding private allocations
	measure func(context.Context) (sessionGitUsage, error)
}

func (l *workspaceSessionLedger) fits(u sessionGitUsage) bool {
	b := protocol.DefaultWorkspaceBudget()
	return u.metadata <= b.MetadataBytes && u.objects <= b.FetchedBytes && u.entries <= b.MetadataEntries && u.metadata >= 0 && u.entries >= 0
}
func (l *workspaceSessionLedger) reserve(ctx context.Context, extra sessionGitUsage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	source, err := l.measure(ctx)
	if err != nil {
		return err
	}
	total := sessionGitUsage{metadata: source.metadata + l.used.metadata + extra.metadata, objects: source.objects, entries: source.entries + l.used.entries + extra.entries}
	if !l.fits(total) {
		return errors.New("session Git allowance exhausted")
	}
	l.used.metadata += extra.metadata
	l.used.entries += extra.entries
	return nil
}
func (l *workspaceSessionLedger) release(u sessionGitUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.used.metadata -= u.metadata
	l.used.entries -= u.entries
}
func (l *workspaceSessionLedger) empty() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used.metadata == 0 && l.used.entries == 0
}
func (e *LocalExplorer) measureSessionGit(ctx context.Context) (sessionGitUsage, error) {
	var usage sessionGitUsage
	for _, member := range e.manifest.Members {
		root, relative, _ := e.anchorFor(path.Join(member.Path, ".git"))
		if root == nil {
			return usage, os.ErrPermission
		}
		var walk func(string, int) error
		objectPrefix := path.Join(relative, "objects") + "/"
		walk = func(name string, depth int) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if depth > 32 {
				return os.ErrPermission
			}
			f, err := workspaceOpenFile(root, name)
			if err != nil {
				return err
			}
			st, err := f.Stat()
			if err != nil {
				_ = f.Close()
				return err
			}
			usage.entries++
			if !(&workspaceSessionLedger{}).fits(usage) {
				_ = f.Close()
				return errors.New("session Git allowance exhausted")
			}
			if st.IsDir() {
				defer func() { _ = f.Close() }()
				for {
					entries, readErr := f.ReadDir(128)
					for _, entry := range entries {
						if err := walk(path.Join(name, entry.Name()), depth+1); err != nil {
							return err
						}
					}
					if readErr == io.EOF {
						break
					}
					if readErr != nil {
						return readErr
					}
				}
			} else {
				_ = f.Close()
				if !workspaceRegularFile(st) {
					return os.ErrPermission
				}
				if strings.HasPrefix(name, objectPrefix) {
					usage.objects += st.Size()
				} else {
					usage.metadata += st.Size()
				}
				if !(&workspaceSessionLedger{}).fits(usage) {
					return errors.New("session Git allowance exhausted")
				}
			}
			return nil
		}
		if err := walk(relative, 0); err != nil {
			return usage, err
		}
	}
	return usage, nil
}
