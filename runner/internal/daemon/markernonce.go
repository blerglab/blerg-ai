package daemon

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// markerKeyFile holds the daemon's key for session marker codes (agent/inputtext.go). A session's
// code is derived from it, so nothing the server, a browser or another session sees can produce
// the code, and the same session gets the same code for as long as the key is kept. That matters
// for Claude: the CLI records a conversation's system prompt (the code's announcement included)
// on its first request and resumes with that record until the conversation is compacted, so a
// new key would mark every later relay with a code the resumed model was told is not Blerg's.
// The key is therefore kept where the conversation is kept: the daemon's state directory on a
// desktop, the retained identity volume in a cluster pod with native history (markerKeyDirFor).
const markerKeyFile = "marker.key"

var (
	markerKeysMu sync.Mutex
	markerKeys   = map[string][]byte{} // by directory; "" is this process's in-memory key
	// markerKeyDir is the desktop's key directory; tests keep the key in memory ("").
	markerKeyDir = DaemonStateDir
)

// markerKeyDirFor is where this host keeps the marker key: next to a cluster pod's retained
// conversation when there is one, else the daemon's state directory.
func (h *AgentHost) markerKeyDirFor() string {
	if h.cfg.NativeHistory != nil && h.cfg.NativeHistory.Directory != "" {
		return h.cfg.NativeHistory.Directory
	}
	return markerKeyDir()
}

// sessionMarkerNonce is the marker code of a session: HMAC-SHA256 of its id under the marker key
// kept in dir, 16 hex characters.
func sessionMarkerNonce(dir, sessionID string) string {
	markerKeysMu.Lock()
	key, ok := markerKeys[dir]
	if !ok {
		key = loadMarkerKey(dir)
		markerKeys[dir] = key
	}
	markerKeysMu.Unlock()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("blerg-marker\x00" + sessionID))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// loadMarkerKey reads the key in dir, or creates it (0600, in a 0700 directory). With no
// directory, or one it cannot use, the key lives in memory for this process only, and it says so
// once: a session resumed by a later process then gets a new code.
func loadMarkerKey(dir string) []byte {
	if dir != "" {
		key, err := persistedMarkerKey(filepath.Join(dir, markerKeyFile))
		if err == nil {
			return key
		}
		log.Printf("marker key: cannot keep it in the state directory (%v); using an in-memory key, so a session resumed after this process ends gets a new marker code", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("daemon: no randomness for the marker key: " + err.Error())
	}
	return key
}

func persistedMarkerKey(path string) ([]byte, error) {
	if key, err := readMarkerKey(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the daemon's own state file
	if errors.Is(err, os.ErrExist) {
		return readMarkerKey(path) // another daemon process wrote it first
	}
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(key)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(path)
		return nil, errors.Join(werr, cerr)
	}
	return key, nil
}

// readMarkerKey accepts only a private regular file of exactly 32 bytes.
func readMarkerKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() != 32 {
		return nil, errors.New("the marker key file is not a private 32-byte file")
	}
	return os.ReadFile(path) //nolint:gosec // the daemon's own state file, checked above
}
