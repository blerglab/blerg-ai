package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeDockerConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSandboxNoProxyIsTheUnionOfDaemonEnvDockerConfigAndServiceNames(t *testing.T) {
	dir := writeDockerConfig(t, `{"proxies":{"default":{"httpProxy":"http://proxy.invalid:3128","noProxy":"corp.invalid, .internal.invalid,localhost"}}}`)
	got := sandboxNoProxy(envMap(map[string]string{
		"NO_PROXY":      "192.0.2.0/24,corp.invalid",
		"no_proxy":      "extra.invalid",
		"DOCKER_CONFIG": dir,
	}), "/nonexistent-home")
	want := "192.0.2.0/24,corp.invalid,extra.invalid,.internal.invalid,localhost,blerg-runner,blerg-board,127.0.0.1"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSandboxNoProxyStarIsTheWholeValue(t *testing.T) {
	if got := sandboxNoProxy(envMap(map[string]string{"NO_PROXY": "corp.invalid,*"}), t.TempDir()); got != "*" {
		t.Errorf("got %q, want *", got)
	}
}

func TestSandboxNoProxyWithNothingConfigured(t *testing.T) {
	got := sandboxNoProxy(envMap(nil), t.TempDir())
	if got != "blerg-runner,blerg-board,localhost,127.0.0.1" {
		t.Errorf("got %s", got)
	}
}

func TestDockerClientNoProxyFollowsTheClientsLookup(t *testing.T) {
	body := `{"proxies":{"default":{"noProxy":"from-default.invalid"},
		"unix:///var/run/docker.sock":{"noProxy":"from-socket.invalid"},
		"tcp://remote.invalid:2376":{"noProxy":"from-remote.invalid"}}}`
	dir := writeDockerConfig(t, body)
	for _, tc := range []struct {
		name, host, want string
	}{
		{"no DOCKER_HOST uses the default unix socket key", "", "from-socket.invalid"},
		{"DOCKER_HOST with its own key", "tcp://remote.invalid:2376", "from-remote.invalid"},
		{"DOCKER_HOST without a key falls back to default", "tcp://other.invalid:2376", "from-default.invalid"},
	} {
		got := dockerClientNoProxy(envMap(map[string]string{"DOCKER_CONFIG": dir, "DOCKER_HOST": tc.host}), "")
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestDockerClientNoProxyIgnoresErrors(t *testing.T) {
	bad := writeDockerConfig(t, `{not json`)
	for name, env := range map[string]map[string]string{
		"missing file":  {"DOCKER_CONFIG": t.TempDir()},
		"broken json":   {"DOCKER_CONFIG": bad},
		"no config dir": {},
	} {
		if got := dockerClientNoProxy(envMap(env), ""); got != "" {
			t.Errorf("%s: got %q", name, got)
		}
	}
	// With no DOCKER_CONFIG the file is ~/.docker/config.json.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(`{"proxies":{"default":{"noProxy":"home.invalid"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dockerClientNoProxy(envMap(nil), home); got != "home.invalid" {
		t.Errorf("home config: got %q", got)
	}
}

// The argv is what decides: Docker injects its configured NO_PROXY only for an exact-case key
// no -e names, so both spellings must be emitted as -e pairs, and only on the sandbox network.
func TestSandboxRunArgsEmitNoProxyPairsOnlyWithTheNetwork(t *testing.T) {
	joined := func(extras sandboxRunExtras) string {
		return " " + strings.Join(sandboxRunArgs("c", "/p", "", nil, extras), " ") + " "
	}
	on := joined(sandboxRunExtras{network: "blerg-sandbox", noProxy: "a.invalid,blerg-runner"})
	for _, want := range []string{" -e NO_PROXY=a.invalid,blerg-runner ", " -e no_proxy=a.invalid,blerg-runner "} {
		if !strings.Contains(on, want) {
			t.Errorf("missing %q in %s", want, on)
		}
	}
	if off := joined(sandboxRunExtras{}); strings.Contains(strings.ToLower(off), "no_proxy") {
		t.Errorf("no network, yet NO_PROXY was set: %s", off)
	}
	// The session env cannot smuggle a proxy variable past the allowlist.
	args := strings.Join(sandboxRunArgs("c", "/p", "", []string{"NO_PROXY=x", "HTTP_PROXY=http://p.invalid"}, sandboxRunExtras{}), " ")
	if strings.Contains(args, "PROXY") {
		t.Errorf("a proxy variable from the session env reached docker run: %s", args)
	}
}

func TestStartSandboxContainerSetsNoProxyOnTheNetwork(t *testing.T) {
	dir := writeDockerConfig(t, `{"proxies":{"default":{"httpProxy":"http://proxy.invalid:3128","noProxy":"corp.invalid"}}}`)
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("NO_PROXY", "mine.invalid")
	t.Setenv("no_proxy", "")
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-noproxy") })

	if _, err := startSandboxContainer("s-noproxy", sandboxTestClone(t, "s-noproxy"), []string{"BLERG_RUNNER_SESSION_ID=s1"},
		sandboxCredential{}, SandboxOptions{Network: "blerg-sandbox"}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	joined, _ := runCall(t, *calls)
	want := "mine.invalid,corp.invalid,blerg-runner,blerg-board,localhost,127.0.0.1"
	for _, w := range []string{" -e NO_PROXY=" + want + " ", " -e no_proxy=" + want + " "} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in %s", w, joined)
		}
	}
	if strings.Contains(joined, "proxy.invalid") {
		t.Errorf("the proxy itself must not be touched: %s", joined)
	}
}
