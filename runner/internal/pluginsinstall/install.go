// Package pluginsinstall installs always-on plugins with the claude CLI. It is shared by the
// cluster pod (which installs into its throwaway HOME) and the workstation daemon (which installs
// into a daemon-owned CLAUDE_CONFIG_DIR and loads the result with --plugin-dir).
//
// Plugins run code in the session, so this is deliberately narrow:
//   - every entry is re-validated and re-checked against the operator's marketplace allow-list
//     here, whatever the server already did — an entry that fails is skipped, never installed;
//   - the claude CLI is run with argument lists only (never a shell), no stdin, a per-command
//     timeout and an overall budget, one command at a time;
//   - one failing plugin never stops the others or the session;
//   - the CLI's output goes to the process log (secret-scrubbed, truncated) and never into the
//     events the browser sees: failures are reported as a fixed short text naming the plugin.
package pluginsinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

const (
	// maxPluginsEnvBytes caps BLERG_RUNNER_PLUGINS: 20 entries of maximal size are well under it.
	maxPluginsEnvBytes = 16 << 10

	// DefaultCmdTimeout and DefaultTotal bound one CLI command and a whole install run.
	DefaultCmdTimeout = 90 * time.Second
	DefaultTotal      = 4 * time.Minute
	pluginOutputCap   = 64 << 10
	pluginLogCap      = 2 << 10
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

// Result is the outcome of an install run.
type Result struct {
	Total     int      // entries requested
	Installed int      // entries installed (or already installed)
	Failed    []string // plugin names that did not install, in list order
	Skipped   []string // plugin names refused before any install (malformed, or marketplace not allowed)
	// Names maps a lowercase marketplace source (owner/repo) to the marketplace's own name, as
	// learned from `claude plugin marketplace list` — the "@name" part of an installed plugin's id.
	Names map[string]string
	// Installed entries, in list order, for callers that need to find them afterwards.
	Entries []pluginspec.Entry
	// MarketplaceFailed: a `marketplace add` or the marketplace listing failed, so entries could
	// not even be attempted — the signal of a broken or unreachable marketplace rather than of one
	// plugin that does not exist.
	MarketplaceFailed bool
}

// Detail is the short, fixed-shape text shown in the start panel: "2 of 2 installed" or
// "1 of 2 installed — frontend-design failed". It carries plugin names (validated to a tiny
// alphabet) and never anything the CLI printed.
func (r Result) Detail() string {
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

// Runner runs one claude command and returns its (combined, capped) output. The default runs
// the binary; tests substitute one.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Options configure an install run.
type Options struct {
	// Bin is the CLI to run ("claude").
	Bin string
	// Env is the complete child environment (see ChildEnv); it must carry no secrets.
	Env []string
	// Allow is the operator's marketplace allow-list.
	Allow pluginspec.Allowlist
	// Secrets are scrubbed from anything logged.
	Secrets []string
	// CmdTimeout and Total bound one command and the whole run; zero means the defaults.
	CmdTimeout, Total time.Duration
	// Run executes one command; nil means a real exec (see execRunner).
	Run Runner
	// LogPrefix starts every log line ("runner: plugins").
	LogPrefix string
}

// installer installs a plugin list with the claude CLI.
type installer struct {
	bin      string
	env      []string
	allow    pluginspec.Allowlist
	secrets  []string // scrubbed from anything logged
	run      Runner
	cmdLimit time.Duration
	total    time.Duration
	logp     string
}

func newInstaller(o Options) *installer {
	p := &installer{bin: o.Bin, env: o.Env, allow: o.Allow, secrets: o.Secrets, run: o.Run,
		cmdLimit: o.CmdTimeout, total: o.Total, logp: o.LogPrefix}
	if p.bin == "" {
		p.bin = "claude"
	}
	if p.cmdLimit <= 0 {
		p.cmdLimit = DefaultCmdTimeout
	}
	if p.total <= 0 {
		p.total = DefaultTotal
	}
	if p.logp == "" {
		p.logp = "plugins"
	}
	if p.run == nil {
		p.run = p.execRunner
	}
	return p
}

// Install runs the whole list: marketplace add per distinct source, a marketplace listing to
// learn names, then one install per entry. Sequential; each failure is recorded and the run
// continues.
func Install(ctx context.Context, o Options, entries []pluginspec.Entry) Result {
	return newInstaller(o).install(ctx, entries)
}

// Update refreshes the marketplaces in names and the installed version of each entry
// (`marketplace update <name>`, then `plugin update <plugin>@<name>`). Best effort: failures are
// logged and otherwise ignored, so a pinned version keeps working offline.
func Update(ctx context.Context, o Options, names map[string]string, entries []pluginspec.Entry) {
	p := newInstaller(o)
	ctx, cancel := context.WithTimeout(ctx, p.total)
	defer cancel()
	done := map[string]bool{}
	for _, e := range entries {
		name := names[strings.ToLower(e.Marketplace)]
		if name == "" || pluginspec.ValidateClaude(e) != nil || !p.allow.Allows(e.Marketplace) {
			continue
		}
		if !done[name] {
			done[name] = true
			_, _ = p.step(ctx, "marketplace update "+name, "plugin", "marketplace", "update", name)
		}
		_, _ = p.step(ctx, "update "+e.Plugin+"@"+name, "plugin", "update", e.Plugin+"@"+name)
	}
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
func (p *installer) execRunner(ctx context.Context, name string, args ...string) (string, error) {
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

// ValidMarketplaceName reports whether s is a marketplace name safe to put in a
// "plugin@name" argument (never flag-shaped).
func ValidMarketplaceName(s string) bool { return marketplaceNameRE.MatchString(s) }

// pluginEnvAllow is the only set of variables the `claude plugin ...` children inherit: the
// basics a CLI and git-over-https need (locale, temp dir, proxies, CA bundles), and nothing else.
var envAllow = map[string]bool{
	"PATH": true, "CLAUDE_CONFIG_DIR": true, "LANG": true, "TERM": true, "TMPDIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "GIT_SSL_CAINFO": true, "NODE_EXTRA_CA_CERTS": true,
}

// ChildEnv builds the environment the claude CLI runs with: an ALLOW-list (PATH, locale, proxy
// and CA settings, CLAUDE_CONFIG_DIR if set) plus HOME set to home, plus any extra KEY=VALUE pairs
// the caller vouches for (the daemon adds CLAUDE_CONFIG_DIR and git's no-prompt settings).
//
// What this does and does not protect: the install children never receive the process's tokens,
// session token, initial prompt or other credentials as environment, and they start in an empty
// directory rather than the workspace. It is NOT a sandbox: a plugin's code runs as the same
// user, so it can still read /proc/<pid>/environ and any file under HOME, and once installed
// a plugin acts with the session's full access. The real mitigation is the marketplace
// allow-list (BLERG_*_PLUGIN_MARKETPLACES): only allow marketplaces you trust.
func ChildEnv(home string, extra ...string) []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if envAllow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, e)
		}
	}
	out = append(out, "HOME="+home)
	return append(out, extra...)
}

func (p *installer) scrub(s string) string {
	for _, sec := range p.secrets {
		s = scrubSecret(s, sec)
	}
	s = strings.TrimSpace(s)
	if len(s) > pluginLogCap {
		s = s[:pluginLogCap] + "…"
	}
	return s
}

// step runs one command under the per-command timeout, logging its output to the pod log.
func (p *installer) step(ctx context.Context, label string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, p.cmdLimit)
	defer cancel()
	out, err := p.run(cctx, p.bin, args...)
	if err != nil {
		log.Printf("%s: %s failed: %v: %s", p.logp, label, err, p.scrub(out))
	} else {
		log.Printf("%s: %s ok", p.logp, label)
	}
	return out, err
}

// marketplaceNames maps lowercase github owner/repo → marketplace name from
// `claude plugin marketplace list --json`. The name in "plugin@marketplace" is the marketplace's
// own manifest name, which is known only after it has been added.
func (p *installer) marketplaceNames(ctx context.Context) map[string]string {
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
		log.Printf("%s: marketplace list output was not JSON", p.logp)
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
func (p *installer) install(ctx context.Context, entries []pluginspec.Entry) Result {
	res := Result{Total: len(entries)}
	ctx, cancel := context.WithTimeout(ctx, p.total)
	defer cancel()

	var ok []pluginspec.Entry
	for _, e := range entries {
		switch {
		case pluginspec.ValidateClaude(e) != nil:
			log.Printf("%s: skipping malformed entry", p.logp)
			res.Skipped = append(res.Skipped, sanitizeName(e.Plugin))
		case !p.allow.Allows(e.Marketplace):
			log.Printf("%s: skipping %q: marketplace %q is not allowed by this install", p.logp, e.Plugin, e.Marketplace)
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
	res.Names = names
	for _, e := range ok {
		key := strings.ToLower(e.Marketplace)
		name := names[key]
		if !added[key] || name == "" {
			res.MarketplaceFailed = true
			res.Failed = append(res.Failed, e.Plugin)
			continue
		}
		if _, err := p.step(ctx, "install "+e.Plugin+"@"+name,
			"plugin", "install", e.Plugin+"@"+name, "--scope", "user"); err != nil {
			res.Failed = append(res.Failed, e.Plugin)
			continue
		}
		res.Installed++
		res.Entries = append(res.Entries, e)
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

// scrubSecret removes every occurrence of secret (raw and URL-escaped) from s.
func scrubSecret(s, secret string) string {
	if strings.TrimSpace(secret) == "" {
		return s
	}
	s = strings.ReplaceAll(s, secret, "REDACTED")
	if esc := url.UserPassword("u", secret).String(); esc != "u:"+secret {
		s = strings.ReplaceAll(s, strings.TrimPrefix(esc, "u:"), "REDACTED")
	}
	return s
}
