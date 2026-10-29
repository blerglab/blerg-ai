package workspacepolicy

import (
	"strings"
	"testing"
)

func TestVisibleBoundaries(t *testing.T) {
	for _, name := range []string{".ssh/config", "ordinary/.git/HEAD", "ordinary/.env", ".aws/public", "ordinary/credentials.private", "ordinary/.env/settings"} {
		if Directory(name) || File(name) {
			t.Errorf("protected path visible: %s", name)
		}
	}
	for _, name := range []string{"secret-project", "secret-project/docs", "secret-project/README.md"} {
		if !Directory(name) {
			t.Errorf("benign ancestor hidden: %s", name)
		}
	}
	if !File("secret-project/README.md") || File("ordinary/secret.txt") {
		t.Fatal("file versus ancestor policy changed")
	}
}

func TestFoldNamePreservesProtectedUnicodeBoundaries(t *testing.T) {
	for _, name := range []string{"credentıals.private", ".gİt/HEAD", "ſecrets/config"} {
		if Directory(name) {
			t.Errorf("protected Unicode path became visible: %s", name)
		}
	}
	if FoldName("é") != FoldName("e\u0301") {
		t.Error("canonical Unicode names compare differently")
	}
}

// The ASCII fast path folds exactly as the full Unicode fold does.
func TestFoldNameASCIIFastPathMatchesUnicodeFold(t *testing.T) {
	var b strings.Builder
	for c := 0; c < 0x80; c++ {
		b.WriteByte(byte(c))
	}
	for _, name := range []string{b.String(), "README.md", "Node_Modules", ".GIT", "lost+found", "a-B_c.D~", ""} {
		if got, want := FoldName(name), foldUnicode(name); got != want {
			t.Errorf("FoldName(%q) = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"ſecrets", "Straße", "e\u0301", "ÉCOLE", "ﬃ"} {
		if got, want := FoldName(name), foldUnicode(name); got != want {
			t.Errorf("FoldName(%q) = %q, want %q", name, got, want)
		}
	}
}
