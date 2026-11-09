package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxHostRefreshUsesTrustedLoginOnce(t *testing.T) {
	home := sandboxHomeFixture(t)
	now := time.Now()
	source := filepath.Join(home, ".codex")
	writeFile(t, filepath.Join(source, codexAuthN), codexAuthFixture(fakeJWT(now.Add(-time.Minute)), "host-refresh", "acct-1", now))
	ids := []string{"host-refresh-one", "host-refresh-two"}
	for _, id := range ids {
		dir, err := prepareSandboxHome(id, home, source, sandboxEngines{codex: true})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "codex", codexAuthN), `{"tokens":{"refresh_token":"sandbox-poison"}}`)
		registerSandbox(id, "fixture")
		t.Cleanup(func() { unregisterSandbox(id); releaseSandboxHome(id) })
	}
	old := engineRegistry["codex"]
	spec := old
	calls := 0
	spec.RefreshHostLogin = func(ctx context.Context, path string) error {
		calls++
		if path != source {
			t.Fatal("untrusted source")
		}
		raw, err := os.ReadFile(filepath.Join(path, codexAuthN))
		if err != nil || strings.Contains(string(raw), "sandbox-poison") {
			t.Fatal("sandbox login used")
		}
		writeFile(t, filepath.Join(path, codexAuthN), codexAuthFixture(fakeJWT(now.Add(time.Hour)), "rotated-host-refresh", "acct-1", now))
		return nil
	}
	engineRegistry["codex"] = spec
	t.Cleanup(func() { engineRegistry["codex"] = old })
	refreshNeededSandboxHostLogins(now, home, source)
	refreshLiveSandboxLogins(now)
	refreshLiveSandboxLogins(now.Add(15 * time.Second))
	if calls != 1 {
		t.Fatalf("refresh calls %d", calls)
	}
	for _, id := range ids {
		dir, _ := sandboxHomePath(id)
		raw, err := os.ReadFile(filepath.Join(dir, "codex", codexAuthN))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "rotated-host-refresh") || strings.Contains(string(raw), "sandbox-poison") || !strings.Contains(string(raw), sandboxNoRefresh) {
			t.Fatal("refresh boundary failed")
		}
		if !strings.Contains(string(raw), fakeJWT(now.Add(time.Hour))) {
			t.Fatal("new access missing")
		}
	}
}

func TestCodexHostRefreshNativeProtocol(t *testing.T) {
	for _, mode := range []string{"success", "refused", "blocked", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(home, ".codex")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := `#!/bin/sh
set -eu
printf '%s\n' "$PWD" > "$CODEX_HOME/cwd"
if [ "${BLERG_RUNNER_DAEMON_TOKEN:-}" != "" ]; then exit 9; fi
while IFS= read -r line; do
 printf '%s\n' "$line" >> "$CODEX_HOME/calls"
 case "$line" in
 *'"method":"initialize"'*) printf '%s\n' '{"id":1,"result":{}}' ;;
 *'"method":"account/read"'*)
  case "$line" in *'"refreshToken":true'*) ;; *) exit 8 ;; esac
  case ` + mode + ` in
   success) printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"}}}' ;;
   refused) printf '%s\n' 'native-private-secret' >&2; printf '%s\n' '{"id":2,"error":{"message":"native-private-secret"}}' ;;
   blocked) sleep 30 ;;
   oversize) head -c 300000 /dev/zero | tr '\000' x ;;
  esac
 ;;
 esac
done
`
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", home)
			t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "daemon-private-secret")
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			started := time.Now()
			err := refreshCodexHostLogin(ctx, source)
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			if mode != "success" && (err == nil || strings.Contains(err.Error(), "private-secret")) {
				t.Fatal("unsafe failure")
			}
			if mode == "refused" && hostLoginRefreshCause(err) != "protocol" {
				t.Fatal("missing protocol failure cause")
			}
			if mode == "oversize" && hostLoginRefreshCause(err) != "read" {
				t.Fatal("missing bounded-read failure cause")
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("refresh did not obey its deadline")
			}
			raw, readErr := os.ReadFile(filepath.Join(source, "calls"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 3 || strings.Contains(string(raw), "thread/") || strings.Contains(string(raw), "turn/") {
				t.Fatal("refresh started non-auth work")
			}
			cwd, readErr := os.ReadFile(filepath.Join(source, "cwd"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err := os.Stat(strings.TrimSpace(string(cwd))); !os.IsNotExist(err) {
				t.Fatal("native working directory was retained")
			}
		})
	}
}

func TestSandboxHostRefreshBackoffAndDemand(t *testing.T) {
	logs := captureLog(t)
	home := sandboxHomeFixture(t)
	now := time.Now()
	source := filepath.Join(home, ".codex")
	writeFile(t, filepath.Join(source, codexAuthN), codexAuthFixture(fakeJWT(now.Add(-time.Minute)), "host-refresh", "acct-1", now))
	old := engineRegistry["codex"]
	spec := old
	calls := 0
	spec.RefreshHostLogin = func(context.Context, string) error { calls++; return errors.New("native-private-secret") }
	engineRegistry["codex"] = spec
	t.Cleanup(func() { engineRegistry["codex"] = old })
	refreshNeededSandboxHostLogins(now, home, source)
	if calls != 0 {
		t.Fatal("refresh without demand")
	}
	id := "refresh-backoff"
	_, err := prepareSandboxHome(id, home, source, sandboxEngines{codex: true})
	if err != nil {
		t.Fatal(err)
	}
	registerSandbox(id, "fixture")
	t.Cleanup(func() { unregisterSandbox(id); releaseSandboxHome(id) })
	refreshNeededSandboxHostLogins(now, home, source)
	refreshNeededSandboxHostLogins(now.Add(15*time.Second), home, source)
	if calls != 1 {
		t.Fatalf("backoff calls %d", calls)
	}
	refreshNeededSandboxHostLogins(now.Add(time.Minute), home, source)
	if calls != 2 {
		t.Fatalf("retry calls %d", calls)
	}
	if strings.Count(logs.String(), "host_login_refresh engine=codex cause=unavailable") != 2 || strings.Contains(logs.String(), "native-private-secret") || strings.Contains(logs.String(), source) {
		t.Fatalf("missing safe, rate-limited failure diagnostic: %s", logs.String())
	}
}

func TestHostRefreshDoesNotBlockLoginCopies(t *testing.T) {
	home := sandboxHomeFixture(t)
	now := time.Now()
	source := filepath.Join(home, ".codex")
	writeFile(t, filepath.Join(source, codexAuthN), codexAuthFixture(fakeJWT(now.Add(-time.Minute)), "host-refresh", "acct-1", now))
	id := "refresh-blocked-copy"
	if _, err := prepareSandboxHome(id, home, source, sandboxEngines{codex: true}); err != nil {
		t.Fatal(err)
	}
	registerSandbox(id, "fixture")
	defer func() { unregisterSandbox(id); releaseSandboxHome(id) }()
	old := engineRegistry["codex"]
	spec := old
	release := make(chan struct{})
	started := make(chan struct{})
	spec.RefreshHostLogin = func(context.Context, string) error { close(started); <-release; return errHostLoginRefresh }
	engineRegistry["codex"] = spec
	defer func() { engineRegistry["codex"] = old }()
	nativeDone := make(chan struct{})
	go func() { refreshNeededSandboxHostLogins(now, home, source); close(nativeDone) }()
	<-started
	done := make(chan struct{})
	go func() { refreshLiveSandboxLogins(now); close(done) }()
	blocked := false
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		blocked = true
	}
	close(release)
	<-nativeDone
	<-done
	if blocked {
		t.Fatal("native refresh held the login copy loop")
	}
}

// A host session can rotate while the daemon's native auth request is in flight.
// The fake native client re-reads that host state instead of persisting stale credentials.
func TestCodexHostRefreshConcurrentHostRotation(t *testing.T) {
	home := sandboxHomeFixture(t)
	now := time.Now()
	source := filepath.Join(home, ".codex")
	auth := filepath.Join(source, codexAuthN)
	writeFile(t, auth, codexAuthFixture(fakeJWT(now.Add(-time.Minute)), "original-refresh", "acct-1", now))
	id := "concurrent-host-rotation"
	dir, err := prepareSandboxHome(id, home, source, sandboxEngines{codex: true})
	if err != nil {
		t.Fatal(err)
	}
	registerSandbox(id, "fixture")
	defer func() { unregisterSandbox(id); releaseSandboxHome(id) }()
	old := engineRegistry["codex"]
	spec := old
	started, rotated := make(chan struct{}), make(chan struct{})
	calls := 0
	spec.RefreshHostLogin = func(context.Context, string) error {
		calls++
		close(started)
		<-rotated
		raw, err := os.ReadFile(auth)
		if err != nil || !strings.Contains(string(raw), "host-session-rotated") {
			return errHostLoginRefresh
		}
		return nil
	}
	engineRegistry["codex"] = spec
	defer func() { engineRegistry["codex"] = old }()
	done := make(chan struct{})
	go func() { refreshNeededSandboxHostLogins(now, home, source); close(done) }()
	<-started
	next := codexAuthFixture(fakeJWT(now.Add(time.Hour)), "host-session-rotated", "acct-1", now)
	writeFile(t, auth+".next", next)
	if err := os.Rename(auth+".next", auth); err != nil {
		close(rotated)
		<-done
		t.Fatal(err)
	}
	close(rotated)
	<-done
	refreshNeededSandboxHostLogins(now.Add(time.Minute), home, source)
	refreshLiveSandboxLogins(now)
	raw, err := os.ReadFile(filepath.Join(dir, "codex", codexAuthN))
	if err != nil || !strings.Contains(string(raw), fakeJWT(now.Add(time.Hour))) || strings.Contains(string(raw), "host-session-rotated") {
		t.Fatal("rotated access was not safely copied")
	}
	if calls != 1 {
		t.Fatalf("duplicate native rotation: %d", calls)
	}
}

func TestHostLoginRefreshCauseAllowlist(t *testing.T) {
	for _, cause := range []string{"source", "temporary_directory", "stdin", "stdout", "start", "write", "protocol", "read", "account"} {
		if got := hostLoginRefreshCause(hostLoginRefreshFailure(cause)); got != cause {
			t.Fatalf("cause = %q, want %q", got, cause)
		}
	}
	for _, err := range []error{errors.New("native-private-secret"), hostLoginRefreshFailure("native-private-secret"), errHostLoginRefresh} {
		if got := hostLoginRefreshCause(err); got != "unavailable" {
			t.Fatalf("unsafe cause: %q", got)
		}
	}
}
