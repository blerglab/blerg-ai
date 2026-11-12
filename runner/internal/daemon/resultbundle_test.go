package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// resultBundleFixture builds a real clone with a remote-tracking refs/remotes/origin/HEAD (what
// a real `git clone` leaves behind) on "main", then checks out workBranch with one extra
// commit — the shape a worker pod's own clone+work branch actually has.
func resultBundleFixture(t *testing.T, workBranch string) (dir, baseSHA, headSHA string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	work := filepath.Join(t.TempDir(), "origin-work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	originGit(t, work, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, work, "add", "f")
	originGit(t, work, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "one")
	originGit(t, "", "init", "-q", "--bare", "--initial-branch=main", origin)
	originGit(t, work, "push", "-q", origin, "main")

	dir = filepath.Join(t.TempDir(), "clone")
	originGit(t, "", "clone", "-q", origin, dir)
	baseSHA = originGit(t, dir, "rev-parse", "HEAD")
	originGit(t, dir, "checkout", "-q", "-b", workBranch)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "add", "f")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "two")
	headSHA = originGit(t, dir, "rev-parse", "HEAD")
	return dir, baseSHA, headSHA
}

func TestResultBundleBaseIsTheMergeBaseWithTheRemoteTrackingDefault(t *testing.T) {
	dir, base, _ := resultBundleFixture(t, "wip/card-session")
	opts := hostgit.Options{Dir: dir, UserConfig: true, Protocols: []string{"file"}}
	got := resultBundleBase(t.Context(), opts, "wip/card-session")
	if got != base {
		t.Fatalf("resultBundleBase = %q, want %q", got, base)
	}
}

func TestResultBundleBaseEmptyWithoutARemoteTrackingDefault(t *testing.T) {
	dir := t.TempDir()
	originGit(t, dir, "init", "-q", "--initial-branch=solo")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "add", "f")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "x")
	opts := hostgit.Options{Dir: dir, UserConfig: true, Protocols: []string{"file"}}
	got := resultBundleBase(t.Context(), opts, "solo")
	if got != "" {
		t.Fatalf("resultBundleBase (no remote) = %q; want empty", got)
	}
}

func TestPublishResultBundleNoOpWithoutTheEnvVar(t *testing.T) {
	t.Setenv(resultBundleEnv, "")
	dir, _, _ := resultBundleFixture(t, "wip/card-session")
	t.Setenv("BLERG_RUNNER_WORK_BRANCH", "wip/card-session")
	if err := publishResultBundle(context.Background(), dir, "sess-1"); err != nil {
		t.Fatalf("expected a silent no-op, got %v", err)
	}
}

func TestPublishResultBundleRefusesWithoutAWorkBranch(t *testing.T) {
	t.Setenv(resultBundleEnv, "1")
	t.Setenv("BLERG_RUNNER_WORK_BRANCH", "")
	dir, _, _ := resultBundleFixture(t, "wip/card-session")
	err := publishResultBundle(context.Background(), dir, "sess-1")
	if err == nil {
		t.Fatal("expected a refusal with no recorded work branch")
	}
}

// TestPublishResultBundleBuildsWhatARealPodWouldPublish runs the actual shipped turn_done code
// (BLG-479 design §4's pod-side half), not a parallel test fixture that merely imitates it: a
// real pod-shaped repo (resultBundleFixture), a real `blerg-runner publish` call intercepted only
// at its process boundary (a stub binary captures the exact bytes this code hands it, nothing
// about how the bundle itself was built), then the captured file is checked with real git —
// `bundle verify` against a repo that holds only the base commit (exactly what a bare-cloned
// peer, or Main's own import step 3, has) and `bundle list-heads` for the advertised ref. This is
// "a real bundle a real pod would publish," independent of any cluster launch (§3).
func TestPublishResultBundleBuildsWhatARealPodWouldPublish(t *testing.T) {
	dir, base, head := resultBundleFixture(t, "wip/card-session")
	t.Setenv(resultBundleEnv, "1")
	t.Setenv("BLERG_RUNNER_WORK_BRANCH", "wip/card-session")

	captured := filepath.Join(t.TempDir(), "captured.bundle")
	stubDir := t.TempDir()
	script := "#!/bin/sh\ncp \"$2\" \"" + captured + "\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "blerg-runner"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")

	root := t.TempDir()
	member := filepath.Join(root, "app")
	if err := os.Rename(dir, member); err != nil {
		t.Fatal(err)
	}
	dir = member
	manifest := &protocol.WorkspaceManifest{Members: []protocol.WorkspaceMember{{ID: "member", Path: "app"}}}
	if err := publishWorkspaceResultBundles(context.Background(), root, "sess-1", manifest); err != nil {
		t.Fatalf("publishResultBundle: %v", err)
	}
	if st, err := os.Stat(captured); err != nil || st.Size() == 0 {
		t.Fatalf("no non-empty bundle was handed to publish: %v", err)
	}

	// A peer that only has the base commit (what a bare clone of this same history, or Main's
	// own fetched integration branch, would hold) — never the head this bundle carries.
	verify := filepath.Join(t.TempDir(), "verify")
	originGit(t, "", "clone", "-q", "--branch", "main", "--single-branch", dir, verify)
	if got := originGit(t, verify, "rev-parse", "HEAD"); got != base {
		t.Fatalf("verify fixture tip = %s, want the base commit %s", got, base)
	}
	if out := originGit(t, verify, "bundle", "verify", captured); !strings.Contains(out, "The bundle") {
		t.Fatalf("git bundle verify did not accept the real published bundle: %s", out)
	}
	heads := originGit(t, verify, "bundle", "list-heads", captured)
	want := head + " refs/heads/wip/card-session"
	if heads != want {
		t.Fatalf("bundle advertises %q, want %q", heads, want)
	}
}

func TestResultBundleBaseUsesRecordedLaunchBase(t *testing.T) {
	dir, base, _ := resultBundleFixture(t, "wip/card-session")
	originGit(t, dir, "update-ref", "refs/blerg/result-base", base)
	originGit(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	if got := resultBundleBase(t.Context(), hostgit.Options{Dir: dir}, "wip/card-session"); got != base {
		t.Fatalf("base = %q, want launch base %q", got, base)
	}
}

func TestResultBundleNoOpReasonsAreLogged(t *testing.T) {
	for _, cause := range []string{"disabled", "base_unavailable", "no_new_commits", "no_members"} {
		t.Run(cause, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			previous := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { failurelog.Default = previous })
			t.Setenv(resultBundleEnv, "1")
			t.Setenv("BLERG_RUNNER_WORK_BRANCH", "wip/card-session")
			dir, base, _ := resultBundleFixture(t, "wip/card-session")
			switch cause {
			case "disabled":
				t.Setenv(resultBundleEnv, "")
			case "base_unavailable":
				originGit(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
			case "no_new_commits":
				originGit(t, dir, "reset", "--hard", base)
			}
			if cause == "no_members" {
				_ = publishWorkspaceResultBundles(t.Context(), dir, "session", &protocol.WorkspaceManifest{})
			} else {
				_ = publishResultBundle(t.Context(), dir, "session")
			}
			if !strings.Contains(logs.String(), cause) {
				t.Fatalf("missing %s diagnostic: %s", cause, logs.String())
			}
		})
	}
}

func TestResultBundlePublishFailureDoesNotExposeCLIOutput(t *testing.T) {
	dir, _, _ := resultBundleFixture(t, "wip/card-session")
	t.Setenv(resultBundleEnv, "1")
	t.Setenv("BLERG_RUNNER_WORK_BRANCH", "wip/card-session")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "blerg-runner"), []byte("#!/bin/sh\necho private-canary >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	err := publishResultBundle(t.Context(), dir, "session")
	if err == nil {
		t.Fatal("publish failure was ignored")
	}
	if strings.Contains(err.Error()+logs.String(), "private-canary") {
		t.Fatal("publish output escaped into diagnostics")
	}
	if !strings.Contains(logs.String(), "publish_failed") {
		t.Fatal("publish refusal was not logged")
	}
}
