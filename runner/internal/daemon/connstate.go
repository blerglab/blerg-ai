package daemon

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The states connection.json reports.
const (
	// ConnStateConnecting: dialling, or reconnecting after the link dropped.
	ConnStateConnecting = "connecting"
	// ConnStateConnected: the server sent daemon_welcome — the token was accepted.
	ConnStateConnected = "connected"
	// ConnStateUnconfirmed: the link stayed open for the unconfirmed window and
	// the server sent no welcome (a server older than 0.2.1); taken as accepted.
	ConnStateUnconfirmed = "unconfirmed"
	// ConnStateRefused: the server closed the link with 4401, the token was
	// refused. It stays the state while the daemon retries, until a link is up.
	ConnStateRefused = "refused"
)

// ConnectionState is connection.json: what the daemon's link to the server is
// doing now, for the installer and `install.sh status` to read. Never holds a
// credential.
type ConnectionState struct {
	State string `json:"state"`
	// Server is the URL dialled, without userinfo or query.
	Server string `json:"server"`
	// Since is when State began (RFC3339).
	Since string `json:"since"`
	// Detail is a short reason, in plain words.
	Detail string `json:"detail"`
	// PID is the daemon process that wrote the file: the installer accepts only
	// the file of the process it just started.
	PID int `json:"pid"`
}

// ConnectionFilePath is connection.json inside stateDir ("" for "").
func ConnectionFilePath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "connection.json")
}

// redactServerURL is the server URL as the file and the log show it: no
// userinfo, query or fragment, in case a credential was put there.
func redactServerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(the configured server URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// connReporter keeps the connection state and writes it to path on every
// change ("" = keep it in memory only: a session pod writes no file).
type connReporter struct {
	path   string
	server string

	mu       sync.Mutex
	state    string
	detail   string
	since    time.Time
	writeErr bool // a failed write was logged; say it again only after one succeeds
}

func newConnReporter(path, serverURL string) *connReporter {
	return &connReporter{path: path, server: redactServerURL(serverURL)}
}

// current is the state now ("" before the first set).
func (r *connReporter) current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// set records state and detail, and rewrites the file when either changed.
// Since moves only when the state does.
func (r *connReporter) set(state, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state == r.state && detail == r.detail {
		return
	}
	if state != r.state {
		r.since = time.Now()
	}
	r.state, r.detail = state, detail
	if r.path == "" {
		return
	}
	err := writeFileAtomic(r.path, ConnectionState{
		State: state, Server: r.server, Since: r.since.UTC().Format(time.RFC3339), Detail: detail, PID: os.Getpid(),
	})
	switch {
	case err != nil && !r.writeErr:
		log.Printf("daemon: cannot write the connection state to %s: %v", r.path, err)
		r.writeErr = true
	case err == nil:
		r.writeErr = false
	}
}

// writeFileAtomic writes v as JSON to path through a temporary file in the
// same directory and a rename, mode 0600 in a 0700 directory, so a reader
// never sees half a file.
func writeFileAtomic(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".connection-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
