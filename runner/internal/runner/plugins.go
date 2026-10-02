package runner

// Always-on plugins: installed into the pod's Claude Code before the engine starts, from the
// list the server put in BLERG_RUNNER_PLUGINS (non-secret configuration, fetched from
// blerg-core for the account that started the session).
//
// Plugins run code in the session, so this is deliberately narrow:
//   - every entry is re-validated and re-checked against the operator's marketplace allow-list
//     here, in the pod, whatever the server already did — an entry that fails is skipped, never
//     installed;
//   - the claude CLI is run with argument lists only (never a shell), no stdin, a per-command
//     timeout and an overall budget, one command at a time;
//   - one failing plugin never stops the others or the session;
//   - the CLI's output goes to the pod log (secret-scrubbed, truncated) and never into the
//     events the browser sees: failures are reported as a fixed short text naming the plugin.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	// maxPluginsEnvBytes caps BLERG_RUNNER_PLUGINS: 20 entries of maximal size are well under it.
	maxPluginsEnvBytes = 16 << 10

	pluginCmdTimeout   = 90 * time.Second
	pluginTotalTimeout = 4 * time.Minute
	pluginOutputCap    = 64 << 10
	pluginLogCap       = 2 << 10
)

// ParsePluginsEnv decodes BLERG_RUNNER_PLUGINS (a JSON array of {marketplace, plugin}). Anything
// malformed, oversize or over the entry cap yields no plugins and an error to log: a broken
// list must never become a partial install.
func ParsePluginsEnv(raw string) ([]pluginspec.Entry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxPluginsEnvBytes {
		return nil, errors.New("plugin list too large")
	}
	var entries []pluginspec.Entry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("plugin list is not valid JSON: %w", err)
	}
	if len(entries) > pluginspec.MaxEntries {
		return nil, errors.New("too many plugins")
	}
	return entries, nil
}

// PluginResult is the outcome of an install run.
type PluginResult struct {
	Total     int      // entries requested
	Installed int      // entries installed (or already installed)
	Failed    []string // plugin names that did not install, in list order
	Skipped   []string // plugin names refused before any install (malformed, or marketplace not allowed)
}

// Detail is the short, fixed-shape text shown in the start panel: "2 of 2 installed" or
// "1 of 2 installed — frontend-design failed". It carries plugin names (validated to a tiny
// alphabet) and never anything the CLI printed.
func (r PluginResult) Detail() string {
	d := fmt.Sprintf("%d of %d installed", r.Installed, r.Total)
	var notes []string
	if len(r.Failed) > 0 {
		notes = append(notes, strings.Join(r.Failed, ", ")+" failed")
	}
	if len(r.Skipped) > 0 {
		notes = append(notes, strings.Join(r.Skipped, ", ")+" skipped (not allowed)")
	}
	if len(notes) > 0 {
		d += " — " + strings.Join(notes, "; ")
	}
	return d
}

// pluginRunner runs one claude command and returns its (combined, capped) output.
type pluginRunner func(ctx context.Context, name string, args ...string) (string, error)

// pluginInstaller installs a plugin list with the claude CLI.
type pluginInstaller struct {
	bin      string
	env      []string
	allow    pluginspec.Allowlist
	secrets  []string // scrubbed from anything logged
	run      pluginRunner
	cmdLimit time.Duration
	total    time.Duration
}

// limitedBuffer keeps at most n bytes and silently drops the rest.
type limitedBuffer struct {
	buf bytes.Buffer
	n   int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.n - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// execRunner is the real pluginRunner: exec.CommandContext with an argument list, no shell, no
// stdin (so a prompt can never wait on a terminal), the allow-listed environment, and an empty
// scratch working directory (so nothing relative can reach the cloned repo).
//
// The command runs in its own process group and a timeout kills the whole group: the CLI spawns
// git and node children, and a survivor would keep writing into $HOME/.claude/plugins while the
// next command runs.
func (p *pluginInstaller) execRunner(ctx context.Context, name string, args ...string) (string, error) {
	scratch, err := os.MkdirTemp("", "blerg-plugins-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is the fixed "claude" and every argument is built from an entry that passed pluginspec validation; there is no shell
	cmd.Env = p.env
	cmd.Dir = scratch
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid = the whole group. Fall back to the process itself if that fails.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	out := &limitedBuffer{n: pluginOutputCap}
	cmd.Stdout = out
	cmd.Stderr = out
	// A survivor may still hold the pipes after the kill; don't wait on it forever.
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	return out.buf.String(), err
}

var marketplaceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// pluginEnvAllow is the only set of variables the `claude plugin ...` children inherit: the
// basics a CLI and git-over-https need (locale, temp dir, proxies, CA bundles), and nothing else.
var pluginEnvAllow = map[string]bool{
	"PATH": true, "CLAUDE_CONFIG_DIR": true, "LANG": true, "TERM": true, "TMPDIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "GIT_SSL_CAINFO": true, "NODE_EXTRA_CA_CERTS": true,
}

// pluginChildEnv builds the environment the claude CLI runs with: an ALLOW-list (PATH, locale,
// proxy and CA settings, CLAUDE_CONFIG_DIR if set) plus HOME set to the directory the engine will
// use — plugins land in $HOME/.claude, which is where the later `claude -p` turns look.
//
// What this does and does not protect: the install children never receive the pod's tokens,
// session token, initial prompt or other credentials as environment, and they start in an empty
// directory rather than the workspace. It is NOT a sandbox: a plugin's code runs as the same
// user, so it can still read /proc/<runner>/environ and any file under HOME, and once installed
// a plugin acts with the session's full access. The real mitigation is the marketplace
// allow-list (BLERG_*_PLUGIN_MARKETPLACES): only allow marketplaces you trust.
func pluginChildEnv(home string) []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if pluginEnvAllow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, e)
		}
	}
	return append(out, "HOME="+home)
}

func (p *pluginInstaller) scrub(s string) string {
	for _, sec := range p.secrets {
		s = scrubToken(s, sec)
	}
	s = strings.TrimSpace(s)
	if len(s) > pluginLogCap {
		s = s[:pluginLogCap] + "…"
	}
	return s
}

// step runs one command under the per-command timeout, logging its output to the pod log.
func (p *pluginInstaller) step(ctx context.Context, label string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, p.cmdLimit)
	defer cancel()
	out, err := p.run(cctx, p.bin, args...)
	if err != nil {
		log.Printf("runner: plugins: %s failed: %v: %s", label, err, p.scrub(out))
	} else {
		log.Printf("runner: plugins: %s ok", label)
	}
	return out, err
}

// marketplaceNames maps lowercase github owner/repo → marketplace name from
// `claude plugin marketplace list --json`. The name in "plugin@marketplace" is the marketplace's
// own manifest name, which is known only after it has been added.
func (p *pluginInstaller) marketplaceNames(ctx context.Context) map[string]string {
	out, err := p.step(ctx, "marketplace list", "plugin", "marketplace", "list", "--json")
	if err != nil {
		return nil
	}
	// Tolerate any lead-in text before the array.
	if i := strings.Index(out, "["); i > 0 {
		out = out[i:]
	}
	var list []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Repo   string `json:"repo"`
	}
	if json.Unmarshal([]byte(out), &list) != nil {
		log.Printf("runner: plugins: marketplace list output was not JSON")
		return nil
	}
	m := map[string]string{}
	for _, e := range list {
		if e.Source == "github" && e.Repo != "" && marketplaceNameRE.MatchString(e.Name) {
			m[strings.ToLower(e.Repo)] = e.Name
		}
	}
	return m
}

// install runs the whole list. Sequential; each failure is recorded and the run continues.
func (p *pluginInstaller) install(ctx context.Context, entries []pluginspec.Entry) PluginResult {
	res := PluginResult{Total: len(entries)}
	ctx, cancel := context.WithTimeout(ctx, p.total)
	defer cancel()

	var ok []pluginspec.Entry
	for _, e := range entries {
		switch {
		case pluginspec.ValidateClaude(e) != nil:
			log.Printf("runner: plugins: skipping malformed entry")
			res.Skipped = append(res.Skipped, sanitizeName(e.Plugin))
		case !p.allow.Allows(e.Marketplace):
			log.Printf("runner: plugins: skipping %q: marketplace %q is not allowed by this install", e.Plugin, e.Marketplace)
			res.Skipped = append(res.Skipped, e.Plugin)
		default:
			ok = append(ok, e)
		}
	}

	added := map[string]bool{} // lowercase source → add succeeded
	tried := map[string]bool{}
	for _, e := range ok {
		key := strings.ToLower(e.Marketplace)
		if tried[key] {
			continue
		}
		tried[key] = true
		_, err := p.step(ctx, "marketplace add "+e.Marketplace, "plugin", "marketplace", "add", e.Marketplace)
		added[key] = err == nil
	}
	var names map[string]string
	if len(added) > 0 {
		names = p.marketplaceNames(ctx)
	}
	for _, e := range ok {
		key := strings.ToLower(e.Marketplace)
		name := names[key]
		if !added[key] || name == "" {
			res.Failed = append(res.Failed, e.Plugin)
			continue
		}
		if _, err := p.step(ctx, "install "+e.Plugin+"@"+name,
			"plugin", "install", e.Plugin+"@"+name, "--scope", "user"); err != nil {
			res.Failed = append(res.Failed, e.Plugin)
			continue
		}
		res.Installed++
	}
	return res
}

// sanitizeName keeps a plugin name that failed validation out of an event: it is replaced by a
// placeholder unless it is itself a valid name.
func sanitizeName(s string) string {
	if pluginspec.ValidPluginName(s) {
		return s
	}
	return "(invalid entry)"
}

// InstallPlugins installs entries into the Claude Code under cfg.Home. The allow-list is
// BLERG_RUNNER_PLUGIN_MARKETPLACES as the server passed it into the pod (default: the
// official marketplace only).
func InstallPlugins(ctx context.Context, cfg Config, entries []pluginspec.Entry) PluginResult {
	allow, _ := pluginspec.ParseAllowlist(os.Getenv("BLERG_RUNNER_PLUGIN_MARKETPLACES"))
	p := &pluginInstaller{
		bin:      "claude",
		env:      pluginChildEnv(cfg.Home),
		allow:    allow,
		secrets:  []string{cfg.GitToken, cfg.APIKey, cfg.DaemonToken, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")},
		cmdLimit: pluginCmdTimeout,
		total:    pluginTotalTimeout,
	}
	p.run = p.execRunner
	return p.install(ctx, entries)
}

// installPluginsFunc is InstallPlugins, swappable in tests.
type installPluginsFunc func(context.Context, Config, []pluginspec.Entry) PluginResult

func installPlugins(ctx context.Context, cfg Config, entries []pluginspec.Entry) PluginResult {
	return InstallPlugins(ctx, cfg, entries)
}

// reportWorkspaceReady closes the clone stage and opens the engine stage, with the plugin
// install between them when this session has always-on plugins (Claude sessions only).
func reportWorkspaceReady(ctx context.Context, cfg Config, stages *stageReporter, install installPluginsFunc) {
	if cfg.PluginsErr != nil {
		log.Printf("runner: plugins: ignoring BLERG_RUNNER_PLUGINS: %v", cfg.PluginsErr)
	}
	engineActive := active(protocol.StageEngine, "Starting the engine")
	// A session holding an MCP gateway grant runs unattended on untrusted text
	// with a narrowed tool list: no plugin (code, hooks, MCP servers of its
	// own) is installed into it, whatever the operator's always-on list says.
	if len(cfg.Plugins) == 0 || cfg.skipsUserConfig() || (cfg.Engine != "" && cfg.Engine != pluginspec.EngineClaude) {
		stages.report(done(protocol.StageClone), engineActive)
		return
	}
	start := active(protocol.StagePlugins, fmt.Sprintf("Installing %d plugin(s)", len(cfg.Plugins)))
	start.Label = "Installing plugins" // the server's plan has it; the label keeps a late-adopted plan readable
	stages.report(done(protocol.StageClone), start)
	res := install(ctx, cfg, cfg.Plugins)
	log.Printf("runner: plugins: %s", res.Detail())
	state := protocol.StageStateDone
	if len(res.Failed)+len(res.Skipped) > 0 {
		state = protocol.StageStateWarning // finished, but not everything asked for was installed
	}
	stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: state, Detail: res.Detail()}, engineActive)
}
