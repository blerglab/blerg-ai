package daemon

import (
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveredSummaryRetainsMetadata(t *testing.T) {
	dir, _ := commitFixture(t)
	base := commitFile(t, dir, "README.md", "one\n", "Base")
	head := commitFile(t, dir, "README.md", "one\ntwo\n", "Delivered")
	pin, ok := pinGitDir(dir, nil)
	if !ok {
		t.Fatal("pin")
	}
	defer pin.close()
	got := deliveredSummary(t.Context(), pin, base, head)
	if got == nil || len(got.Commits) != 1 || got.Commits[0].SHA != head || len(got.Files) != 1 || got.Files[0].Adds != 1 {
		t.Fatalf("delivered metadata missing: %+v", got)
	}
	if got := deliveredSummary(t.Context(), pin, head, head); got != nil {
		t.Fatalf("empty delivery: %+v", got)
	}
}

func TestHostDeliveredSummaryCollection(t *testing.T) {
	dir, _ := commitFixture(t)
	base := commitFile(t, dir, "app.go", "base\n", "Base")
	head := commitFile(t, dir, "app.go", "base\nnext\n", "Delivered")
	root, err := workspaceDirectoryIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	common, err := workspaceDirectoryIdentity(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	w := &LocalWorkspace{state: workspacePrivateManifest{Sources: []WorkspaceSource{{Path: dir, CommonPath: filepath.Join(dir, ".git"), RootIdentity: root, CommonIdentity: common}}, Members: []workspacePrivateMember{{Result: protocol.WorkspaceMemberResult{Base: base}}}}}
	h := &hostWorkspace{w: w}
	h.collect(t.Context())
	got := w.state.Members[0].Result
	if got.Pending || got.Summary == nil || got.Summary.Commits[0].SHA != head {
		t.Fatalf("host metadata missing: %+v", got)
	}
}

func TestDeliveredSummaryTruncatesFiles(t *testing.T) {
	dir, _ := commitFixture(t)
	base := commitFile(t, dir, "base.txt", "base\n", "Base")
	folder := strings.Repeat("d", 200)
	if err := os.Mkdir(filepath.Join(dir, folder), 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 301 {
		name := filepath.Join(dir, folder, fmt.Sprintf("%03d-%s", i, strings.Repeat("f", 195)))
		if err := os.WriteFile(name, []byte("one\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	originGit(t, dir, "add", ".")
	originGit(t, dir, "commit", "-qm", "Many files")
	head := originGit(t, dir, "rev-parse", "HEAD")
	pin, ok := pinGitDir(dir, nil)
	if !ok {
		t.Fatal("pin")
	}
	defer pin.close()
	got := deliveredSummary(t.Context(), pin, base, head)
	if got == nil || !got.Truncated || len(got.Commits) != 1 || len(got.Files) >= 300 || !got.Valid() {
		t.Fatalf("unbounded metadata: %+v", got)
	}
}
