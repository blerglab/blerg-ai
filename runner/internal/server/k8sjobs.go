package server

import (
	"context"
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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/commandsteps"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
	"github.com/blerglab/blerg-ai/runner/skills"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JobManager creates and deletes per-session runner Jobs against the k8s API
// using the in-cluster service account. It deliberately speaks plain HTTP to
// the API server (no client-go dependency) — the two calls we need are tiny.
type JobManager struct {
	// Tests advance the create deadline after the real POST has committed.
	launchCreateContext func(context.Context) (context.Context, context.CancelFunc)

	resolveWorkBranch func(context.Context, string) (string, error)
	// Trusted operator scheduling policy, never supplied by a session request.
	ExcludedNodes []string

	// Explicit storage class opts into enforced persistent-volume capacity.
	WorkspaceStorageClass string
	WorkspaceDefaultClass string
	// Tests replace the network read; nil uses the guarded Git HTTP precheck.
	repositoryRead func(context.Context, string, string) error
	// Tests replace the base-branch provider check (BLG-501); nil uses the guarded Git
	// HTTP advertisement read (checkBaseBranchRemote).
	baseBranchRemote func(ctx context.Context, cloneURL, branch, token string) (remoteSHA string, cred credentialRefusal, err error)
	// Optional owner for start watchers; nil retains process-lifetime defaults.
	startWatches  *clusterWatchLifetime
	codexCatalog  clusterCodexCatalogCache
	NativeHistory NativeHistoryConfig
	historyPool   *pgxpool.Pool
	launchHub     *Hub
	// Test seam; production always uses the durable current-launch binding.
	mintRuntimeLaunch func(context.Context, string, string) (string, string, error)

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
	PluginAllow       pluginspec.Allowlist
	PluginAllowRaw    string
	MarketplacePolicy *pluginspec.LivePolicy

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

	// activeCache holds Status's live Job count for activeJobsCacheTTL: the
	// launch sheet, the sessions sidebar and the app shell each read
	// /api/cluster/status, and a Job list per read is the slowest part of it.
	// Capacity checks never read it — they count Jobs live.
	activeCacheMu  sync.Mutex
	activeCacheAt  time.Time
	activeCacheVal int

	// Hermes is the cluster's shared Hermes provider configuration
	// (hermesproviders.go); zero when none is configured.
	Hermes HermesSharedConfig
}

// clusterSecretsCacheTTL bounds how often AvailableEngines re-queries the
// k8s API for the configured credential Secret(s).
const clusterSecretsCacheTTL = 30 * time.Second

// activeJobsCacheTTL bounds how often Status lists session Jobs.
const activeJobsCacheTTL = 10 * time.Second

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
		WorkspaceStorageClass: os.Getenv("BLERG_RUNNER_WORKSPACE_STORAGE_CLASS"),
		WorkspaceDefaultClass: os.Getenv("BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS"),
		BaseURL:               "https://" + host + ":" + port,
		Token:                 string(bytes.TrimSpace(token)),
		Namespace:             ns,
		Image:                 image,
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
		NativeHistory:    NativeHistoryConfig{Enabled: strings.EqualFold(os.Getenv("BLERG_RUNNER_NATIVE_HISTORY"), "true"), StorageClass: os.Getenv("BLERG_RUNNER_NATIVE_HISTORY_STORAGE_CLASS"), Size: envOr("BLERG_RUNNER_NATIVE_HISTORY_SIZE", "1Gi"), MaxClaims: envOrInt("BLERG_RUNNER_NATIVE_HISTORY_MAX_CLAIMS", 128), Retention: time.Duration(envOrInt("BLERG_RUNNER_NATIVE_HISTORY_RETENTION_HOURS", 7*24)) * time.Hour},

		ExcludedNodes:           excludedNodesFromEnv(),
		CPURequest:              envOr("BLERG_RUNNER_POD_CPU_REQUEST", "500m"),
		MemRequest:              envOr("BLERG_RUNNER_POD_MEM_REQUEST", "1Gi"),
		CPULimit:                envOr("BLERG_RUNNER_POD_CPU_LIMIT", "1"),
		MemLimit:                envOr("BLERG_RUNNER_POD_MEM_LIMIT", "4Gi"),
		TerminationGraceSeconds: int64(envOrInt("BLERG_RUNNER_POD_TERMINATION_GRACE_SECONDS", 120)),
		TTLSecondsAfterFinished: int64(envOrInt("BLERG_RUNNER_POD_TTL_AFTER_FINISHED_SECONDS", 3600)),

		Hermes: hermesSharedFromEnv(),
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

// doWithContentType is the guarded JSON API boundary. PATCH requests need a
// patch media type (e.g. application/merge-patch+json).
func (j *JobManager) doWithContentType(ctx context.Context, method, path string, body any, contentType string) (*http.Response, error) {
	if err := j.guardJobScheduling(method, path, body); err != nil {
		return nil, err
	}
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	return j.sendJSON(ctx, method, path, raw, contentType)
}

func (j *JobManager) sendJSON(ctx context.Context, method, path string, raw []byte, contentType string) (*http.Response, error) {
	var rdr io.Reader
	if raw != nil {
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
	return j.activeSessionJobsContext(context.Background())
}
func (j *JobManager) activeSessionJobsContext(ctx context.Context) (int, error) {
	resp, err := j.doWithContentType(ctx, http.MethodGet,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs?labelSelector=app%3Dblerg-runner-agent", nil, "application/json")
	if err != nil {
		return 0, jobSubmissionTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, jobSubmissionRejection(resp)
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
		return 0, jobSubmissionTransportError(err)
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
	return j.fetchSecretKeysContext(context.Background(), name)
}

func (j *JobManager) fetchSecretKeysContext(ctx context.Context, name string) (map[string]bool, error) {
	resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+url.PathEscape(name), nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return map[string]bool{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("engine credential availability could not be read")
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, &secret) != nil {
		return nil, errors.New("engine credential availability response is invalid")
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
			logLaunchFailure("launch_credentials", jobSubmissionTransportError(err))
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
	for _, descriptor := range engines.All() {
		if !descriptor.SupportsRuntime("cluster") {
			continue
		}
		for _, key := range descriptor.CredentialEnvKeys {
			if has(key) {
				out = append(out, descriptor.ID)
				break
			}
		}
	}
	return j.withSharedConfigEngines(out), has("BLERG_RUNNER_GIT_TOKEN")
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

// EffectiveMaxSessions is effectiveMaxSessions for callers outside the package.
func (j *JobManager) EffectiveMaxSessions() int { return j.effectiveMaxSessions() }

// statusActiveSessions is the live session Job count for Status, at most
// activeJobsCacheTTL old. Concurrent callers share one Job list. A failure is
// not cached: -1 ("couldn't tell", distinct from a real 0), asked again next
// time.
func (j *JobManager) statusActiveSessions() int {
	j.activeCacheMu.Lock()
	defer j.activeCacheMu.Unlock()
	if !j.activeCacheAt.IsZero() && time.Since(j.activeCacheAt) < activeJobsCacheTTL {
		return j.activeCacheVal
	}
	n, err := j.ActiveSessionJobs()
	if err != nil {
		logLaunchFailure("launch_admission", err)
		return -1
	}
	j.activeCacheAt, j.activeCacheVal = time.Now(), n
	return n
}

func (j *JobManager) Status() protocol.ClusterStatus {
	active := j.statusActiveSessions()
	ttl, idle := j.limits()
	return protocol.ClusterStatus{
		Configured:              true,
		WorkspaceCapabilities:   j.workspaceCapabilities(),
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
	SkillPolicy           *skills.Policy
	CommandSteps          *commandsteps.Definition
	WorkBranch            string
	WorkBranchCustom      bool
	Workspace             *protocol.WorkspaceManifest
	WorkspaceMembers      []clusterworkspace.Member `json:"-"` // authorized private contexts from selection service
	workspacePlan         *clusterworkspace.Plan
	workspaceClaim        string
	nativeHistory         *db.SessionNativeHistory
	SessionID             string
	Repo                  string
	Title                 string
	Model                 string
	Effort                string // "" | one of models.Efforts — see BLERG_RUNNER_EFFORT
	Engine                string // "" (claude) | "codex" — see BLERG_RUNNER_ENGINE
	InitialPrompt         string
	InitialPromptSource   string
	InitialPromptClientID string
	Resume                bool
	// HostGenerationFloor is the session's highest hosting so far, for a resumed pod's fresh home.
	HostGenerationFloor  int64
	RequireRecoveryReady bool // internal child lineage, never caller env
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
	// ReadOnlyGitWorker marks a cluster worker under BLG-460's option B: the pod must never
	// receive a write-capable git credential, from the launcher's own personal token (the
	// fetchPersonalCredential path below) or the operator's shared Secret (gitOperatorOK's
	// fallback). Deliberately independent of NoOperatorFallback, which also forces a personal
	// engine credential — a read-only worker still uses the owner's/operator's normal engine
	// credentials, only its git credential is restricted. Set only by a cluster-child launch
	// path (startClusterChild, and selectHostClusterChild once it exists); never decoded from a
	// request body.
	ReadOnlyGitWorker bool
	// ReadOnlyGitToken is the read-only, repo-scoped token already resolved for this worker's
	// single-repo clone (empty for a public repository, which needs none). Ignored unless
	// ReadOnlyGitWorker is set. Never a write-capable credential.
	ReadOnlyGitToken string
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
	Verbosity   string
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

// errWorkerNoHermesCredential is BLG-460 security review finding 2a: a cluster worker has no
// personal Hermes credential to filter, and must never silently fall back to the operator's
// shared one unfiltered (core cannot decrypt that Secret to check it) — the start is refused
// with a reason the person can act on, instead of a pod that mysteriously has no Hermes
// provider credentials at all.
var errWorkerNoHermesCredential = errors.New("this cluster worker has no personal Hermes credential to use read-only; add one in Settings before starting Hermes cluster workers")

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
func singleEngineCredentialKey(engine string) (string, bool) {
	descriptor, ok := engines.Lookup(engine)
	if !ok || len(descriptor.CredentialEnvKeys) != 1 {
		return "", false
	}
	return descriptor.CredentialEnvKeys[0], true
}

// filterGitTokensFromEnvBlob drops every "KEY=VALUE" line from a multi-line .env-style
// credential blob (HERMES_ENV_CONTENTS) whose key names a git or GitHub/GitLab token, for a
// BLG-460 read-only cluster worker (L6). A line that doesn't parse as KEY=VALUE is dropped
// too — if it can't be identified as safe, it is not kept. Comments and blank lines pass
// through unchanged. workspaceHosts (this launch's own resolved repository hosts) scopes the
// embedded-URL-credential check (security review finding 2b): a DATABASE_URL or HTTPS_PROXY
// value can carry basic-auth userinfo too, and must survive unless its host is actually a git
// provider or this launch's own repository host. The second return value names every dropped
// key, for the caller to log (never the values).
func filterGitTokensFromEnvBlob(blob []byte, workspaceHosts ...string) ([]byte, []string) {
	lines := strings.Split(string(blob), "\n")
	kept := make([]string, 0, len(lines))
	var dropped []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			kept = append(kept, line)
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		keyTrimmed := strings.TrimSpace(key)
		if isGitTokenEnvKey(keyTrimmed) || looksLikeGitTokenValue(value, workspaceHosts...) {
			dropped = append(dropped, keyTrimmed)
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), dropped
}

// isGitTokenEnvKey reports whether an env var name, as a whole, is shaped like a git provider
// credential. Security review finding 4: the previous "contains GIT and contains TOKEN"
// substring test matched DIGITALOCEAN_TOKEN (DIGITAL contains "git" as a substring) — a false
// positive with nothing to do with git. Matching whole name segments instead (GIT_TOKEN,
// *_GIT_TOKEN, GITHUB_*, GITLAB_*, GH_*, GL_TOKEN, BITBUCKET_*) both fixes that and still
// covers every name this filter is meant to catch: GH_PAT/GH_ENTERPRISE_TOKEN match GH_*,
// GITHUB_PAT matches GITHUB_*, GITLAB_PAT/GITLAB_ACCESS_TOKEN match GITLAB_*. GL_TOKEN is
// matched exactly, not as a GL_* prefix — "GL" alone is too ambiguous to treat as git-related
// on its own (unlike GH, which has no other common meaning in this context).
func isGitTokenEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	switch {
	case upper == "GIT_TOKEN", upper == "GL_TOKEN":
		return true
	case strings.HasSuffix(upper, "_GIT_TOKEN"):
		return true
	case strings.HasPrefix(upper, "GITHUB_"), strings.HasPrefix(upper, "GITLAB_"),
		strings.HasPrefix(upper, "GH_"), strings.HasPrefix(upper, "BITBUCKET_"):
		return true
	}
	return false
}

// gitTokenValuePrefixes are the provider-stamped prefixes of a bare git token value — present
// regardless of what env var name carries it, which is exactly the case isGitTokenEnvKey
// cannot cover: an unrelated-looking key whose value is still a live credential.
var gitTokenValuePrefixes = []string{"ghp_", "github_pat_", "gho_", "glpat-"}

// embeddedURLCredential matches a credential embedded in a URL's userinfo
// (scheme://user:token@host/...) — a second way a git token travels under a key name that
// gives no hint at all, e.g. a REMOTE_URL or clone-URL style variable. Matching this alone is
// not sufficient to drop a line (see looksLikeGitTokenValue): plenty of harmless values share
// this exact shape.
var embeddedURLCredential = regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)

// looksLikeGitTokenValue reports whether an env var's VALUE, regardless of its key name, looks
// like it holds a git credential: a known provider token prefix, or a URL with credentials
// embedded in its userinfo AND aimed at a git provider or one of workspaceHosts. Security
// review finding 2b: the second check used to fire on ANY userinfo URL, which drops a
// perfectly ordinary DATABASE_URL=postgres://user:pass@db or
// HTTPS_PROXY=http://user:pass@proxy line too — basic auth in a URL is common and not itself a
// git credential. Scoping it to an actual git host (fixed list or this launch's own resolved
// repository hosts) keeps the check aimed at what it's for.
func looksLikeGitTokenValue(value string, workspaceHosts ...string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range gitTokenValuePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return embeddedURLCredential.MatchString(trimmed) && isGitProviderURL(trimmed, workspaceHosts...)
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
// the shared operator Secret.
func sessionCredentialEnvKeys() []string {
	keys := map[string]bool{gitTokenKey: true}
	for _, descriptor := range engines.All() {
		if !descriptor.SupportsRuntime("cluster") {
			continue
		}
		for _, key := range descriptor.CredentialEnvKeys {
			keys[key] = true
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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
	resp, err := j.createLaunchObject(ctx, "/api/v1/namespaces/"+j.Namespace+"/secrets", secret)
	if err != nil {
		return false, launchCreateFailure(err, errSessionSecretCreate)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		failure := jobSubmissionRejection(resp)
		var status *jobSubmissionError
		if errors.As(failure, &status) && status.reason == "AlreadyExists" {
			return false, errSessionSecretExists
		}
		return false, launchCreateFailure(failure, errSessionSecretCreate)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// The k8s API's rejection body can echo back parts of the request —
		// including the names (and, on some validation errors, the shape) of
		// the very credential material this Secret carries. It reaches a
		// browser through the spawn endpoint's error, so it never leaves the
		// server: both caller errors and logs exclude the response payload.
		failure := jobSubmissionRejection(resp)
		logLaunchFailure("secrets", failure)
		return false, launchCreateFailure(failure, errSessionSecretCreate)
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
func (j *JobManager) waitSessionGone(parent context.Context, sessionID string, withJob bool) bool {
	ctx, cancel := context.WithTimeout(parent, sessionCleanupWait)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return false
		}
		jobThere := false
		if withJob {
			jobThere, _ = j.jobPresenceContext(ctx, sessionID)
		}
		if !jobThere && !j.secretExists(ctx, sessionSecretName(sessionID)) && ctx.Err() == nil {
			return true
		}
		timer := time.NewTimer(sessionCleanupPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// clearFinishedSession removes a FINISHED Job and the per-session Secret it
// left behind (both live on for ttlSecondsAfterFinished after the pod ends),
// then waits for them to be gone. A live Job is never touched. Errors are
// projected to fixed reason codes and returned as errSessionSecretCreate.
func (j *JobManager) clearFinishedSession(ctx context.Context, sessionID string) error {
	exists, finished := j.jobPresenceContext(ctx, sessionID)
	if !exists || !finished {
		return nil
	}
	if err := j.deleteSessionJobContext(ctx, sessionID); err != nil {
		logLaunchFailure("writer_cleanup", err)
		return errSessionSecretCreate
	}
	if !j.waitSessionGone(ctx, sessionID, true) {
		logLaunchFailure("writer_cleanup", &jobSubmissionError{reason: "Timeout"})
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
		return jobSubmissionTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusAccepted {
		return jobSubmissionRejection(resp)
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

// fetchReadOnlyGitToken reads the account's read-only, per-provider-owner token for a
// BLG-460 cluster worker (option B) — never the account's write-capable personal git
// credential (fetchPersonalCredential) and never the shared operator Secret. It reuses
// fetchCoreCredential's existing accountID+key boundary with a key scheme
// ("git-readonly:<provider>:<owner>") distinct from an engine name, so storing one
// requires no change to that boundary. core does not yet have a "Read-only tokens for
// workers" Settings section to populate this kind (BLG-460 open question/L6): until it
// does, every call here fails closed with found=false, which is the correct behavior
// for a private repository (refused downstream) and a no-op for a public one (needs
// none).
func (j *JobManager) fetchReadOnlyGitToken(ctx context.Context, accountID, provider, owner string, proof coreProof) (string, bool, error) {
	if owner == "" {
		return "", false, nil
	}
	plaintext, found, err := fetchCoreCredential(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, accountID, readOnlyGitCredentialKind(provider, owner), proof)
	if !found {
		return "", false, err
	}
	return string(plaintext), true, err
}

// readOnlyGitCredentialKind is the credential-kind key a read-only per-owner token is
// stored and fetched under — distinct from any engine name or the write-capable git
// kind (gitCredentialFor), so the two can never collide in the same account's vault.
// Lowercases owner: core always stores these lowercased (BLG-460 security re-review
// finding 3), but a clone URL's owner segment keeps whatever case the repository host used.
func readOnlyGitCredentialKind(provider, owner string) string {
	return "git-readonly:" + provider + ":" + strings.ToLower(owner)
}

// gitOwnerFromCloneURL is the first path segment of a clone URL (its GitHub/GitLab
// owner — a user or an organization): https://github.com/acme/widget.git -> "acme".
// Empty for a clone URL with no owner segment.
func gitOwnerFromCloneURL(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimPrefix(u.Path, "/")
	owner, _, _ := strings.Cut(trimmed, "/")
	return owner
}

// fixedGitProviderHosts are the git-hosting providers refused regardless of what any
// particular workspace points at: the two public SaaS hosts, and the official GitHub MCP
// server (api.githubcopilot.com), which is not a repository host at all but still reaches
// GitHub with whatever scope its own connection was granted.
var fixedGitProviderHosts = []string{"github.com", "gitlab.com", "api.githubcopilot.com"}

// isGitProviderURL reports whether raw's host is a known git-hosting provider, or one of
// workspaceHosts (L5): a write-capable MCP connection to one of these must never attach to a
// cluster worker under option B. workspaceHosts covers what the fixed list cannot — GitHub
// Enterprise and self-hosted GitLab, whose hostnames are specific to the install and only
// knowable from the session's own resolved repository hosts, never from a static list.
func isGitProviderURL(raw string, workspaceHosts ...string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	for _, provider := range fixedGitProviderHosts {
		if host == provider || strings.HasSuffix(host, "."+provider) {
			return true
		}
	}
	for _, wh := range workspaceHosts {
		if wh != "" && host == strings.ToLower(hostnameOnly(wh)) {
			return true
		}
	}
	return false
}

// hostnameOnly strips a ":port" suffix from a host[:port] string (clusterworkspace.Member's
// CredentialHost is u.Host, which keeps the port when the clone URL has one, e.g.
// "git.corp:8443") — security review finding 1: without this, a workspace host compared with
// a connection URL's port-free Hostname() would never match, so a GitHub Enterprise or
// self-hosted GitLab instance on a non-default port would never be recognized as a git
// provider host at all. Returns the input unchanged if it has no port (or isn't a valid
// host:port, e.g. already bare).
func hostnameOnly(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return h
}

// workspaceMemberHosts is the lower-cased, deduplicated set of hosts a cluster workspace's own
// members actually clone from — resolved per member at launch (CredentialHost), so it covers a
// GitHub Enterprise or self-hosted GitLab host without needing to know about either kind in
// advance.
func workspaceMemberHosts(members []clusterworkspace.Member) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, m := range members {
		host := strings.ToLower(strings.TrimSpace(m.CredentialHost))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

// hostOfGitURL is the lower-cased hostname of a clone URL, or "" if it doesn't parse — for a
// single-repo (non-workspace) launch's one repository host.
func hostOfGitURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// resumeWorkspaceGitHosts is workspaceMemberHosts/hostOfGitURL for a resuming session: a
// workspace resume re-reads its own persisted per-member contexts (the same durable record
// clusterChildWorkspace already reads for a fresh child); a single-repo resume uses its own
// recorded clone URL. ok is false only when a workspace resume's host lookup itself failed or
// came back malformed (a query error, or contexts that don't unmarshal) — security review
// finding 3: silently treating that as "no extra hosts" would have narrowed what L5 refuses
// without anyone knowing why. The caller must refuse the resume's MCP grants outright on
// ok==false, not quietly fall back to the fixed provider list. A single-repo resume with no
// GitURL (a NoRepo session) is a confident, ordinary "no hosts", not a failure: ok is true.
func resumeWorkspaceGitHosts(ctx context.Context, pool *pgxpool.Pool, sessionID string, workspace *protocol.WorkspaceManifest, gitURL string) (hosts []string, ok bool) {
	if workspace == nil {
		if h := hostOfGitURL(gitURL); h != "" {
			return []string{h}, true
		}
		return nil, true
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT contexts FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2 AND owner_id=$3`,
		sessionID, workspace.LaunchGeneration, workspace.OwnerID).Scan(&raw); err != nil {
		log.Printf("resume %s: could not read workspace repository hosts for the git-provider MCP check: %v", sessionID, err)
		return nil, false
	}
	var contexts []clusterworkspace.Member
	if json.Unmarshal(raw, &contexts) != nil {
		log.Printf("resume %s: workspace repository host record did not parse for the git-provider MCP check", sessionID)
		return nil, false
	}
	return workspaceMemberHosts(contexts), true
}

// fetchPersonalCredential reads the owner's credential. Only an absent credential
// with a currently verified principal permits an operator fallback; failures do
// not change whose account will run the session.
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

// fetchCoreCredential is the shared bounded, fail-closed Core fetch boundary.
func fetchCoreCredential(parent context.Context, client *http.Client, coreURL, internalKey, accountID, engine string, proof coreProof) ([]byte, bool, error) {
	if coreURL == "" || internalKey == "" || accountID == "" || engine == "" {
		return nil, false, nil
	}
	if !proof.valid() {
		return nil, false, errNoLivenessProof
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	raw, err := json.Marshal(internalCredentialFetchRequest{AccountID: accountID, Engine: engine, TokenID: proof.TokenID, SessionID: proof.SessionID})
	if err != nil {
		return nil, false, errPersonalCredentialUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/credentials/fetch", bytes.NewReader(raw)) // #nosec G704 -- Target is operator-configured Core URL; requester fields are JSON data and redirects are disabled.
	if err != nil {
		return nil, false, errPersonalCredentialUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", internalKey)
	resp, err := coreHTTPClient(client).Do(req) // #nosec G704 -- Target is operator-configured Core URL; requester fields are JSON data and redirects are disabled.
	if err != nil {
		return nil, false, errPersonalCredentialUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
		if err != nil || len(raw) > 1<<20 {
			return nil, false, errPersonalCredentialUnavailable
		}
		var out internalCredentialFetchResponse
		if json.Unmarshal(raw, &out) != nil {
			return nil, false, errPersonalCredentialUnavailable
		}
		plaintext, err := base64.StdEncoding.DecodeString(out.PlaintextBase64)
		if err != nil || len(bytes.TrimSpace(plaintext)) == 0 {
			return nil, false, errPersonalCredentialUnavailable
		}
		return plaintext, true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, false, errors.Join(errPersonalCredentialUnavailable, errCorePrincipalRefused)
	case http.StatusConflict:
		return nil, false, errCoreManagedCredential
	case http.StatusNotFound:
		// Core intentionally uses the same 404 for an absent credential and a
		// revoked principal. Resolve that ambiguity before allowing fallback.
		if err := verifyCorePrincipal(ctx, client, coreURL, internalKey, accountID, proof); err != nil {
			return nil, false, errors.Join(errPersonalCredentialUnavailable, err)
		}
		return nil, false, nil
	default:
		return nil, false, errPersonalCredentialUnavailable
	}
}

func (j *JobManager) assignWorkBranch(ctx context.Context, spec *SessionJobSpec) error {
	branchAPI := &API{coreURL: j.CoreURL, coreInternalKey: j.CoreInternalKey}
	resolve := j.resolveWorkBranch
	if resolve == nil {
		resolve = func(ctx context.Context, id string) (string, error) {
			return ensureWorkBranch(ctx, j.historyPool, id, branchAPI.branchUsername)
		}
	}
	branchContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	branch, err := resolve(branchContext, spec.SessionID)
	if err != nil {
		return err
	}
	spec.WorkBranch = branch
	if j.historyPool != nil {
		row, err := db.GetSession(branchContext, j.historyPool, spec.SessionID)
		if err != nil {
			return err
		}
		if row == nil {
			return errors.New("session branch intent could not be read")
		}
		spec.WorkBranchCustom = row.WorkBranchCustom
	}
	return nil
}

// CreateSessionJob creates the runner Job. Enforces the concurrency cap.
//
// BLG-460: "cluster worker" (read-only git, option B) is decided exactly once, here, from
// the session's own persisted record — never from a caller-supplied flag. Every path that
// creates or resumes a pod funnels through this one function, so there is nowhere left for
// a call site to forget it: a fresh worker start and a resumed one both re-derive the same
// answer from the same row, every time. A caller-set spec.ReadOnlyGitWorker is ignored; only
// the row decides.
func (j *JobManager) CreateSessionJob(spec SessionJobSpec) (launchErr error) {
	defer func() {
		if refusal := clusterRefusal(launchErr); refusal != nil {
			logClusterRefusal(refusal)
			launchErr = refusal
		}
	}()
	worker, err := j.isClusterWorkerSession(context.Background(), spec.SessionID)
	if err != nil {
		return err
	}
	spec.ReadOnlyGitWorker = worker
	if spec.Workspace != nil {
		if err := j.assignWorkBranch(context.Background(), &spec); err != nil {
			return err
		}
		return j.withClusterWorkspace(spec)
	}
	return j.withNativeHistory(spec)
}

// isClusterWorkerSession reports whether sessionID's own row already marks it as a child of
// another session (isChildSession) running on the cluster runtime (onClusterParent) — a
// BLG-460 cluster worker under option B. The row is the single source of truth: a fresh
// worker's row is inserted (with its parent/delegation markers already set) before
// CreateSessionJob is ever called for it, and a resumed worker's row still carries the same
// markers it was created with. Without a wired history pool (standalone/test fixtures that
// never persist a session at all) there is nothing to prove worker-ness from, so this reports
// false rather than failing every ordinary launch closed.
func (j *JobManager) isClusterWorkerSession(ctx context.Context, sessionID string) (bool, error) {
	if j.historyPool == nil || sessionID == "" {
		return false, nil
	}
	row, err := db.GetSession(ctx, j.historyPool, sessionID)
	if err != nil {
		return false, err
	}
	return isClusterPodWorker(row) && onClusterParent(row), nil
}

func (j *JobManager) createSessionJob(spec SessionJobSpec) error {
	return j.createSessionJobContext(context.Background(), spec)
}

func (j *JobManager) createSessionJobContext(parent context.Context, spec SessionJobSpec) (launchErr error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	if spec.SkillPolicy == nil {
		var skillErr error
		spec.SkillPolicy, skillErr = sessionSkills(ctx, j.historyPool, spec.SessionID)
		if skillErr != nil {
			return fmt.Errorf("resolve session skills: %w", skillErr)
		}
	}
	progress := &clusterLaunchProgress{j: j, session: spec.SessionID}
	if spec.workspacePlan != nil {
		progress.next(ctx, "launch_admission", "Checking cluster capacity and launch configuration")
		defer progress.finish(ctx, &launchErr)
	}
	limit := j.effectiveMaxSessions()
	if n, err := j.activeSessionJobsContext(ctx); err != nil {
		if spec.workspacePlan != nil {
			return errors.New("cluster launch capacity could not be verified")
		}
	} else if n >= limit {
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
	if !spec.NoRepo && spec.workspacePlan == nil {
		cloneURL = j.CloneURL(spec.Repo, spec.GitURL)
	}
	// Preserve capacity refusal before reading or reserving a branch. Workspace
	// plans already resolved theirs before their private launch record was made.
	if spec.WorkBranch == "" {
		if err := j.assignWorkBranch(ctx, &spec); err != nil {
			return err
		}
	}

	env := []map[string]any{
		{"name": "GIT_AUTHOR_NAME", "value": "Blerg Agent"},
		{"name": "GIT_AUTHOR_EMAIL", "value": "agent@blerg.invalid"},
		{"name": "GIT_COMMITTER_NAME", "value": "Blerg Agent"},
		{"name": "GIT_COMMITTER_EMAIL", "value": "agent@blerg.invalid"},
		{"name": "BLERG_RUNNER_SERVER_WS", "value": j.ServerWSURL},
		{"name": "BLERG_RUNNER_SERVER_HTTP", "value": j.ServerHTTPURL},
		{"name": "BLERG_RUNNER_SESSION_ID", "value": spec.SessionID},
		{"name": "BLERG_RUNNER_WORK_BRANCH", "value": spec.WorkBranch},
		{"name": "BLERG_RUNNER_WORK_BRANCH_CUSTOM", "value": strconv.FormatBool(spec.WorkBranchCustom)},
		{"name": "BLERG_RUNNER_ATTACHMENTS_DIR", "value": "/home/agent/.blerg-runner-daemon/sessions/" + spec.SessionID + "/attachments"},
		{"name": "BLERG_RUNNER_REPO", "value": spec.Repo},
		{"name": "BLERG_RUNNER_TITLE", "value": spec.Title},
		{"name": "BLERG_RUNNER_MODEL", "value": spec.Model},
		{"name": "BLERG_RUNNER_EFFORT", "value": spec.Effort},
		{"name": "BLERG_RUNNER_ENGINE", "value": spec.Engine},
		{"name": "BLERG_RUNNER_GIT_URL", "value": cloneURL},
	}
	// BLG-479: a worker's turn_done bundles and publishes its own commits (design §4) only when
	// this is set — from the same ReadOnlyGitWorker derivation as the credential lockdown above
	// (isClusterPodWorker(row) && onClusterParent(row)), never a signal the pod could set for
	// itself. A desktop/non-worker session never gets this var at all.
	if spec.ReadOnlyGitWorker {
		env = append(env, map[string]any{"name": "BLERG_WORKER_RESULT_BUNDLE", "value": "1"})
	}
	// Read the actual runner container limits, never node capacity or a caller's
	// ExtraEnv. Divisors make the entry point independent of quantity spelling.
	for _, field := range []struct{ name, resource, divisor string }{
		{"BLERG_RUNNER_POD_CPU_MILLICORES", "limits.cpu", "1m"},
		{"BLERG_RUNNER_POD_MEMORY_BYTES", "limits.memory", "1"},
	} {
		if _, exists := spec.ExtraEnv[field.name]; exists {
			return fmt.Errorf("env key %s is reserved", field.name)
		}
		env = append(env, map[string]any{"name": field.name, "valueFrom": map[string]any{"resourceFieldRef": map[string]string{"containerName": "runner", "resource": field.resource, "divisor": field.divisor}}})
	}
	// Always-on plugins: non-secret configuration, resolved (fail soft) once per start.
	j.ResolvePlugins(ctx, &spec)
	env = append(env, j.pluginEnv(spec)...)
	podTTL, podIdle := j.limits()
	if podIdle > 0 {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_IDLE_TIMEOUT_SECONDS", "value": strconv.FormatInt(podIdle, 10)})
	}
	if spec.Resume {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_RESUME", "value": "1"})
		if spec.HostGenerationFloor > 0 {
			env = append(env, map[string]any{"name": "BLERG_RUNNER_HOST_GENERATION_FLOOR", "value": strconv.FormatInt(spec.HostGenerationFloor, 10)})
		}
	}
	if spec.nativeHistory != nil {
		env = append(env,
			map[string]any{"name": "BLERG_RUNNER_NATIVE_HISTORY", "value": "1"},
			map[string]any{"name": "BLERG_RUNNER_NATIVE_HISTORY_ACCOUNT", "value": spec.nativeHistory.AccountID},
			map[string]any{"name": "BLERG_RUNNER_NATIVE_HISTORY_UID", "value": spec.nativeHistory.ClaimUID})
	}
	if spec.NoRepo {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_NO_REPO", "value": "1"})
	}
	if spec.NewRepo {
		env = append(env, map[string]any{"name": "BLERG_RUNNER_NEW_REPO", "value": "1"})
	}
	const recoveryReadyEnv = "BLERG_RUNNER_REQUIRE_RECOVERY_READY"
	if _, dup := spec.ExtraEnv[recoveryReadyEnv]; dup {
		return fmt.Errorf("env key %s is reserved", recoveryReadyEnv)
	}
	if spec.RequireRecoveryReady {
		env = append(env, map[string]any{"name": recoveryReadyEnv, "value": "1"})
	}
	// A grant implies the restriction, so a session that reaches here with one and no flag
	// (a caller that forgot) is still restricted.
	if spec.RestrictTools {
		if _, dup := spec.ExtraEnv[restrictToolsEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", restrictToolsEnvVar)
		}
		env = append(env, map[string]any{"name": restrictToolsEnvVar, "value": "1"})
	}
	if _, dup := spec.ExtraEnv[skillPolicyEnv]; dup {
		return fmt.Errorf("env key %s is reserved", skillPolicyEnv)
	}
	skillRaw, skillErr := json.Marshal(spec.SkillPolicy)
	if skillErr != nil {
		return skillErr
	}
	env = append(env, map[string]any{"name": skillPolicyEnv, "value": string(skillRaw)})
	if spec.Verbosity != "" {
		if _, dup := spec.ExtraEnv[verbosityEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", verbosityEnvVar)
		}
		env = append(env, map[string]any{"name": verbosityEnvVar, "value": spec.Verbosity})
	}
	if spec.Interaction != "" {
		if _, dup := spec.ExtraEnv[interactionEnvVar]; dup {
			return fmt.Errorf("env key %s is reserved", interactionEnvVar)
		}
		env = append(env, map[string]any{"name": interactionEnvVar, "value": spec.Interaction})
	}

	effectiveEngine := spec.Engine
	if effectiveEngine == "" {
		effectiveEngine = "claude"
	}
	// Every cluster pod gets a distinct, durable launch capability. No caller
	// can select the daemon master or reuse a previous launch's runtime token.
	if spec.workspacePlan != nil {
		progress.next(ctx, "runtime_token", "Issuing session-scoped runtime and messaging tokens")
	}
	var runtimeRaw, messageRaw string
	var runtimeErr error
	switch {
	case j.historyPool != nil:
		// A duplicate start must not revoke a pod that still owns a live Job.
		// Unknown Kubernetes state is treated as live by jobPresence.
		var enrolled bool
		if err := j.historyPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_runtime WHERE session_id=$1)`, spec.SessionID).Scan(&enrolled); err != nil {
			return errSessionJobCreate
		}
		if enrolled {
			if exists, finished := j.jobPresenceContext(ctx, spec.SessionID); exists && !finished {
				return errSessionJobCreate
			}
		}
		runtimeRaw, messageRaw, runtimeErr = db.MintRuntimeLaunch(ctx, j.historyPool, spec.SessionID, spec.ExtraEnv[sessionTokenEnv], sessionTokenTTL)
	case j.mintRuntimeLaunch != nil:
		runtimeRaw, messageRaw, runtimeErr = j.mintRuntimeLaunch(ctx, spec.SessionID, spec.ExtraEnv[sessionTokenEnv])
	default:
		runtimeErr = db.ErrTokenInvalid
	}
	if runtimeErr != nil || runtimeRaw == "" || messageRaw == "" {
		return errSessionSecretCreate
	}
	launchEnv := make(map[string]string, len(spec.ExtraEnv)+2)
	for k, v := range spec.ExtraEnv {
		if !foreignEngineCredential(k, effectiveEngine) {
			launchEnv[k] = v
		}
	}
	launchEnv[runtimeTokenEnv] = runtimeRaw
	launchEnv[sessionTokenEnv] = messageRaw
	spec.ExtraEnv = launchEnv

	// sessionData collects everything that must never appear as a literal
	// env value in the Job spec (visible via `kubectl describe job`/`get job
	// -o yaml` to anyone who can read Jobs but not Secrets): the initial
	// prompt, every caller-supplied ExtraEnv entry (runner-brokered session
	// tokens, e.g. BLERG_BOARD_TOKEN), and — below — a personal engine
	// credential when one was found. Whatever ends up in here is written to
	// one per-session Secret and referenced from the Job via secretKeyRef.
	if spec.workspacePlan != nil {
		progress.next(ctx, "launch_credentials", "Resolving personal engine credentials")
	}
	if _, exists := spec.ExtraEnv["BLERG_RUNNER_FIXED_COMMAND_STEPS"]; exists {
		return fmt.Errorf("env key BLERG_RUNNER_FIXED_COMMAND_STEPS is reserved")
	}
	if spec.CommandSteps != nil {
		if err := spec.CommandSteps.Validate(); err != nil {
			return err
		}
		copied := make(map[string]string, len(spec.ExtraEnv)+1)
		for key, value := range spec.ExtraEnv {
			copied[key] = value
		}
		raw, _ := json.Marshal(spec.CommandSteps)
		copied["BLERG_RUNNER_FIXED_COMMAND_STEPS"] = string(raw)
		spec.ExtraEnv = copied
	}
	sessionData := make(map[string][]byte, len(spec.ExtraEnv)+2)
	if spec.InitialPromptSource != "" || spec.InitialPromptClientID != "" {
		copied := make(map[string]string, len(spec.ExtraEnv)+1)
		for k, v := range spec.ExtraEnv {
			copied[k] = v
		}
		spec.ExtraEnv = copied
		if spec.InitialPromptSource != "" {
			spec.ExtraEnv["BLERG_RUNNER_INITIAL_PROMPT_SOURCE"] = spec.InitialPromptSource
		}
		if spec.InitialPromptClientID != "" {
			spec.ExtraEnv["BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID"] = spec.InitialPromptClientID
		}
	}
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
	// The cluster's shared Hermes providers: Secret only, Hermes sessions only.
	hermesEnv, err := j.addHermesSharedSessionEnv(spec, effectiveEngine, sessionData)
	if err != nil {
		return err
	}

	// Personal-credential-first: for the engine actually being spawned, and
	// for git access, try the spawning account's own credentials (via
	// blerg-core) before falling back to the shared operator Secret.
	// personalKeys holds every env-var key (of the six below) that the
	// operator Secret must NOT supply for this session; the subset of those
	// keys present in sessionData is sourced from the per-session Secret
	// instead, and the rest are omitted from the pod entirely. Empty means no
	// personal credential was found, i.e. today's behavior for every key.
	if err := clusterWorkspaceSecret(spec, sessionData, &env); err != nil {
		return err
	}
	personalKeys := map[string]bool{}
	managedCredential := false
	gitKind, gitOperatorOK := j.gitCredentialFor(cloneURL)
	// BLG-460: a read-only cluster worker's git credential is decided once, unconditionally,
	// before anything below gets a chance to run — never the launcher's personal credential,
	// never the operator's shared Secret. ReadOnlyGitWorker itself is set only by
	// CreateSessionJob, derived from the session's own row, never by a caller. Nothing past
	// this point may override sessionData[gitTokenKey] for a worker: the fetch below is
	// explicitly gated on this same flag (not the reverse), so removing that gate is its own,
	// independently observable regression — it is not masked by a later step undoing it.
	if spec.ReadOnlyGitWorker {
		personalKeys[gitTokenKey] = true
		if spec.ReadOnlyGitToken != "" {
			sessionData[gitTokenKey] = []byte(spec.ReadOnlyGitToken)
		}
		// L6, fail-closed: a worker's Hermes env blob can itself carry a git token, and core
		// never decrypts the operator's shared Secret to filter it — there is no plaintext
		// here to check. Claim the key unconditionally so the operator-fallback loop below
		// never sources it; if no personal (filtered) credential is found later, the pod
		// simply has no HERMES_ENV_CONTENTS at all rather than an unfiltered operator blob.
		personalKeys["HERMES_ENV_CONTENTS"] = true
	}
	if spec.SpawningAccountID != "" {
		if effectiveEngine == "claude" {
			plaintext, found, err := j.fetchPersonalCredential(ctx, spec.SpawningAccountID, "claude", spec.proof())
			if spec.NoOperatorFallback && (err != nil || !found) {
				logLaunchFailure("launch_credentials", &jobSubmissionError{reason: "Unauthorized"})
				return ErrNoPersonalCredential
			}
			if err != nil {
				return err
			}
			if found {
				// Both claude keys become personal, only the chosen one is
				// emitted: a personal API key must not run alongside the
				// operator's OAuth token (or vice versa), because the pod
				// picks its billing mode from whichever var is set.
				for _, k := range claudeKeys {
					personalKeys[k] = true
				}
				sessionData[claudeCredentialKey(plaintext)] = plaintext
			}
		} else if credKey, ok := singleEngineCredentialKey(effectiveEngine); ok {
			plaintext, found, err := j.fetchPersonalCredential(ctx, spec.SpawningAccountID, effectiveEngine, spec.proof())
			switch {
			case errors.Is(err, errCoreManagedCredential):
				for _, key := range []string{"CODEX_AUTH_JSON", "OPENAI_API_KEY", "CODEX_API_KEY"} {
					if _, exists := spec.ExtraEnv[key]; exists {
						return errManagedCredential
					}
				}
				if err = j.bindManagedCredential(ctx, spec); err != nil {
					return err
				}
				managedCredential = true
				personalKeys[credKey] = true
				env = append(env, map[string]any{"name": managedCredentialEnv, "value": "1"})
				found = true
			case err == nil && found:
				// L6: a Hermes credential is a whole .env blob that may itself carry a git
				// or GitHub token (k8sjobs.go's own sessionCredentialEnvKeys comment: "the
				// git token, when present there"). A cluster worker never gets one, from
				// any source — strip any such line before it ever reaches sessionData.
				if spec.ReadOnlyGitWorker {
					hosts := workspaceMemberHosts(spec.WorkspaceMembers)
					if h := hostOfGitURL(cloneURL); h != "" {
						hosts = append(hosts, h)
					}
					var dropped []string
					plaintext, dropped = filterGitTokensFromEnvBlob(plaintext, hosts...)
					if len(dropped) > 0 {
						log.Printf("cluster worker %s: dropped %d git-token-looking key(s) from HERMES_ENV_CONTENTS: %v", spec.SessionID, len(dropped), dropped)
					}
				}
				sessionData[credKey] = plaintext
				personalKeys[credKey] = true
			//nolint:gosec // credKey is an env var NAME ("HERMES_ENV_CONTENTS"), never a credential value
			case spec.ReadOnlyGitWorker && credKey == "HERMES_ENV_CONTENTS" && err == nil && !found:
				// Security review finding 2a: silently handing the pod no HERMES_ENV_CONTENTS
				// at all is indistinguishable, from the person's side, from a broken launch.
				// Refuse with a reason they can act on, rather than leave them guessing why
				// Hermes has no provider credentials.
				logLaunchFailure("launch_credentials", &jobSubmissionError{reason: "NotFound"})
				return errWorkerNoHermesCredential
			}
			if spec.NoOperatorFallback && (err != nil || !found) {
				return ErrNoPersonalCredential
			}
			if err != nil {
				return err
			}
		}
		// The git credential is independent of the engine: whoever launched
		// the session clones as themselves when they've connected a token
		// for the clone URL's provider, whatever agent they're running.
		// A read-only cluster worker (BLG-460) never reaches fetchPersonalCredential for
		// git at all: its token, if any, was already resolved once at launch
		// (ReadOnlyGitToken), never the launcher's own write-capable credential.
		if gitKind != "" && !spec.ReadOnlyGitWorker {
			plaintext, found, err := j.fetchPersonalCredential(ctx, spec.SpawningAccountID, gitKind, spec.proof())
			if spec.NoOperatorFallback && (err != nil || !found) {
				return ErrNoPersonalCredential
			}
			if err != nil {
				return err
			}
			if found {
				sessionData[gitTokenKey] = plaintext
				personalKeys[gitTokenKey] = true
			}
		}
	}
	if effectiveEngine == "codex" && !managedCredential {
		// Resolve every snapshot at the server boundary. Neither the pod's
		// initial environment nor a mounted Secret may contain refresh authority.
		if err := j.prepareCodexSnapshot(ctx, spec, sessionData); err != nil {
			return err
		}
		personalKeys["CODEX_AUTH_JSON"] = true
	}
	if !managedCredential && j.historyPool != nil {
		// A replacement enrolled in raw/operator authentication must not leave
		// the previous pod authorized to obtain a later managed connection.
		if _, err := j.historyPool.Exec(ctx, "DELETE FROM session_managed_credential WHERE session_id=$1", spec.SessionID); err != nil {
			return errManagedCredential
		}
	}
	// The operator's shared git token belongs to its own base host only
	// (gitCredentialFor): for a clone anywhere else it is claimed as
	// "personal" so the operator Secret can never supply it — the key is
	// then either the launcher's own token for that provider or omitted.
	if !gitOperatorOK || spec.NoOperatorGitToken || spec.workspacePlan != nil {
		personalKeys[gitTokenKey] = true
	}

	if spec.workspacePlan == nil {
		if err := j.precheckRepositoryContext(ctx, spec, cloneURL, sessionData, personalKeys, gitOperatorOK); err != nil {
			return err
		}
	}

	// BLG-460 fail-closed backstop: a worker's git credential, if any, is its one read-only
	// token and nothing else — checked once, right before anything gets persisted, against
	// whatever sessionData actually ended up holding, not against the control flow that put
	// it there. This refuses a worker spec regardless of how a write-capable value got into
	// sessionData[gitTokenKey] (a bug here, a future code path, an ExtraEnv collision).
	if spec.ReadOnlyGitWorker {
		if got, ok := sessionData[gitTokenKey]; ok && string(got) != spec.ReadOnlyGitToken {
			return errWorkerWriteCredential
		}
	}

	// secretCreated tracks whether this call actually created a per-session
	// Secret — the signal every cleanup/adoption path below keys off, since a
	// Secret can now exist purely for the prompt/ExtraEnv even when no
	// personal credential was found (personalKeys empty).
	secretCreated := false
	if len(sessionData) > 0 {

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

	if hermesEnv != nil {
		env = append(env, hermesEnv)
	}

	sessionCredentialKeys := sessionCredentialEnvKeys()
	// A pod receives provider authority only for its selected engine.
	for _, key := range sessionCredentialKeys {
		foreignProvider := foreignEngineCredential(key, effectiveEngine)
		if foreignProvider {
			continue
		}
		// A caller-supplied ExtraEnv entry of the same name was already
		// emitted (from the per-session Secret) by the loop above. Emitting
		// this one too would put two env entries with the same name in one
		// container — a shape no caller should have to reason about.
		if _, dup := spec.ExtraEnv[key]; dup {
			continue
		}
		if personalKeys[key] {
			if _, ok := sessionData[key]; ok {
				env = append(env, map[string]any{
					"name": key,
					"valueFrom": map[string]any{
						"secretKeyRef": map[string]any{"name": sessionSecretName(spec.SessionID), "key": key, "optional": false},
					},
				})
			}
			// Claimed by the personal set but not the value that was chosen
			// (the other half of the claude pair): emit nothing, so the
			// operator Secret cannot supply it behind the user's back.
			continue
		}
		if spec.NoOperatorFallback {
			continue
		}
		secretName := j.SecretName
		// Operator-synced credentials (rotation flows from the secrets operator): the
		// oauth token, codex's auth.json contents, hermes's whole .env
		// contents, and, when present there, the git token.
		if (key != "ANTHROPIC_API_KEY") && j.OAuthSecretName != "" {
			secretName = j.OAuthSecretName
		}
		env = append(env, map[string]any{
			"name": key,
			"valueFrom": map[string]any{
				// ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN are alternative
				// billing modes — either may be absent. CODEX_AUTH_JSON and
				// HERMES_ENV_CONTENTS are only needed when the session's engine
				// is codex or hermes respectively.
				"secretKeyRef": map[string]any{"name": secretName, "key": key,
					"optional": true},
			},
		})
	}
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
	if spec.workspacePlan != nil {
		progress.next(ctx, "pod_manifest", "Validating protected mounts, bootstrap and resource limits")
	}
	// These shapes are constructed above; fail closed if a future manifest changes.
	jobSpec, ok := job["spec"].(map[string]any)
	if !ok {
		return errNativeHistory
	}
	template, ok := jobSpec["template"].(map[string]any)
	if !ok {
		return errNativeHistory
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return errNativeHistory
	}
	if err := clusterWorkspacePod(spec, pod); err != nil {
		return err
	}
	if err := nativeHistoryPod(spec, pod); err != nil {
		return err
	}
	if spec.workspacePlan != nil {
		progress.next(ctx, "secrets", "Creating credential references after pod validation")
	}
	// Allocate credentials only after the complete pod has passed validation.
	if len(sessionData) > 0 {
		// A FINISHED Job (graceful stop, idle exit, deadline) keeps its
		// per-session Secret for ttlSecondsAfterFinished, and that Secret would
		// 409 the create below — clear both first. A live Job is left alone.
		if err := j.clearFinishedSession(ctx, spec.SessionID); err != nil {
			return err
		}
		created, err := j.createSessionSecret(ctx, spec.SessionID, sessionData)
		if errors.Is(err, errSessionSecretExists) {
			if spec.workspacePlan != nil {
				return &jobSubmissionError{reason: "AlreadyExists", cause: errSessionSecretCreate}
			}
			// Still 409: either a live session's Secret (refuse, untouched) or
			// an orphan of a crashed start (no live Job owns it) — replace the
			// orphan and retry exactly once.
			if exists, finished := j.jobPresenceContext(ctx, spec.SessionID); exists && !finished {
				return errSessionSecretCreate
			}
			if derr := j.deleteSessionSecret(ctx, spec.SessionID); derr != nil {
				logLaunchFailure("secret_cleanup", derr)
				return errSessionSecretCreate
			}
			if !j.waitSessionGone(ctx, spec.SessionID, false) {
				logLaunchFailure("secret_cleanup", &jobSubmissionError{reason: "Timeout"})
				return errSessionSecretCreate
			}
			created, err = j.createSessionSecret(ctx, spec.SessionID, sessionData)
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
			logLaunchFailure("secrets", err)
			return launchCreateFailure(err, errSessionSecretCreate)
		}
		secretCreated = created
	}
	if spec.workspacePlan != nil {
		progress.next(ctx, protocol.StageQueued, "Submitting the consumer Job; PVC binding may follow scheduling")
	}
	resp, err := j.createLaunchObject(ctx, "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs", job)
	if err != nil {
		// The Secret was created moments ago for a Job that will never exist.
		// Leaving it behind would strand a plaintext credential/prompt/env in
		// the cluster forever — nothing else ever looks at it again.
		j.deleteOrphanSessionSecret(ctx, secretCreated, spec.SessionID)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		if spec.workspacePlan != nil {
			// A workspace generation was fenced for this dispatch. A competing Job
			// is not proof that this generation was submitted; never report success
			// or replace a writer whose authority this call cannot establish.
			j.deleteOrphanSessionSecret(ctx, secretCreated, spec.SessionID)
			return jobSubmissionRejection(resp)
		}
		// A live Job means a double spawn (fine). A FINISHED Job (pod died,
		// deadline hit) blocks resume forever if left in place — delete it
		// and retry once.
		if spec.retried {
			// The Secret this call created will never be adopted by any Job
			// (the retry's own Secret already carries the same data) — same
			// leak as the network-error path above.
			j.deleteOrphanSessionSecret(ctx, secretCreated, spec.SessionID)
			return &jobSubmissionError{reason: "Conflict"}
		}
		if _, finished := j.jobPresenceContext(ctx, spec.SessionID); finished {
			if err := j.deleteSessionJobContext(ctx, spec.SessionID); err != nil {
				return err
			}
			spec.retried = true
			return j.createSessionJobContext(ctx, spec)
		}
		// The Job that already exists (the one holding the live Job's
		// conflicting name) is not this call's Job — it will never adopt the
		// Secret just created here via ownerReferences, so it would strand a
		// plaintext credential/prompt/env forever if left in place.
		j.deleteOrphanSessionSecret(ctx, secretCreated, spec.SessionID)
		return nil
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// The k8s API's rejection body echoes back parts of the manifest it
		// refused — which here carries the session's env, image and namespace.
		// This error reaches the caller as the start's 503 AND is persisted as
		// the session's error_reason, so it is a fixed string. Logs/status keep
		// only the HTTP status and an allowlisted Kubernetes reason class.
		failure := jobSubmissionRejection(resp)
		logLaunchFailure("queued", failure)
		j.deleteOrphanSessionSecret(ctx, secretCreated, spec.SessionID)
		return failure
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
			logLaunchFailure("secret_adoption", &jobSubmissionError{reason: "UnknownAPIError"})
		} else if err := j.adoptSessionSecret(ctx, spec.SessionID, created.Metadata.UID); err != nil {
			logLaunchFailure("secret_adoption", err)
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
func (j *JobManager) deleteOrphanSessionSecret(parent context.Context, created bool, sessionID string) {
	if !created {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	if err := j.deleteSessionSecret(cleanup, sessionID); err != nil {
		logLaunchFailure("secret_cleanup", err)
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
func (j *JobManager) adoptSessionSecret(parent context.Context, sessionID, jobUID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
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
	resp, err := j.doWithContentType(ctx, http.MethodPatch,
		"/api/v1/namespaces/"+j.Namespace+"/secrets/"+sessionSecretName(sessionID),
		patch, "application/merge-patch+json")
	if err != nil {
		return jobSubmissionTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return jobSubmissionRejection(resp)
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
	return j.jobPresenceContext(context.Background(), sessionID)
}
func (j *JobManager) jobPresenceContext(ctx context.Context, sessionID string) (exists, finished bool) {
	resp, err := j.doWithContentType(ctx, http.MethodGet,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+j.jobName(sessionID), nil, "application/json")
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
	return j.deleteSessionJobContext(context.Background(), sessionID)
}
func (j *JobManager) deleteSessionJobContext(ctx context.Context, sessionID string) error {
	resp, err := j.doWithContentType(ctx, http.MethodDelete,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+j.jobName(sessionID)+"?propagationPolicy=Background", nil, "application/json")
	if err != nil {
		return jobSubmissionTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusAccepted {
		return jobSubmissionRejection(resp)
	}
	// Best-effort: also remove any per-session credential Secret. Most
	// sessions never had one (no personal credential was found at spawn
	// time), in which case this is a harmless 404. Logged rather than
	// returned as an error so a Secret-delete hiccup never blocks the more
	// important Job teardown above.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := j.deleteSessionSecret(cleanup, sessionID); err != nil {
		logLaunchFailure("secret_cleanup", err)
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
	_ = resumeClusterSessionMode(ctx, h, pool, sessionID, text, requesterAccountID, requesterSessionID, false)
}

func resumeClusterSessionStrict(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, accountID, authSessionID string) error {
	return resumeClusterSessionMode(ctx, h, pool, sessionID, text, accountID, authSessionID, true)
}

// resumeInput identifies the exact durable input carried by a replacement pod.
// Its source must match reconnect replay because native receipts hash the full input.
type resumeInput struct {
	ClientID, Source string
	Proof            coreProof
}

func resumeClusterSessionMode(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, requesterAccountID, requesterSessionID string, automatic bool, inputs ...resumeInput) error {
	return h.guardDelegationInput(ctx, sessionID, func() error {
		return resumeClusterSessionModeGuarded(ctx, h, pool, sessionID, text, requesterAccountID, requesterSessionID, automatic, inputs...)
	})
}

func resumeClusterSessionModeGuarded(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, requesterAccountID, requesterSessionID string, automatic bool, inputs ...resumeInput) error {
	if problem := parentResumeProblem(ctx, pool, sessionID); problem != nil {
		return problem
	}
	jm := h.JobManager()
	if jm == nil || pool == nil {
		logLaunchFailure("queued", &jobSubmissionError{reason: "ServiceUnavailable"})
		return errors.New("cluster resume was not accepted")
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil {
		logLaunchFailure("selection", &jobSubmissionError{reason: "InternalError"})
		return errors.New("cluster resume was not accepted")
	}
	if row.Status != "disconnected" {
		logLaunchFailure("queued", &jobSubmissionError{reason: "Invalid"})
		return errors.New("cluster resume was not accepted")
	}
	if len(inputs) > 0 {
		eligible, _, reason := sessionResumeEligibility(ctx, pool, jm, row, requesterAccountID, inputs[0].Proof)
		if !eligible {
			return errors.New(reason)
		}
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
		logLaunchFailure("queued", &jobSubmissionError{reason: "Invalid"})
		model = ""
	}
	if !models.ValidEffortFor(engine, effort) {
		logLaunchFailure("queued", &jobSubmissionError{reason: "Invalid"})
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
		logLaunchFailure("launch_credentials", &jobSubmissionError{reason: "Unauthorized"})
	}
	// The token the session was started with travels with the resume too:
	// without it core has nothing to check for an agent-owned session, and
	// the resumed pod would silently fall back to the operator Secret.
	// Otherwise the resume is authorised by the requester's own browser session, which core
	// checks belongs to the launcher's account (they are the same person: equality above).
	tokenID, authSessionID := "", ""
	if spawningAccountID != "" {
		// An owner-authorized new request uses its exact live proof. Revoking
		// the launch token must not strand a session after the owner remints it.
		if len(inputs) > 0 {
			if !inputs[0].Proof.valid() && jm.personalCredentialsWired() {
				return errNoLivenessProof
			}
			tokenID, authSessionID = inputs[0].Proof.TokenID, inputs[0].Proof.SessionID
		} else if tokenID = derefOrEmpty(row.TokenID); tokenID == "" {
			// Compatibility for trusted internal callers predating resumeInput.
			// Public REST/MCP/browser callers always provide their own proof.
			authSessionID = requesterSessionID
		}
	}
	if automatic {
		if launcher == "" || requesterAccountID != launcher || requesterSessionID == "" {
			return errors.New("automatic resume needs retained owner authority")
		}
		tokenID, authSessionID = "", requesterSessionID
	}
	// A session that holds MCP connections resumes only for the account that started it, from
	// a signed-in browser session; otherwise the resume is refused and nothing is started.
	grants, problem := grantResumeProblem(ctx, pool, sessionID, launcher, requesterAccountID, requesterSessionID)
	if problem != nil {
		logLaunchFailure("authorization", &jobSubmissionError{reason: "Forbidden"})
		return errors.New("cluster resume was not accepted")
	}
	if clusterPodInterrupted(row) {
		text = "Your last turn was interrupted by pod termination. " + derefOrEmpty(row.ErrorReason) +
			"\nInspect restored files and the recorded session branch before continuing; the interrupted turn may contain uncommitted work.\n\n" + text
	}
	// The session stays "disconnected" until the new pod connects, so restart
	// its status clock now: the reconciler measures both the 24 h resume
	// expiry and the missing-Job grace from that stamp, and neither must fire
	// on a session whose replacement pod is still booting.
	if err := db.TouchSessionStatusChanged(ctx, pool, sessionID); err != nil {
		logLaunchFailure("queued", &jobSubmissionError{reason: "InternalError"})
	} else {
		// The status stays disconnected while booting. Send the cleared reason
		// now so connected browsers stop displaying the previous pod's death.
		broadcastSessionCreated(ctx, h, pool, sessionID)
	}
	// The owner's newest proof is the one a child this session starts is authorized with. (A
	// child's is recorded once its new pod exists, below.)
	if spawningAccountID != "" && !isChildSession(row) {
		recordLaunchAuthSession(ctx, pool, sessionID, authSessionID)
	}
	newRepo := db.GetSessionNewRepo(ctx, pool, sessionID)
	floor, _ := db.HighestHostGeneration(ctx, pool, sessionID) // without it the pod starts from the clock
	// A resume is a start too: the browser shows its progress the same way.
	spec := SessionJobSpec{
		SessionID: sessionID, Repo: row.Repo, Title: title, Model: model, Effort: effort, Engine: engine,
		RequireRecoveryReady: isChildSession(row) || row.DelegationVia == "person",
		InitialPrompt:        text, Resume: true, HostGenerationFloor: floor,
		InitialPromptClientID: func() string {
			if len(inputs) > 0 {
				return inputs[0].ClientID
			}
			return ""
		}(),
		InitialPromptSource: func() string {
			if automatic {
				return limitResumeSource
			}
			if len(inputs) > 0 {
				return inputs[0].Source
			}
			return ""
		}(),
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
		// So is a cron's cluster child (migration 068).
		NoOperatorFallback: automatic || row.CronID != nil || db.GetSessionNoOperatorFallback(ctx, pool, sessionID),
		// A resumed session is restricted exactly when it was started so (migration 036): a
		// cron's, or a board-started grant session. A launch-sheet session with connections
		// comes back with its tools.
		RestrictTools: row.CronID != nil || db.GetSessionRestrictTools(ctx, pool, sessionID),
		// The mode the session was started in (migration 038); a row from before it is read
		// by who started it (effectiveInteraction).
		Interaction: effectiveInteraction(row),
		Verbosity:   effectiveVerbosity(row),
	}
	w, workspaceErr := db.GetSessionWorkspace(ctx, pool, sessionID)
	if w != nil && w.Manifest != nil && !protocol.WorkspaceRuntimeEnabled("cluster", "linux") {
		return errors.New("cluster workspace runtime is disabled")
	}
	if workspaceErr != nil {
		return workspaceErr
	}
	if w != nil && w.Manifest != nil {
		generation, _, err := db.GetWorkspaceRuntime(ctx, pool, sessionID)
		if err != nil {
			return err
		}
		if generation > 0 || row.WorkspaceKind == "folder" {
			spec.Workspace = w.Manifest
			spec.NoRepo = false
			spec.Repo = ""
			spec.GitURL = ""
		}
	} else if row.WorkspaceKind == "folder" {
		return errClusterWorkspace
	}
	if len(grants) > 0 {
		// L5: a resumed cluster worker is refused a git-provider MCP connection by default,
		// exactly as a fresh one is (startClusterChild) — never attached, never recorded as
		// an exception. isClusterPodWorker(row) && onClusterParent(row) is the same worker
		// definition CreateSessionJob derives credentials from (a person channel included).
		if isClusterPodWorker(row) && onClusterParent(row) {
			resumeGitHosts, hostsOK := resumeWorkspaceGitHosts(ctx, pool, sessionID, spec.Workspace, derefOrEmpty(row.GitURL))
			// Security review finding 3: a host lookup that itself failed must fail the
			// check closed — refuse every grant — rather than silently fall back to
			// checking against only the fixed provider list, which would narrow what L5
			// refuses without anyone being told why.
			if !hostsOK {
				logLaunchFailure("authorization", &jobSubmissionError{reason: "InternalError"})
				announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, nil)
				clusterJobCreateFailed(ctx, h, pool, sessionID, "could not verify this worker's repository hosts; refusing its MCP connections")
				return errors.New("cluster resume was not accepted")
			}
			for _, grant := range grants {
				if isGitProviderURL(grant.URLSnapshot, resumeGitHosts...) {
					logLaunchFailure("authorization", &jobSubmissionError{reason: "Forbidden"})
					announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, nil)
					clusterJobCreateFailed(ctx, h, pool, sessionID, "a cluster worker cannot be granted an MCP connection to a git provider")
					return errors.New("cluster resume was not accepted")
				}
			}
		}
		// New tokens for the new pod, replacing the old ones in one transaction: the old
		// tokens died with the old pod.
		gateway, err := reissueGrants(ctx, h, pool, sessionID, launcher, requesterSessionID, grants)
		if err != nil {
			logLaunchFailure("authorization", &jobSubmissionError{reason: "Forbidden"})
			announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, nil)
			clusterJobCreateFailed(ctx, h, pool, sessionID, "MCP connections could not be granted again")
			return errors.New("cluster resume was not accepted")
		}
		spec.MCPGateway = gateway
		for _, grant := range grants {
			if grant.Builtin == mcpgw.BuiltinBoard {
				spec.ExtraEnv = sessionBoardEnv(spec.ExtraEnv, grant.BuiltinRef, gateway)
				break
			}
		}
	}
	jm.ResolvePlugins(ctx, &spec)
	announceClusterStart(ctx, h, pool, sessionID, row.Repo, true, pluginStage(spec.Plugins, spec.PluginsNote))
	if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		logLaunchFailure("queued", err)
		clusterJobCreateFailed(ctx, h, pool, sessionID, err.Error())
		return errors.New("cluster resume was not accepted")
	}
	clusterJobCreated(ctx, h, pool, sessionID, 0)
	// A child resumed by its owner keeps that sign-in as its own proof: the one its desktop parent
	// resumes it with next (delegateChild). Only once the pod exists: a failed resume changes nothing.
	if spawningAccountID != "" && isChildSession(row) {
		recordLaunchAuthSession(ctx, pool, sessionID, authSessionID)
	}
	return nil
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

func foreignEngineCredential(key, engine string) bool {
	engine = engines.Normalize(engine)
	for _, descriptor := range engines.All() {
		for _, owned := range descriptor.CredentialEnvKeys {
			if owned == key {
				return descriptor.ID != engine
			}
		}
	}
	return false
}
