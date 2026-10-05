package pluginsinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

const official = "anthropics/claude-plugins-official"

func ent(p string) pluginspec.Entry { return pluginspec.Entry{Marketplace: official, Plugin: p} }

func newTestInstaller(run Runner) *installer {
	allow, _ := pluginspec.ParseAllowlist("")
	return newInstaller(Options{Allow: allow, Run: run, CmdTimeout: time.Second, Total: 5 * time.Second})
}

const listJSON = `[{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"}]`

func TestInstallCommandsAreExactArgLists(t *testing.T) {
	var calls [][]string
	p := newTestInstaller(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "list" {
			return listJSON, nil
		}
		return "", nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Installed != 2 || res.Total != 2 || res.Detail() != "2 of 2 installed" {
		t.Fatalf("res = %+v %q", res, res.Detail())
	}
	want := [][]string{
		{"claude", "plugin", "marketplace", "add", official},
		{"claude", "plugin", "marketplace", "list", "--json"},
		{"claude", "plugin", "install", "frontend-design@claude-plugins-official", "--scope", "user"},
		{"claude", "plugin", "install", "superpowers@claude-plugins-official", "--scope", "user"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if strings.Join(calls[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Errorf("call %d = %v, want %v", i, calls[i], want[i])
		}
	}
}

func TestOneFailureDoesNotStopOthersAndDetailIsFixed(t *testing.T) {
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "install" && strings.HasPrefix(args[2], "frontend-design@") {
			// Raw output that must never reach an event.
			return "fatal: could not read Password for https://x-access-token:SECRET123@github.com", errors.New("exit 1")
		}
		return listJSON, nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("frontend-design"), ent("superpowers")})
	if res.Installed != 1 || res.Total != 2 {
		t.Fatalf("res = %+v", res)
	}
	if got := res.Detail(); got != "1 of 2 installed — frontend-design failed" {
		t.Fatalf("detail = %q", got)
	}
	if strings.Contains(res.Detail(), "SECRET123") {
		t.Fatal("raw output leaked into the detail")
	}
}

func TestMarketplaceAddFailureFailsItsPluginsOnly(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("*")
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 3 && args[2] == "add" && args[3] == "bad/market" {
			return "nope", errors.New("exit 1")
		}
		if args[1] == "marketplace" && args[2] == "list" {
			return `[{"name":"claude-plugins-official","source":"github","repo":"anthropics/claude-plugins-official"},{"name":"bad","source":"github","repo":"bad/market"}]`, nil
		}
		return "", nil
	})
	p.allow = allow
	res := p.install(context.Background(), []pluginspec.Entry{{Marketplace: "bad/market", Plugin: "x"}, ent("superpowers")})
	if res.Installed != 1 || len(res.Failed) != 1 || res.Failed[0] != "x" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDisallowedAndMalformedEntriesAreSkippedNeverRun(t *testing.T) {
	var calls []string
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return listJSON, nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{
		{Marketplace: "evil/plugins", Plugin: "steal"},
		{Marketplace: "../../etc", Plugin: "x"},
		{Marketplace: official, Plugin: "a;rm -rf /"},
		{Marketplace: official, Plugin: "--scope"},
		ent("superpowers"),
	})
	for _, c := range calls {
		if strings.Contains(c, "evil") || strings.Contains(c, "..") || strings.Contains(c, ";") || strings.Contains(c, "--scope --") {
			t.Fatalf("a refused entry reached the CLI: %q", c)
		}
	}
	if res.Installed != 1 || len(res.Skipped) != 4 {
		t.Fatalf("res = %+v", res)
	}
	if want := "1 of 5 installed — steal, x, (invalid entry), (invalid entry) skipped (not allowed)"; res.Detail() != want {
		t.Fatalf("detail = %q, want %q", res.Detail(), want)
	}
	if strings.Contains(res.Detail(), "rm -rf") {
		t.Fatalf("invalid entry text reached the detail: %q", res.Detail())
	}
}

func TestPerCommandTimeoutAndTotalBudget(t *testing.T) {
	p := newTestInstaller(func(ctx context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return listJSON, nil
		}
		if args[1] == "install" {
			<-ctx.Done() // hangs until the per-command timeout fires
			return "", ctx.Err()
		}
		return "", nil
	})
	p.cmdLimit = 50 * time.Millisecond
	start := time.Now()
	res := p.install(context.Background(), []pluginspec.Entry{ent("a"), ent("b")})
	if res.Installed != 0 || len(res.Failed) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("per-command timeout was not applied")
	}

	// The overall budget ends the run: once spent, remaining commands see a dead context.
	p2 := newTestInstaller(func(ctx context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return listJSON, nil
		}
		if args[1] == "install" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", nil
	})
	p2.cmdLimit = 10 * time.Second
	p2.total = 80 * time.Millisecond
	start = time.Now()
	res = p2.install(context.Background(), []pluginspec.Entry{ent("a"), ent("b"), ent("c")})
	if res.Installed != 0 || time.Since(start) > 3*time.Second {
		t.Fatalf("total budget not enforced: %+v after %s", res, time.Since(start))
	}
}

func TestMarketplaceNameFromListMustBeSafe(t *testing.T) {
	var installArg string
	p := newTestInstaller(func(_ context.Context, _ string, args ...string) (string, error) {
		if args[1] == "marketplace" && args[2] == "list" {
			return `[{"name":"--evil flag","source":"github","repo":"anthropics/claude-plugins-official"}]`, nil
		}
		if args[1] == "install" {
			installArg = args[2]
		}
		return "", nil
	})
	res := p.install(context.Background(), []pluginspec.Entry{ent("superpowers")})
	if installArg != "" || res.Installed != 0 {
		t.Fatalf("an unsafe marketplace name reached install: %q %+v", installArg, res)
	}
}

func TestParsePluginsEnv(t *testing.T) {
	if e, err := ParsePluginsEnv(""); e != nil || err != nil {
		t.Fatal("empty is no plugins")
	}
	e, err := ParsePluginsEnv(`[{"marketplace":"a/b","plugin":"c"}]`)
	if err != nil || len(e) != 1 || e[0].Plugin != "c" {
		t.Fatalf("%v %v", e, err)
	}
	for _, bad := range []string{`{`, `{"a":1}`, strings.Repeat("x", maxPluginsEnvBytes+1)} {
		if e, err := ParsePluginsEnv(bad); e != nil || err == nil {
			t.Errorf("%.20q accepted", bad)
		}
	}
	many := "[" + strings.Repeat(`{"marketplace":"a/b","plugin":"c"},`, 20) + `{"marketplace":"a/b","plugin":"c"}]`
	if _, err := ParsePluginsEnv(many); err == nil {
		t.Error("over the cap accepted")
	}
}

// A timeout must kill the whole process group, not just claude: a grandchild that would write a
// marker after the timeout must be dead. Uses a real process tree.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	script := "#!/bin/sh\n(sleep 1; echo alive > \"" + marker + "\") &\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	allow, _ := pluginspec.ParseAllowlist("")
	p := newInstaller(Options{Env: ChildEnv(home), Allow: allow, CmdTimeout: 200 * time.Millisecond, Total: time.Minute})
	start := time.Now()
	_, err := p.step(context.Background(), "test", "plugin", "install", "x@y")
	if err == nil {
		t.Fatal("the hung command should have failed")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
	time.Sleep(1800 * time.Millisecond) // well past when the grandchild would have written
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the grandchild outlived the timeout")
	}
}
