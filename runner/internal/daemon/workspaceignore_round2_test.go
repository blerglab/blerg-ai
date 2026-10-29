package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceSearchIgnoredMemberAncestor(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		for _, scope := range []string{"", "member"} {
			t.Run(runtime+"/"+scope, func(t *testing.T) {
				e, root, req := ignoreRepoFixture(t, runtime)
				ignoreFile(t, root, ".gitignore", "vendor/\n")
				nested := filepath.Join(root, "vendor/lib")
				ignoreFile(t, nested, "src/match-real.js", "")
				ignoreFile(t, nested, ".gitignore", "build/\n")
				ignoreFile(t, nested, "build/match-hidden.js", "")
				ignoreFile(t, root, "vendor/match-outer.js", "")
				childRepoGit(t, nested, "init")
				e.manifest.Members = append(e.manifest.Members, protocol.WorkspaceMember{ID: "nested", Name: "nested", Path: "member/vendor/lib"})
				e.pinMembers()
				req.Path = scope
				res := e.Explore(t.Context(), req)
				if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/vendor/lib/src/match-real.js" {
					t.Fatalf("member below ignored ancestor: %+v", res)
				}
			})
		}
	}
}

func TestWorkspaceSearchFallbackMemberAncestor(t *testing.T) {
	for _, runtime := range []string{"host", "sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			e, root, req := ignoreRepoFixture(t, runtime)
			nested := filepath.Join(root, "node_modules/lib")
			ignoreFile(t, nested, "src/match-real.js", "")
			ignoreFile(t, nested, "target/match-hidden.js", "")
			ignoreFile(t, root, "node_modules/match-outer.js", "")
			childRepoGit(t, nested, "init")
			e.manifest.Members = append(e.manifest.Members, protocol.WorkspaceMember{ID: "nested", Name: "nested", Path: "member/node_modules/lib"})
			e.pinMembers()
			t.Cleanup(hostgit.SetBinaryForTest(filepath.Join(root, "absent-git")))
			res := e.Explore(t.Context(), req)
			if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/node_modules/lib/src/match-real.js" {
				t.Fatalf("fallback pruned independent root or leaked ignored siblings: %+v", res)
			}
		})
	}
}

func TestWorkspaceSearchIgnoredDiscoveredAncestor(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "host")
	ignoreFile(t, root, ".gitignore", "vendor/\n")
	nested := filepath.Join(root, "vendor/lib")
	ignoreFile(t, nested, "match-real.js", "")
	childRepoGit(t, nested, "init")
	// Folder browsing discovers the repository without promoting it to a member.
	browse := req
	browse.Operation, browse.Path, browse.Filter = "list", "member/vendor", ""
	res := e.Explore(t.Context(), browse)
	if !res.OK || len(res.Entries) != 1 || !res.Entries[0].Repository || res.Entries[0].MemberID != "" {
		t.Fatalf("discovery: %+v", res)
	}
	res = e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 1 || res.Entries[0].Path != "member/vendor/lib/match-real.js" {
		t.Fatalf("discovered repository below ignored ancestor: %+v", res)
	}
}

func TestWorkspaceSearchLargeIndexUnderContention(t *testing.T) {
	if goruntime.GOOS == "darwin" && raceDetector {
		// Darwin samples Git's memory by spawning /bin/ps every 20 ms, which on a loaded
		// race-built runner overruns the 1 s ignore budget about half the time (BLG-319).
		t.Skip("Darwin memory sampling is too slow for this budget under -race (BLG-319)")
	}
	e, root, req := ignoreRepoFixture(t, "cluster")
	ignoreFile(t, root, ".gitignore", "build/\n")
	for d := 0; d < 100; d++ {
		for f := 0; f < 1000; f++ {
			ignoreFile(t, root, fmt.Sprintf("src/d%03d/file-%04d.js", d, f), "")
		}
	}
	ignoreFile(t, root, "a-match-real.js", "")
	childRepoGit(t, root, "add", ".")
	for i := 0; i < 250; i++ {
		ignoreFile(t, root, fmt.Sprintf("build/match-%03d.js", i), "")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "git")
	// Delay each Git subprocess to model scheduler contention deterministically. The three
	// processes' delay takes under a third of the 1 s budget, so the real work still fits on a
	// race-instrumented build on a slower runner (macOS CI).
	ignoreFile(t, filepath.Dir(binary), "git", "#!/bin/sh\nsleep 0.1\nexec '"+realGit+"' \"$@\"\n")
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hostgit.SetBinaryForTest(binary))
	start := time.Now()
	policy := newWorkspaceIgnorePolicy(e)
	rules := policy.forDirectory(workspaceIgnoreRequest(t.Context()), "member")
	t.Logf("ignore rules prepared in %v of the %v budget", time.Since(start), workspaceIgnoreTimeout)
	if rules == nil || rules.failed {
		t.Error("100k tracked files fell back under simulated contention")
	}
	if rules != nil && len(rules.tracked) != 0 {
		t.Errorf("retained %d unrelated tracked paths", len(rules.tracked))
	}
	policy.close()
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) == 0 {
		t.Fatalf("large search: %+v", res)
	}
	for _, row := range res.Entries {
		if strings.Contains(row.Path, "/build/") {
			t.Fatalf("ignored hit: %+v", row)
		}
	}
	t.Logf("100k tracked files, 100ms delay per Git process: %v (truncated=%v)", time.Since(start), res.Truncated)
}

// Expire just after the walk's last cancellation check before a failed open.
type workspaceWalkErrorContext struct {
	context.Context
	expired     bool
	beforeError func()
}

func (c *workspaceWalkErrorContext) Err() error {
	if c.expired {
		return context.DeadlineExceeded
	}
	if c.beforeError != nil {
		c.beforeError()
	}
	return nil
}
func TestWorkspaceSearchWalkErrorAtDeadline(t *testing.T) {
	e, root, req := ignoreRepoFixture(t, "cluster")
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	e.manifest.Members = nil
	ignoreFile(t, root, "a-match-real.js", "")
	ignoreFile(t, root, "z-directory/file.js", "")
	ctx := &workspaceWalkErrorContext{Context: t.Context()}
	checks := 0
	ctx.beforeError = func() {
		checks++
		// Root listing checks twice, then the walk checks each of its two rows.
		if checks == 4 {
			if err := os.RemoveAll(filepath.Join(root, "z-directory")); err != nil {
				t.Fatal(err)
			}
			ctx.expired = true
		}
	}
	budget := 100
	res := e.searchWithBudget(ctx, req, "member", &budget, 200, 1<<20, 0)
	if !ctx.expired {
		t.Fatalf("fixture did not expire (checks=%d)", checks)
	}
	if !res.OK || !res.Truncated || len(res.Entries) != 1 || res.Entries[0].Name != "a-match-real.js" {
		t.Fatalf("walk error discarded partial hits at deadline: %+v", res)
	}
}

func TestWorkspaceSearchDeadlineDoesNotUpgradeFailure(t *testing.T) {
	e, _, req := ignoreRepoFixture(t, "host")
	req.Filter = ""
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	e.live = func() bool {
		calls++
		if calls == 2 {
			<-ctx.Done()
		}
		return true
	}
	res := e.Explore(ctx, req)
	if res.OK || res.ErrorCode != "invalid" {
		t.Fatalf("delivery deadline upgraded an invalid search to success: %+v", res)
	}
}

func TestWorkspaceSearchNoIgnoredWireField(t *testing.T) {
	// An obsolete input must not become part of any later reply.
	var row protocol.WorkspaceEntry
	if err := json.Unmarshal([]byte(`{"name":"file","path":"file","ignored":true}`), &row); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"ignored"`) {
		t.Fatalf("internal ignore state leaked: %s", raw)
	}
}
