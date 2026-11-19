package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeWarnsOnceAndNeverKillsHostTerminals(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("LEGACY_TMUX_CALLS", calls)
	bin := writeScript(t, "tmux", `printf '%s\n' "$*" >> "$LEGACY_TMUX_CALLS"
printf '%s\n' 'blerg-legacy' 'unrelated'
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var logs syncBuffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	var warning LegacyTerminalWarning
	warning.Warn()
	warning.Warn()
	raw, err := os.ReadFile(calls)
	if err != nil || string(raw) != "list-sessions -F #S\n" {
		t.Fatalf("expected one read-only tmux probe, got %q: %v", raw, err)
	}
	if strings.Count(logs.String(), "blerg-legacy") != 1 || strings.Contains(logs.String(), "unrelated") {
		t.Fatalf("surviving Terminal warning: %s", logs.String())
	}
	var next LegacyTerminalWarning
	next.Warn()
	next.Warn()
	raw, err = os.ReadFile(calls)
	if err != nil || string(raw) != "list-sessions -F #S\nlist-sessions -F #S\n" || strings.Count(logs.String(), "blerg-legacy") != 2 {
		t.Fatalf("a new daemon instance did not warn once: calls %q, error %v, logs %s", raw, err, logs.String())
	}
}
