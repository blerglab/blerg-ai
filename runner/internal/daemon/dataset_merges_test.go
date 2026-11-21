package daemon

import (
	"bytes"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"log/slog"
	"strings"
	"testing"
)

func TestDatasetMergesRealGit(t *testing.T) {
	dir, binding := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "tag", "v0.5.0")
	originGit(t, dir, "checkout", "-qb", "feature")
	commitFile(t, dir, "one", "one", "One")
	originGit(t, dir, "checkout", "-q", "main")
	originGit(t, dir, "merge", "--no-ff", "feature", "-m", "Merge feature")
	req := protocol.ReadCommit{SessionID: "s", DatasetMerges: true, Release: "0.6.0", ReleaseBase: "v0.5.0"}
	got := readGitBinding(t.Context(), req, false, binding)
	if !got.OK || got.Merges == nil || len(got.Merges.Commits) != 1 {
		t.Fatalf("merge history: %+v", got)
	}
	originGit(t, dir, "checkout", "-qb", "unmerged")
	commitFile(t, dir, "other", "other", "Other")
	again := readGitBinding(t.Context(), req, false, binding)
	if !again.OK || len(again.Merges.Commits) != 1 || again.Merges.Commits[0].SHA != got.Merges.Commits[0].SHA {
		t.Fatalf("read must use main: %+v", again)
	}
	req.ReleaseBase = "missing"
	if readGitBinding(t.Context(), req, false, binding).OK {
		t.Fatal("missing release boundary became zero")
	}
}

func TestDatasetMergesFailuresLogTypedCauses(t *testing.T) {
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	result := readDatasetMerges(t.Context(), protocol.ReadCommit{SessionID: "PRIVATE_CANARY"}, workspaceFileBinding{})
	if result.OK || result.ErrorCode != "binding_unavailable" || !strings.Contains(logs.String(), "binding_unavailable") || strings.Contains(logs.String(), "PRIVATE_CANARY") {
		t.Fatalf("unsafe merge failure: %s", logs.String())
	}
}
