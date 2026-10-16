package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// fakeAppServer writes a stand-in `codex` that speaks just enough of the
// app-server protocol: it checks the handshake and the read in order, answers
// with the given result line, and then hangs (the reader must not wait for it).
// Before the answer it sends a notification and a request of its own that
// carries our request's id: neither is the answer.
func fakeAppServer(t *testing.T, answer string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
[ "$1" = "app-server" ] || exit 2
read -r init
case "$init" in *'"method":"initialize"'*'"clientInfo"'*) ;; *) exit 3;; esac
printf '%s\n' '{"id":1001,"result":{"userAgent":"fake/0.154"}}'
read -r initialized
case "$initialized" in *'"method":"initialized"'*) ;; *) exit 4;; esac
read -r req
case "$req" in *'"method":"account/rateLimits/read"'*) ;; *) exit 5;; esac
printf '%s\n' '{"method":"account/rateLimits/updated","params":{},"emittedAtMs":1}'
printf '%s\n' '{"id":1002,"method":"item/tool/requestUserInput","params":{}}'
printf '%s\n' '` + answer + `'
sleep 30
`
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

const codexLimitsAnswer = `{"id":1002,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1792159200},"secondary":{"usedPercent":7.5,"windowDurationMins":10080,"resetsAt":1792600000},"credits":{"balance":"12"},"planType":"plus","rateLimitReachedType":null},"rateLimitsByLimitId":{},"accountId":"acct-SECRET-123"}}`

func TestReadCodexRateLimitsFromAppServer(t *testing.T) {
	bin := fakeAppServer(t, codexLimitsAnswer)
	start := time.Now()
	got, err := readCodexRateLimits(context.Background(), bin, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the read waited for the app-server to exit (%v)", time.Since(start))
	}
	want := []protocol.UsageWindow{
		{ID: "5h", UsedPercent: 42, ResetsAt: time.Unix(1792159200, 0).UTC()},
		{ID: "7d", UsedPercent: 7.5, ResetsAt: time.Unix(1792600000, 0).UTC()},
	}
	if len(got) != len(want) {
		t.Fatalf("windows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].UsedPercent != want[i].UsedPercent || !got[i].ResetsAt.Equal(want[i].ResetsAt) {
			t.Fatalf("window %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Only percentages and reset times: nothing of the account leaves the reader.
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"acct-SECRET-123", "plus", "balance"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%q leaked into the report: %s", secret, raw)
		}
	}
}

func TestReadCodexRateLimitsFailures(t *testing.T) {
	for name, answer := range map[string]string{
		"an error answer": `{"id":1002,"error":{"code":-32600,"message":"not logged in"}}`,
		"no windows":      `{"id":1002,"result":{"rateLimits":{"primary":null,"secondary":null}}}`,
		"not json":        `garbage`,
		"another id only": `{"id":7,"result":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			bin := fakeAppServer(t, answer)
			start := time.Now()
			got, err := readCodexRateLimits(context.Background(), bin, 2*time.Second)
			if err == nil {
				t.Fatalf("read = %+v, want an error", got)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("a failed read took %v", time.Since(start))
			}
		})
	}
	// No binary at all.
	if _, err := readCodexRateLimits(context.Background(), filepath.Join(t.TempDir(), "codex"), time.Second); err == nil {
		t.Fatal("a missing binary read without an error")
	}
}

// Window ids come from the window's length; an unknown length falls back to
// its position (Codex's primary is the short window, secondary the weekly).
func TestCodexWindowIDs(t *testing.T) {
	for _, c := range []struct {
		mins     int
		fallback string
		want     string
	}{{300, "7d", "5h"}, {10080, "5h", "7d"}, {0, "5h", "5h"}, {60, "7d", "7d"}} {
		if got := codexWindowID(c.mins, c.fallback); got != c.want {
			t.Errorf("codexWindowID(%d, %q) = %q, want %q", c.mins, c.fallback, got, c.want)
		}
	}
}
