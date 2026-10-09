package server

import (
	"context"
	"log"
	"sort"
	"sync"

	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JobManager creates and deletes per-session runner Jobs against the k8s API
// using the in-cluster service account. It deliberately speaks plain HTTP to
// the API server (no client-go dependency) — the two calls we need are tiny.
type JobManager struct {
	BaseURL   string // https://<apiserver>
	Token     string
	Namespace string
	Image     string
	Client    *http.Client

	// Pod env plumbing.
	ServerWSURL   string // ws URL runner pods dial, e.g. ws://blerg-runner-server.default:8080/ws/daemon
	ServerHTTPURL string
	SecretName    string // Secret with ANTHROPIC_API_KEY / BLERG_RUNNER_DAEMON_TOKEN / BLERG_RUNNER_GIT_TOKEN
	// OAuthSecretName holds CLAUDE_CODE_OAUTH_TOKEN when an external secrets
	// operator syncs it into its own secret (BLERG_RUNNER_OAUTH_SECRET_NAME);
	// defaults to SecretName.
	OAuthSecretName string
	GitURLBase      string // e.g. https://github.com/<org> — repo appended
	// ExplicitGitURLBase is BLERG_RUNNER_AGENT_GIT_BASE exactly as the
	// operator set it ("" when unset), as opposed to GitURLBase, which is
	// always populated with a default. The spawn path needs the difference:
	// an operator-chosen base is a complete answer for a bare repo name
	// (it may be a self-hosted host with single-segment paths), while the
	// bare-github.com default is not.
	ExplicitGitURLBase string
	// GitHubOrg is BLERG_RUNNER_GITHUB_ORG as the server saw it, kept here so
	// the spawn path can tell whether a bare (one-segment) repo name has an
	// org to resolve against — see HandlePostSessions's cluster branch.
	GitHubOrg     string
	MaxSessions   int   // concurrent cluster session cap (env default; see effectiveMaxSessions)
	PodTTLSeconds int64 // activeDeadlineSeconds: the hard cap on a pod's lifetime, busy or not
	// PodIdleSeconds ends a session pod after this long with no user message or
	// finished turn (0 = never). The pod enforces it (BLERG_RUNNER_IDLE_TIMEOUT_SECONDS).
	PodIdleSeconds int64
	// Overrides, when set, returns the admin-set values that win over the two
	// fields above (db.SettingPodTTLSeconds / db.SettingPodIdleTimeoutSeconds).
	// Read at each spawn so a change applies to the next pod without a restart.
	Overrides func() map[string]int64

	// CoreURL/CoreInternalKey point at blerg-core's internal
	// credential-fetch endpoint (Task 15's POST /internal/credentials/fetch)
	// so a spawned session can use the spawning account's personal
	// credential instead of the shared operator Secret when one exists.
	// Either being empty disables the personal-credential lookup entirely
	// (CreateSessionJob falls back to today's shared-Secret behavior).
	CoreURL          string // BLERG_CORE_URL
	CoreInternalKey  string // BLERG_RUNNER_CORE_INTERNAL_KEY — same value as core's BLERG_CORE_INTERNAL_KEY
	CredentialClient *http.Client

	// PluginAllow is the operator's marketplace allow-list for always-on plugins
	// (BLERG_RUNNER_PLUGIN_MARKETPLACES), enforced when a session's list is resolved and again
	// in the pod (PluginAllowRaw is the string handed to it).
	PluginAllow    pluginspec.Allowlist
	PluginAllowRaw string

	// Pod resource/lifecycle knobs — k8s quantity strings (e.g. "500m",
	// "1Gi") passed through as-is, not parsed. Defaults match what was
	// previously hardcoded in the job manifest.
	CPURequest              string
	MemRequest              string
	CPULimit                string
	MemLimit                string
	TerminationGraceSeconds int64
	TTLSecondsAfterFinished int64

	// engineCache holds the last AvailableEngines() result — a status
	// dashboard or launch-UI poll shouldn't hit the k8s API on every request
	// for something (Secret contents) that changes on operator timescales,
	// not per-request ones.
	engineCacheMu  sync.Mutex
	engineCacheAt  time.Time
	engineCacheVal []string
	// gitCacheVal is filled by the same Secret sweep as engineCacheVal:
	// whether the operator Secret(s) carry a non-empty BLERG_RUNNER_GIT_TOKEN.
	gitCacheVal bool
}

// clusterSecretsCacheTTL bounds how often AvailableEngines re-queries the
// k8s API for the configured credential Secret(s).
const clusterSecretsCacheTTL = 30 * time.Second

// clusterDaemonID is the fixed synthetic daemon ID cluster-runtime sessions
// are recorded under in the daemons/sessions tables — there's no real daemon
// process for them, but sessions still need a daemon_id FK. Fixed rather
// than generated so every cluster session, across every pod, groups under
// one row (and so this ID can be used as a stable filter — see
// HandleGetSessions's daemon_id param and the cluster status dashboard).
const clusterDaemonID = "00000000-0000-4000-8000-00000000f00d"

// NewJobManagerFromEnv configures the manager from the in-cluster environment.
// Returns nil (not an error) when not running in a k8s cluster, or when
// cluster-runtime spawns aren't configured — callers treat nil as "runtime
// unavailable".
func NewJobManagerFromEnv(gitHubOrg string) *JobManager {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	image := os.Getenv("BLERG_RUNNER_AGENT_IMAGE")
	if host == "" || image == "" {
		return nil
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	ns := os.Getenv("BLERG_RUNNER_AGENT_NAMESPACE")
	if ns == "" {
		ns = "blerg-runner-sessions"
	}
	internal := os.Getenv("BLERG_RUNNER_INTERNAL_URL")
	if internal == "" {
		internal = "http://blerg-runner-server.default.svc.cluster.local:8080"
	}
	explicitGitBase := os.Getenv("BLERG_RUNNER_AGENT_GIT_BASE")
	gitBase := defaultGitURLBase(explicitGitBase, gitHubOrg)
	pluginAllow, pluginAllowRaw := pluginAllowlistFromEnv()
	return &JobManager{
		PluginAllow: pluginAllow, PluginAllowRaw: pluginAllowRaw,
		BaseURL:   "https://" + host + ":" + port,
		Token:     string(bytes.TrimSpace(token)),
		Namespace: ns,
		Image:     image,
		Client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
		ServerWSURL:        strings.Replace(internal, "http", "ws", 1) + "/ws/daemon",
		ServerHTTPURL:      internal,
		SecretName:         envOr("BLERG_RUNNER_AGENT_SECRET", "blerg-runner-agent"),
		OAuthSecretName:    envOr("BLERG_RUNNER_OAUTH_SECRET_NAME", envOr("BLERG_RUNNER_AGENT_SECRET", "blerg-runner-agent")),
		GitURLBase:         gitBase,
		ExplicitGitURLBase: explicitGitBase,
		GitHubOrg:          gitHubOrg,
		MaxSessions:        envOrInt("BLERG_RUNNER_MAX_SESSIONS", 4),
		PodTTLSeconds:      int64(envOrInt("BLERG_RUNNER_POD_TTL_SECONDS", 7*24*3600)),
		PodIdleSeconds:     int64(envOrInt("BLERG_RUNNER_POD_IDLE_TIMEOUT_SECONDS", 24*3600)),

		CoreURL:          os.Getenv("BLERG_CORE_URL"),
		CoreInternalKey:  os.Getenv("BLERG_RUNNER_CORE_INTERNAL_KEY"),
		CredentialClient: &http.Client{Timeout: 10 * time.Second},

		CPURequest:              envOr("BLERG_RUNNER_POD_CPU_REQUEST", "500m"),
		MemRequest:              envOr("BLERG_RUNNER_POD_MEM_REQUEST", "1Gi"),
		CPULimit:                envOr("BLERG_RUNNER_POD_CPU_LIMIT", "1"),
		MemLimit:                envOr("BLERG_RUNNER_POD_MEM_LIMIT", "4Gi"),
		TerminationGraceSeconds: int64(envOrInt("BLERG_RUNNER_POD_TERMINATION_GRACE_SECONDS", 120)),
		TTLSecondsAfterFinished: int64(envOrInt("BLERG_RUNNER_POD_TTL_AFTER_FINISHED_SECONDS", 3600)),
	}
}

// defaultGitURLBase resolves the base a cluster session's clone URL is built
// from. An explicit BLERG_RUNNER_AGENT_GIT_BASE always wins; otherwise a
// configured org gives the familiar per-org base, and with neither the base is
// bare github.com — cluster sessions are the default runtime now, so an
// operator who configured nothing must still be able to launch, which they can
// by naming repos as org/name (enforced at spawn time).
func defaultGitURLBase(envBase, gitHubOrg string) string {
	if envBase != "" {
		return envBase
	}
	if gitHubOrg != "" {
		return "https://github.com/" + gitHubOrg
	}
	return "https://github.com"
}

func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (j *JobManager) jobName(sessionID string) string {
	return "blerg-runner-agent-" + strings.ToLower(sessionID)
}

func (j *JobManager) do(method, path string, body any) (*http.Response, error) {
	return j.doWithContentType(context.Background(), method, path, body, "application/json")
}

// doWithContentType is do with an explicit Content-Type — k8s PATCH requests
// need a patch media type (e.g. application/merge-patch+json) rather than
// plain application/json.
func (j *JobManager) doWithContentType(ctx context.Context, method, path string, body any, contentType string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.BaseURL+path, rdr) //nolint:gosec // BaseURL is the in-cluster Kubernetes API from operator config; the path is built by JobManager
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+j.Token)
	req.Header.Set("Content-Type", contentType)
	return j.Client.Do(req) //nolint:gosec // BaseURL is the in-cluster Kubernetes API from operator config; the path is built by JobManager
}

// ActiveSessionJobs counts live (non-completed) session Jobs.
func (j *JobManager) ActiveSessionJobs() (int, error) {
	resp, err := j.do(http.MethodGet,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs?labelSelector=app%3Dblerg-runner-agent", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("list jobs: %d: %s", resp.StatusCode, raw)
	}
	var list struct {
		Items []struct {
			Status struct {
				Active    int `json:"active"`
				Succeeded int `json:"succeeded"`
				Failed    int `json:"failed"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return 0, err
	}
	n := 0
	for _, it := range list.Items {
		if it.Status.Succeeded == 0 && it.Status.Failed == 0 {
			n++
		}
	}
	return n, nil
}

// fetchSecretKeys GETs a Secret and returns the set of keys with non-empty
// values. k8s stores Secret .data values base64-encoded; a key can exist
// with an empty value, so presence alone isn't enough — decode and check
// length. A missing Secret is reported as zero keys, not an error: an
// operator who hasn't configured any engine yet is a valid, common state.
func (j *JobManager) fetchSecretKeys(name string) (map[string]bool, error) {
	resp, err := j.do(http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return map[string]bool{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("get secret %s: %d: %s", name, resp.StatusCode, raw)
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&secret); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(secret.Data))
	for k, v := range secret.Data {
		decoded, decErr := base64.StdEncoding.DecodeString(v)
		out[k] = decErr == nil && len(decoded) > 0
	}
	return out, nil
}

// detectAvailableEngines checks which engines' credential keys are actually
// present in the configured Secret(s) — the cluster-runtime analog of
// engines.go's host-filesystem DetectAvailable checks. OpenClaw is
// deliberately absent: it isn't wired into CreateSessionJob's env or built
// into the pod image (Dockerfile.devcontainer) today. The second return value
// is whether those same Secret(s) carry a shared git token.
func (j *JobManager) detectAvailableEngines() ([]string, bool) {
	names := []string{j.SecretName}
	if j.OAuthSecretName != "" && j.OAuthSecretName != j.SecretName {
		names = append(names, j.OAuthSecretName)
	}
	keysBySecret := make(map[string]map[string]bool, len(names))
	for _, name := range names {
		keys, err := j.fetchSecretKeys(name)
		if err != nil {
			// Best-effort, like the desktop daemon's own DetectAvailable
			// checks: a fetch failure (e.g. missing RBAC) means "can't
			// tell" for that secret, not "definitely not configured" —
			// logged once so a misconfigured RBAC grant is discoverable.
			log.Printf("cluster: fetch secret %q: %v", name, err)
			continue
		}
		keysBySecret[name] = keys
	}
	has := func(key string) bool {
		for _, name := range names {
			if keysBySecret[name][key] {
				return true
			}
		}
		return false
	}
	var out []string
	if has("ANTHROPIC_API_KEY") || has("CLAUDE_CODE_OAUTH_TOKEN") {
		out = append(out, "claude")
	}
	if has("CODEX_AUTH_JSON") {
		out = append(out, "codex")
	}
	if has("HERMES_ENV_CONTENTS") {
		out = append(out, "hermes")
	}
	return out, has("BLERG_RUNNER_GIT_TOKEN")
}

// gitConfigured reports whether the operator Secret(s) carry a shared git
// token — a user with no personal GitHub credential can only clone when they
// do. Shares AvailableEngines's cache: both come from the same Secret sweep.
func (j *JobManager) gitConfigured() bool {
	j.AvailableEngines() // refreshes gitCacheVal alongside engineCacheVal
	j.engineCacheMu.Lock()
	defer j.engineCacheMu.Unlock()
	return j.gitCacheVal
}

// AvailableEngines returns the engine IDs whose credentials are configured
// for cluster-runtime sessions right now, cached for clusterSecretsCacheTTL.
func (j *JobManager) AvailableEngines() []string {
	j.engineCacheMu.Lock()
	if !j.engineCacheAt.IsZero() && time.Since(j.engineCacheAt) < clusterSecretsCacheTTL {
		defer j.engineCacheMu.Unlock()
		return j.engineCacheVal
	}
	j.engineCacheMu.Unlock()

	found, git := j.detectAvailableEngines()

	j.engineCacheMu.Lock()
	j.engineCacheVal = found
	j.gitCacheVal = git
	j.engineCacheAt = time.Now()
	j.engineCacheMu.Unlock()
	return found
}

// Status summarizes cluster-runtime configuration and live state for the
// status dashboard and the launch UI's engine gating. Never includes secret
// values — only key presence (via AvailableEngines) and non-secret config.
// limits is the pod lifetime cap and idle timeout in effect now: an admin's
// stored override where there is one, the environment default otherwise.
func (j *JobManager) limits() (ttl, idle int64) {
	ttl, idle = j.PodTTLSeconds, j.PodIdleSeconds
	if j.Overrides != nil {
		o := j.Overrides()
		if v, ok := o[db.SettingPodTTLSeconds]; ok && v > 0 {
			ttl = v
		}
		if v, ok := o[db.SettingPodIdleTimeoutSeconds]; ok && v >= 0 {
			idle = v
		}
	}
	return ttl, idle
}

// effectiveMaxSessions is the concurrent session cap in force now: the admin
// override (db.SettingMaxSessions) when one is set and positive, else the
// environment value. Read at each use, so a change applies to the next start.
func (j *JobManager) effectiveMaxSessions() int {
	if j.Overrides != nil {
		if v, ok := j.Overrides()[db.SettingMaxSessions]; ok && v > 0 {
			return int(v)
		}
	}
	return j.MaxSessions
}

func (j *JobManager) Status() protocol.ClusterStatus {
	active, err := j.ActiveSessionJobs()
	if err != nil {
		log.Printf("cluster status: list active jobs: %v", err)
		active = -1 // -1 signals "couldn't tell", distinct from a real 0
	}
	ttl, idle := j.limits()
	return protocol.ClusterStatus{
		Configured:              true,
		Namespace:               j.Namespace,
		Image:                   j.Image,
		DaemonID:                clusterDaemonID,
		MaxSessions:             j.effectiveMaxSessions(),
		MaxSessionsDefault:      j.MaxSessions,
		ActiveSessions:          active,
		AvailableEngines:        nonNilStrings(j.AvailableEngines()),
		GitConfigured:           j.gitConfigured(),
		SecretName:              j.SecretName,
		OAuthSecretName:         j.OAuthSecretName,
		CPURequest:              j.CPURequest,
		MemRequest:              j.MemRequest,
		CPULimit:                j.CPULimit,
		MemLimit:                j.MemLimit,
		PodTTLSeconds:           ttl,
		PodIdleTimeoutSeconds:   idle,
		TerminationGraceSeconds: j.TerminationGraceSeconds,
		TTLSecondsAfterFinished: j.TTLSecondsAfterFinished,
	}
}

// SessionJobSpec is what the spawn/resume paths need to start a pod.
type SessionJobSpec struct {
	SessionID     string
	Repo          string
	Title         string
	Model         string
	Effort        string // "" | one of models.Efforts — see BLERG_RUNNER_EFFORT
	Engine        string // "" (claude) | "codex" — see BLERG_RUNNER_ENGINE
	InitialPrompt string
	Resume        bool
	// ExtraEnv is caller-supplied env for runner-brokered sessions (e.g.
	// blerg-board's BLERG_BOARD_URL/BLERG_BOARD_TOKEN). BLERG_RUNNER_*/ANTHROPIC_API_KEY are
	// rejected upstream so callers cannot shadow blerg-owned variables.
	ExtraEnv map[string]string
	// GitURL overrides GitURLBase+repo for repos outside the default org
	// (runner-brokered sessions may work cross-org repos).
	GitURL string
	// NoRepo is a "No repository" session: Repo and GitURL are empty, the pod
	// is told BLERG_RUNNER_NO_REPO=1 and works in an empty directory, and no
	// clone URL — hence no git token of any kind — goes into the pod.
	NoRepo bool
	// NewRepo is a "New repository" session (newrepo.go): the pod is told
	// BLERG_RUNNER_NEW_REPO=1 and, when the clone of GitURL fails, initialises
	// an empty repository whose origin is GitURL instead of failing the start.
	NewRepo bool
	// NoOperatorGitToken withholds the operator's shared BLERG_RUNNER_GIT_TOKEN
	// from the pod: it gets the launcher's own token for the clone host or
	// none. Set for every NewRepo session — a repository the person could
	// not create (or see) must never be reachable with a token that is not
	// theirs. Unlike NoOperatorFallback it says nothing about engine
	// credentials.
	NoOperatorGitToken bool
	// SpawningAccountID is the verified account id (identity.Principal.Sub)
	// of the human who requested this session, used to look up a personal
	// credential for spec.Engine via fetchPersonalCredential before falling
	// back to the shared operator Secret. Empty for callers that don't have
	// (or don't trust) an account identity — runner-brokered sessions, and
	// any resume requested by someone other than the session's launcher —
	// which always use the shared Secret.
	SpawningAccountID string
	// TokenID is the agent token that asked for this session (empty for human
	// and runner-key starts). It travels with the credential fetch so
	// blerg-core can check the token is still live before handing back the
	// owner's credentials — an account id alone is not proof of that.
	TokenID string
	// AuthSessionID is the browser session (the `sid` of the launching token) that authorises
	// the credential fetches for a human-launched session. Exactly one of TokenID /
	// AuthSessionID is set for a session with a SpawningAccountID; core verifies whichever it
	// is against the account, so neither being set fails the start (errNoLivenessProof).
	AuthSessionID string
	// Plugins is the account's always-on plugin list for this session (Claude sessions with a
	// spawning account only), resolved by ResolvePlugins; PluginsNote says why it is not the
	// whole list. PluginsResolved marks it done so the start plan and CreateSessionJob agree.
	Plugins         []pluginspec.Entry
	PluginsNote     string
	PluginsResolved bool
	// MCPGateway is the session's MCP gateway grant (mcpstart.go): the gateway address and one
	// token per connection. Its JSON goes ONLY into the per-session Secret, and the Job carries
	// a secretKeyRef for mcpGatewayEnvVar, never the value. A session with a grant gets
	// no always-on plugins (pluginsWanted).
	MCPGateway *protocol.MCPGatewayConfig
	// RestrictTools makes the pod run Claude Code hardened (tool allow-list, no ambient MCP, no
	// user settings, plugins, hooks or skills): set for every unattended session — a cron's, or
	// one the board started with a grant — never for a launch-sheet session. The Job carries it
	// as the plain env value restrictToolsEnvVar=1 (not a secret); the pod reads and unsets it
	// at once. Set only in process.
	RestrictTools bool
	// Interaction is the session's interaction mode ("interactive" or "unattended",
	// interaction.go). The Job carries it as the plain env value of interactionEnvVar (not a
	// secret); the pod reads and unsets it at once, and states it in the engine's system prompt.
	// Empty sets nothing, which the pod reads as interactive.
	Interaction string
	// NoOperatorFallback is set for a cron's session (spec 7.4): the shared operator Secret must
	// never supply this session's credentials. CreateSessionJob then fails with
	// ErrNoPersonalCredential, creating no Secret and no Job, whenever the owner's personal
	// credential fetch returns nothing for any reason (no account, core not configured, 404, 5xx,
	// a network error). Set only in process; nothing decoded from a request body reaches it.
	NoOperatorFallback bool
	retried            bool // internal: finished-Job conflict retry guard
}

// ErrNoPersonalCredential is what CreateSessionJob returns for a NoOperatorFallback session whose
// personal credential could not be fetched. It is a credential problem, not a capacity one: the
// cron scheduler fails the run (cron.ErrCredential) instead of holding it.
var ErrNoPersonalCredential = errors.New("the owner's personal credential is unavailable: a cron never runs on the shared operator credential")

// errClusterCap is the cluster's concurrent session cap, reported by CreateSessionJob (its text
// is "cluster session cap reached (N)"). The cron start path treats it as capacity.
var errClusterCap = errors.New("cluster session cap reached")

// CloneURL is the git URL a cluster session clones: the caller's explicit
// override when given (cross-org repos), else the configured base with the
// repo appended. Exported so the start path can record the URL a session
// actually ran with instead of re-deriving a possibly different one later.
func (j *JobManager) CloneURL(repo, override string) string {
	return firstNonEmpty(override, j.CloneURLFor("", repo))
}

// CloneURLFor is the clone URL for repo on the git provider with id provider
// ("" = the default, GitHub). The operator's configured base applies to the
// default provider only:
//   - an explicit BLERG_RUNNER_AGENT_GIT_BASE, or a bare one-segment name,
//     is the base with the repo appended (as it always was);
//   - an "owner/name" with no explicit base is that provider's own HTTPS
//     URL. (Appending it to the per-org default base would name
//     github.com/<org>/<owner>/<name>, which is no repository.)
//
// A non-default provider always gets its own URL: the operator base is not
// one of its hosts. Never carries a token — the pod injects it (runner.authURL).
func (j *JobManager) CloneURLFor(provider, repo string) string {
	if provider == "" {
		provider = gitprovider.GitHubID
	}
	if provider == gitprovider.GitHubID && (j.ExplicitGitURLBase != "" || !strings.Contains(repo, "/")) {
		return strings.TrimRight(j.GitURLBase, "/") + "/" + repo + ".git"
	}
	if ref, ok := gitprovider.Default.ParseFullName(provider, repo); ok {
		p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
		return p.CloneURL(ref.Owner, ref.Name, "")
	}
	// Not a valid name on that provider: the spawn paths refuse this before
	// getting here; fall back to the historical shape rather than panic.
	return strings.TrimRight(j.GitURLBase, "/") + "/" + repo + ".git"
}

// gitCredentialFor decides which git token a session cloning cloneURL may
// carry. kind is the personal-credential kind to fetch for the launcher (""
// = none); operatorOK says whether the operator Secret's shared
// BLERG_RUNNER_GIT_TOKEN may be supplied instead.
//
// A token only ever goes to a host it belongs to — the pod injects it into
// the clone URL, so this decides where it is sent:
//   - the operator's shared token belongs to the operator's configured base
//     host (github.com by default), nowhere else;
//   - a personal token belongs to its registered provider's host only
//     ("github" → github.com, "gitlab" → gitlab.com, …). A host no provider
//     is registered for gets no personal token — not even when it is the
//     operator's own base: a personal GitHub token is GitHub's, and an
//     operator pointing the base at git.acme.example does not make it that
//     host's;
//   - any other host (a caller-supplied git_url elsewhere) gets no token at
//     all: a public clone still works, and no credential leaves for a host
//     nobody vouched for.
func (j *JobManager) gitCredentialFor(cloneURL string) (kind string, operatorOK bool) {
	host := urlHost(cloneURL)
	if host == "" {
		return "", false
	}
	operatorOK = host == urlHost(j.GitURLBase)
	return personalGitKind(cloneURL), operatorOK
}

// personalGitKind is the personal-credential kind whose token may be sent
// with a clone of cloneURL: the provider registered for its host ("github"
// for github.com, "gitlab" for gitlab.com, …), or "" for any other host —
// which gets no personal token at all. The one home of that rule for every
// runtime: a cluster pod (gitCredentialFor) and a daemon's named clone
// (HandlePostSessions) both ask it.
func personalGitKind(cloneURL string) string {
	host := urlHost(cloneURL)
	if host == "" {
		return ""
	}
	if p, ok := gitprovider.Default.ProviderForHost(host); ok {
		return p.ID()
	}
	return ""
}

// urlHost is the lower-cased host of an http(s)/ssh URL or an scp-like
// remote, "" when there is none.
func urlHost(raw string) string {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	hostPart, _, ok := strings.Cut(raw, ":")
	if !ok {
		return ""
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	return strings.ToLower(hostPart)
}

// engineCredentialKey maps an engine id to the k8s Secret key that holds its
// credential. claude is deliberately absent: its credential can be either a
// subscription OAuth token or an API key, and which env var it must land in is
// decided from the fetched value — see claudeCredentialKey.
var engineCredentialKey = map[string]string{
	"codex":  "CODEX_AUTH_JSON",
	"hermes": "HERMES_ENV_CONTENTS",
}

// claudeOAuthPrefix marks a Claude subscription OAuth token. Anything else is
// treated as an API key. The session pod (internal/runner) picks subscription
// vs API mode purely from which of the two env vars is set, so this prefix
// test is what decides how the session bills.
const claudeOAuthPrefix = "sk-ant-oat"

// claudeKeys are the two mutually exclusive Claude credential env vars. When a
// personal claude credential exists BOTH are treated as personal, so the one
// that wasn't chosen is not silently supplied from the operator Secret — an
// operator OAuth token must never shadow a user's own API key, or vice versa.
var claudeKeys = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// sessionCredentialKeys are the credential env vars every session pod is
// wired with, each sourced from either the launcher's per-session Secret or
// the shared operator Secret — see CreateSessionJob's provenance log.
var sessionCredentialKeys = []string{
	"ANTHROPIC_API_KEY",
	"BLERG_RUNNER_DAEMON_TOKEN",
	"BLERG_RUNNER_GIT_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CODEX_AUTH_JSON",
	"HERMES_ENV_CONTENTS",
}

// claudeCredentialKey picks the env var a fetched personal Claude credential
// belongs in.
func claudeCredentialKey(plaintext []byte) string {
	if strings.HasPrefix(string(plaintext), claudeOAuthPrefix) {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return "ANTHROPIC_API_KEY"
}

// gitTokenKey is the env var the session pod reads whichever git token was
// chosen from (internal/runner injects it into the clone URL). The personal
// credential kind is the clone host's gitprovider ID (gitCredentialFor).
const gitTokenKey = "BLERG_RUNNER_GIT_TOKEN" //nolint:gosec // the value is the name of an environment variable, not a credential

// sessionSecretName is the per-session Secret holding the spawning account's
// personal credentials (engine and/or git) plus anything else that must not
// appear literally in the Job spec.
func sessionSecretName(sessionID string) string {
	return "blerg-runner-session-" + strings.ToLower(sessionID)
}

// createSessionSecret creates a per-session k8s Secret named
// blerg-runner-session-<sessionID> holding data (plaintext values — k8s's
// Secret API accepts plaintext under stringData and base64-encodes/stores it
// server-side, so callers here don't need to pre-encode, matching
// fetchSecretKeys's own base64.StdEncoding.DecodeString read-side handling).
// A nil/empty data is a no-op — created=false, err=nil — so callers can pass
// through whatever was collected (personal credential, initial prompt,
// brokered ExtraEnv) without a separate emptiness check.
func (j *JobManager) createSessionSecret(ctx context.Context, sessionID string, data map[string][]byte) (created bool, err error) {
	if len(data) == 0 {
		return false, nil
	}
	stringData := make(map[string]string, len(data))
	for k, v := range data {
		stringData[k] = string(v)
	}
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      sessionSecretName(sessionID),
			"namespace": j.Namespace,
			"labels":    map[string]any{"app": "blerg-runner-agent", "session": sessionID},
		},
		"type":       "Opaque",
		"stringData": stringData,
	}
	resp, err := j.doWithContentType(ctx, http.MethodPost, "/api/v1/namespaces/"+j.Namespace+"/secrets", secret, "application/json")
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		log.Printf("create session secret for %s: 409 already exists", sessionID)
		return false, errSessionSecretExists
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// The k8s API's rejection body can echo back parts of the request —
		// including the names (and, on some validation errors, the shape) of
		// the very credential material this Secret carries. It reaches a
		// browser through the spawn endpoint's error, so it never leaves the
		// server: the caller gets one fixed string, the detail goes to the log.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("create session secret for %s: %d: %s", sessionID, resp.StatusCode, raw)
		return false, errSessionSecretCreate
	}
	return true, nil
}

// errSessionSecretCreate is the only thing a failed per-session Secret create
// ever tells its caller. See createSessionSecret.
var errSessionSecretCreate = errors.New("could not prepare session credentials")

// errSessionSecretExists is createSessionSecret's internal answer to a 409; it
// never leaves CreateSessionJob (the caller sees errSessionSecretCreate).
var errSessionSecretExists = errors.New("session secret already exists")

// sessionCleanupWait bounds how long CreateSessionJob waits for a replaced
// session's old Job/Secret to be gone; sessionCleanupPoll is the poll interval.
var (
	sessionCleanupWait = 10 * time.Second
	sessionCleanupPoll = 200 * time.Millisecond
)

// secretExists reports whether the named Secret is present. Only a definite
// 200 counts as present: any other answer (404, no RBAC, network) is "not
// known to exist", and a Secret that is still there then surfaces as the 409
// on create, which has its own handling.
func (j *JobManager) secretExists(ctx context.Context, name string) bool {
	resp, err := j.doWithContentType(ctx, http.MethodGet,
		"/api/v1/namespaces/"+j.Namespace+"/secrets/"+name, nil, "application/json")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// waitSessionGone polls until the session's Secret (and, when withJob, its
// Job) no longer exist, so a fresh Secret POST cannot race the old one's
// deletion. It returns false when they are still there after
// sessionCleanupWait.
func (j *JobManager) waitSessionGone(sessionID string, withJob bool) bool {
	deadline := time.Now().Add(sessionCleanupWait)
	for {
		jobThere := false
		if withJob {
			jobThere, _ = j.jobPresence(sessionID)
		}
		if !jobThere && !j.secretExists(context.Background(), sessionSecretName(sessionID)) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(sessionCleanupPoll)
	}
}

// clearFinishedSession removes a FINISHED Job and the per-session Secret it
// left behind (both live on for ttlSecondsAfterFinished after the pod ends),
// then waits for them to be gone. A live Job is never touched. Errors are
// logged in detail and returned as the fixed errSessionSecretCreate.
func (j *JobManager) clearFinishedSession(sessionID string) error {
	exists, finished := j.jobPresence(sessionID)
	if !exists || !finished {
		return nil
	}
	if err := j.DeleteSessionJob(sessionID); err != nil {
		log.Printf("clear finished job for %s: %v", sessionID, err)
		return errSessionSecretCreate
	}
	if !j.waitSessionGone(sessionID, true) {
		log.Printf("clear finished job for %s: old job/secret still present after %s", sessionID, sessionCleanupWait)
		return errSessionSecretCreate
	}
	return nil
}

// errSessionJobCreate is the only thing a rejected Job create ever tells its
// caller. See CreateSessionJob's non-2xx branch.
var errSessionJobCreate = errors.New("could not create session job")

// deleteSessionSecret removes the per-session Secret for a session, if one
// was created. A missing Secret (404 — the common case, since most sessions
// never get a personal-credential Secret at all) is not an error.
func (j *JobManager) deleteSessionSecret(ctx context.Context, sessionID string) error {
	resp, err := j.doWithContentType(ctx, http.MethodDelete,
		"/api/v1/namespaces/"+j.Namespace+"/secrets/"+sessionSecretName(sessionID), nil, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("delete secret: %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// internalCredentialFetchRequest/Response mirror core's
// POST /internal/credentials/fetch request/response bodies exactly
// (core/internal/api/internal_handlers.go's internalFetchRequest/Response).
type internalCredentialFetchRequest struct {
	AccountID string `json:"account_id"`
	Engine    string `json:"engine"`
	// Exactly one of TokenID / SessionID names the live thing authorising the
	// fetch: the agent token acting for AccountID, or the human browser
	// session that launched it. Core verifies that specific token/session is
	// live and AccountID's; there is no "any live session" fallback.
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type internalCredentialFetchResponse struct {
	PlaintextBase64 string `json:"plaintext_base64"`
}

// fetchPersonalCredential calls blerg-core's internal credential-fetch
// endpoint (Task 15) for accountID's personal credential for engine.
// found=false, err=nil covers every case CreateSessionJob should treat as
// "fall back to the shared Secret": the feature not being configured
// (CoreURL/CoreInternalKey unset), a 404 (no personal credential — the
// normal, common case), and any other failure (network error, 401, 403,
// 503) — those latter cases usually mean real misconfiguration, so they are
// logged loudly here for an operator to notice, even though the caller
// still degrades gracefully.
func (j *JobManager) fetchPersonalCredential(ctx context.Context, accountID, engine string, proof coreProof) ([]byte, bool, error) {
	return fetchCoreCredential(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, accountID, engine, proof)
}

// personalCredentialsWired reports whether this server talks to core's internal API at all
// (BLERG_CORE_URL + the internal key). Without it there are no personal credentials to fetch,
// and so no proof to require.
func (j *JobManager) personalCredentialsWired() bool {
	return j.CoreURL != "" && j.CoreInternalKey != ""
}

// proof is the liveness proof this spec carries for core.
func (s SessionJobSpec) proof() coreProof {
	return coreProof{TokenID: s.TokenID, SessionID: s.AuthSessionID}
}

// fetchCoreCredential is fetchPersonalCredential without a JobManager, for
// callers that hold their own core address (the API's per-user repo
// listing). Same contract: found=false, err=nil for everything that should
// degrade to "no personal credential". client may be nil.
//
// The one exception is a call with no (or two) liveness proofs: that is a
// caller bug or a pre-sid token, and it fails closed with errNoLivenessProof
// without contacting core — never a silent fallback to the operator secret.
func fetchCoreCredential(ctx context.Context, client *http.Client, coreURL, internalKey, accountID, engine string, proof coreProof) ([]byte, bool, error) {
	if coreURL == "" || internalKey == "" || accountID == "" || engine == "" {
		return nil, false, nil
	}
	if !proof.valid() {
		return nil, false, errNoLivenessProof
	}
	client = coreHTTPClient(client) // never follows a redirect: the request carries the internal key
	raw, err := json.Marshal(internalCredentialFetchRequest{AccountID: accountID, Engine: engine, TokenID: proof.TokenID, SessionID: proof.SessionID})
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/credentials/fetch", bytes.NewReader(raw))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", internalKey)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("fetchPersonalCredential: request to core failed, falling back to shared secret: %v", err)
		return nil, false, nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out internalCredentialFetchResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			log.Printf("fetchPersonalCredential: decode response failed, falling back to shared secret: %v", err)
			return nil, false, nil
		}
		plaintext, err := base64.StdEncoding.DecodeString(out.PlaintextBase64)
		if err != nil {
			log.Printf("fetchPersonalCredential: decode plaintext_base64 failed, falling back to shared secret: %v", err)
			return nil, false, nil
		}
		// An empty (or whitespace-only) credential is not a credential:
		// treated as found it would both suppress the operator fallback and
		// ship an empty env var into the pod, i.e. a session that fails to
		// authenticate with nothing to point at. Same answer as a 404.
		if len(bytes.TrimSpace(plaintext)) == 0 {
			log.Printf("fetchPersonalCredential: core returned an empty credential for account=%s engine=%s, falling back to shared secret",
				accountID, engine)
			return nil, false, nil
		}
		return plaintext, true, nil
	case http.StatusNotFound:
		// Normal, common case: account_id simply hasn't configured a
		// personal credential for this engine.
		return nil, false, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("fetchPersonalCredential: core returned %d for account=%s engine=%s, falling back to shared secret: %s",
			resp.StatusCode, accountID, engine, body)
		return nil, false, nil
	}
}

// CreateSessionJob creates the runner Job. Enforces the concurrency cap.
func (j *JobManager) CreateSessionJob(spec SessionJobSpec) error {
	limit := j.effectiveMaxSessions()
	if n, err := j.ActiveSessionJobs(); err == nil && n >= limit {
		return fmt.Errorf("%w (%d)", errClusterCap, limit)
	}
	// A cron session has no fallback: with nothing to authenticate as, the shared Secret would.
	if spec.NoOperatorFallback && spec.SpawningAccountID == "" {
		return ErrNoPersonalCredential
	}
	// Fail closed: a session for an account, with personal credentials wired, must name the
	// live token/session that authorises reading them. Checked before anything is created.
	if spec.SpawningAccountID != "" && j.personalCredentialsWired() && !spec.proof().valid() {
		return errNoLivenessProof
	}
	cloneURL := ""
	if !spec.NoRepo {
		cloneURL = j.CloneURL(spec.Repo, spec.GitURL)
	}
	env := []map[string]any{
		{"name": "BLERG_RUNNER_SERVER_WS", "value": j.ServerWSURL},
		{"name": "BLERG_RUNNER_SERVER_HTTP", "value": j.ServerHTTPURL},
		{"name": "BLERG_RUNNER_SESSION_ID", "value": spec.SessionID},
		{"name": "BLERG_RUNNER_REPO", "value": spec.Repo},
		{"name": "BLERG_RUNNER_TITLE", "value": spec.Title},
		{"name": "BLERG_RUNNER_MODEL", "value": spec.Model},
		{"name": "BLERG_RUNNER_EFFORT", "value": spec.Effort},
		{"name": "BLERG_RUNNER_ENGINE", "value": spec.Engine},
		{"name": "BLERG_RUNNER_GIT_URL", "value": cloneURL},
	}
	// Always-on plugins: non-secret configuration, resolved (fail soft) once per start.
	j.ResolvePlugins(context.Background(), &spec)
	env = append(env, j.pluginEnv(spec)...)
	podTTL, podIdle := j.limits()
	if podIdle > 0 {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_IDLE_TIMEOUT_SECONDS", "value": strconv.FormatInt(podIdle, 10)})
	}
	if spec.Resume {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_RESUME", "value": "1"})
	}
	if spec.NoRepo {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_NO_REPO", "value": "1"})
	}
	if spec.NewRepo {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_NEW_REPO", "value": "1"})
	}
	// A grant implies the restriction, so a session that reaches here with one and no flag
	// (a caller that forgot) is still restricted.
	if spec.RestrictTools {
		if _, dup := spec.ExtraEnv[restrictToolsEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", restrictToolsEnvVar)
		}
		env = append(env, map[string]any{"name": restrictToolsEnvVar, "value": "1"})
	}
	if spec.Interaction != "" {
		if _, dup := spec.ExtraEnv[interactionEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", interactionEnvVar)
		}
		env = append(env, map[string]any{"name": interactionEnvVar, "value": spec.Interaction})
	}

	// sessionData collects everything that must never appear as a literal
	// env value in the Job spec (visible via `kubectl describe job`/`get job
	// -o yaml` to anyone who can read Jobs but not Secrets): the initial
	// prompt, every caller-supplied ExtraEnv entry (runner-brokered session
	// tokens, e.g. BLERG_BOARD_TOKEN), and — below — a personal engine
	// credential when one was found. Whatever ends up in here is written to
	// one per-session Secret and referenced from the Job via secretKeyRef.
	sessionData := make(map[string][]byte, len(spec.ExtraEnv)+2)
	if spec.InitialPrompt != "" {
		sessionData["BLERG_RUNNER_INITIAL_PROMPT"] = []byte(spec.InitialPrompt)
	}
	for k, v := range spec.ExtraEnv {
		sessionData[k] = []byte(v)
	}
	// The MCP gateway grant: tokens, so Secret only. The caller's env can never name this key.
	if spec.MCPGateway != nil {
		if _, dup := spec.ExtraEnv[mcpGatewayEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", mcpGatewayEnvVar)
		}
		raw, err := json.Marshal(spec.MCPGateway)
		if err != nil {
			return errSessionSecretCreate
		}
		sessionData[mcpGatewayEnvVar] = raw
	}

	// Personal-credential-first: for the engine actually being spawned, and
	// for git access, try the spawning account's own credentials (via
	// blerg-core) before falling back to the shared operator Secret.
	// personalKeys holds every env-var key (of the six below) that the
	// operator Secret must NOT supply for this session; the subset of those
	// keys present in sessionData is sourced from the per-session Secret
	// instead, and the rest are omitted from the pod entirely. Empty means no
	// personal credential was found, i.e. today's behavior for every key.
	personalKeys := map[string]bool{}
	gitKind, gitOperatorOK := j.gitCredentialFor(cloneURL)
	effectiveEngine := spec.Engine
	if effectiveEngine == "" {
		effectiveEngine = "claude"
	}
	if spec.SpawningAccountID != "" {
		if effectiveEngine == "claude" {
			plaintext, found, err := j.fetchPersonalCredential(context.Background(), spec.SpawningAccountID, "claude", spec.proof())
			if spec.NoOperatorFallback && (err != nil || !found) {
				log.Printf("cluster session %s: no personal claude credential for account %s and no operator fallback for a cron: not starting",
					spec.SessionID, spec.SpawningAccountID)
				return ErrNoPersonalCredential
			}
			if err == nil && found {
				// Both claude keys become personal, only the chosen one is
				// emitted: a personal API key must not run alongside the
				// operator's OAuth token (or vice versa), because the pod
				// picks its billing mode from whichever var is set.
				for _, k := range claudeKeys {
					personalKeys[k] = true
				}
				sessionData[claudeCredentialKey(plaintext)] = plaintext
			}
		} else if credKey, ok := engineCredentialKey[effectiveEngine]; ok {
			plaintext, found, err := j.fetchPersonalCredential(context.Background(), spec.SpawningAccountID, effectiveEngine, spec.proof())
			if spec.NoOperatorFallback && (err != nil || !found) {
				return ErrNoPersonalCredential
			}
			if err == nil && found {
				sessionData[credKey] = plaintext
				personalKeys[credKey] = true
			}
		}
		// The git credential is independent of the engine: whoever launched
		// the session clones as themselves when they've connected a token
		// for the clone URL's provider, whatever agent they're running.
		if gitKind != "" {
			plaintext, found, err := j.fetchPersonalCredential(context.Background(), spec.SpawningAccountID, gitKind, spec.proof())
			if spec.NoOperatorFallback && (err != nil || !found) {
				return ErrNoPersonalCredential
			}
			if err == nil && found {
				sessionData[gitTokenKey] = plaintext
				personalKeys[gitTokenKey] = true
			}
		}
	}
	// The operator's shared git token belongs to its own base host only
	// (gitCredentialFor): for a clone anywhere else it is claimed as
	// "personal" so the operator Secret can never supply it — the key is
	// then either the launcher's own token for that provider or omitted.
	if !gitOperatorOK || spec.NoOperatorGitToken {
		personalKeys[gitTokenKey] = true
	}

	// secretCreated tracks whether this call actually created a per-session
	// Secret — the signal every cleanup/adoption path below keys off, since a
	// Secret can now exist purely for the prompt/ExtraEnv even when no
	// personal credential was found (personalKeys empty).
	secretCreated := false
	if len(sessionData) > 0 {
		// A FINISHED Job (graceful stop, idle exit, deadline) keeps its
		// per-session Secret for ttlSecondsAfterFinished, and that Secret would
		// 409 the create below — clear both first. A live Job is left alone.
		if err := j.clearFinishedSession(spec.SessionID); err != nil {
			return err
		}
		created, err := j.createSessionSecret(context.Background(), spec.SessionID, sessionData)
		if errors.Is(err, errSessionSecretExists) {
			// Still 409: either a live session's Secret (refuse, untouched) or
			// an orphan of a crashed start (no live Job owns it) — replace the
			// orphan and retry exactly once.
			if exists, finished := j.jobPresence(spec.SessionID); exists && !finished {
				return errSessionSecretCreate
			}
			if derr := j.deleteSessionSecret(context.Background(), spec.SessionID); derr != nil {
				log.Printf("delete orphan session secret for %s: %v", spec.SessionID, derr)
				return errSessionSecretCreate
			}
			if !j.waitSessionGone(spec.SessionID, false) {
				log.Printf("orphan session secret for %s still present after %s", spec.SessionID, sessionCleanupWait)
				return errSessionSecretCreate
			}
			created, err = j.createSessionSecret(context.Background(), spec.SessionID, sessionData)
			if errors.Is(err, errSessionSecretExists) {
				err = errSessionSecretCreate
			}
		}
		if err != nil {
			// Fail closed rather than falling back to a literal env value:
			// the prompt/ExtraEnv (runner-brokered session tokens) and any
			// personal credential must never appear as plaintext in the Job
			// spec, so a Secret-create failure fails the whole spawn instead
			// of silently downgrading confidentiality.
			//
			// The error travels to a browser (the spawn endpoint surfaces
			// it), so it carries no k8s detail — that is what
			// errSessionSecretCreate is. Whatever the failure actually said
			// was logged where it happened.
			log.Printf("create per-session secret for %s: %v", spec.SessionID, err)
			return errSessionSecretCreate
		}
		secretCreated = created
		if spec.InitialPrompt != "" {
			env = append(env, map[string]any{
				"name": "BLERG_RUNNER_INITIAL_PROMPT",
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": "BLERG_RUNNER_INITIAL_PROMPT"},
				},
			})
		}
		if spec.MCPGateway != nil {
			env = append(env, map[string]any{
				"name": mcpGatewayEnvVar,
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": mcpGatewayEnvVar, "optional": false},
				},
			})
		}
		extraKeys := make([]string, 0, len(spec.ExtraEnv))
		for k := range spec.ExtraEnv {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			env = append(env, map[string]any{
				"name": k,
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": k},
				},
			})
		}
	}

	// provenance records, per credential env var, where this session's value
	// came from — see the log line after the loop. Keys only, never values.
	provenance := make([]string, 0, len(sessionCredentialKeys))
	// A restricted session (a cron, a grant, RestrictTools) runs claude with file tools only, so
	// the operator's codex and hermes credentials are of no use to it and only widen what a
	// compromised pod exposes: they are withheld.
	restricted := spec.RestrictTools || spec.MCPGateway != nil || spec.NoOperatorFallback
	for _, key := range sessionCredentialKeys {
		if restricted && (key == "CODEX_AUTH_JSON" || key == "HERMES_ENV_CONTENTS") {
			if _, dup := spec.ExtraEnv[key]; !dup {
				provenance = append(provenance, key+"=withheld")
				continue
			}
		}
		// A caller-supplied ExtraEnv entry of the same name was already
		// emitted (from the per-session Secret) by the loop above. Emitting
		// this one too would put two env entries with the same name in one
		// container — a shape no caller should have to reason about.
		if _, dup := spec.ExtraEnv[key]; dup {
			provenance = append(provenance, key+"=caller")
			continue
		}
		if personalKeys[key] {
			if _, ok := sessionData[key]; ok {
				provenance = append(provenance, key+"=personal")
				env = append(env, map[string]any{
					"name": key,
					"valueFrom": map[string]any{
						"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": key, "optional": false},
					},
				})
			} else {
				provenance = append(provenance, key+"=omitted")
			}
			// Claimed by the personal set but not the value that was chosen
			// (the other half of the claude pair): emit nothing, so the
			// operator Secret cannot supply it behind the user's back.
			continue
		}
		secretName := j.SecretName
		// Operator-synced credentials (rotation flows from the secrets operator): the
		// oauth token, codex's auth.json contents, hermes's whole .env
		// contents, and, when present there, the git token.
		if (key == "CLAUDE_CODE_OAUTH_TOKEN" || key == "BLERG_RUNNER_GIT_TOKEN" || key == "CODEX_AUTH_JSON" || key == "HERMES_ENV_CONTENTS") && j.OAuthSecretName != "" {
			secretName = j.OAuthSecretName
		}
		provenance = append(provenance, key+"=operator")
		env = append(env, map[string]any{
			"name": key,
			"valueFrom": map[string]any{
				// ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN are alternative
				// billing modes — either may be absent. CODEX_AUTH_JSON and
				// HERMES_ENV_CONTENTS are only needed when the session's engine
				// is codex or hermes respectively.
				"secretKeyRef": map[string]any{"name": secretName, "key": key,
					"optional": key != "BLERG_RUNNER_DAEMON_TOKEN"},
			},
		})
	}
	// One line per session saying which identity each credential env var runs
	// under: personal (the launcher's own, via the per-session Secret),
	// operator (the shared Secret), caller (a runner-brokered ExtraEnv entry),
	// or omitted (deliberately not supplied — the unchosen half of the
	// claude OAuth/API pair). Key names and outcomes only; never a value.
	log.Printf("cluster session %s credential provenance: %s", spec.SessionID, strings.Join(provenance, " "))
	job := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      j.jobName(spec.SessionID),
			"namespace": j.Namespace,
			"labels":    map[string]any{"app": "blerg-runner-agent", "session": spec.SessionID},
		},
		"spec": map[string]any{
			"backoffLimit":            0,
			"activeDeadlineSeconds":   podTTL,
			"ttlSecondsAfterFinished": j.TTLSecondsAfterFinished,
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "blerg-runner-agent", "session": spec.SessionID},
				},
				"spec": map[string]any{
					"restartPolicy":                 "Never",
					"terminationGracePeriodSeconds": j.TerminationGraceSeconds,
					// The pod never talks to the Kubernetes API: everything it
					// needs arrives as env from the per-session Secret. Without
					// this the namespace's default ServiceAccount token would be
					// mounted into a pod that runs arbitrary agent-driven code.
					"automountServiceAccountToken": false,
					"containers": []map[string]any{{
						"name":            "runner",
						"image":           j.Image,
						"securityContext": sessionContainerSecurityContext(),
						"env":             env,
						"resources": map[string]any{
							"requests": map[string]any{"cpu": j.CPURequest, "memory": j.MemRequest},
							"limits":   map[string]any{"cpu": j.CPULimit, "memory": j.MemLimit},
						},
					}},
				},
			},
		},
	}
	resp, err := j.do(http.MethodPost, "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs", job)
	if err != nil {
		// The Secret was created moments ago for a Job that will never exist.
		// Leaving it behind would strand a plaintext credential/prompt/env in
		// the cluster forever — nothing else ever looks at it again.
		j.deleteOrphanSessionSecret(secretCreated, spec.SessionID)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		// A live Job means a double spawn (fine). A FINISHED Job (pod died,
		// deadline hit) blocks resume forever if left in place — delete it
		// and retry once.
		if spec.retried {
			// The Secret this call created will never be adopted by any Job
			// (the retry's own Secret already carries the same data) — same
			// leak as the network-error path above.
			j.deleteOrphanSessionSecret(secretCreated, spec.SessionID)
			return fmt.Errorf("create job: conflict persists for %s", spec.SessionID)
		}
		if j.jobFinished(spec.SessionID) {
			if err := j.DeleteSessionJob(spec.SessionID); err != nil {
				return err
			}
			spec.retried = true
			return j.CreateSessionJob(spec)
		}
		// The Job that already exists (the one holding the live Job's
		// conflicting name) is not this call's Job — it will never adopt the
		// Secret just created here via ownerReferences, so it would strand a
		// plaintext credential/prompt/env forever if left in place.
		j.deleteOrphanSessionSecret(secretCreated, spec.SessionID)
		return nil
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// The k8s API's rejection body echoes back parts of the manifest it
		// refused — which here carries the session's env, image and namespace.
		// This error reaches the caller as the start's 503 AND is persisted as
		// the session's error_reason, so it is a fixed string: the detail goes
		// to the log, where an operator can read it.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("create session job for %s: %d: %s", spec.SessionID, resp.StatusCode, raw)
		j.deleteOrphanSessionSecret(secretCreated, spec.SessionID)
		return errSessionJobCreate
	}
	// Hand the per-session Secret's lifetime to the Job via ownerReferences,
	// so k8s's own GC cascade deletes it whenever the Job goes away — including
	// the paths runner never hears about (ttlSecondsAfterFinished and
	// activeDeadlineSeconds expiry), which is every session that ends on its
	// own rather than being explicitly killed through DeleteSessionJob.
	if secretCreated {
		var created struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil || created.Metadata.UID == "" {
			log.Printf("adopt session secret for %s: could not read created Job uid (%v) — the Secret will only be cleaned up on explicit session deletion", spec.SessionID, err)
		} else if err := j.adoptSessionSecret(spec.SessionID, created.Metadata.UID); err != nil {
			log.Printf("adopt session secret for %s: %v", spec.SessionID, err)
		}
	}
	return nil
}

// deleteOrphanSessionSecret removes a per-session Secret that was created for
// a Job which then never came into being (or is not the Job that will end up
// owning the Secret) — a failed create, a 409 against an already-live Job, or
// a persisting conflict. A no-op when no per-session Secret was created for
// this call (created == false). Best-effort and logged: the caller is
// already returning (or has already handled) the more important error/state.
func (j *JobManager) deleteOrphanSessionSecret(created bool, sessionID string) {
	if !created {
		return
	}
	if err := j.deleteSessionSecret(context.Background(), sessionID); err != nil {
		log.Printf("delete orphaned session secret for %s: %v", sessionID, err)
	}
}

// adoptSessionSecret sets an ownerReferences entry on the per-session Secret
// pointing at the just-created Job, via a JSON merge patch.
//
// This can only happen AFTER the Job exists (the uid isn't known before), and
// the Secret has to exist BEFORE the Job (the pod's secretKeyRef resolves at
// container start with optional:false) — hence create-then-adopt rather than
// setting ownerReferences at create time.
//
// controller/blockOwnerDeletion are both false: this is a pure
// "delete me with my owner" link, not a controller claim, and blocking the
// Job's own deletion on it would be a worse failure mode than a stray Secret.
func (j *JobManager) adoptSessionSecret(sessionID, jobUID string) error {
	patch := map[string]any{
		"metadata": map[string]any{
			"ownerReferences": []map[string]any{{
				"apiVersion":         "batch/v1",
				"kind":               "Job",
				"name":               j.jobName(sessionID),
				"uid":                jobUID,
				"controller":         false,
				"blockOwnerDeletion": false,
			}},
		},
	}
	resp, err := j.doWithContentType(context.Background(), http.MethodPatch,
		"/api/v1/namespaces/"+j.Namespace+"/secrets/"+sessionSecretName(sessionID),
		patch, "application/merge-patch+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("patch secret ownerReferences: %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// jobFinished reports whether the session's Job object exists in a terminal
// state (Succeeded/Failed > 0).
func (j *JobManager) jobFinished(sessionID string) bool {
	_, finished := j.jobPresence(sessionID)
	return finished
}

// jobPresence reports whether the session's Job object exists and, if so,
// whether it is in a terminal state. An unreadable answer counts as "exists,
// not finished" so it is never mistaken for a deletable Job; only a definite
// 404 is "does not exist".
func (j *JobManager) jobPresence(sessionID string) (exists, finished bool) {
	resp, err := j.do(http.MethodGet,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+j.jobName(sessionID), nil)
	if err != nil {
		return true, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, false
	}
	if resp.StatusCode != http.StatusOK {
		return true, false
	}
	var job struct {
		Status struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&job) != nil {
		return true, false
	}
	return true, job.Status.Succeeded > 0 || job.Status.Failed > 0
}

// DeleteSessionJob removes the Job (and its pod) for a killed session.
func (j *JobManager) DeleteSessionJob(sessionID string) error {
	resp, err := j.do(http.MethodDelete,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+j.jobName(sessionID)+"?propagationPolicy=Background", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("delete job: %d: %s", resp.StatusCode, raw)
	}
	// Best-effort: also remove any per-session credential Secret. Most
	// sessions never had one (no personal credential was found at spawn
	// time), in which case this is a harmless 404. Logged rather than
	// returned as an error so a Secret-delete hiccup never blocks the more
	// important Job teardown above.
	if err := j.deleteSessionSecret(context.Background(), sessionID); err != nil {
		log.Printf("delete session secret for %s: %v", sessionID, err)
	}
	return nil
}

// ─── Cluster session routing helpers ─────────────────────────────────────────

// hubJobs is set once at startup; nil means the cluster runtime is
// unavailable (not in-cluster, or BLERG_RUNNER_AGENT_IMAGE unset).
func (h *Hub) SetJobManager(j *JobManager) { h.mu.Lock(); h.jobs = j; h.mu.Unlock() }

func (h *Hub) JobManager() *JobManager { h.mu.Lock(); defer h.mu.Unlock(); return h.jobs }

// resumeClusterSession re-creates the runner Job for a disconnected cluster
// session, delivering text as the resume prompt. No-ops (with a log) when the
// session isn't a resumable cluster session.
//
// requesterAccountID is the verified account id of whoever asked for the
// resume ("" for callers with no human identity — the board/runner-key path).
// It is what keeps a resume from re-minting somebody else's credentials: the
// stored spawning_account_id is only handed back to CreateSessionJob when the
// requester IS that account. Anyone else resumes on the operator Secret.
func resumeClusterSession(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, requesterAccountID, requesterSessionID string) {
	jm := h.JobManager()
	if jm == nil || pool == nil {
		log.Printf("agent_user_message %s: no daemon and no cluster runtime — dropping", sessionID)
		return
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil {
		log.Printf("resume %s: session lookup failed: %v", sessionID, err)
		return
	}
	if row.Status != "disconnected" {
		log.Printf("resume %s: status %q not resumable — dropping message", sessionID, row.Status)
		return
	}
	title := ""
	if row.Title != nil {
		title = *row.Title
	}
	model := ""
	if row.Model != nil {
		model = *row.Model
	}
	engine := ""
	if row.Engine != nil {
		engine = *row.Engine
	}
	// The session's current effort (its launch effort, or whatever it was
	// switched to since) carries over to the resumed pod like the model does.
	effort := derefOrEmpty(row.Effort)
	// The row mirrors whatever the session last reported (model_changed),
	// and rows written before in-session changes were validated can hold a
	// value the pod would refuse (spawnModelProblem) — which would turn a
	// resume into a dead pod. Drop what fails the engine's rules and resume
	// on the engine's default instead.
	if !models.ValidModelFor(engine, model) {
		log.Printf("resume %s: dropping stored model %q (fails the %s model rule)", sessionID, model, models.NormalizeEngine(engine))
		model = ""
	}
	if !models.ValidEffortFor(engine, effort) {
		log.Printf("resume %s: dropping stored effort %q (not a %s effort)", sessionID, effort, models.NormalizeEngine(engine))
		effort = ""
	}
	// Only the launcher's own resume re-mints the launcher's personal
	// credentials. Without this equality check, any authenticated user (or the
	// board's runner key) sending a message to a disconnected cluster session
	// would spawn a pod holding another human's Claude token and GitHub token.
	launcher := derefOrEmpty(row.SpawningAccountID)
	spawningAccountID := ""
	if launcher != "" && requesterAccountID == launcher {
		spawningAccountID = launcher
	} else if launcher != "" {
		log.Printf("resume %s: resuming without personal credentials — requester %q is not the launcher %q",
			sessionID, requesterAccountID, launcher)
	}
	// The token the session was started with travels with the resume too:
	// without it core has nothing to check for an agent-owned session, and
	// the resumed pod would silently fall back to the operator Secret.
	// Otherwise the resume is authorised by the requester's own browser session, which core
	// checks belongs to the launcher's account (they are the same person: equality above).
	tokenID, authSessionID := "", ""
	if spawningAccountID != "" {
		if tokenID = derefOrEmpty(row.TokenID); tokenID == "" {
			authSessionID = requesterSessionID
		}
	}
	// A session that holds MCP connections resumes only for the account that started it, from
	// a signed-in browser session; otherwise the resume is refused and nothing is started.
	grants, problem := grantResumeProblem(ctx, pool, sessionID, launcher, requesterAccountID, requesterSessionID)
	if problem != nil {
		log.Printf("resume %s: refused: %s", sessionID, problem.Message)
		return
	}
	// The session stays "disconnected" until the new pod connects, so restart
	// its status clock now: the reconciler measures both the 24 h resume
	// expiry and the missing-Job grace from that stamp, and neither must fire
	// on a session whose replacement pod is still booting.
	if err := db.TouchSessionStatusChanged(ctx, pool, sessionID); err != nil {
		log.Printf("resume %s: touch status clock: %v", sessionID, err)
	}
	newRepo := db.GetSessionNewRepo(ctx, pool, sessionID)
	// A resume is a start too: the browser shows its progress the same way.
	spec := SessionJobSpec{
		SessionID: sessionID, Repo: row.Repo, Title: title, Model: model, Effort: effort, Engine: engine,
		InitialPrompt: text, Resume: true,
		// The old pod's token died with its session's last end; the new pod gets its own.
		ExtraEnv: withClusterSessionToken(ctx, pool, sessionID, nil),
		// A cluster row's repo is empty only for a "No repository" session:
		// every other cluster start refuses an empty repo. It resumes the same
		// way — an empty workspace, nothing cloned.
		NoRepo: scratch.IsNoRepo(row.Repo),
		// The clone URL the session started with (recorded at start): a
		// cross-org or non-GitHub repository resumes from the same place
		// instead of being re-derived from the operator's base. Empty on
		// rows from before it was recorded, which re-derive as they always did.
		GitURL: derefOrEmpty(row.GitURL),
		// A "New repository" session resumes as one: the pod may still have to
		// initialise, and the operator's git token stays out (newrepo.go).
		NewRepo:            newRepo,
		NoOperatorGitToken: newRepo,
		SpawningAccountID:  spawningAccountID,
		TokenID:            tokenID,
		AuthSessionID:      authSessionID,
		// A resumed cron session is still a cron's: it never falls back to the operator credential
		// (a resume by anyone but the owner has no account, and so no credential, and fails).
		NoOperatorFallback: row.CronID != nil,
		// A resumed session is restricted exactly when it was started so (migration 036): a
		// cron's, or a board-started grant session. A launch-sheet session with connections
		// comes back with its tools.
		RestrictTools: row.CronID != nil || db.GetSessionRestrictTools(ctx, pool, sessionID),
		// The mode the session was started in (migration 038); a row from before it is read
		// by who started it (effectiveInteraction).
		Interaction: effectiveInteraction(row),
	}
	if len(grants) > 0 {
		// New tokens for the new pod, replacing the old ones in one transaction: the old
		// tokens died with the old pod.
		gateway, err := reissueGrants(ctx, h, pool, sessionID, launcher, requesterSessionID, grants)
		if err != nil {
			log.Printf("resume %s: re-issue MCP grants: %v", sessionID, err)
			announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, nil)
			clusterJobCreateFailed(ctx, h, pool, sessionID, "MCP connections could not be granted again")
			return
		}
		spec.MCPGateway = gateway
	}
	jm.ResolvePlugins(ctx, &spec)
	announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, pluginStage(spec.Plugins, spec.PluginsNote))
	if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		log.Printf("resume %s: %v", sessionID, err)
		clusterJobCreateFailed(ctx, h, pool, sessionID, err.Error())
		return
	}
	clusterJobCreated(ctx, h, pool, sessionID, 0)
}

// derefOrEmpty returns "" for a nil string pointer, else the pointed-to
// value — used for the several *string SessionRow fields that are read back
// on resume.
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sessionAgentUID is the uid of the `agent` user baked into
// runner/Dockerfile.devcontainer (useradd -u 1000). The image names the user
// rather than numbering it, and the kubelet cannot verify that a name is
// non-root, so runAsNonRoot needs the number spelled out here.
const sessionAgentUID = 1000

// sessionContainerSecurityContext is the hardening every session pod gets:
// non-root, no privilege escalation, no Linux capabilities, the runtime's
// default seccomp filter (the Kubernetes "restricted" Pod Security profile).
// It deliberately does NOT set readOnlyRootFilesystem: the agent writes
// /workspace (its HOME), /tmp and the npm, go and pip caches.
func sessionContainerSecurityContext() map[string]any {
	return map[string]any{
		"runAsNonRoot":             true,
		"runAsUser":                sessionAgentUID,
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]any{"drop": []string{"ALL"}},
		"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
	}
}

// nonNilStrings turns a nil slice into an empty one, so JSON encodes [] and never null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
