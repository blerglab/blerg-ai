package daemon

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Explicit opt-in: ordinary package tests do not build million-commit fixtures.
func TestBranchGraphLargeHistory(t *testing.T) {
	if os.Getenv("BLERG_GRAPH_MEASURE") != "1" {
		t.Skip("set BLERG_GRAPH_MEASURE=1 for the large-history measurement")
	}
	for _, count := range []int{500000, 1000000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir, binding := commitFixture(t)
			cmd := exec.Command("git", "-C", dir, "fast-import", "--quiet", "--done")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			out := bufio.NewWriter(input)
			for i := range count {
				fmt.Fprintf(out, "commit refs/heads/main\ncommitter Fixture <fixture@example.com> %d +0000\ndata 7\nCommit\n\n", 1000000000+i)
			}
			fmt.Fprintln(out, "done")
			if err := out.Flush(); err != nil {
				t.Fatal(err)
			}
			if err := input.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			originGit(t, dir, "config", "core.commitGraph", "false")
			for _, depth := range []int{50, 200} {
				start := time.Now()
				got := readBranchGraph(t.Context(), "s", binding, depth)
				elapsed := time.Since(start)
				if !got.OK || len(got.Graph.Commits) != depth || !got.Graph.More {
					t.Fatalf("graph: %+v", got)
				}
				t.Logf("commits=%d depth=%d commitGraph=off elapsed=%s", count, depth, elapsed)
			}
		})
	}
}
