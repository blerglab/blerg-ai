package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenEqual reports whether got is the configured secret want. Constant-time
// over the bytes so a loopback caller can't learn the master token one byte
// at a time (desktop-security M1); a blank want never matches, so an unset
// deployment can't be walked into with an empty header.
func tokenEqual(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// checkBearerToken validates the Authorization: Bearer {token} header.
// Returns true if valid, writes 401 and returns false otherwise.
func checkBearerToken(w http.ResponseWriter, r *http.Request, token string) bool {
	auth := r.Header.Get("Authorization")
	t, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !tokenEqual(t, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// validateRepoName checks that a repo name is safe for use in git operations
// and as a path segment under a repos root, rejecting path traversal and
// absolute paths.
//
// Exactly ONE interior "/" is allowed, i.e. "name" or "org/name". Org-qualified
// repos are first-class across the stack — boards store them, agents naturally
// write them, and runner.JoinRepoURL resolves an "org/name" against the git
// base's host precisely so they work (k8sjobs does the same) — so refusing
// every slash here would 422 every start on any board configured that way.
// The one allowed slash is a *name* separator, never a path: each segment must
// be non-empty, must not begin with a dot, and no segment may be "..", so
// nothing can climb out of a repos root. The daemon's symlink-aware
// resolveProjectPath remains the real containment boundary.
func validateRepoName(repo string) error {
	bad := fmt.Errorf("invalid repo name %q", repo)
	if repo == "" || strings.ContainsAny(repo, "\\\x00\n") || strings.HasPrefix(repo, "/") {
		return bad
	}
	segments := strings.Split(repo, "/")
	if len(segments) > 2 {
		return bad
	}
	for _, s := range segments {
		if s == "" || strings.HasPrefix(s, ".") {
			return bad
		}
	}
	return nil
}

// validateRepoNames applies validateRepoName to every repo in the slice,
// returning the first error found. Any endpoint that accepts a list of repo
// strings from a request body (board creation, ticket repo patches) must run
// this before those strings reach the database or the daemon, in addition to
// whatever board-membership checks apply downstream (defence in depth).
func validateRepoNames(repos []string) error {
	for _, repo := range repos {
		if err := validateRepoName(repo); err != nil {
			return err
		}
	}
	return nil
}

// API groups the dependencies shared by all REST handlers.
type API struct {
	// beforeMarkPrivate is a test seam: it runs at the top of MarkPrivate, that is
	// between a session's insert and its marking (privacy_atomic_test.go).
	beforeMarkPrivate func(sessionID string)
	hub               *Hub
	dbPool            *pgxpool.Pool
	daemonToken       string
	repos             *RepoLister
	userRepos         *UserRepoLister // per-caller repos from their own git tokens; nil without core
	vapidPublicKey    string
	waiters           *messageWaiters
	embedder          Embedder
	runnerKey         string
	coreAuth          *coreauth.Client
	cron              *CronService // the cron start path and lifecycle (cronstart.go); nil when crons are off
	metrics           metricsState // GET /metrics: its bearer token and a short cache (metrics.go)

	// coreURL/coreInternalKey address blerg-core's internal credential
	// endpoints for browser-facing lookups (GET /api/me/credentials).
	// Deliberately the API's own copy rather than the JobManager's: the
	// JobManager exists only in-cluster with an agent image configured,
	// while this endpoint must answer on every deployment shape.
	coreURL         string // BLERG_CORE_URL
	coreInternalKey string // BLERG_RUNNER_CORE_INTERNAL_KEY

	// ssePollInterval/sseKeepaliveInterval pace the agent-contract SSE stream
	// (events_stream.go). Zero means the production defaults; tests shorten
	// them so a stream's timing can be exercised in milliseconds rather than
	// by sleeping through real seconds.
	ssePollInterval      time.Duration
	sseKeepaliveInterval time.Duration

	// startWatchInterval paces the cluster start watcher's pod polls
	// (startstages.go); zero means the default. Tests shorten it.
	startWatchInterval time.Duration

	// webhookClient/webhookBackoff configure the completion webhook
	// (webhook.go). Both are nil in production — the defaults are a dedicated
	// 10 s client that refuses redirects and the documented 5 s/30 s/120 s
	// retry schedule — and set by tests, which cannot afford to sleep through
	// two and a half minutes of real backoff.
	webhookClient  *http.Client
	webhookBackoff []time.Duration

	// The reconciler's windows (jobreconcile.go), zero meaning the documented
	// defaults. Tests set them because the real values — a day of resumability,
	// half an hour of waiting for a daemon — cannot be waited out.
	clusterResumeWindow    time.Duration
	daemonLostWindow       time.Duration
	clusterJobMissingGrace time.Duration

	// modelSources backs GET /api/models/{engine} (see SetModelSources).
	modelSources *models.Registry

	// fetchGitToken fetches an account's personal git token for a daemon's
	// named clone (namedCloneSpawn). nil = core's internal credential fetch;
	// tests set it.
	fetchGitToken func(ctx context.Context, accountID, kind string) ([]byte, bool, error)
	// repoVisibility answers repoPrivate; nil = ask the provider. Tests set it.
	repoVisibility func(ctx context.Context, p gitprovider.Provider, token string, ref gitprovider.Ref) (private, known bool)
	// createRepo creates a new repository for a cluster session (newrepo.go); nil = the provider.
	// Tests set it.
	createRepo func(ctx context.Context, p gitprovider.Provider, token string, ref gitprovider.Ref, private bool) error

	// pluginAllow is this server's marketplace allow-list (BLERG_RUNNER_PLUGIN_MARKETPLACES), read
	// once at construction, for sessions sent to a workstation daemon (daemonPlugins). The cluster
	// path keeps its own copy on the JobManager, which a desktop install does not have.
	pluginAllow pluginspec.Allowlist
	// fetchPlugins fetches an account's always-on plugin list for a daemon session. nil = core's
	// internal endpoint; tests set it.
	fetchPlugins func(ctx context.Context, accountID string, proof coreProof) ([]pluginspec.Entry, error)
	// fetchTokenName asks core for an agent token's label (startedby.go). nil = the HTTP
	// lookup; tests set it.
	fetchTokenName func(ctx context.Context, accountID, tokenID string) (string, error)
}

// SetEmbedder configures the knowledge-search embedder (nil = keyword fallback).
func (a *API) SetEmbedder(e Embedder) { a.embedder = e }

// SetCoreAuth wires in blerg-core as an additional, additive credential
// authority for the runner contract endpoints (see authRunner). A nil client
// (BLERG_CORE_URL unset) leaves that branch inert.
func (a *API) SetCoreAuth(c *coreauth.Client) { a.coreAuth = c }

// SetCoreCredentials wires in blerg-core's internal credential API. Either
// argument being empty leaves the feature off — GET /api/me/credentials then
// answers "unavailable" rather than failing.
func (a *API) SetCoreCredentials(url, internalKey string) {
	a.coreURL, a.coreInternalKey = strings.TrimRight(url, "/"), internalKey
	a.userRepos = nil
	if a.coreURL != "" && a.coreInternalKey != "" {
		// GET /api/repos lists each caller's own repositories through their
		// personal git-provider tokens, which live in core.
		a.userRepos = NewUserRepoLister(gitprovider.Default,
			func(ctx context.Context, accountID string) ([]string, bool) {
				kinds, _, ok := a.listPersonalCredentialKinds(ctx, accountID)
				return kinds, ok
			},
			func(ctx context.Context, accountID, kind string) ([]byte, bool) {
				// The proof rides on ctx: the browser request's own session
				// (its token's sid), which core checks belongs to accountID.
				tok, found, err := fetchCoreCredential(ctx, nil, a.coreURL, a.coreInternalKey, accountID, kind, coreProofFrom(ctx))
				return tok, err == nil && found
			})
	}
}

// coreAuthBrowserCap is the capability a core-issued token must carry for
// browser-facing endpoints. The browser API manages sessions, so a bare
// aud:"blerg-runner" token is not enough on its own — it must also assert
// this capability.
const coreAuthBrowserCap = "session.start"

// authBrowser validates a core-issued authentication token for browser-facing
// endpoints. Returns the verified principal and true if valid, otherwise
// writes 401 and returns an empty principal and false.
func (a *API) authBrowser(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	raw := ""
	if h := r.Header.Get("Authorization"); h != "" {
		raw = strings.TrimPrefix(h, "Bearer ")
	}
	p, ok := a.verifyBrowserToken(raw)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return identity.Principal{}, false
	}
	return p, true
}

// verifyBrowserToken is the shared verification core behind both authBrowser
// (REST, token from the Authorization header) and AuthorizeBrowserWS (the
// /ws/browser upgrade, token from the Sec-WebSocket-Protocol list). Keeping
// one implementation means the socket can never drift into being a weaker
// gate than the REST surface it shares state with.
func (a *API) verifyBrowserToken(raw string) (identity.Principal, bool) {
	if raw == "" || a.coreAuth == nil {
		return identity.Principal{}, false
	}
	p, err := identity.Verify(raw, coreAuthAudience, a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps)
	if err != nil || !p.Has(coreAuthBrowserCap) {
		return identity.Principal{}, false
	}
	return p, true
}

// AuthorizeBrowserWS gates the /ws/browser upgrade. It is passed to
// Hub.ServeBrowser from main.go; see wsBearerToken (browser_conn.go) for how
// the token reaches a request that cannot carry custom headers. It returns the
// verified account id alongside the verdict so the socket can attribute the
// commands it accepts to the human who opened it.
func (a *API) AuthorizeBrowserWS(r *http.Request) (accountID, sessionID string, ok bool) {
	p, ok := a.verifyBrowserToken(wsBearerToken(r))
	if !ok {
		return "", "", false
	}
	return p.Sub, p.Sid, true
}

// NewAPI creates an API handler group.
func NewAPI(hub *Hub, dbPool *pgxpool.Pool, daemonToken string, repos *RepoLister, vapidPublicKey string) *API {
	api := &API{
		hub:            hub,
		dbPool:         dbPool,
		daemonToken:    daemonToken,
		repos:          repos,
		vapidPublicKey: vapidPublicKey,
		waiters:        newMessageWaiters(),
	}
	api.pluginAllow, _ = pluginAllowlistFromEnv()
	// Several paths that end a session are package functions on the hub side
	// of the server (daemon message handling, disconnect, reconciliation) with
	// no API to call. Registering the notifier on the Hub — the one object
	// they all already hold — is the least invasive way to give them the
	// completion webhook without threading an extra argument through each.
	if hub != nil {
		hub.SetPrivacyPool(dbPool) // lets every broadcast withhold private sessions (privacy.go)
		hub.SetCompletionNotifier(api.notifyCompletion)
		hub.SetAutoStopper(api.autoStopOnTurnDone)
	}
	return api
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api writeJSON: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sessionRowToInfo maps a row to the browser's SessionInfo, as seen by viewer
// (the requesting account; "" when there is none): the end attribution says
// whether "you" ended the session, never whose account did.
func sessionRowToInfo(row db.SessionRow, viewer string) protocol.SessionInfo {
	info := protocol.SessionInfo{
		ID:          row.ID,
		DaemonID:    row.DaemonID,
		Status:      row.Status,
		ProjectPath: row.ProjectPath,
		Repo:        row.Repo,
		StartedAt:   row.StartedAt.UTC().Format(time.RFC3339),
		Unread:      row.Unread,
		Starred:     row.Starred,
	}
	if row.Title != nil {
		info.Title = *row.Title
	}
	if row.Model != nil {
		info.Model = *row.Model
	}
	if row.Engine != nil {
		info.Engine = *row.Engine
	}
	if row.Effort != nil {
		info.Effort = *row.Effort
	}
	if row.EndedAt != nil {
		s := row.EndedAt.UTC().Format(time.RFC3339)
		info.EndedAt = &s
	}
	if row.Runtime != nil {
		info.Runtime = *row.Runtime
	}
	info.Kind = row.Kind
	info.SkipPermissions = row.SkipPermissions
	if row.ErrorReason != nil {
		info.ErrorReason = *row.ErrorReason
	}
	info.EndReason, info.EndedBy = endAttribution(&row, viewer)
	if row.CronID != nil {
		info.CronID = *row.CronID
	}
	info.StartedBy = startedByOf(&row)
	info.Interaction = effectiveInteraction(&row)
	return info
}

// messageRowToInfo maps a DB message row to the wire shape, preserving the nullable
// answer/answered_at (answer may be nil for an update).
func messageRowToInfo(row db.MessageRow) protocol.MessageInfo {
	info := protocol.MessageInfo{
		ID:        row.ID,
		SessionID: row.SessionID,
		Kind:      row.Kind,
		Body:      row.Body,
		Status:    row.Status,
		Answer:    row.Answer,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
	}
	if row.AnsweredAt != nil {
		s := row.AnsweredAt.UTC().Format(time.RFC3339)
		info.AnsweredAt = &s
	}
	return info
}

// ─── GET /api/daemons ─────────────────────────────────────────────────────────

// HandleGetDaemons returns a JSON array of all connected daemons.
func (a *API) HandleGetDaemons(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	daemons := a.hub.GetAllDaemons()
	result := make([]protocol.DaemonInfo, 0, len(daemons))
	for _, d := range daemons {
		// Cluster-runtime session pods connect as their own ephemeral
		// per-session "daemon" (mode "runner", see internal/runner/runner.go)
		// purely so the existing daemon-connection WS routing works
		// uniformly — they're not a real desktop daemon a user should be
		// able to pick as a "Run on" target, so exclude them here.
		if d.Mode == "runner" {
			continue
		}
		claudeCLI, apiKey := d.HostClaude()
		result = append(result, protocol.DaemonInfo{
			ID:                 d.ID,
			Name:               d.Name,
			Mode:               d.Mode,
			ReposRoot:          d.CurrentReposRoot(),
			Status:             "connected",
			Version:            d.Version,
			SandboxAvailable:   d.SandboxAvailable(),
			AvailableEngines:   d.AvailableEngines(),
			ClaudeCLIAvailable: claudeCLI,
			AnthropicKeySet:    apiKey,
			CloneFrom:          d.CanCloneTarget(),

			AllowHostCredentialClone: d.AllowsHostCredentialClone(),
			SandboxClaudeCredential:  d.SandboxClaudeCredential(),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

// ─── GET /api/cluster/status ──────────────────────────────────────────────────

// HandleGetClusterStatus returns the k8s cluster-runtime's configuration and
// live state for the status dashboard and the launch UI's engine gating.
// Returns {"configured": false} (never an error) when this server has no
// cluster runtime at all — that's a normal, common deployment shape (desktop
// or plain daemon setups), not a fault.
func (a *API) HandleGetClusterStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	jm := a.hub.JobManager()
	if jm == nil {
		writeJSON(w, http.StatusOK, protocol.ClusterStatus{Configured: false})
		return
	}
	writeJSON(w, http.StatusOK, jm.Status()) //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
}

// ─── GET /api/me/credentials ──────────────────────────────────────────────────

// internalCredentialListRequest/Response mirror blerg-core's
// POST /internal/credentials/list wire shape. The response carries credential
// kind *names* only — never any value — so there is nothing secret to leak
// here; core's body is still never forwarded to the browser, so core's error
// text and internal detail stay on the server side.
type internalCredentialListRequest struct {
	AccountID string `json:"account_id"`
	// Exactly one of these, as on the fetch: which live token/session authorises the listing.
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type internalCredentialListResponse struct {
	Engines []string `json:"engines"`
}

// HandleGetMyCredentials reports which personal credentials the calling
// account has stored in blerg-core, so the launch UI can tell the user which
// engines they can run in the cluster and whether their own GitHub token will
// be used for cloning.
//
// Always 200 for an authenticated caller: a core that is unconfigured,
// unreachable, or erroring yields {"engines":[],"git":false,"unavailable":true}
// so the UI degrades to "unknown" instead of blocking. A 404 from core is in
// that same "couldn't tell" bucket: core's list endpoint 404s when the account
// has no LIVE SESSION (its anti-enumeration shape, identical to fetch's), which
// says nothing about whether credentials are stored — reporting it as a
// definite empty answer would tell the user they have none when they may have
// several.
func (a *API) HandleGetMyCredentials(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	out := protocol.MyCredentials{Engines: []string{}, GitProviders: []string{}}
	kinds, found, ok := a.listPersonalCredentialKinds(withCoreProof(r.Context(), proofFromPrincipal(principal)), principal.Sub)
	if !ok {
		out.Unavailable = true
		writeJSON(w, http.StatusOK, out)
		return
	}
	if found {
		for _, kind := range kinds {
			// Every registered git provider's kind is git access, not an
			// engine — "github", "gitlab", or whatever is registered next.
			if _, isGit := gitprovider.Default.ProviderForCredentialKind(kind); isGit {
				out.Git = true
				out.GitProviders = append(out.GitProviders, kind)
				continue
			}
			out.Engines = append(out.Engines, kind)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listPersonalCredentialKinds asks core which credential kinds accountID has.
// ok=false means "couldn't tell" (not configured, transport failure, a 404 —
// which is core's "no live session", not "no credentials" — or any other error
// status); ok=true with found=false is core's definite "this account has none",
// i.e. a 200 with an empty engines list.
func (a *API) listPersonalCredentialKinds(ctx context.Context, accountID string) (kinds []string, found, ok bool) {
	if a.coreURL == "" || a.coreInternalKey == "" || accountID == "" {
		return nil, false, false
	}
	proof := coreProofFrom(ctx)
	if !proof.valid() {
		// A token from before core stamped its session id: "couldn't tell",
		// never a guess. It expires within minutes; a reload mints a new one.
		log.Printf("api me/credentials: no session id on the caller's token for account=%s; not asking core", accountID)
		return nil, false, false
	}
	client := coreHTTPClient(nil) // never follows a redirect: the request carries the internal key
	raw, err := json.Marshal(internalCredentialListRequest{AccountID: accountID, TokenID: proof.TokenID, SessionID: proof.SessionID})
	if err != nil {
		return nil, false, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/credentials/list", bytes.NewReader(raw))
	if err != nil {
		return nil, false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("api me/credentials: request to core failed: %v", err)
		return nil, false, false
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var body internalCredentialListResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			log.Printf("api me/credentials: decode response failed: %v", err)
			return nil, false, false
		}
		return body.Engines, true, true
	case http.StatusNotFound:
		// NOT "this account has no credentials": core's list endpoint 404s
		// when the account has no live human session, deliberately
		// indistinguishable from a missing credential so a key holder cannot
		// enumerate who is logged in. Treat it as "couldn't tell" — claiming
		// an empty set here would show a user with credentials stored that
		// they have none.
		log.Printf("api me/credentials: core returned 404 (no live session) for account=%s", accountID)
		return nil, false, false
	default:
		// Status only — core's body is deliberately not read or logged.
		log.Printf("api me/credentials: core returned %d for account=%s", resp.StatusCode, accountID)
		return nil, false, false
	}
}

// ─── GET /api/sessions ────────────────────────────────────────────────────────

// HandleGetSessions returns a JSON array of sessions, optionally filtered by
// ?status=, ?daemon_id=, or ?runtime=cluster. runtime=cluster lists every
// cluster-runtime session regardless of which per-pod daemon currently owns
// it — daemon_id can't do this: each cluster session pod connects as its
// own ephemeral daemon (see db.ListSessionsByDaemonMode's doc comment), so a
// fixed daemon_id only matches a session in the brief window before its pod
// has connected.
func (a *API) HandleGetSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []protocol.SessionInfo{})
		return
	}
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	daemonID := r.URL.Query().Get("daemon_id")
	runtime := r.URL.Query().Get("runtime")
	var (
		rows []db.SessionRow
		err  error
	)
	switch {
	case runtime == "cluster":
		rows, err = db.ListSessionsByDaemonMode(ctx, a.dbPool, "runner")
	case daemonID != "":
		rows, err = db.ListSessionsByDaemon(ctx, a.dbPool, daemonID)
	case status != "":
		rows, err = db.ListSessionsByStatus(ctx, a.dbPool, status)
	default:
		rows, err = db.ListSessions(ctx, a.dbPool)
	}
	if err != nil {
		log.Printf("api list sessions: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	result := make([]protocol.SessionInfo, 0, len(rows))
	for _, row := range rows {
		if daemonID != "" && status != "" && row.Status != status {
			continue
		}
		if !canSeeAccount(principal.Sub, &row) { // a private session is its owner's alone
			continue
		}
		result = append(result, sessionRowToInfo(row, principal.Sub))
	}
	writeJSON(w, http.StatusOK, result)
}

// ─── POST /api/sessions ───────────────────────────────────────────────────────

type spawnSessionRequest struct {
	DaemonID                   string `json:"daemon_id"`
	Repo                       string `json:"repo"`
	Title                      string `json:"title"`
	InitialPrompt              string `json:"initial_prompt"`
	Model                      string `json:"model,omitempty"`
	Effort                     string `json:"effort,omitempty"` // "" | one of models.Efforts
	DangerouslySkipPermissions bool   `json:"dangerously_skip_permissions,omitempty"`
	// NewRepo: on a daemon, create Repo as a new empty folder (git init) under
	// the repos root. On the cluster, create Repo ("owner/name", required in
	// that form) on Provider with the caller's own token and start in it; see
	// newrepo.go for what happens when it cannot be created.
	NewRepo bool `json:"new_repo,omitempty"`
	// Visibility of a cluster NewRepo: "private" (default) or "public".
	Visibility string `json:"visibility,omitempty"`
	Kind       string `json:"kind,omitempty"`    // "" (tmux) | "agent"
	Runtime    string `json:"runtime,omitempty"` // "" (daemon) | "docker" | "cluster"
	Engine     string `json:"engine,omitempty"`  // "" (claude) | "codex" | "hermes" | "openclaw" (openclaw: daemon runtime only, not cluster)
	// Provider is the git provider Repo lives on — a registered gitprovider
	// ID ("github", "gitlab", …), honoured on every runtime; "" is the legacy
	// GitHub default (see repo_provider.go). It is what RepoInfo.provider
	// said for the picked repository.
	Provider string `json:"provider,omitempty"`
	// Clone says Repo is a hosted "owner/name" on Provider (both required)
	// to clone fresh — a repository the caller named, listed or not — rather
	// than a folder the daemon has. On a daemon it lands in a folder chosen
	// by cloneFolderFor; a cluster pod clones every repo anyway.
	Clone bool `json:"clone,omitempty"`
	// GitURL names the repository by remote URL instead (https, ssh:// or
	// scp-like, on a registered provider's host); it implies Clone and
	// fixes Provider and Repo (resolveNamedRepo).
	GitURL string `json:"git_url,omitempty"`
	// NoRepo is "No repository": a session tied to no repository at all.
	// Explicit, never inferred from an empty Repo — a caller that leaves repo
	// blank by mistake still gets "repo is required". On the cluster the pod
	// works in an empty directory; on a daemon (This machine or Local
	// sandbox) the session runs in a new scratch folder under the repos root,
	// named ScratchFolder or, when that is empty, a generated name. Repo,
	// GitURL, Provider, Clone and NewRepo must all be unset with it.
	NoRepo bool `json:"no_repo,omitempty"`
	// ScratchFolder is the scratch folder a no_repo daemon session gets
	// (scratch.Valid: ".scratch-" plus letters, digits, '-' or '_'). Optional
	// — the launch sheet sends the name it showed before Launch — and refused
	// without no_repo or on the cluster, where there is no folder to name.
	ScratchFolder string `json:"scratch_folder,omitempty"`
	// MCP selects the caller's own MCP connections (and tools) for this session
	// (mcpstart.go). Only this route, for a signed-in person, accepts it. Absent or
	// empty means none.
	MCP []MCPSelection `json:"mcp,omitempty"`
	// Interaction says whether a person is reading the session's chat as it works:
	// "interactive" (the default here: the launch sheet is a person) or "unattended".
	// Anything else is a 400 (interaction.go).
	Interaction string `json:"interaction,omitempty"`
}

// noRepoProblem is what is wrong with a no_repo start that also names a
// repository ("" = nothing). Shared by POST /api/sessions and the v1 start, so
// both refuse the same combinations with the same words.
func noRepoProblem(repo, gitURL, provider string, clone, newRepo bool) string {
	if repo != "" || gitURL != "" || provider != "" || clone || newRepo {
		return "no_repo cannot be combined with repo, git_url, provider, clone or new_repo"
	}
	return ""
}

// scratchFolderProblem validates a POST /api/sessions scratch_folder.
func scratchFolderProblem(req spawnSessionRequest) string {
	if req.ScratchFolder == "" {
		return ""
	}
	if !req.NoRepo {
		return "scratch_folder needs no_repo"
	}
	if req.Runtime == "cluster" {
		return "scratch_folder does not apply to cluster sessions: they have no folder"
	}
	if !scratch.Valid(req.ScratchFolder) {
		return `scratch_folder must be ".scratch-" followed by up to 64 letters, digits, '-' or '_'`
	}
	return ""
}

// HandlePostSessions validates the request body, generates a session UUID, and
// forwards a spawn_session message to the target daemon.
func (a *API) HandlePostSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	var req spawnSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Only a signed-in person may attach MCP connections: refuse anyone else before
	// anything else is looked at.
	if len(req.MCP) > 0 {
		if apiErr := checkGrantRequester(requesterOf(principal)); apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
	}
	// runtime is a closed set (spec §2). An unrecognised value is a caller
	// mistake, and the only safe answer is to refuse: silently falling back
	// would put the session on the least-sandboxed runtime of the three.
	if !validRuntime(req.Runtime) {
		writeError(w, http.StatusUnprocessableEntity, "invalid runtime")
		return
	}
	if req.NoRepo {
		if msg := noRepoProblem(req.Repo, req.GitURL, req.Provider, req.Clone, req.NewRepo); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, msg)
			return
		}
	}
	if msg := scratchFolderProblem(req); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// A repository named by URL, or by owner/name to clone: one shape for
	// every runtime from here on (repo = owner/name, provider set).
	if msg := resolveNamedRepo(&req); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// The launch sheet is a person: interactive unless the request says nobody will be reading.
	// From here on req.Interaction is the resolved mode.
	interaction, interactionErr := resolveInteraction(req.Interaction, true, false)
	if interactionErr != nil {
		writeAPIError(w, interactionErr)
		return
	}
	req.Interaction = interaction
	// model and effort end up as engine CLI arguments on every runtime, so
	// both are checked here, before anything is recorded or sent.
	// A daemon's reported model list only applies to a session on it.
	listDaemon := req.DaemonID
	if req.Runtime == "cluster" {
		listDaemon = ""
	}
	if msg := a.modelEffortProblem(r.Context(), req.Engine, req.Model, req.Effort, listDaemon); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	// Cluster runtime: the server creates a runner Job instead of forwarding
	// to a daemon. Agent-kind only — a terminal session needs a PTY someone
	// can attach to, which a Job pod does not offer.
	if req.Runtime == "cluster" {
		if req.Kind != "agent" {
			writeError(w, http.StatusUnprocessableEntity, "terminal sessions run on a daemon")
			return
		}
		// Fail closed BEFORE anything is recorded: when personal credentials
		// are wired, core will only hand them over against a named live
		// session, and a token without a session id names none.
		if jm := a.hub.JobManager(); jm != nil && jm.personalCredentialsWired() && !proofFromPrincipal(principal).valid() {
			writeError(w, http.StatusUnauthorized, errNoLivenessProof.Error())
			return
		}
		if req.NoRepo {
			a.startClusterNoRepo(w, r, principal.Sub, proofFromPrincipal(principal), requesterOf(principal), req)
			return
		}
		if req.Repo == "" {
			writeError(w, http.StatusUnprocessableEntity, "repo is required")
			return
		}
		if err := validateRepoName(req.Repo); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		jm := a.hub.JobManager()
		if jm == nil {
			writeError(w, http.StatusServiceUnavailable, "cluster runtime not configured on this server")
			return
		}
		// A cluster session clones from the configured git base. With neither
		// an explicit BLERG_RUNNER_AGENT_GIT_BASE nor an org to default to,
		// that base is bare github.com, so a one-segment repo name has
		// nothing to resolve against — refuse it up front rather than
		// spawning a pod that fails its clone. Either piece of config is a
		// complete answer on its own (an operator-chosen base may well be a
		// host whose paths are single-segment), and a repo already given as
		// org/name always resolves regardless.
		// Backstop for a bare local-folder name: if the caller named the
		// daemon it picked the folder from and that daemon reported the
		// folder's GitHub origin, that org/name is the repository — resolve
		// it here. Only the explicitly requested daemon is consulted (two
		// machines' same-named folders can be different repos), and only
		// when nothing else would resolve the bare name.
		if req.Provider != "" {
			if _, ok := gitprovider.Default.ProviderForCredentialKind(req.Provider); !ok {
				writeError(w, http.StatusUnprocessableEntity, "unknown git provider")
				return
			}
		}
		// A new repository is created under exactly the owner the caller
		// typed: no bare-name resolution against the operator's org or a
		// daemon's folder origin may pick an owner for them.
		if req.NewRepo && !strings.Contains(req.Repo, "/") {
			writeError(w, http.StatusUnprocessableEntity, "a new repository must be given as owner/name")
			return
		}
		if req.NewRepo && req.Visibility != "" && req.Visibility != "private" && req.Visibility != "public" {
			writeError(w, http.StatusUnprocessableEntity, `visibility must be "private" or "public"`)
			return
		}
		// The origin a daemon reported carries its provider, so a folder
		// whose origin is on GitLab resolves to GitLab, never to a same-named
		// GitHub guess.
		if !strings.Contains(req.Repo, "/") && jm.GitHubOrg == "" && jm.ExplicitGitURLBase == "" && req.DaemonID != "" {
			if d := a.hub.GetDaemon(req.DaemonID); d != nil {
				if o, ok := d.RepoOrigin(req.Repo); ok && (req.Provider == "" || req.Provider == o.Provider) {
					req.Repo, req.Provider = o.FullName, o.Provider
				}
			}
		}
		if !strings.Contains(req.Repo, "/") && jm.GitHubOrg == "" && jm.ExplicitGitURLBase == "" {
			writeError(w, http.StatusUnprocessableEntity,
				"repo must be given as org/name for cluster sessions (or set BLERG_RUNNER_GITHUB_ORG)")
			return
		}
		// A non-default provider has no operator base to resolve a bare name
		// against, and its own naming rules to meet.
		if req.Provider != "" && req.Provider != gitprovider.GitHubID {
			if _, ok := gitprovider.Default.ParseFullName(req.Provider, req.Repo); !ok {
				writeError(w, http.StatusUnprocessableEntity, "repo must be given as owner/name on "+req.Provider)
				return
			}
		}
		// The caller's MCP selection is checked, against core and the live upstream, before
		// anything is recorded (mcpstart.go).
		grant, apiErr := a.resolveGrant(r.Context(), requesterOf(principal), req.MCP,
			grantTarget{Runtime: runnerRuntimeCluster, Engine: req.Engine, Kind: req.Kind})
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		// A new repository is created (or not — the session still starts)
		// before anything is recorded; the outcome leads the start plan.
		var lead []protocol.StartStage
		if req.NewRepo {
			// The repository is created on the provider's own host, but the pod clones from CloneURLFor: when an
			// operator's git base redirects that, the clone would never find what was just created.
			if msg := newRepoHostProblem(jm.CloneURLFor(req.Provider, req.Repo), req.Provider); msg != "" {
				writeError(w, http.StatusUnprocessableEntity, msg)
				return
			}
			st, apiErr := a.createClusterRepo(withCoreProof(r.Context(), proofFromPrincipal(principal)), req, principal.Sub)
			if apiErr != nil {
				writeAPIError(w, apiErr)
				return
			}
			lead = append(lead, st)
		}
		gitURL := jm.CloneURLFor(req.Provider, req.Repo)
		sessionID := newUUID()
		// Create the session row BEFORE the Job: agent_events has an FK on
		// sessions, and the runner's session_started is fire-and-forget over
		// a possibly-not-yet-open WS — without this row a lost session_started
		// makes the session invisible and every event append fail.
		if a.dbPool != nil {
			if err := db.UpsertDaemon(r.Context(), a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
				log.Printf("cluster daemon upsert: %v", err)
			}
			// runtime='cluster' goes in with the row (see InsertClusterSession):
			// the reconciler selects on that column and must not be able to
			// miss a session because a follow-up write failed.
			// A start that carries a grant goes in private, with its owner, in this same
			// insert: no other account can list it before attachGrant marks it.
			origin := sessionOriginFor(principal.Sub, grant != nil, "")
			a.notePrivateInsert(sessionID, origin)
			if err := db.InsertClusterSessionAs(r.Context(), a.dbPool, sessionID, clusterDaemonID, "starting",
				"/workspace/"+req.Repo, req.Repo, req.Title, req.Model, origin); err != nil {
				writeError(w, http.StatusInternalServerError, "session create failed")
				return
			}
			// Persist the spawning account (from the verified token, never the
			// request body) so a later resume (resumeClusterSession) can look
			// up this account's personal credential again instead of falling
			// back to the shared operator Secret.
			if err := db.SetSessionSpawningAccount(r.Context(), a.dbPool, sessionID, principal.Sub); err != nil {
				log.Printf("cluster SetSessionSpawningAccount %s: %v", sessionID, err)
			}
			if err := db.SetSessionKind(r.Context(), a.dbPool, sessionID, "agent"); err != nil {
				log.Printf("cluster SetSessionKind %s: %v", sessionID, err)
			}
			if err := db.SetSessionEngine(r.Context(), a.dbPool, sessionID, req.Engine); err != nil {
				log.Printf("cluster SetSessionEngine %s: %v", sessionID, err)
			}
			// The clone URL (no token in it) is what names the provider: a
			// resume clones the same repository from the same host, and
			// picks the git credential by that host.
			if err := db.SetSessionGitURL(r.Context(), a.dbPool, sessionID, gitURL); err != nil {
				log.Printf("cluster SetSessionGitURL %s: %v", sessionID, err)
			}
			if req.NewRepo {
				if err := db.SetSessionNewRepo(r.Context(), a.dbPool, sessionID); err != nil {
					log.Printf("cluster SetSessionNewRepo %s: %v", sessionID, err)
				}
			}
			recordInteraction(r.Context(), a.dbPool, sessionID, req.Interaction)
		}
		if a.dbPool != nil {
			recordLaunchEffort(r.Context(), a.dbPool, sessionID, req.Effort)
		}
		// The grant goes on now that the spawning account is recorded (the session is made
		// private after it); its tokens go only into the per-session Secret.
		var gateway *protocol.MCPGatewayConfig
		if grant != nil {
			if gateway, apiErr = a.attachGrant(r.Context(), sessionID, grant); apiErr != nil {
				a.abortGrantSession(r.Context(), sessionID)
				writeAPIError(w, apiErr)
				return
			}
		}
		// Visible now, with its start plan: creating the Job (credential
		// lookups included) and everything after it is what the browser's
		// progress panel follows.
		spec := SessionJobSpec{
			MCPGateway: gateway,
			// A launch-sheet session is watched: it keeps its tools, settings and plugins,
			// connections or not (docs/design/interactive-mcp-sessions.md).
			RestrictTools: false,
			Interaction:   req.Interaction,
			SessionID:     sessionID, Repo: req.Repo, Title: req.Title,
			Model: req.Model, Effort: req.Effort, Engine: req.Engine, InitialPrompt: req.InitialPrompt,
			ExtraEnv: withClusterSessionToken(r.Context(), a.dbPool, sessionID, nil),
			GitURL:   gitURL,
			// A new repository: the pod initialises when the clone fails, and
			// never holds the operator's git token (newrepo.go).
			NewRepo: req.NewRepo, NoOperatorGitToken: req.NewRepo,
			// Derived from the verified access token's Sub, never taken from
			// the request body — a caller must never claim to be spawning on
			// behalf of a different account than its own token proves.
			SpawningAccountID: principal.Sub,
			// The session (the token's sid) that launched this: core checks it
			// is live and this account's before releasing a credential.
			AuthSessionID: proofFromPrincipal(principal).SessionID,
			TokenID:       proofFromPrincipal(principal).TokenID,
		}
		jm.ResolvePlugins(r.Context(), &spec)
		announceClusterStart(r.Context(), a.hub, a.dbPool, sessionID, req.Repo, false, pluginStage(spec.Plugins, spec.PluginsNote), lead...)
		if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
			// The row exists (and browsers were told), so the failure is
			// recorded on it rather than leaving a session that is forever
			// about to begin — the same text the caller gets in the 503
			// (CreateSessionJob keeps anything sensitive out of it).
			a.markClusterStartFailed(r.Context(), sessionID, err.Error())
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		clusterJobCreated(r.Context(), a.hub, a.dbPool, sessionID, a.startWatchEvery())
		writeJSON(w, http.StatusAccepted, map[string]string{"session_id": sessionID})
		return
	}

	if req.NoRepo {
		if req.DaemonID == "" {
			writeError(w, http.StatusUnprocessableEntity, "daemon_id is required")
			return
		}
	} else if req.DaemonID == "" || req.Repo == "" {
		writeError(w, http.StatusUnprocessableEntity, "daemon_id and repo are required")
		return
	}
	// Applied to every daemon-targeted spawn, not only new_repo: the daemon
	// process is the one that actually mounts/chdirs into the repo, so the
	// server must reject a path-traversal attempt here regardless of whether
	// the caller also asked to create the directory. A no_repo spawn names no
	// repo; its scratch folder was checked above (scratch.Valid) or is ours.
	if !req.NoRepo {
		if err := validateRepoName(req.Repo); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "repo must be a folder name or org/name (one slash at most, no '..', no leading dot)")
			return
		}
	}
	// R5 (see skipPermissionsAllowed): enforced here, not just in the launch
	// sheet, so a hand-crafted request gets the same answer.
	if req.DangerouslySkipPermissions && !skipPermissionsAllowed(req.Runtime) {
		writeError(w, http.StatusUnprocessableEntity, "dangerously_skip_permissions is only allowed with the Docker sandbox runtime")
		return
	}

	// A named provider is honoured on the daemon too (see repo_provider.go):
	// the daemon clones an absent folder from that provider's host, never
	// from GitHub.
	if msg := repoProviderProblem(req.Provider, req.Repo, false); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	daemon := a.hub.GetDaemon(req.DaemonID)
	if daemon == nil {
		writeError(w, http.StatusNotFound, "daemon not found or not connected")
		return
	}
	if !daemon.CanCloneFrom(req.Provider) {
		writeError(w, http.StatusUnprocessableEntity,
			"this daemon is too old to clone from "+req.Provider+" — update it, or clone the repository under its repos root")
		return
	}
	// MCP connections: never on the bare host, only on a daemon that says it can deliver them
	// (mcpstart.go). Checked before any row, token or clone credential exists.
	grant, apiErr := a.resolveGrant(r.Context(), requesterOf(principal), req.MCP,
		grantTarget{Runtime: runtimeName(req.Runtime), Engine: req.Engine, Kind: req.Kind, Daemon: daemon})
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}

	// A named clone: the daemon gets a folder to clone into (or to use, when
	// it already holds this repository) and the repository itself, plus —
	// only when a clone will actually happen — the caller's own token for
	// that repository's provider. See namedCloneSpawn.
	folder := req.Repo
	if req.NoRepo {
		// "No repository" on a daemon is still a real folder on its disk
		// (the Local sandbox bind-mounts the same one), so it gets a
		// disposable, dot-prefixed one: out of the repo picker, and marked as
		// no-repo wherever the session's repo is shown.
		folder = req.ScratchFolder
		if folder == "" {
			folder = scratch.NewName()
		}
	}
	cloneFrom, gitToken := "", ""
	if req.Clone {
		if !daemon.CanCloneTarget() {
			writeError(w, http.StatusUnprocessableEntity,
				"this daemon is too old to clone a repository by name — update it, or clone the repository under its repos root")
			return
		}
		var problem string
		var status int
		folder, cloneFrom, gitToken, status, problem = a.namedCloneSpawn(withCoreProof(r.Context(), proofFromPrincipal(principal)), daemon, req.Provider, req.Repo, principal.Sub, req.Runtime == "docker")
		if problem != "" {
			writeError(w, status, problem)
			return
		}
	}

	sessionID := newUUID()
	msg := protocol.SpawnSession{
		Provider:                   req.Provider,
		CloneFrom:                  cloneFrom,
		GitToken:                   gitToken,
		Type:                       "spawn_session",
		SessionID:                  sessionID,
		Repo:                       folder,
		Title:                      req.Title,
		Cols:                       80,
		Rows:                       24,
		InitialPrompt:              req.InitialPrompt,
		Model:                      req.Model,
		Effort:                     req.Effort,
		DangerouslySkipPermissions: req.DangerouslySkipPermissions,
		// A scratch folder is created exactly like a new folder; NewRepo
		// alongside NoRepo keeps that true on a daemon older than NoRepo.
		NewRepo: req.NewRepo || req.NoRepo,
		NoRepo:  req.NoRepo,
		Kind:    req.Kind,
		// Both kinds honour the Docker runtime now: a sandboxed agent-kind
		// session runs its engine inside the same hardened container a
		// sandboxed terminal session gets (spec §3).
		Sandbox: req.Runtime == "docker",
		Engine:  req.Engine,
		// Interactive unless the caller said nobody is reading (interaction.go).
		Interaction: req.Interaction,
		SessionToken: a.spawnSessionToken(r.Context(), sessionID, daemon, folder, req.Title, req.Model,
			sessionOriginFor(principal.Sub, grant != nil, "")),
	}

	if a.dbPool != nil {
		if err := db.SetSessionPosture(r.Context(), a.dbPool, sessionID, runtimeName(req.Runtime), req.DangerouslySkipPermissions); err != nil {
			log.Printf("SetSessionPosture %s: %v", sessionID, err)
		}
		recordLaunchEffort(r.Context(), a.dbPool, sessionID, req.Effort)
		recordInteraction(r.Context(), a.dbPool, sessionID, req.Interaction)
		// The engine too, as the cluster and v1 paths already do: the
		// in-session model switcher asks for the session engine's model list.
		if req.Engine != "" {
			if err := db.SetSessionEngine(r.Context(), a.dbPool, sessionID, req.Engine); err != nil {
				log.Printf("SetSessionEngine %s: %v", sessionID, err)
			}
		}
		// Record the kind with the posture rather than waiting for the
		// daemon's session_started: the sessions list labels a session from
		// this column, and a session sits at "starting" — visible, unlabelled
		// — for as long as the daemon takes to answer, or forever if it never
		// does. The daemon's own SetSessionKind stays as the confirming write.
		if req.Kind == "agent" {
			if err := db.SetSessionKind(r.Context(), a.dbPool, sessionID, "agent"); err != nil {
				log.Printf("SetSessionKind %s: %v", sessionID, err)
			}
		}
		// Desktop sessions are attributed too (agent-safety I6) — from the
		// verified token, never the body, same as the cluster branch.
		if err := db.SetSessionSpawningAccount(r.Context(), a.dbPool, sessionID, principal.Sub); err != nil {
			log.Printf("SetSessionSpawningAccount %s: %v", sessionID, err)
		}
	}

	if grant != nil {
		// The spawning account is recorded above; the session becomes private after it, and
		// the tokens travel only in this spawn message.
		if msg.MCPGateway, apiErr = a.attachGrant(r.Context(), sessionID, grant); apiErr != nil {
			a.abortGrantSession(r.Context(), sessionID)
			writeAPIError(w, apiErr)
			return
		}
		// A launch-sheet session is watched: it keeps its tools, settings and plugins,
		// connections or not (docs/design/interactive-mcp-sessions.md).
	}
	// Always-on plugins (non-secret): only for a Claude agent-kind session, on a daemon that can
	// load them.
	var pluginNote string
	msg.Plugins, pluginNote = a.daemonPlugins(r.Context(), daemon, principal.Sub, proofFromPrincipal(principal), req.Kind, req.Engine, msg.RestrictTools)

	data, err := json.Marshal(msg) //nolint:gosec // the spawn message must carry the session token (and any MCP gateway grant) to the daemon over the authenticated websocket; never logged
	if err != nil {
		if grant != nil {
			a.abortGrantSession(r.Context(), sessionID)
		}
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	select {
	case daemon.send <- data:
	default:
		abortSpawnSessionToken(r.Context(), a.dbPool, sessionID)
		writeError(w, http.StatusServiceUnavailable, "daemon send buffer full")
		return
	}
	if req.Kind == "agent" {
		announceDaemonAgentStart(r.Context(), a.hub, a.dbPool, sessionID, daemon.Name, req.Runtime == "docker", pluginStage(msg.Plugins, pluginNote))
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"session_id": sessionID})
}

// startClusterNoRepo is the cluster branch of HandlePostSessions for a
// "No repository" session: no repo to validate or resolve, no clone URL, and
// so no git credential of any kind for the pod. The row records repo "" (the
// cluster has no folder to name) and the pod works in an empty directory.
func (a *API) startClusterNoRepo(w http.ResponseWriter, r *http.Request, accountID string, proof coreProof, who grantRequester, req spawnSessionRequest) {
	jm := a.hub.JobManager()
	if jm == nil {
		writeError(w, http.StatusServiceUnavailable, "cluster runtime not configured on this server")
		return
	}
	grant, apiErr := a.resolveGrant(r.Context(), who, req.MCP,
		grantTarget{Runtime: runnerRuntimeCluster, Engine: req.Engine, Kind: req.Kind})
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	sessionID := newUUID()
	if a.dbPool != nil {
		if err := db.UpsertDaemon(r.Context(), a.dbPool, clusterDaemonID, "cluster", "runner", ""); err != nil {
			log.Printf("cluster daemon upsert: %v", err)
		}
		// Private, with its owner, in the insert itself when a grant is attached (see above).
		origin := sessionOriginFor(accountID, grant != nil, "")
		a.notePrivateInsert(sessionID, origin)
		if err := db.InsertClusterSessionAs(r.Context(), a.dbPool, sessionID, clusterDaemonID, "starting",
			clusterNoRepoWorkdir, "", req.Title, req.Model, origin); err != nil {
			writeError(w, http.StatusInternalServerError, "session create failed")
			return
		}
		if err := db.SetSessionSpawningAccount(r.Context(), a.dbPool, sessionID, accountID); err != nil {
			log.Printf("cluster SetSessionSpawningAccount %s: %v", sessionID, err)
		}
		if err := db.SetSessionKind(r.Context(), a.dbPool, sessionID, "agent"); err != nil {
			log.Printf("cluster SetSessionKind %s: %v", sessionID, err)
		}
		if err := db.SetSessionEngine(r.Context(), a.dbPool, sessionID, req.Engine); err != nil {
			log.Printf("cluster SetSessionEngine %s: %v", sessionID, err)
		}
		recordLaunchEffort(r.Context(), a.dbPool, sessionID, req.Effort)
		recordInteraction(r.Context(), a.dbPool, sessionID, req.Interaction)
	}
	var gateway *protocol.MCPGatewayConfig
	if grant != nil {
		if gateway, apiErr = a.attachGrant(r.Context(), sessionID, grant); apiErr != nil {
			a.abortGrantSession(r.Context(), sessionID)
			writeAPIError(w, apiErr)
			return
		}
	}
	spec := SessionJobSpec{
		MCPGateway: gateway,
		// A launch-sheet session is watched: not restricted (docs/design/interactive-mcp-sessions.md).
		RestrictTools: false,
		Interaction:   req.Interaction,
		SessionID:     sessionID, NoRepo: true, Title: req.Title,
		Model: req.Model, Effort: req.Effort, Engine: req.Engine, InitialPrompt: req.InitialPrompt,
		ExtraEnv:          withClusterSessionToken(r.Context(), a.dbPool, sessionID, nil),
		SpawningAccountID: accountID,
		AuthSessionID:     proof.SessionID,
		TokenID:           proof.TokenID,
	}
	jm.ResolvePlugins(r.Context(), &spec)
	announceClusterStart(r.Context(), a.hub, a.dbPool, sessionID, "", false, pluginStage(spec.Plugins, spec.PluginsNote))
	if err := jm.CreateSessionJob(spec); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		a.markClusterStartFailed(r.Context(), sessionID, err.Error())
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	clusterJobCreated(r.Context(), a.hub, a.dbPool, sessionID, a.startWatchEvery())
	writeJSON(w, http.StatusAccepted, map[string]string{"session_id": sessionID})
}

// clusterNoRepoWorkdir is the project_path recorded for a no-repo cluster
// session: the directory the pod works in (runner.ScratchDir under its home).
const clusterNoRepoWorkdir = "/workspace/scratch"

// skipPermissionsAllowed reports whether a spawn on the given runtime may
// bypass the engine's permission prompts (R5): only inside the Docker sandbox.
// On the bare daemon — the desktop default, and the runtime every board-driven
// session uses — that flag means "run anything as me, never ask", so it is
// refused there.
//
// The single home of this rule: every spawn path (REST launch sheet,
// board-driven daemon start) asks this function rather than re-testing the
// runtime string, so the answer cannot drift between call sites.
func skipPermissionsAllowed(runtime string) bool { return runtime == "docker" }

// validRuntime reports whether a spawn's runtime is one the server knows
// (spec §2). "" is accepted and means "daemon" — callers older than the
// explicit Run column never sent the field.
func validRuntime(rt string) bool {
	switch rt {
	case "", daemonRuntimeName, "docker", "cluster":
		return true
	}
	return false
}

// daemonRuntimeName is the posture value stored on the session row for a
// session hosted by a connected daemon (as opposed to "docker" or "cluster").
const daemonRuntimeName = "daemon"

// runtimeName normalises the request's runtime into the posture value stored on
// the session row. Every known value maps to itself; only "" — a caller older
// than the explicit Run column — becomes "daemon". "cluster" is spelled out
// rather than left to the fallback: the cluster branch above handles those
// spawns today, so it does not reach here, but a caller that did would
// otherwise have its session recorded as running on the daemon host, which is
// exactly the posture claim that must never be wrong.
func runtimeName(rt string) string {
	switch rt {
	case "docker", "cluster":
		return rt
	default:
		return daemonRuntimeName
	}
}

// abortSpawnSessionToken undoes mintSpawnSessionToken for a spawn_session
// that never reached the daemon: the token is revoked and the pre-created
// "starting" row deleted (its tokens cascade), so neither a live credential
// nor a phantom session is left behind. No-op without a DB.
func abortSpawnSessionToken(ctx context.Context, pool *pgxpool.Pool, sessionID string) {
	if pool == nil {
		return
	}
	if err := db.RevokeBoardTokensForSession(ctx, pool, sessionID); err != nil {
		log.Printf("spawn %s: abort revoke: %v", sessionID, err)
	}
	revokeSessionGrants(ctx, pool, sessionID, "spawn aborted") // a session with a grant that never started
	if err := db.DeleteSession(ctx, pool, sessionID); err != nil {
		log.Printf("spawn %s: abort delete row: %v", sessionID, err)
	}
	removeSessionArtifactFiles(sessionID) // the rows went with the session; a file is the server's to remove
}

// sessionTokenTTL bounds the per-session messaging token; session end revokes
// it earlier (HandleSessionEnded → RevokeBoardTokensForSession).
const sessionTokenTTL = 24 * time.Hour

// mintSpawnSessionToken pre-creates the session row (status "starting", so
// the token's session FK resolves before the daemon's session_started — an
// upsert that then just refreshes status/daemon) and mints the per-session
// messaging token the daemon exports as BLERG_RUNNER_SESSION_TOKEN in place
// of the master token. Shared by every daemon-routed spawn path (REST,
// browser WS, board-driven). Returns "" — logged, spawn proceeds, the session
// simply cannot message — when there is no DB or minting fails. The raw
// token is never logged.
func mintSpawnSessionToken(ctx context.Context, pool *pgxpool.Pool, sessionID string, dc *DaemonConn, repo, title, model string) string {
	return mintSpawnSessionTokenAs(ctx, pool, sessionID, dc, repo, title, model, db.SessionOrigin{})
}

// spawnSessionToken is mintSpawnSessionTokenAs that first tells the hub the
// session is private (notePrivateInsert), so even its first broadcast is scoped.
func (a *API) spawnSessionToken(ctx context.Context, sessionID string, dc *DaemonConn, repo, title, model string, origin db.SessionOrigin) string {
	a.notePrivateInsert(sessionID, origin)
	return mintSpawnSessionTokenAs(ctx, a.dbPool, sessionID, dc, repo, title, model, origin)
}

// mintSpawnSessionTokenAs is mintSpawnSessionToken for a start whose origin is
// known up front (a grant or a cron): the row goes in with its owner and its
// private flag, so no other account can list it before MarkPrivate runs. That
// holds on the failure paths too: a token that cannot be minted leaves the row
// that was inserted private (it is never inserted public and marked later), and
// a row that could not be pre-created is inserted private by the daemon's
// session_started, which reads the hub's private note (HandleSessionStarted).
func mintSpawnSessionTokenAs(ctx context.Context, pool *pgxpool.Pool, sessionID string, dc *DaemonConn, repo, title, model string, origin db.SessionOrigin) string {
	if pool == nil {
		return ""
	}
	if err := db.InsertSessionAs(ctx, pool, sessionID, dc.ID, "starting", path.Join(dc.CurrentReposRoot(), repo), repo, title, model, origin); err != nil {
		log.Printf("spawn %s: pre-create session row: %v", sessionID, err)
		return ""
	}
	tok, err := db.MintSessionToken(ctx, pool, sessionID, []string{"message"}, sessionTokenTTL)
	if err != nil {
		log.Printf("spawn %s: MintSessionToken: %v", sessionID, err)
		return ""
	}
	return tok
}

// sessionTokenEnv is the env var the `blerg-runner` CLI reads its per-session token from.
const sessionTokenEnv = "BLERG_RUNNER_SESSION_TOKEN" //nolint:gosec // the name of an environment variable, not a credential

// withClusterSessionToken returns env plus a freshly minted per-session messaging token, the
// cluster counterpart of what the daemon exports to a desktop session: the pod image's
// `blerg-runner` CLI (update, ask, note, publish) authenticates with it, scoped to this one
// session. It rides in ExtraEnv, i.e. the per-session Secret, never as a literal in the Job spec.
// The session row must already exist (the token's foreign key). The caller's map is not modified;
// with no database, or when minting fails (logged, never the token), env is returned as it was and
// the session simply cannot message. Session end revokes it like every other session token.
func withClusterSessionToken(ctx context.Context, pool *pgxpool.Pool, sessionID string, env map[string]string) map[string]string {
	if pool == nil {
		return env
	}
	tok, err := db.MintSessionToken(ctx, pool, sessionID, []string{"message"}, sessionTokenTTL)
	if err != nil {
		log.Printf("cluster start %s: MintSessionToken: %v", sessionID, err)
		return env
	}
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out[sessionTokenEnv] = tok
	return out
}

// ─── DELETE /api/sessions/{id} ────────────────────────────────────────────────

// HandleDeleteSession forwards a kill_session message to the daemon that owns
// the given session, then responds 204. The stop is attributed to the signed-in
// caller (migration 018): recorded before the kill is sent, so the daemon's
// session_ended that follows reads as this stop rather than as the process
// exiting on its own.
func (a *API) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	end := stopEndByBrowser(principal)
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}

	// A private session is answered exactly as an unknown one, before anything
	// about it (its daemon, its state) can be told apart.
	if !a.browserCanSee(r.Context(), principal.Sub, sessionID) {
		writeError(w, http.StatusNotFound, "session not found or daemon not connected")
		return
	}

	daemon := a.hub.FindDaemonForSession(sessionID)
	if daemon == nil {
		// Orphan: the daemon that owned this session is gone. A row already in a
		// terminal/detached state has nothing left to kill — let the user clear
		// it instead of 404ing forever (trial blocker 2). This subsumes the
		// disconnected-cluster case (tear down any Job first) and now also
		// covers error/stopped rows left behind by a daemon that vanished
		// mid-spawn.
		if a.dbPool != nil {
			row, err := db.GetSession(r.Context(), a.dbPool, sessionID)
			// A cluster session still "starting" has no owner until its pod
			// connects — which is exactly when Stop matters most (the pod
			// can't pull its image, can't be scheduled, is stuck cloning).
			// Its Job is the thing to kill.
			startingCluster := err == nil && row != nil && row.Status == "starting" &&
				row.Runtime != nil && *row.Runtime == "cluster" && a.hub.JobManager() != nil
			if err == nil && row != nil &&
				(row.Status == "error" || row.Status == "stopped" || row.Status == "disconnected" || startingCluster) {
				if jm := a.hub.JobManager(); jm != nil && (row.Status == "disconnected" || startingCluster) {
					if err := jm.DeleteSessionJob(sessionID); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
						log.Printf("delete %s: delete job: %v", sessionID, err)
					}
				}
				now := time.Now()
				// A row that already ended (a lost daemon, a failed Job) keeps
				// the reason it ended for; one that was only cut off (a
				// disconnect "error", a resumable cluster session, a start
				// still pending) is ended here, by this caller.
				setSessionStatusEnd(r.Context(), a.hub, a.dbPool, sessionID, "stopped", &now, nil, true, end)
				revokeSessionTokens(r.Context(), a.dbPool, sessionID, "delete: orphaned session")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		writeError(w, http.StatusNotFound, "session not found or daemon not connected")
		return
	}

	msg := protocol.KillSession{
		Type:      "kill_session",
		SessionID: sessionID,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}

	// Record the stop BEFORE the kill leaves: the daemon can answer with
	// session_ended faster than a write after the send would land, and that
	// exit must not be taken for one nobody asked for.
	recorded := false
	if a.dbPool != nil {
		var err error
		if recorded, err = db.RecordSessionEnd(r.Context(), a.dbPool, sessionID, end); err != nil {
			log.Printf("delete %s: record end: %v", sessionID, err)
		}
	}

	select {
	case daemon.send <- data:
	default:
		// The kill never left, so nothing was stopped: take back the
		// attribution this request wrote — only if it is still exactly that
		// one on a session that has still not ended (a compare-and-swap, so
		// an overlapping stop or a real ending is never erased).
		if recorded {
			if _, err := db.RetractSessionEnd(r.Context(), a.dbPool, sessionID, end); err != nil {
				log.Printf("delete %s: retract end: %v", sessionID, err)
			}
		}
		writeError(w, http.StatusServiceUnavailable, "daemon send buffer full")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── POST /api/sessions/{id}/pause ───────────────────────────────────────────

// HandlePauseSession frees a cluster session's pod without ending the session:
// the row goes to "disconnected", the state a lost pod leaves behind, so the
// next message resumes it exactly as it would after an eviction. Whatever the
// agent was in the middle of is cut off; its conversation is not.
func (a *API) HandlePauseSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	if !a.browserCanSee(r.Context(), principal.Sub, sessionID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	jm := a.hub.JobManager()
	if a.dbPool == nil || jm == nil {
		writeError(w, http.StatusConflict, "only cluster sessions can be paused")
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sessionID)
	if err != nil || row == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if row.Runtime == nil || *row.Runtime != "cluster" {
		writeError(w, http.StatusConflict, "only cluster sessions can be paused")
		return
	}
	if row.Status == "disconnected" {
		// Already paused (or its pod already gone): nothing to do.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if row.Status != "running" && row.Status != "idle" && row.Status != "waiting" {
		writeError(w, http.StatusConflict, "session is "+row.Status+" and cannot be paused")
		return
	}
	// Mark it first: the pod's connection drop that follows then finds a
	// session that is already not active, and leaves it as it is.
	setSessionStatus(r.Context(), a.hub, a.dbPool, sessionID, "disconnected", nil, nil, false)
	if err := jm.DeleteSessionJob(sessionID); err != nil { //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
		log.Printf("pause %s: delete job: %v", sessionID, err)
		writeError(w, http.StatusBadGateway, "could not stop the session's pod")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── PATCH /api/sessions/{id} ─────────────────────────────────────────────────

type patchSessionRequest struct {
	Title string `json:"title"`
}

// HandlePatchSession updates the title of the given session.
func (a *API) HandlePatchSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	if !a.browserCanSee(r.Context(), principal.Sub, sessionID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	var req patchSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}

	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	if err := db.UpdateSessionTitle(r.Context(), a.dbPool, sessionID, req.Title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		log.Printf("api patch session title: %v", err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	a.hub.BroadcastTitleChanged(sessionID, req.Title)
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /api/push/vapid-public-key ──────────────────────────────────────────

// HandleGetVapidPublicKey returns the VAPID public key for push subscription.
func (a *API) HandleGetVapidPublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": a.vapidPublicKey})
}

// ─── POST /api/push/subscribe ─────────────────────────────────────────────────

type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// HandlePostPushSubscribe stores a browser push subscription.
func (a *API) HandlePostPushSubscribe(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	var req pushSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Endpoint == "" || req.Keys.P256DH == "" || req.Keys.Auth == "" {
		writeError(w, http.StatusUnprocessableEntity, "endpoint, keys.p256dh, and keys.auth are required")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	if err := db.UpsertPushSubscription(r.Context(), a.dbPool, req.Endpoint, req.Keys.P256DH, req.Keys.Auth); err != nil {
		log.Printf("UpsertPushSubscription: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save subscription")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// recordLaunchEffort stores the effort a session was launched with, so the
// session list and detail can show it before (or without) any engine event
// reporting it. Best-effort; a no-op for no effort.
func recordLaunchEffort(ctx context.Context, pool *pgxpool.Pool, sessionID, effort string) {
	if effort == "" {
		return
	}
	if err := db.UpdateSessionMeta(ctx, pool, sessionID, "", effort); err != nil {
		log.Printf("record launch effort %s: %v", sessionID, err)
	}
}
