package daemon

// BLG-479's pod-side half of the result-bundle design
// (docs/design/cluster-workers-from-desktop.md §4): at turn_done, a worker pod bundles this
// turn's new commits and publishes them through the ordinary publish path. The server sets
// BLERG_WORKER_RESULT_BUNDLE from the same ReadOnlyGitWorker derivation that locks the pod's
// credential down (k8sjobs.go) — a pod can never turn this on for itself. Main's own import
// never trusts anything about how this bundle was built or what base it names; this file only
// decides how much the bundle carries.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
)

const resultBundleEnv = "BLERG_WORKER_RESULT_BUNDLE"
const resultBundleTimeout = 2 * time.Minute
const resultBundleMemoryLimitKB = 512 << 10

// publishResultBundle is a no-op (nil) unless BLERG_WORKER_RESULT_BUNDLE=1, there is a recorded
// work branch, and that branch has commits past its own clone's starting point.
// Any real failure is returned for the caller to log; it never panics and
// never touches any ref other than reading one.
//
//nolint:gosec // fixed publish command; its operand is a server-created temporary bundle path
func publishResultBundle(ctx context.Context, workDir, sessionID string) error {
	_ = sessionID
	if os.Getenv(resultBundleEnv) != "1" {
		bundleStatus("disabled")
		return nil
	}
	branch := os.Getenv("BLERG_RUNNER_WORK_BRANCH")
	if branch == "" {
		bundleStatus("work_branch_missing")
		return errors.New("no work branch recorded for this session")
	}
	opts := hostgit.Options{Dir: workDir, SafeDirectory: workDir, Untrusted: true, Timeout: resultBundleTimeout, MemoryLimit: true, LimitKB: resultBundleMemoryLimitKB}

	base := resultBundleBase(ctx, opts, branch)
	if base == "" {
		bundleStatus("base_unavailable")
		return errors.New("result bundle base is unavailable")
	}
	head, err := hostgit.Output(ctx, opts, "rev-parse", branch)
	if err != nil {
		bundleStatus("head_unavailable")
		return errors.New("could not read the work branch tip")
	}
	head = strings.TrimSpace(head)
	if head == base {
		bundleStatus("no_new_commits")
		return nil
	}

	tmp, err := os.CreateTemp("", "result-*.bundle")
	if err != nil {
		bundleStatus("temporary_file_failed")
		return errors.New("could not create a temporary bundle file")
	}
	bundlePath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(bundlePath) }()

	// Allowed to lazily fetch any blob in range it doesn't have yet — the clone is partial
	// (§2) — so the result is a complete bundle, never a promisor bundle with missing objects.
	bundleOpts := opts
	bundleOpts.LazyFetch = true
	bundleOpts.Protocols = []string{"https", "ssh"}
	if _, err := hostgit.Output(ctx, bundleOpts, "bundle", "create", bundlePath, base+".."+branch); err != nil {
		bundleStatus("bundle_create_failed")
		return errors.New("could not create the result bundle")
	}

	cmd := exec.CommandContext(ctx, "blerg-runner", "publish", bundlePath)
	cmd.Dir = workDir
	err = cmd.Run()
	if err != nil {
		failurelog.Default.Event("result_bundle:publish_failed", slog.LevelWarn, "component", "daemon", "operation", "result_bundle", "cause", "publish_failed", "error_kind", failurelog.Cause(err))
		return errors.New("could not publish the result bundle: publish command failed")
	}
	bundleStatus("published")
	return nil
}

// resultBundleBase prefers the exact launch boundary recorded by DiskGit. Older
// clones fall back to the remote default's merge-base. Main independently checks
// prerequisites and history during import; this ref grants no trust.
func resultBundleBase(ctx context.Context, opts hostgit.Options, branch string) string {
	if base, err := hostgit.Output(ctx, opts, "rev-parse", "--verify", "refs/blerg/result-base^{commit}"); err == nil {
		return strings.TrimSpace(base)
	}
	upstream, err := hostgit.Output(ctx, opts, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil || strings.TrimSpace(upstream) == "" {
		return ""
	}
	base, err := hostgit.Output(ctx, opts, "merge-base", branch, strings.TrimSpace(upstream))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(base)
}

// Use manifest paths, never directory discovery: the root is a workspace, not a clone.
func publishWorkspaceResultBundles(ctx context.Context, root, sessionID string, manifest *protocol.WorkspaceManifest) error {
	if os.Getenv(resultBundleEnv) != "1" || manifest == nil {
		return publishResultBundle(ctx, root, sessionID)
	}
	if len(manifest.Members) == 0 {
		bundleStatus("no_members")
		return nil
	}
	var failures []error
	for _, member := range manifest.Members {
		if member.Path != "." && !workspacepolicy.SafeSegment(member.Path) {
			bundleStatus("invalid_member_path")
			failures = append(failures, errors.New("invalid result bundle member path"))
			continue
		}
		if err := publishResultBundle(ctx, filepath.Join(root, member.Path), sessionID); err != nil {
			bundleStatus("member_failed")
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func bundleStatus(cause string) {
	switch cause {
	case "disabled", "no_new_commits", "no_members", "published":
		slog.Info("result bundle", "component", "daemon", "operation", "result_bundle", "cause", cause)
		return
	}
	failurelog.Default.Event("result_bundle:"+cause, slog.LevelWarn, "component", "daemon", "operation", "result_bundle", "cause", cause)
}
