package chatproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// chatproxy.go is a COPY of packages/chat/proxy/go/chatproxy.go (the package says to copy it: an
// app cannot import the reference module). Inside this repository the copy must not drift from
// the reference, so a fix lands in one place and reaches both. Outside it (the Docker build
// context has no packages/) the reference is absent and there is nothing to compare.
func TestCopyMatchesTheReference(t *testing.T) {
	ref, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "chat", "proxy", "go", "chatproxy.go"))
	if err != nil {
		t.Skipf("reference not present: %v", err)
	}
	mine, err := os.ReadFile("chatproxy.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ref, mine) {
		t.Fatal("board/internal/chatproxy/chatproxy.go differs from packages/chat/proxy/go/chatproxy.go: " +
			"change the reference and copy it here (cp packages/chat/proxy/go/chatproxy.go board/internal/chatproxy/)")
	}
}
