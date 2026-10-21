package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// writePrivateOnHost writes body to <fresh 0700 dir>/name (0600) under $TMPDIR, never under a
// workdir, and returns the directory and the file's path.
func writePrivateOnHost(dirPrefix, name string, body []byte) (dir, path string, err error) {
	dir, err = os.MkdirTemp("", dirPrefix) // 0700, unique
	if err != nil {
		return "", "", fmt.Errorf("could not create a private directory: %w", err)
	}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil { //nolint:gosec // G703: a fixed name in the fresh, private MkdirTemp directory above.
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("could not write a private file: %w", err)
	}
	return dir, path, nil
}

// writePrivateInContainer is writePrivateOnHost inside a sandbox container (under
// sandboxMCPDirBase). The body goes on stdin, never on an argv.
func writePrivateInContainer(container, dirPrefix, name string, body []byte) (dir, path string, err error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", "", err
	}
	dir = sandboxMCPDirBase + "/" + dirPrefix + hex.EncodeToString(rnd[:])
	// mkdir -m 700 (no -p) fails if the name exists, so nobody else's directory is adopted; the
	// umask makes the file 0600. The directory and name are arguments, not interpolated.
	const script = `umask 077 && mkdir -m 700 "$1" && cat > "$1/$2"`
	if err := dockerRunStdin(body, "exec", "-i", container, "sh", "-c", script, "sh", dir, name); err != nil {
		return "", "", fmt.Errorf("could not write a private file inside the sandbox: %w", err)
	}
	return dir, dir + "/" + name, nil
}

// privateFile is a 0600 file with fixed content in its own private directory, on the host or
// inside the session's sandbox, for what must not go on a command line. Ensure writes it (again,
// when it vanished); Remove deletes it for good.
type privateFile struct {
	dirPrefix, name string
	prefix          sandboxExec

	mu     sync.Mutex
	body   string
	dir    string
	path   string
	closed bool
}

func newPrivateFile(dirPrefix, name string, prefix sandboxExec) *privateFile {
	return &privateFile{dirPrefix: dirPrefix, name: name, prefix: prefix}
}

// Ensure returns the path of a file holding body, writing it when it is missing or held other text.
func (f *privateFile) Ensure(body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return "", fmt.Errorf("the session has ended")
	}
	if f.path != "" && f.body == body && f.existsLocked() {
		return f.path, nil
	}
	f.removeLocked()
	var err error
	if f.prefix.enabled() {
		f.dir, f.path, err = writePrivateInContainer(f.prefix.container(), f.dirPrefix, f.name, []byte(body))
	} else {
		f.dir, f.path, err = writePrivateOnHost(f.dirPrefix, f.name, []byte(body))
	}
	if err != nil {
		f.dir, f.path = "", ""
		return "", err
	}
	f.body = body
	return f.path, nil
}

// Remove deletes the file and its directory, and keeps Ensure from writing it again. Safe on nil
// and twice.
func (f *privateFile) Remove() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.removeLocked()
}

func (f *privateFile) existsLocked() bool {
	if f.prefix.enabled() {
		return dockerRun("exec", f.prefix.container(), "test", "-f", f.path) == nil
	}
	st, err := os.Stat(f.path)
	return err == nil && st.Mode().IsRegular()
}

func (f *privateFile) removeLocked() {
	if f.dir == "" {
		return
	}
	if f.prefix.enabled() {
		_ = dockerRun("exec", f.prefix.container(), "rm", "-rf", f.dir)
	} else {
		_ = os.RemoveAll(f.dir) //nolint:gosec // G703: f.dir is created only by this object's MkdirTemp.
	}
	f.dir, f.path = "", ""
}
