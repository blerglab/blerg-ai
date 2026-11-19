package daemon

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

// Main's first-parent merge history is the source of truth. ReleaseBase is an
// explicit tag/revision boundary; absent means all retained main history.
// No checkout, hooks, fetch, caller paths, commit prose, or Board inference.
func readDatasetMerges(parent context.Context, req protocol.ReadCommit, binding workspaceFileBinding) protocol.CommitResult {
	fail := func(cause string) protocol.CommitResult {
		failurelog.Default.Event("dataset_merges:"+cause, slog.LevelWarn, "component", "daemon", "operation", "dataset_merges", "cause", cause)
		return protocol.CommitResult{ErrorCode: cause}
	}
	if binding.live == nil || binding.info == nil || !binding.live() {
		return fail("binding_unavailable")
	}
	if req.Release != "" && (req.ReleaseBase == "" || !workbranch.Valid(req.ReleaseBase)) {
		return fail("invalid_release")
	}
	dir, trusted, ok := commitReadDir(req.SessionID, binding)
	if !ok {
		return fail("repository_unavailable")
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, commitReadTimeout)
	defer cancel()
	if !dir.vet(ctx, trusted) {
		return fail("repository_untrusted")
	}
	tip, code := resolveRef(ctx, dir, "refs/heads/main")
	if code != "" {
		return fail("main_unavailable")
	}
	history := &protocol.MergeHistory{Tip: tip, Commits: []protocol.GraphCommit{}}
	revision := tip
	if req.ReleaseBase != "" {
		base, code := resolveRef(ctx, dir, req.ReleaseBase)
		if code != "" {
			return fail("release_base_unavailable")
		}
		if dir.git(ctx, &cappedOutput{max: 100, strict: true}, "merge-base", "--is-ancestor", base, tip) != nil {
			return fail("release_not_ancestor")
		}
		revision = base + ".." + tip
	}
	out := &cappedOutput{max: 1 << 20, strict: true}
	if dir.git(ctx, out, "log", "--first-parent", "--merges", "--max-count=10001", "--format=%H %cI", revision, "--") != nil {
		return fail("git_log_failed")
	}
	for _, line := range strings.Split(strings.TrimSpace(out.buf.String()), "\n") {
		if line == "" {
			continue
		}
		sha, date, ok := strings.Cut(line, " ")
		if _, err := time.Parse(time.RFC3339, date); !ok || err != nil || !protocol.ValidSessionOriginCommit(sha) || len(history.Commits) == 10000 {
			return fail("invalid_history")
		}
		history.Commits = append(history.Commits, protocol.GraphCommit{SHA: sha, Date: date})
	}
	if !binding.live() || ctx.Err() != nil {
		return fail("read_canceled")
	}
	history.ObservedAt = time.Now().UTC()
	return protocol.CommitResult{OK: true, Merges: history}
}
