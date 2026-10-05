package server

// Always-on plugins, server side. When a cluster session starts for an account, the server asks
// blerg-core for that account's plugin list (core's internal endpoint, same internal key and
// liveness idea as the credential fetch) and hands it to the pod as NON-secret configuration in
// the BLERG_RUNNER_PLUGINS env var. The pod installs it (internal/runner/plugins.go).
//
// Plugins are best-effort, unlike credentials: if core cannot be reached or answers with an
// error the session still starts, without plugins, and its start panel says so.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// maxPluginsResponse bounds what is read back from core: 20 entries are a couple of KiB.
const maxPluginsResponse = 64 << 10

type internalPluginsRequest struct {
	AccountID string `json:"account_id"`
	Engine    string `json:"engine"`
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type internalPluginsResponse struct {
	Plugins []pluginspec.Entry `json:"plugins"`
}

// errPluginsUnavailable is the fixed reason shown in the start panel when the list could not be
// loaded; whatever actually went wrong is in the server log.
var errPluginsUnavailable = errors.New("plugins skipped: your plugin list couldn't be loaded")

// fetchCorePlugins asks core for accountID's always-on plugins for the claude engine. Core's
// liveness proof is mandatory and names WHICH live thing stands behind the session: an agent
// token's id, or the launching browser session's id, and core checks that specific one belongs
// to the account. Not configured (no core URL or key) is (nil, nil): there is simply nothing to
// fetch. A call with no proof fails closed without contacting core.
func fetchCorePlugins(ctx context.Context, client *http.Client, coreURL, internalKey, accountID string, proof coreProof) ([]pluginspec.Entry, error) {
	if coreURL == "" || internalKey == "" || accountID == "" {
		return nil, nil
	}
	if !proof.valid() {
		return nil, errNoLivenessProof
	}
	client = coreHTTPClient(client) // never follows a redirect: the request carries the internal key
	raw, err := json.Marshal(internalPluginsRequest{
		AccountID: accountID, Engine: pluginspec.EngineClaude, TokenID: proof.TokenID, SessionID: proof.SessionID,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/plugins/list", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", internalKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to core failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPluginsResponse))
	switch resp.StatusCode {
	case http.StatusOK:
		var out internalPluginsResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode plugin list: %w", err)
		}
		return out.Plugins, nil
	case http.StatusNotFound:
		// No live principal (a human session that expired or was revoked, e.g. on resume) or an
		// older core without the endpoint. Not silent: the caller shows the same visible note as
		// any other failure to load the list.
		return nil, errors.New("core has no live session or token for this account (404)")
	default:
		return nil, fmt.Errorf("core returned %d: %.200s", resp.StatusCode, body)
	}
}

// pluginAllowlistFromEnv reads BLERG_RUNNER_PLUGIN_MARKETPLACES (same grammar and default as
// core's BLERG_CORE_PLUGIN_MARKETPLACES) and the raw string to pass on to the pod.
func pluginAllowlistFromEnv() (pluginspec.Allowlist, string) {
	raw := os.Getenv("BLERG_RUNNER_PLUGIN_MARKETPLACES")
	a, dropped := pluginspec.ParseAllowlist(raw)
	for _, d := range dropped {
		log.Printf("ignoring invalid BLERG_RUNNER_PLUGIN_MARKETPLACES entry %q (want GitHub owner/repo)", d)
	}
	return a, raw
}

// pluginsWanted: only Claude sessions with a known spawning account get always-on plugins.
// A session with an MCP grant gets none: the pod would ignore them (it skips user config when
// a grant is present) and a plugin's own MCP servers must never widen a grant session.
// A cron session (NoOperatorFallback) gets none either: nobody is watching it.
func pluginsWanted(spec SessionJobSpec) bool {
	return spec.MCPGateway == nil && !spec.RestrictTools && !spec.NoOperatorFallback && spec.SpawningAccountID != "" && (spec.Engine == "" || spec.Engine == pluginspec.EngineClaude)
}

// ResolvePlugins fills spec.Plugins (and PluginsNote when the list could not be used) exactly
// once per start. Every entry is re-checked against this server's own allow-list: an entry not
// allowed is skipped, never installed. Never fails the start.
func (j *JobManager) ResolvePlugins(ctx context.Context, spec *SessionJobSpec) {
	if spec.PluginsResolved {
		return
	}
	spec.PluginsResolved = true
	spec.Plugins, spec.PluginsNote = nil, ""
	if !pluginsWanted(*spec) {
		return
	}
	if j.personalCredentialsWired() && !spec.proof().valid() {
		// No named live token/session: nothing can be asked of core, and a start that needs
		// core at all refuses in CreateSessionJob (errNoLivenessProof), so no note here.
		log.Printf("cluster session %s: always-on plugins skipped: %v", spec.SessionID, errNoLivenessProof)
		return
	}
	list, err := fetchCorePlugins(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof())
	if err != nil {
		log.Printf("cluster session %s: always-on plugins unavailable, starting without them: %v", spec.SessionID, err)
		spec.PluginsNote = errPluginsUnavailable.Error()
		return
	}
	spec.Plugins, spec.PluginsNote = resolvePluginList(list, j.PluginAllow, "cluster session "+spec.SessionID)
}

// resolvePluginList re-checks a list from core against this server's own allow-list: malformed,
// duplicate (case-insensitive marketplace) and disallowed entries are skipped, never installed,
// and the list is capped at MaxEntries. note says what was skipped ("" when nothing was).
func resolvePluginList(list []pluginspec.Entry, allow pluginspec.Allowlist, who string) ([]pluginspec.Entry, string) {
	var out []pluginspec.Entry
	skipped := 0
	seen := map[pluginspec.Entry]bool{}
	for _, e := range list {
		key := pluginspec.Entry{Marketplace: strings.ToLower(e.Marketplace), Plugin: e.Plugin}
		if pluginspec.ValidateClaude(e) != nil || !allow.Allows(e.Marketplace) || seen[key] {
			log.Printf("%s: skipping plugin %q from %q (malformed, duplicate or marketplace not allowed)", who, e.Plugin, e.Marketplace)
			skipped++
			continue
		}
		seen[key] = true
		out = append(out, e)
		if len(out) == pluginspec.MaxEntries {
			break
		}
	}
	note := ""
	if skipped > 0 {
		note = fmt.Sprintf("%d skipped: marketplace not allowed by this install", skipped)
	}
	return out, note
}

// daemonPlugins resolves the always-on plugin list for a session sent to a workstation daemon:
// the same predicate as the cluster's pluginsWanted (a Claude agent-kind session, with a known
// account, neither restricted nor granted), plus the daemon must have said in its hello that it
// honours SpawnSession.Plugins. Never fails the start: a list that could not be loaded gives a
// note for the start panel and a session without plugins. The daemon re-checks every entry.
func (a *API) daemonPlugins(ctx context.Context, d *DaemonConn, accountID string, proof coreProof, kind, engine string, restricted bool) ([]pluginspec.Entry, string) {
	if d == nil || !d.CanPlugins() || kind != "agent" || restricted || accountID == "" ||
		(engine != "" && engine != pluginspec.EngineClaude) || a.coreURL == "" || a.coreInternalKey == "" {
		return nil, ""
	}
	if !proof.valid() {
		return nil, ""
	}
	fetch := a.fetchPlugins
	if fetch == nil {
		fetch = func(ctx context.Context, accountID string, proof coreProof) ([]pluginspec.Entry, error) {
			return fetchCorePlugins(ctx, nil, a.coreURL, a.coreInternalKey, accountID, proof)
		}
	}
	list, err := fetch(ctx, accountID, proof)
	if err != nil {
		log.Printf("daemon %s: always-on plugins unavailable, starting without them: %v", d.Name, err)
		return nil, errPluginsUnavailable.Error()
	}
	return resolvePluginList(list, a.pluginAllow, "daemon "+d.Name)
}

// pluginEnv is the pod env carrying the list: the entries as compact JSON plus the allow-list the
// pod re-checks against. Empty when there is nothing to install.
func (j *JobManager) pluginEnv(spec SessionJobSpec) []map[string]any {
	if len(spec.Plugins) == 0 {
		return nil
	}
	raw, err := json.Marshal(spec.Plugins)
	if err != nil {
		return nil
	}
	env := []map[string]any{{"name": "BLERG_RUNNER_PLUGINS", "value": string(raw)}}
	if j.PluginAllowRaw != "" {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_PLUGIN_MARKETPLACES", "value": j.PluginAllowRaw})
	}
	return env
}

// pluginStage is the start-plan stage for the plugin install, or nil when the session has no
// plugins and nothing to say about them. The detail records what was requested; a session whose
// list could not be loaded gets a stage in the "warning" state saying so: it is over, it did not
// happen, and the panel shows it as such rather than folding it to a green check.
func pluginStage(plugins []pluginspec.Entry, note string) *protocol.StartStage {
	switch {
	case len(plugins) > 0:
		names := make([]string, len(plugins))
		for i, p := range plugins {
			names[i] = p.Plugin
		}
		detail := strings.Join(names, ", ")
		if note != "" {
			detail += " (" + note + ")"
		}
		s := stage(protocol.StagePlugins, "Installing plugins", protocol.StageStatePending, detail)
		return &s
	case note != "":
		s := stage(protocol.StagePlugins, "Installing plugins", protocol.StageStateWarning, note)
		return &s
	}
	return nil
}
