package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The marker key is kept in its directory, private, and read back on the next start, so a
// session's code survives a daemon restart.
func TestMarkerKeyIsKeptPrivatelyAndReused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	a := loadMarkerKey(dir)
	st, err := os.Stat(filepath.Join(dir, markerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || len(a) != 32 {
		t.Fatalf("key file mode %v, key length %d", st.Mode().Perm(), len(a))
	}
	if b := loadMarkerKey(dir); !bytes.Equal(a, b) {
		t.Fatal("a restart drew a new key")
	}
	first, again, other := sessionMarkerNonce(dir, "s1"), sessionMarkerNonce(dir, "s1"), sessionMarkerNonce(dir, "s2")
	if first != again || first == other || len(first) != 16 {
		t.Fatal("session codes are not stable per session")
	}
}

// With nowhere to keep the key (no directory, a directory it cannot write, a key file that is
// not private), the key lives in memory, and the daemon says so once.
func TestMarkerKeyFallsBackToMemoryAndSaysSo(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if c := loadMarkerKey(""); len(c) != 32 || logs.Len() != 0 {
		t.Fatalf("no state directory: key length %d, log %q", len(c), logs.String())
	}
	readOnly := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 { // root writes anyway
		logs.Reset()
		if c := loadMarkerKey(readOnly); len(c) != 32 || !strings.Contains(logs.String(), "in-memory key") {
			t.Fatalf("unwritable directory: key length %d, log %q", len(c), logs.String())
		}
	}
	loose := t.TempDir()
	if err := os.WriteFile(filepath.Join(loose, markerKeyFile), bytes.Repeat([]byte{1}, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if c := loadMarkerKey(loose); bytes.Equal(c, bytes.Repeat([]byte{1}, 32)) || !strings.Contains(logs.String(), "in-memory key") {
		t.Fatalf("a world-readable key file was used (log %q)", logs.String())
	}
}

// A cluster pod keeps the key next to its retained conversation (the native-history identity
// volume), so a replacement pod resuming the conversation derives the same code; without native
// history it is the daemon's state directory.
func TestMarkerKeyLivesWithTheRetainedConversation(t *testing.T) {
	identity := t.TempDir()
	pod := &AgentHost{cfg: AgentHostConfig{NativeHistory: &NativeHistoryConfig{Directory: identity}}}
	if got := pod.markerKeyDirFor(); got != identity {
		t.Fatalf("pod key dir = %q, want the identity volume", got)
	}
	code := sessionMarkerNonce(pod.markerKeyDirFor(), "s-pod")
	if _, err := os.Stat(filepath.Join(identity, markerKeyFile)); err != nil {
		t.Fatalf("no key on the identity volume: %v", err)
	}
	// A replacement pod: a new process (no cached key) with the same volume.
	markerKeysMu.Lock()
	delete(markerKeys, identity)
	markerKeysMu.Unlock()
	replacement := &AgentHost{cfg: AgentHostConfig{NativeHistory: &NativeHistoryConfig{Directory: identity}}}
	if sessionMarkerNonce(replacement.markerKeyDirFor(), "s-pod") != code {
		t.Fatal("a replacement pod derived a different code for the same session")
	}
	desktop := &AgentHost{}
	if got := desktop.markerKeyDirFor(); got != markerKeyDir() {
		t.Fatalf("desktop key dir = %q", got)
	}
}
