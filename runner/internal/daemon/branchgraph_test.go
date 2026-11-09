package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit/hostgittest"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBranchGraphRealRepository(t *testing.T) {
	dir, binding := commitFixture(t)
	root := commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "checkout", "-qb", "wip/example")
	tip := commitFile(t, dir, "work", "work", "Work")
	originGit(t, dir, "checkout", "-q", "main")
	res := readGitBinding(t.Context(), protocol.ReadCommit{SessionID: "s", Graph: true, Depth: 50}, false, binding)
	if !res.OK || res.Graph == nil {
		t.Fatalf("graph: %+v", res)
	}
	if len(res.Graph.Refs) != 2 || len(res.Graph.Commits) != 2 {
		t.Fatalf("graph: %+v", res.Graph)
	}
	if res.Graph.Commits[0].SHA != tip || res.Graph.Commits[0].Parents[0] != root {
		t.Fatalf("parents: %+v", res.Graph)
	}
	limited := readGitBinding(t.Context(), protocol.ReadCommit{SessionID: "s", Graph: true, Depth: 1}, false, binding)
	if !limited.OK || !limited.Graph.More || len(limited.Graph.Commits) != 1 {
		t.Fatalf("depth: %+v", limited)
	}
	if got := originGit(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("read wrote files: %s", got)
	}
}

func TestBranchGraphIgnoresPlantedConfiguration(t *testing.T) {
	dir, b, _ := plantedWorkspace(t)
	probe := hostgittest.Plant(t, filepath.Join(dir, ".git"))
	if res := readBranchGraph(t.Context(), "s", b, 50); !res.OK {
		t.Fatalf("graph: %+v", res)
	}
	probe.Check(t)
}
func TestBranchGraphRefusesUnboundAndInvalidDepth(t *testing.T) {
	_, b := commitFixture(t)
	for _, depth := range []int{-1, 0, 201} {
		if res := readBranchGraph(t.Context(), "s", b, depth); res.OK {
			t.Fatal("invalid depth accepted")
		}
	}
	b.live = func() bool { return false }
	if res := readBranchGraph(t.Context(), "s", b, 50); res.OK {
		t.Fatal("retired binding accepted")
	}
}

func TestBranchGraphCapsRefsAndKeepsMain(t *testing.T) {
	dir, b := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	for i := 0; i < 65; i++ {
		originGit(t, dir, "branch", fmt.Sprintf("aaa/%02d", i))
	}
	result := readBranchGraph(t.Context(), "s", b, 50)
	if !result.OK || len(result.Graph.Refs) != 64 || !result.Graph.RefsLimited || result.Graph.Refs[0].Name != "main" {
		t.Fatalf("ref bound: %+v", result)
	}
}

func TestBranchGraphRecentRefsAndDefault(t *testing.T) {
	dir, b := commitFixture(t)
	old := commitFile(t, dir, "base", "base", "Old")
	for i := 0; i < 371; i++ {
		originGit(t, dir, "branch", fmt.Sprintf("aaa/%02d", i), old)
	}
	originGit(t, dir, "checkout", "-qb", "zzz/recent")
	commitFile(t, dir, "new", "new", "Recent")
	// Different commit dates, rather than alphabetical branch names, choose the cap.
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-01T00:00:00Z")
	originGit(t, dir, "commit", "--amend", "--no-edit", "--date=2030-01-01T00:00:00Z")
	recent := originGit(t, dir, "rev-parse", "HEAD")
	originGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/zzz/recent")
	got := readBranchGraph(t.Context(), "s", b, 50)
	if !got.OK {
		t.Fatalf("graph: %+v", got)
	}
	found := false
	for _, ref := range got.Graph.Refs {
		if ref.SHA == recent {
			found = true
		}
	}
	if !found || got.Graph.Refs[0].Name != "zzz/recent" {
		t.Fatalf("recent default lost: %+v", got.Graph)
	}
}

func TestBranchGraphSkipsBadRef(t *testing.T) {
	dir, b := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	// A branch can point at a blob through low-level plumbing or repository damage.
	blob := originGit(t, dir, "rev-parse", "HEAD:base")
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "broken"), []byte(blob+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := readBranchGraph(t.Context(), "s", b, 50)
	if !got.OK || len(got.Graph.Commits) != 1 {
		t.Fatalf("bad ref broke healthy history: %+v", got)
	}
}

func TestBranchGraphSkipsMissingObject(t *testing.T) {
	dir, b := commitFixture(t)
	commitFile(t, dir, "base", "base", "Base")
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "broken"), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := readBranchGraph(t.Context(), "s", b, 50)
	if !got.OK || len(got.Graph.Commits) != 1 {
		t.Fatalf("missing object hid healthy history: %+v", got)
	}
}

func TestBranchGraphReleaseLandmarks(t *testing.T) {
	dir, binding := commitFixture(t)
	base := commitFile(t, dir, "base", "base", "Base")
	originGit(t, dir, "tag", "v1.0.0", base)
	originGit(t, dir, "checkout", "-qb", "feature/search")
	feature := commitFile(t, dir, "search", "search", "Search BLG-12")
	originGit(t, dir, "checkout", "-q", "main")
	originGit(t, dir, "merge", "--no-ff", "feature/search", "-m", "Merge search #12")
	originGit(t, dir, "tag", "-a", "v1.1.0", "-m", "Search release")
	result := readBranchGraph(t.Context(), "s", binding, 50)
	if !result.OK {
		t.Fatalf("read: %+v", result)
	}
	data, err := json.Marshal(result.Graph)
	if err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Tags []struct {
			Name     string
			Contains []string
			Previous string
			Complete bool
		}
		Commits []struct{ Date string }
	}
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Tags) != 2 {
		t.Fatalf("tags: %s", data)
	}
	var found bool
	for _, tag := range graph.Tags {
		if tag.Name == "v1.1.0" {
			found = slices.Contains(tag.Contains, feature) && tag.Previous == "v1.0.0" && tag.Complete
		}
	}
	if !found || len(graph.Commits) == 0 || graph.Commits[0].Date == "" {
		t.Fatalf("release ancestry/dates: %s", data)
	}
}

func TestBranchGraphTagBoundsAndUnknownAncestry(t *testing.T) {
	dir, binding := commitFixture(t)
	base := commitFile(t, dir, "base", "base", "Base")
	for i := 0; i < 18; i++ {
		originGit(t, dir, "tag", fmt.Sprintf("v1.%02d", i), base)
	}
	commitFile(t, dir, "tip", "tip", "Tip")
	result := readBranchGraph(t.Context(), "s", binding, 1)
	if !result.OK || len(result.Graph.Tags) != 16 || !result.Graph.TagsLimited {
		t.Fatalf("tag cap: %+v", result)
	}
	for _, tag := range result.Graph.Tags {
		if tag.Complete || len(tag.Contains) != 0 {
			t.Fatalf("unknown ancestry claimed: %+v", tag)
		}
	}
}
