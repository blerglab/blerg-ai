package daemon

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"path/filepath"
	"time"
)

// Test-only local Git peer; production always uses isolated Docker peers.
func fetchWorkspaceWorkerPack(ctx context.Context, timeout time.Duration, path, _ string, c *sandboxClone, branch, target string) (string, error) {
	serve, err := hostgit.ServeCommand("upload-pack")
	if err != nil {
		return "", err
	}
	return hostGitLocalContext(ctx, timeout, c.Repo, []string{"fetch.fsckObjects=true", "transfer.fsckObjects=true", "fetch.unpackLimit=0"}, sandboxFetchMemoryKB,
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--no-write-commit-graph", "--upload-pack="+serve, filepath.Join(path, ".git"), "+"+branch+":"+target)
}
