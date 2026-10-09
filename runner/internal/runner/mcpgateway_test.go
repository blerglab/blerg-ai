package runner

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const podGatewayJSON = `{"base_url":"http://gw.svc:9100","servers":[{"name":"calendar","token":"tok-pod-secret"}]}`

func TestConfigFromEnvConsumesGatewayVariable(t *testing.T) {
	t.Setenv(daemon.MCPGatewayEnvVar, podGatewayJSON)
	cfg := ConfigFromEnv()
	if cfg.MCPGatewayErr != nil {
		t.Fatal(cfg.MCPGatewayErr)
	}
	g := cfg.MCPGateway
	if g == nil || g.BaseURL != "http://gw.svc:9100" || len(g.Servers) != 1 || g.Servers[0].Token != "tok-pod-secret" {
		t.Fatalf("parsed %+v", g)
	}
	// Unset before anything is spawned: not in this process's environment, so
	// not in any child (plugin installer, claude, a tool's shell).
	if v, ok := os.LookupEnv(daemon.MCPGatewayEnvVar); ok {
		t.Fatalf("variable still set (%d bytes)", len(v))
	}
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "tok-pod-secret") || strings.Contains(string(out), daemon.MCPGatewayEnvVar) {
		t.Errorf("gateway grant present in a child's environment")
	}
}

func TestConfigFromEnvGatewayAbsentAndInvalid(t *testing.T) {
	t.Setenv(daemon.MCPGatewayEnvVar, "")
	if cfg := ConfigFromEnv(); cfg.MCPGateway != nil || cfg.MCPGatewayErr != nil {
		t.Fatalf("unset variable produced %+v / %v", cfg.MCPGateway, cfg.MCPGatewayErr)
	}
	// Present but unusable: an error, no grant, and still unset — the session
	// must fail closed rather than run without the MCP config and tool list.
	for name, raw := range map[string]string{
		"not json":    "tok-secret-garbage",
		"bad url":     `{"base_url":"file:///x","servers":[{"name":"a","token":"tok-secret"}]}`,
		"no servers":  `{"base_url":"http://gw:1","servers":[]}`,
		"trailing":    podGatewayJSON + `{}`,
		"empty token": `{"base_url":"http://gw:1","servers":[{"name":"a","token":""}]}`,
	} {
		t.Setenv(daemon.MCPGatewayEnvVar, raw)
		cfg := ConfigFromEnv()
		if cfg.MCPGateway != nil || cfg.MCPGatewayErr == nil {
			t.Errorf("%s: grant %+v err %v", name, cfg.MCPGateway, cfg.MCPGatewayErr)
			continue
		}
		if strings.Contains(cfg.MCPGatewayErr.Error(), "tok-secret") {
			t.Errorf("%s: the error leaks the value: %v", name, cfg.MCPGatewayErr)
		}
		if _, ok := os.LookupEnv(daemon.MCPGatewayEnvVar); ok {
			t.Errorf("%s: variable left set", name)
		}
	}
}

// The pod's synthesised spawn carries the grant; without one it is exactly
// what it was.
func TestPodSpawnCarriesGateway(t *testing.T) {
	g := &protocol.MCPGatewayConfig{BaseURL: "http://gw:1", Servers: []protocol.MCPGatewayServer{{Name: "a", Token: "t"}}}
	spawn := podSpawn(Config{SessionID: "s1", Repo: "r", Title: "T", Model: "m", Effort: "high", Engine: "", MCPGateway: g}, "/workspace/r")
	if spawn.MCPGateway != g || spawn.Kind != "agent" || spawn.SessionID != "s1" {
		t.Fatalf("spawn = %+v", spawn)
	}
	plain := podSpawn(Config{SessionID: "s1", Repo: "r"}, "/workspace/r")
	if plain.MCPGateway != nil {
		t.Errorf("a pod without a grant got one: %+v", plain.MCPGateway)
	}
	noRepo := podSpawn(Config{SessionID: "s1", NoRepo: true}, "/workspace/scratch")
	if noRepo.ProjectPath != "/workspace/scratch" {
		t.Errorf("no-repo spawn lost its project path: %+v", noRepo)
	}
}

// A restricted session installs no plugins and downloads no user config bundle
// (which carries the user's plugins, hooks and settings); a grant alone does
// neither (docs/design/interactive-mcp-sessions.md).
func TestRestrictedSessionSkipsPluginsAndUserConfig(t *testing.T) {
	installed := false
	install := func(context.Context, Config, []pluginspec.Entry) PluginResult {
		installed = true
		return PluginResult{}
	}
	fs := &fakeSender{}
	r := &stageReporter{s: fs, sessionID: "sid"}
	cfg := Config{Plugins: []pluginspec.Entry{ent("a")}, MCPGateway: &protocol.MCPGatewayConfig{}, RestrictTools: true}
	reportWorkspaceReady(context.Background(), cfg, r, install)
	if installed {
		t.Error("a restricted session installed plugins")
	}
	if got := strings.Join(stageIDsAndStates(t, fs), " | "); strings.Contains(got, "plugins") {
		t.Errorf("a plugins stage was reported: %s", got)
	}
	cfg.RestrictTools = false // a grant alone: watched, so plugins and config load
	reportWorkspaceReady(context.Background(), cfg, r, install)
	if !installed {
		t.Error("a watched grant session no longer installs plugins")
	}

	if !(Config{RestrictTools: true}).skipsUserConfig() || cfg.skipsUserConfig() {
		t.Error("only a restricted session skips the user's config bundle")
	}
}

// The pod reads the restriction flag from ONE plain variable and unsets it at
// once, like the gateway variable, so no child process sees it.
func TestConfigFromEnvConsumesRestrictToolsVariable(t *testing.T) {
	t.Setenv(daemon.RestrictToolsEnvVar, "1")
	cfg := ConfigFromEnv()
	if !cfg.RestrictTools {
		t.Fatal("RestrictTools not set from the variable")
	}
	if v, ok := os.LookupEnv(daemon.RestrictToolsEnvVar); ok {
		t.Fatalf("variable still set (%q)", v)
	}
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), daemon.RestrictToolsEnvVar) {
		t.Errorf("restriction variable present in a child's environment")
	}
	// "true" restricts like "1", in any case.
	for _, v := range []string{"true", "TRUE", " True "} {
		t.Setenv(daemon.RestrictToolsEnvVar, v)
		if !ConfigFromEnv().RestrictTools {
			t.Errorf("value %q did not restrict the pod", v)
		}
	}
	// Empty, "0" and "false" do not restrict on their own.
	for _, v := range []string{"", "0", "false", "no"} {
		t.Setenv(daemon.RestrictToolsEnvVar, v)
		if ConfigFromEnv().RestrictTools {
			t.Errorf("value %q restricted the pod", v)
		}
	}
	_ = os.Unsetenv(daemon.RestrictToolsEnvVar)
	if ConfigFromEnv().RestrictTools {
		t.Error("an unset variable restricted the pod")
	}
}

// A restricted pod's synthesised spawn carries RestrictTools; a grant alone
// does not imply it (docs/design/interactive-mcp-sessions.md).
func TestPodSpawnCarriesRestrictTools(t *testing.T) {
	if s := podSpawn(Config{SessionID: "s1", Repo: "r", RestrictTools: true}, "/w"); !s.RestrictTools || s.MCPGateway != nil {
		t.Fatalf("restrict-only spawn = %+v", s)
	}
	g := &protocol.MCPGatewayConfig{BaseURL: "http://gw:1", Servers: []protocol.MCPGatewayServer{{Name: "a", Token: "t"}}}
	if s := podSpawn(Config{SessionID: "s1", Repo: "r", MCPGateway: g}, "/w"); s.RestrictTools || s.MCPGateway == nil {
		t.Errorf("a grant alone must carry the grant and no restriction: %+v", s)
	}
	if s := podSpawn(Config{SessionID: "s1", Repo: "r", MCPGateway: g, RestrictTools: true}, "/w"); !s.RestrictTools {
		t.Errorf("a restricted grant pod's spawn is not restricted: %+v", s)
	}
	if s := podSpawn(Config{SessionID: "s1", Repo: "r"}, "/w"); s.RestrictTools {
		t.Errorf("a plain pod's spawn is restricted: %+v", s)
	}
	if !(Config{RestrictTools: true}).skipsUserConfig() {
		t.Error("a restricted pod must skip the user's config bundle")
	}
	installed := false
	install := func(context.Context, Config, []pluginspec.Entry) PluginResult {
		installed = true
		return PluginResult{}
	}
	fs := &fakeSender{}
	r := &stageReporter{s: fs, sessionID: "sid"}
	reportWorkspaceReady(context.Background(), Config{Plugins: []pluginspec.Entry{ent("a")}, RestrictTools: true}, r, install)
	if installed {
		t.Error("a restricted session installed plugins")
	}
}

// The pod reads the interaction mode from ONE plain variable and unsets it at once, like the
// restriction, and its spawn carries it to the agent host.
func TestConfigFromEnvConsumesInteractionVariable(t *testing.T) {
	t.Setenv(daemon.InteractionEnvVar, "unattended")
	cfg := ConfigFromEnv()
	if cfg.Interaction != protocol.InteractionUnattended {
		t.Fatalf("Interaction = %q, want unattended", cfg.Interaction)
	}
	if v, ok := os.LookupEnv(daemon.InteractionEnvVar); ok {
		t.Fatalf("variable still set (%q)", v)
	}
	out, err := exec.Command("env").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), daemon.InteractionEnvVar) {
		t.Errorf("interaction variable present in a child's environment")
	}
	if s := podSpawn(cfg, "/w"); s.Interaction != protocol.InteractionUnattended {
		t.Errorf("pod spawn interaction = %q", s.Interaction)
	}
	for v, want := range map[string]string{
		"interactive": protocol.InteractionInteractive, " Interactive ": protocol.InteractionInteractive, //nolint:gocritic // mapKey: the padding is the point, the value is trimmed
		"UNATTENDED": protocol.InteractionUnattended,
		// Anything else is no mode at all, which the agent host reads as interactive.
		"": "", "1": "", "watched": "",
	} {
		t.Setenv(daemon.InteractionEnvVar, v)
		if got := ConfigFromEnv().Interaction; got != want {
			t.Errorf("value %q: Interaction = %q, want %q", v, got, want)
		}
		if _, ok := os.LookupEnv(daemon.InteractionEnvVar); ok {
			t.Errorf("value %q: variable still set", v)
		}
	}
	_ = os.Unsetenv(daemon.InteractionEnvVar)
	if got := ConfigFromEnv().Interaction; got != "" {
		t.Errorf("an unset variable gave Interaction %q", got)
	}
	if s := podSpawn(Config{SessionID: "s1", Repo: "r"}, "/w"); s.Interaction != "" {
		t.Errorf("a pod with no mode spawned with %q", s.Interaction)
	}
}
