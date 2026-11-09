package daemon

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBoardHistoryRealGit(t *testing.T) {
	dir, binding := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "checkout", "-qb", "feature")
	commitFile(t, dir, "feature", "feature", "BLG-528 implementation")
	originGit(t, dir, "checkout", "-q", "main")
	originGit(t, dir, "merge", "--no-ff", "feature", "-m", "Merge BLG-528, BLG-529: board follows code")
	merged := originGit(t, dir, "rev-parse", "HEAD")
	follow := commitFile(t, dir, "follow", "follow", "BLG-528 follow-up")
	req := protocol.ReadCommit{SessionID: "s", BoardHistory: true, SHA: merged}
	got := readGitBinding(t.Context(), req, false, binding)
	if !got.OK || got.CodeHistory == nil || len(got.CodeHistory.Commits) != 4 {
		t.Fatalf("history: %+v", got)
	}
	commits := got.CodeHistory.Commits
	if commits[0].SHA != follow || commits[0].Deployed || commits[1].SHA != merged || !commits[1].Deployed || len(commits[1].Parents) != 2 {
		t.Fatalf("containment: %+v", commits)
	}
	originGit(t, dir, "revert", "--no-edit", "-m", "1", merged)
	got = readGitBinding(t.Context(), req, false, binding)
	if !got.OK || got.CodeHistory.Commits[0].Reverts != merged {
		t.Fatalf("merge revert trailer: %+v", got.CodeHistory)
	}
	originGit(t, dir, "checkout", "-qb", "unmerged")
	commitFile(t, dir, "unmerged", "unmerged", "Merge BLG-999")
	got = readGitBinding(t.Context(), req, false, binding)
	if !got.OK || len(got.CodeHistory.Commits) != 5 {
		t.Fatalf("must read main: %+v", got)
	}
	req.SHA = "--all"
	if readGitBinding(t.Context(), req, false, binding).OK {
		t.Fatal("invalid deploy accepted")
	}
}
func TestBoardHistoryFailurePrivacy(t *testing.T) {
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = old }()
	got := readBoardHistory(t.Context(), protocol.ReadCommit{SessionID: "PRIVATE_CANARY"}, workspaceFileBinding{})
	if got.OK || !strings.Contains(logs.String(), "binding_unavailable") || strings.Contains(logs.String(), "PRIVATE_CANARY") {
		t.Fatalf("unsafe failure: %s", logs.String())
	}
}

func TestBoardHistoryRejectsUnpinnedBinding(t *testing.T) {
	_, binding := commitFixture(t)
	binding.info = nil
	got := readBoardHistory(t.Context(), protocol.ReadCommit{SessionID: "s", BoardHistory: true}, binding)
	if got.ErrorCode != "binding_unavailable" {
		t.Fatalf("un-pinned binding reached Git: %+v", got)
	}
}

func TestBoardHistoryNestedMerge(t *testing.T) {
	dir, binding := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "checkout", "-qb", "integration")
	originGit(t, dir, "checkout", "-qb", "feature")
	commitFile(t, dir, "feature", "feature", "Implementation")
	originGit(t, dir, "checkout", "-q", "integration")
	originGit(t, dir, "merge", "--no-ff", "feature", "-m", "Merge BLG-562: reconcile", "-m", "Includes BLG-563 and BLG-563. PRIVATE_CANARY")
	merged := originGit(t, dir, "rev-parse", "HEAD")
	originGit(t, dir, "checkout", "-q", "main")
	originGit(t, dir, "merge", "--no-ff", "integration", "-m", "Integrate fixes")
	deployed := originGit(t, dir, "rev-parse", "HEAD")
	got := readGitBinding(t.Context(), protocol.ReadCommit{SessionID: "s", BoardHistory: true, SHA: deployed}, false, binding)
	if got.CodeHistory != nil {
		for _, c := range got.CodeHistory.Commits {
			if c.SHA == merged && c.Deployed {
				if !slices.Equal(c.BodyCards, []int{563}) {
					t.Fatalf("missing merge-body evidence: %+v", c)
				}
				return
			}
		}
	}
	t.Fatalf("nested deployed merge missing: %+v", got)
}

func TestBoardHistoryLocalBranchActivityIsNotMergeProof(t *testing.T) {
	dir, binding := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "checkout", "-qb", "wip/worker")
	commitFile(t, dir, "work", "work", "Merge BLG-585: unmerged branch activity")
	got := readGitBinding(t.Context(), protocol.ReadCommit{SessionID: "s", BoardHistory: true, BoardActivitySince: "2000-01-01T00:00:00Z"}, false, binding)
	if !got.OK || got.CodeHistory == nil || !got.CodeHistory.ActivityComplete {
		t.Fatalf("missing activity: %+v", got)
	}
	found := false
	for _, a := range got.CodeHistory.Activity {
		if strings.Contains(a.Subject, "BLG-585") {
			found = true
		}
	}
	if !found {
		t.Fatal("unmerged worker branch missing from activity")
	}
	for _, c := range got.CodeHistory.Commits {
		if strings.Contains(c.Subject, "BLG-585") {
			t.Fatal("branch activity became main-merge proof")
		}
	}
}
