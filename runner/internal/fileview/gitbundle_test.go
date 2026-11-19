package fileview

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// realGitBundleHead runs real git to produce a real bundle and returns its first 512 bytes —
// the same slice size the server reads before classifying a publish (BLG-460/BLG-479: "real
// git and real bundles", not hand-crafted header bytes).
func realGitBundleHead(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "fixture@example.test")
	run("config", "user.name", "fixture")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-q", "-m", "one")
	bundlePath := filepath.Join(t.TempDir(), "result.bundle")
	run("bundle", "create", bundlePath, "main")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 512 {
		data = data[:512]
	}
	return data
}

func TestClassifyRecognizesARealGitBundleByExtension(t *testing.T) {
	head := realGitBundleHead(t)
	ct, view := Classify("result.bundle", head, int64(len(head)))
	if ct != GitBundle || view != None {
		t.Fatalf("Classify(result.bundle) = %s/%s, want %s/%s", ct, view, GitBundle, None)
	}
}

func TestClassifyRecognizesARealGitBundleWithoutTheExtension(t *testing.T) {
	head := realGitBundleHead(t)
	// No ".bundle" name at all: classification must come from the bytes, matching the design's
	// "sniffed, not claimed" rule — a trusted turn_done step and an untrusted publish by the
	// agent's own shell both go through the exact same sniff.
	ct, view := Classify("result", head, int64(len(head)))
	if ct != GitBundle || view != None {
		t.Fatalf("Classify(result) = %s/%s, want %s/%s", ct, view, GitBundle, None)
	}
}

func TestClassifyRefusesABundleNameWithoutBundleBytes(t *testing.T) {
	// A ".bundle" name on ordinary text must NOT earn the git-bundle content type — a name
	// proves nothing a publisher couldn't fake.
	body := []byte("just some text, not a git bundle\n")
	ct, view := Classify("result.bundle", body, int64(len(body)))
	if ct == GitBundle {
		t.Fatalf("Classify(result.bundle, non-bundle bytes) = %s/%s, want anything but %s", ct, view, GitBundle)
	}
}

func TestClassifyRecognizesTheV3BundleHeaderToo(t *testing.T) {
	// git's v3 bundle format (capability lines after the same first line) — constructed
	// directly since the git version running these tests may default to v2; the point is the
	// classifier's own header-prefix check, not which format a particular git emits.
	head := []byte("# v3 git bundle\n@object-format=sha1\n")
	ct, view := Classify("result.bundle", head, int64(len(head)))
	if ct != GitBundle || view != None {
		t.Fatalf("Classify(v3 header) = %s/%s, want %s/%s", ct, view, GitBundle, None)
	}
}

func TestClassifyGitBundleNeverOffersAPreview(t *testing.T) {
	head := realGitBundleHead(t)
	_, view := Classify("result.bundle", head, int64(len(head)))
	if view != None {
		t.Fatalf("a git bundle must never get a preview view, got %q", view)
	}
}

func TestViewForContentTypeGitBundleIsNone(t *testing.T) {
	if got := ViewForContentType(GitBundle); got != None {
		t.Fatalf("ViewForContentType(%s) = %s, want %s", GitBundle, got, None)
	}
}

func TestLooksLikeGitBundleRejectsArbitraryPrefix(t *testing.T) {
	if looksLikeGitBundle([]byte("# v2 git bundlX\n")) {
		t.Fatal("a near-miss header must not be recognized")
	}
	if looksLikeGitBundle(nil) {
		t.Fatal("an empty head must not be recognized")
	}
}
