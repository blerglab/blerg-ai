package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A session's messaging token normally lives in its environment
// (BLERG_RUNNER_SESSION_TOKEN), fixed when the process started. When the
// server finds a session running without a valid token (a start race revoked
// it) it sends a fresh one as session_token; the daemon cannot change a running
// process's environment, so it writes the token to a file the `blerg-runner`
// CLI reads before the variable:
//
//	<state dir>/session-tokens/<session id>
//
// where <state dir> is $BLERG_RUNNER_DAEMON_STATE_DIR, else ~/.blerg-runner-daemon
// (DaemonStateDir). A session on the host shares the daemon's HOME and
// environment, so the CLI derives the same path from its own
// BLERG_RUNNER_SESSION_ID with no new variable — which is what lets a session
// that is already running pick it up. A sandboxed session gets the same file
// at the same place under the container's HOME (/home/agent), written through
// `docker exec`. The file is 0600 in a 0700 directory, outside every repo; the
// session's end removes it.

// sessionTokensDir is the directory, inside the state directory, of the token
// files. Its name is part of the CLI's contract: keep it stable.
const sessionTokensDir = "session-tokens"

// SessionTokenPath is sessionID's token file under stateDir ("" when stateDir
// is "" or sessionID is not a safe file name).
func SessionTokenPath(stateDir, sessionID string) string {
	if stateDir == "" || sessionID == "" || safeFileName(sessionID) != sessionID {
		return ""
	}
	return filepath.Join(stateDir, sessionTokensDir, sessionID)
}

// writeSessionTokenFile writes token to sessionID's token file under stateDir,
// atomically (a reader sees the old token or the new one, never a torn file)
// and private to the daemon's user.
func writeSessionTokenFile(stateDir, sessionID, token string) error {
	path := SessionTokenPath(stateDir, sessionID)
	if path == "" {
		return errors.New("no state directory or an unsafe session id")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory's mode alone.
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs its execute bit; 0700 is private to the daemon's user
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+sessionID+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removeSessionTokenFile removes sessionID's token file, if any. Best effort.
func removeSessionTokenFile(stateDir, sessionID string) {
	if path := SessionTokenPath(stateDir, sessionID); path != "" {
		_ = os.Remove(path)
	}
}

// sandboxTokenScript writes stdin to the token file under the container's
// own HOME; $1 is the session id. The token never appears in an argv.
const sandboxTokenScript = `umask 077; d="$HOME/.blerg-runner-daemon/` + sessionTokensDir + `"; ` +
	`mkdir -p "$d" && cat > "$d/.$1.tmp" && mv -f "$d/.$1.tmp" "$d/$1"`

// writeSandboxTokenFile writes token into container at the path the CLI
// inside it reads. A var so tests can record it without Docker.
var writeSandboxTokenFile = func(container, sessionID, token string) error {
	cmd := exec.Command("docker", "exec", "-i", container, "sh", "-c", sandboxTokenScript, "sh", sessionID) //nolint:gosec // literal docker binary; container and session id are daemon-generated/validated; the script is a constant and the token goes on stdin
	cmd.Stdin = strings.NewReader(token + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handleSessionToken stores a fresh messaging token the server sent for a
// session (protocol.SessionToken) where the session's `blerg-runner` reads
// it. Never logs the token.
func (m *Manager) handleSessionToken(raw []byte) {
	var msg protocol.SessionToken
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad session_token: %v", err)
		return
	}
	if msg.SessionID == "" || safeFileName(msg.SessionID) != msg.SessionID ||
		msg.Token == "" || strings.ContainsAny(msg.Token, "\r\n\x00") {
		log.Printf("manager: session_token for %q refused: malformed", msg.SessionID)
		return
	}
	// The host file is written for a sandboxed session too: it is what a
	// container made again later (a restart's recovery) is given
	// (copyTokenIntoSandbox) — do not drop it as redundant.
	result := protocol.SessionTokenResult{Type: "session_token_result", SessionID: msg.SessionID, OK: true}
	if err := writeSessionTokenFile(DaemonStateDir(), msg.SessionID, msg.Token); err != nil {
		log.Printf("manager: session %s: storing its fresh token: %v", msg.SessionID, err)
		result.OK, result.Error = false, "storing the token on the host failed"
	} else {
		log.Printf("manager: session %s: fresh messaging token stored", msg.SessionID)
	}
	container := dockerContainerName(msg.SessionID)
	if result.OK && slices.Contains(listSandboxContainers(), container) {
		if err := writeSandboxTokenFile(container, msg.SessionID, msg.Token); err != nil {
			log.Printf("manager: session %s: storing its fresh token in the sandbox: %v", msg.SessionID, err)
			result.OK, result.Error = false, "storing the token in the session's sandbox failed"
		}
	}
	// The server revokes a token it hears was not stored (or hears nothing
	// about), so its next heal can try again.
	if m.client != nil {
		if err := m.client.Send(result); err != nil {
			log.Printf("manager: session %s: answering session_token: %v", msg.SessionID, err)
		}
	}
}

// copyTokenIntoSandbox gives a newly made container the session's re-issued
// token, when there is one: the container's own files are new, so the copy a
// previous container held is gone. Best effort, logged; never the token.
func copyTokenIntoSandbox(container, sessionID string) {
	path := SessionTokenPath(DaemonStateDir(), sessionID)
	if path == "" {
		return
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the daemon's own state directory and a validated session id
	if err != nil {
		return // none: the session's environment token is its token
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return
	}
	if err := writeSandboxTokenFile(container, sessionID, tok); err != nil {
		log.Printf("sandbox: session %s: copying its re-issued token in: %v", sessionID, err)
	}
}
