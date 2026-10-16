package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHistoryOpenedRootCannotBeRedirectedByDirectorySwap(t *testing.T) {
	parent, work := t.TempDir(), t.TempDir()
	dir, retained, replacement := filepath.Join(parent, "identity"), filepath.Join(parent, "retained"), filepath.Join(parent, "replacement")
	for _, path := range []string{dir, replacement} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	k, err := openNativeHistory(NativeHistoryConfig{Directory: dir, AccountID: "fixture-owner", ClaimUID: "fixture-pvc"}, "fixture-session", "codex", work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.root.Close() }()
	if err = os.Rename(dir, retained); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(replacement, dir); err != nil {
		t.Fatal(err)
	}
	if err = k.setNativeID("fixture-native-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(replacement, "conversation.json")); !os.IsNotExist(err) {
		t.Fatal("opened keeper followed a swapped directory into replacement storage")
	}
	raw, err := os.ReadFile(filepath.Join(retained, "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved nativeConversation
	if err = json.Unmarshal(raw, &saved); err != nil || saved.NativeID != "fixture-native-thread" {
		t.Fatal("original opened identity was not updated")
	}
	if other, err := openNativeHistory(NativeHistoryConfig{Directory: dir, AccountID: "fixture-owner", ClaimUID: "fixture-pvc"}, "fixture-session", "codex", work); err == nil {
		_ = other.root.Close()
		t.Fatal("new keeper accepted a symlinked identity root")
	}
}
