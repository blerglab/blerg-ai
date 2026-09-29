package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyScreenSnapshots replays full-pane terminal snapshots stored under
// testdata/screens through classifyScreen. Each fixture is a synthetic screen
// shaped like a `tmux capture-pane -p` dump of a live Claude Code session — the
// layout is authentic, the content is invented (a fictional notes app). The
// expected state is encoded in the filename as "<state>__<description>.txt"
// (e.g. "waiting__question-with-letter-options.txt").
//
// Unlike the hand-written fixtures in screenstate_test.go — which isolate one
// signal each — these snapshots carry the full noise of a real pane (banners,
// task lists, wrapped prose, box-drawing rules) and guard against regressions
// that only surface against realistic input. To extend coverage, drop a new
// file in the directory; no code change required. NEVER commit a raw capture:
// rewrite names, paths, ids and prose into a neutral scenario first, keeping
// the layout (line positions, widths, glyphs) that the classifier reads.
func TestClassifyScreenSnapshots(t *testing.T) {
	dir := filepath.Join("testdata", "screens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}

	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		name := e.Name()
		want, _, ok := strings.Cut(strings.TrimSuffix(name, ".txt"), "__")
		if !ok {
			t.Fatalf("snapshot %q is not named <state>__<description>.txt", name)
		}
		seen++

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read snapshot: %v", err)
			}
			if got := classifyScreen(string(data)); got != want {
				t.Errorf("classifyScreen(%s) = %q, want %q", name, got, want)
			}
		})
	}

	if seen == 0 {
		t.Fatal("no snapshot fixtures found in testdata/screens")
	}
}
