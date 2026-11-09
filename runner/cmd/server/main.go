package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/health"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/contracts/secrets"
	"github.com/blerglab/blerg-ai/contracts/updatenotice"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcp"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

// validateSecrets refuses to boot on placeholder or too-short shared secrets (R4) — a copied
// .env.example must never become a live credential.
func validateSecrets() error {
	for _, c := range []struct {
		name     string
		required bool
	}{
		{"BLERG_RUNNER_DAEMON_TOKEN", true},
		{"BLERG_RUNNER_KEY", false},
		{"BLERG_RUNNER_CORE_INTERNAL_KEY", false},
	} {
		v := os.Getenv(c.name)
		var err error
		if c.required {
			err = secrets.Require(c.name, v)
		} else {
			err = secrets.Check(c.name, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var version = "dev"

// release (the VERSION file) and commit (short git sha) are stamped by the build; empty when not.
var release, commit string

// spaHandler serves static files from dir, falling back to index.html for any
// path that doesn't correspond to a real file — standard SPA routing support.
// index.html is served with Cache-Control: no-cache so browsers always
// re-validate it and pick up new hashed asset filenames after deploys.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		_, err := os.Stat(path) //nolint:gosec // G703: only decides the SPA fallback below; fs.ServeHTTP (not this path) actually serves the file, through http.Dir's own containment
		if os.IsNotExist(err) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html")) //nolint:gosec // G703: dir is the operator-configured web dist root (BLERG_RUNNER_WEB_DIST or the default), not request input; "index.html" is a fixed name
			return
		}
		// Hashed assets (js/css) can be cached aggressively; index.html cannot.
		if filepath.Base(path) == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}

// gzipResponseWriter wraps http.ResponseWriter to write through a gzip encoder.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz io.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

// gzipHandler wraps a handler to serve gzip-compressed responses to clients
// that accept it. Clears Content-Length since the compressed size is unknown.
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer func() { _ = gz.Close() }()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	DaemonToken     string // BLERG_RUNNER_DAEMON_TOKEN
	DatabaseURL     string // BLERG_RUNNER_DATABASE_URL
	Port            string // BLERG_RUNNER_LISTEN_PORT (default "8080")
	GitHubOrg       string // BLERG_RUNNER_GITHUB_ORG
	GitHubToken     string // BLERG_RUNNER_GITHUB_TOKEN
	VapidPublicKey  string // BLERG_RUNNER_VAPID_PUBLIC_KEY
	VapidPrivateKey string // BLERG_RUNNER_VAPID_PRIVATE_KEY
	CoreURL         string // BLERG_CORE_URL
	CoreRegisterKey string // BLERG_CORE_REGISTER_KEY
	CoreInternalKey string // BLERG_RUNNER_CORE_INTERNAL_KEY (core's BLERG_CORE_INTERNAL_KEY)
	RunnerSelfURL   string // BLERG_RUNNER_SELF_URL
}

func loadConfig() Config {
	// Deliberately NOT named BLERG_RUNNER_PORT: Kubernetes auto-injects a
	// Docker-links-style <SERVICE_NAME>_PORT env var (here, literally
	// BLERG_RUNNER_PORT=tcp://<clusterIP>:8080) into every pod for each
	// Service that already exists in the namespace when the pod starts —
	// including the blerg-runner Service itself. That silently clobbers a
	// same-named custom var, so this app-level listen port uses a name that
	// can't collide with k8s's own convention.
	port := os.Getenv("BLERG_RUNNER_LISTEN_PORT")
	if port == "" {
		port = "8080"
	}
	return Config{
		DaemonToken:     os.Getenv("BLERG_RUNNER_DAEMON_TOKEN"),
		DatabaseURL:     os.Getenv("BLERG_RUNNER_DATABASE_URL"),
		Port:            port,
		GitHubOrg:       os.Getenv("BLERG_RUNNER_GITHUB_ORG"),
		GitHubToken:     os.Getenv("BLERG_RUNNER_GITHUB_TOKEN"),
		VapidPublicKey:  os.Getenv("BLERG_RUNNER_VAPID_PUBLIC_KEY"),
		VapidPrivateKey: os.Getenv("BLERG_RUNNER_VAPID_PRIVATE_KEY"),
		CoreURL:         os.Getenv("BLERG_CORE_URL"),
		CoreRegisterKey: os.Getenv("BLERG_CORE_REGISTER_KEY"),
		CoreInternalKey: os.Getenv("BLERG_RUNNER_CORE_INTERNAL_KEY"),
		RunnerSelfURL:   envOrDefault("BLERG_RUNNER_SELF_URL", "http://blerg-runner:8080"),
	}
}

// envOrDefault returns the environment variable's value, or def if unset/empty.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "live-data-renew" {
		if livetls.RunRenew(os.Args[2:]) != nil {
			failurelog.Default.Event("live_data_tls_renewal_unavailable", slog.LevelWarn)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := validateSecrets(); err != nil {
		return err
	}
	cfg := loadConfig()

	ctx := context.Background()
	srv, err := health.Listen(ctx, ":"+cfg.Port)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = srv.Close() }()

	// Connect to Postgres (optional — server degrades gracefully without it).
	var dbPool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		var err error
		dbPool, err = openDatabase(ctx, cfg.DatabaseURL, &srv.Gate)
		if err != nil {
			return fmt.Errorf("connect to database: %w", err)
		}
		defer dbPool.Close()

		log.Println("database connected and migrations applied")
		// Read-only indexes are built without holding writes (db/concurrent_indexes.go), after the
		// server is up; until one exists its read still works.
		go func() {
			// Another runner may hold the build lock, or the build may fail: try again a minute later
			// until it is done (or the server stops).
			for {
				err := db.EnsureConcurrentIndexes(ctx, dbPool)
				if err == nil {
					return
				}
				if !errors.Is(err, db.ErrConcurrentIndexesBusy) {
					log.Printf("concurrent indexes: %v (retrying in a minute)", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Minute):
				}
			}
		}()
		// The live-file unique index (BLG-470, internal/db/live_artifact_index.go): same
		// reasoning as the pair above, but its own retry loop (liveindex.go), since a build
		// failure here can be deterministic (duplicate rows the repair pass could not clear) —
		// retrying that forever, logging every minute, never helps.
		go runLiveArtifactIndexLoop(ctx, func(c context.Context) error { return db.EnsureLiveArtifactIndex(c, dbPool) }, ctxSleep)
		// A one-shot sweep (BLG-470 round 3 #3): removes any ".live-staging-*" file a crash left
		// behind and logs any live row whose bytes on disk no longer match it. Runs once, not in a
		// retry loop — nothing about either check changes on its own between attempts.
		go server.SweepLiveArtifacts(ctx, dbPool)
	} else {
		log.Println("BLERG_RUNNER_DATABASE_URL not set — running without database")
	}

	// A data volume the runner's user cannot write to makes every publish and upload fail with
	// "storage error"; say so once, loudly, at startup. The runner still starts.
	if msg := server.StorageProblem(); msg != "" {
		log.Print(msg)
	}

	hub := server.NewHub()
	hub.SetServerVersion(version)
	hub.SetServerBuild(release, commit)
	// A newer public release, as core's update checker found it (nil without BLERG_CORE_URL).
	if feed := updatenotice.NewCoreFeed(os.Getenv("BLERG_CORE_URL")); feed != nil {
		hub.SetLatestRelease(feed.Latest)
	}
	// The same version the hub reports also goes in the agent manifest served at
	// GET /agents and sent to core on registration.
	server.SetComponentVersion(version)
	server.SetComponentCommit(commit)
	if jm := server.NewJobManagerFromEnv(cfg.GitHubOrg); jm != nil {
		// The shared Hermes provider configuration is read with the desktop
		// daemon's own probe and local rule, so cluster model IDs, names and the
		// local-hardware claim match a workstation's.
		jm.Hermes.Probe = daemon.ProbeHermesModelsConfig
		jm.Hermes.Local = daemon.HermesConfigLocal
		if jm.Hermes.Config != "" {
			log.Println("cluster agent runtime: shared Hermes provider configuration loaded")
		}
		hub.SetJobManager(jm)
		log.Println("cluster agent runtime enabled")
	}

	// webDist defaults to the dev/deploy layout (relative to this binary's
	// own working directory); BLERG_RUNNER_WEB_DIST overrides it for a
	// release-perf run (runner/cmd/release-perf), which never builds into
	// the source checkout's own frontend/dist — only into its own
	// isolated tree under the run's evidence directory.
	webDist := "./frontend/dist"
	if v := os.Getenv("BLERG_RUNNER_WEB_DIST"); v != "" {
		webDist = v
	}

	// The UI package the frontend build packed beside its dist (GET /packages/,
	// advertised as ui.chat_package in /agents and in the registration below). A
	// build without one is the normal case for a workstation install: nothing is
	// served, nothing is advertised.
	if err := server.LoadPackages(webDist + "/packages"); err != nil {
		log.Printf("ui packages: %v", err)
	}

	// What one file and a session's files may weigh (BLERG_RUNNER_MAX_FILE_MB and the two per-session
	// totals); an invalid value keeps the default and is logged.
	server.ConfigureFileLimits(os.Getenv)

	// Register with blerg-core (disabled unless BLERG_CORE_URL + BLERG_CORE_REGISTER_KEY are set).
	go db.RunInsightTimingBackfill(ctx, dbPool)
	go server.StartCoreRegistration(ctx, cfg.CoreURL, cfg.CoreRegisterKey, cfg.RunnerSelfURL, version)

	// Build the GitHub repo lister (nil org → disabled).
	var repoLister *server.RepoLister
	if cfg.GitHubOrg != "" {
		repoLister = server.NewRepoLister(cfg.GitHubOrg, cfg.GitHubToken, hub)
	}

	api := server.NewAPI(hub, dbPool, cfg.DaemonToken, repoLister, cfg.VapidPublicKey)
	// Restore policy before background consumers or HTTP can start sessions.
	api.SetCoreCredentials(cfg.CoreURL, cfg.CoreInternalKey)
	liveTLS := configureLiveDataTLS(api)
	if liveTLS != nil {
		go api.RunLivePolling(ctx, liveTLS.Installation, liveTLS.Runner, os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true", netguard.Policy{AllowHTTPHosts: netguard.ParseHostList(os.Getenv("BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS")), AllowPrivateHosts: netguard.ParseHostList(os.Getenv("BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS"))})
	}

	if liveTLS == nil && (os.Getenv("BLERG_LIVE_DATA_TLS_DIR") != "" || os.Getenv("BLERG_LIVE_DATA_TLS_WARNING") != "") {
		srv.SetWarning("live_data_tls_unavailable")
	}
	if liveTLS != nil {
		srv.SetWarningCheck(func() string { return liveTLS.Warning(time.Now()) })
	}
	if err := api.StartPluginPolicy(ctx); err != nil {
		return fmt.Errorf("marketplace policy: %w", err)
	}
	if jm := hub.JobManager(); jm != nil {
		jm.Overrides = api.SettingsOverrides()
		api.ConfigureClusterWorkspaces()
		// The advisory-lock pools (pod frames, limit resume) are sized from the
		// session cap in force when they are first used.
		db.SetLockPoolSessions(jm.EffectiveMaxSessions)
	}
	// Close out cluster sessions whose Job ended without the pod reporting back
	// (deadline, OOM kill, image pull failure, node loss) so they reach a
	// terminal status and fire their completion webhook. Inert without a
	// cluster runtime or a database; stops with ctx.
	go api.StartClusterJobReconciler(ctx, 0)
	go api.RunBoundAskReconciliation(ctx)
	go api.RunLimitResumes(ctx)
	go api.RunDatasetRetention(ctx)
	go api.RunWorkspaceMembership(ctx)
	go api.RunCommitCapture(ctx)
	// Held idle notices (a turn ended while background work ran) are looked at again every minute:
	// work that never reports its end, or goes stale, does not hold one forever. Stops with ctx.
	go server.RunIdleNoticeChecks(ctx, hub, dbPool, 0)
	go server.RunHeldPushChecks(ctx, dbPool, hub)
	// Own listener; off unless BLERG_RUNNER_MCP_GW_ADDR is set. Session starts reach it (and the
	// address sessions dial, BLERG_RUNNER_MCP_GW_URL) through SetMCPGateway.
	gateway := server.StartMCPGateway(ctx, dbPool, cfg.CoreURL, cfg.CoreInternalKey)
	api.SetMCPGateway(gateway, server.MCPGatewayURLFromEnv(), cfg.CoreURL, cfg.CoreInternalKey)
	// Lifecycle delivery survives gateway disablement; absence never renews a lease.
	if dbPool != nil && cfg.CoreURL != "" && cfg.CoreInternalKey != "" {
		lifecycleCore := &mcpgw.HTTPCoreClient{BaseURL: cfg.CoreURL, InternalKey: cfg.CoreInternalKey, ClusterDeployment: os.Getenv("BLERG_MCP_CLUSTER_DEPLOYMENT") == "true"}
		mcpgw.StartSessionBindingLoop(ctx, dbPool, lifecycleCore, gateway != nil)
	}
	// The cron scheduler, watchdog and sweeper: on only with the database and blerg-core, and it
	// logs why when off. Must follow SetMCPGateway (its start path reads that configuration).
	server.StartCrons(ctx, api, cfg.CoreURL, cfg.CoreInternalKey)
	// Proposal expiry, lost approvals and pruning: independent of crons and of blerg-core.
	server.StartProposalMaintenance(ctx, api)
	go api.RunBlackboardSnapshots(ctx)
	go api.RunBlackboardReleaseCuts(ctx)
	// Delegation requests: expire stale ones, mark starts lost to a restart (every 5 min).
	server.StartDelegationMaintenance(ctx, api)
	if emb := server.NewEmbedderFromEnv(); emb != nil {
		api.SetEmbedder(emb)
		log.Println("knowledge-search embedder enabled (Voyage)")
	}

	mux := http.NewServeMux()

	// Health check endpoint.
	mux.HandleFunc("GET /healthz", healthzHandler())
	mux.HandleFunc("GET /readyz", health.Handler(readinessCheck(dbPool)))

	// WebSocket endpoints.
	mux.HandleFunc("/ws/daemon", hub.ServeDaemon(cfg.DaemonToken, dbPool))
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(dbPool, api.AuthorizeBrowserWS))

	// REST endpoints.
	mux.HandleFunc("GET /api/daemons", api.HandleGetDaemons)
	mux.HandleFunc("PUT /api/daemons/{id}/repos-root", api.HandlePutDaemonReposRoot)
	mux.HandleFunc("GET /api/daemons/{id}/worker-priority", api.HandleWorkerPriority)
	mux.HandleFunc("PUT /api/daemons/{id}/worker-priority", api.HandleWorkerPriority)
	mux.HandleFunc("POST /api/daemons/{id}/engine-check", api.HandleEngineCheck)
	mux.HandleFunc("GET /api/cluster/status", api.HandleGetClusterStatus)
	mux.HandleFunc("GET /api/site-board", api.HandleGetSiteBoard)
	mux.HandleFunc("PUT /api/cluster/settings", api.HandlePutClusterSettings)
	mux.HandleFunc("GET /api/settings/usage-notices", api.HandleUsageNoticeSettings)
	mux.HandleFunc("PUT /api/settings/usage-notices", api.HandleUsageNoticeSettings)
	mux.HandleFunc("GET /api/settings/card-time-budget", api.HandleCardTimeBudgetSettings)
	mux.HandleFunc("PUT /api/settings/card-time-budget", api.HandleCardTimeBudgetSettings)
	mux.HandleFunc("GET /api/insights", api.HandleGetInsights)
	mux.HandleFunc("GET /api/insights/prices", api.HandleGetInsightPrices)
	mux.HandleFunc("PUT /api/insights/prices", api.HandlePutInsightPrice)
	mux.HandleFunc("DELETE /api/insights/prices", api.HandleDeleteInsightPrice)
	// Prometheus metrics: off (404) unless BLERG_RUNNER_METRICS_TOKEN is set; then a scraper presents it as a bearer token.
	api.SetMetricsToken(os.Getenv("BLERG_RUNNER_METRICS_TOKEN"))
	mux.HandleFunc("GET /metrics", api.HandleMetrics)
	mux.HandleFunc("GET /api/me/credentials", api.HandleGetMyCredentials)
	api.RegisterThemeRoutes(mux)
	api.RegisterWhatsNewRoutes(mux)
	mux.HandleFunc("GET /api/me/preferences", api.HandleGetMyPreferences)
	mux.HandleFunc("GET /api/skills/settings", api.HandleSkillSettings)
	mux.HandleFunc("PUT /api/skills/settings", api.HandleSkillSettings)
	mux.HandleFunc("GET /api/sessions/{id}/skills", api.HandleSessionSkills)
	mux.HandleFunc("PUT /api/me/preferences", api.HandlePutMyPreferences)
	mux.HandleFunc("GET /api/sessions", api.HandleGetSessions)
	mux.HandleFunc("POST /api/sessions", api.HandleLaunchSession) // HandlePostSessions plus launch permissions
	mux.HandleFunc("DELETE /api/sessions/{id}", api.HandleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/pause", api.HandlePauseSession)
	mux.HandleFunc("GET /api/sessions/{id}/limit-resume", api.HandleLimitResume)
	mux.HandleFunc("PUT /api/sessions/{id}/limit-resume", api.HandleLimitResume)
	mux.HandleFunc("GET /api/sessions/{id}/plan-nudges", api.HandlePlanNudges)
	mux.HandleFunc("PUT /api/sessions/{id}/plan-nudges", api.HandlePlanNudges)
	mux.HandleFunc("PATCH /api/sessions/{id}", api.HandlePatchSession)
	mux.HandleFunc("GET /api/repos", api.HandleGetRepos)
	api.RegisterWorkspaceDiscoveryRoutes(mux)
	mux.HandleFunc("GET /api/board-repo-choices", api.HandleBoardRepoChoices)
	mux.HandleFunc("GET /api/push/vapid-public-key", api.HandleGetVapidPublicKey)
	mux.HandleFunc("POST /api/push/subscribe", api.HandlePostPushSubscribe)
	mux.HandleFunc("DELETE /api/push/subscribe", api.HandleDeletePushSubscribe)
	mux.HandleFunc("GET /api/sessions/{id}/agent-events", api.HandleGetAgentEvents)
	mux.HandleFunc("GET /api/sessions/{id}/capabilities", api.HandleGetCapabilities)
	mux.HandleFunc("GET /api/sessions/{id}/insights", api.HandleGetSessionInsights)
	api.RegisterTopicRoutes(mux)
	api.RegisterCategoryRoutes(mux)
	api.RegisterLiveRoutes(mux)
	api.RegisterWorklistRoutes(mux)
	api.RegisterDelegationRoutes(mux)
	api.RegisterMCPRoutes(mux)
	// The MCP gateway on the web port too, for sessions on workstation daemons: only grants issued
	// for it. BLERG_RUNNER_MCP_GW_PUBLIC=off (or the gateway off) leaves the prefix a 404.
	api.RegisterMCPGatewayRoutes(mux, server.MCPGatewayPublicFromEnv())
	api.RegisterCronRoutes(mux, cfg.CoreURL, cfg.CoreInternalKey)
	api.RegisterProposalRoutes(mux)
	api.RegisterToolChangeRoutes(mux)
	// Runner contract (blerg-board) — enabled only when BLERG_RUNNER_KEY is set.
	api.SetRunnerKey(os.Getenv("BLERG_RUNNER_KEY"))
	// Additive: also accept blerg-core-issued tokens (aud "blerg-runner") once
	// BLERG_CORE_URL is set, so blerg-core can act as identity authority.
	api.SetCoreAuth(coreauth.New(cfg.CoreURL))
	api.RegisterShellRoutes(mux)
	// P0b/runtime adapters are deliberately absent until their containment gates pass.
	if err := api.StartRuntimeShellChannel(ctx); err != nil {
		log.Printf("shell channel unavailable: %v", err)
	}
	// The per-engine model pickers (GET /api/models/{engine}). One source per
	// engine; an engine with none gets an empty list. Claude's is the public
	// Claude Code catalog, fetched at startup and every 6 h, compiled-in list
	// until then. BLERG_RUNNER_MODEL_CATALOG_URL="" disables fetching
	// (air-gapped installs). Every other engine's list is what connected
	// daemons probe and report (codex, hermes today), served from the hub.
	catalogURL := models.DefaultCatalogURL
	if v, ok := os.LookupEnv("BLERG_RUNNER_MODEL_CATALOG_URL"); ok {
		catalogURL = strings.TrimSpace(v)
	}
	claudeCatalog := models.NewCatalog(catalogURL)
	go claudeCatalog.Run(ctx, models.DefaultRefreshInterval)
	api.SetModelSources(server.NewModelSources(claudeCatalog, hub))
	// Restore account-scoped cluster catalogs without holding up startup or the UI.
	go api.WarmClusterModelCache(ctx)
	// The runner contract plus its public documentation (GET /agents,
	// GET /openapi.json). One registration function, shared with the test that
	// checks openapi.json describes exactly what is wired.
	server.RegisterRunnerContractRoutes(mux, api)
	// The same contract as MCP tools (the manifest's mcp_url), authenticated
	// by the same runner credential.
	mux.Handle("/mcp", mcp.New(api))
	mux.HandleFunc("POST /api/agent/memories", api.HandlePostMemory)
	mux.HandleFunc("DELETE /api/agent/memories", api.HandleDeleteMemory)
	mux.HandleFunc("GET /api/agent/memories", api.HandleGetMemories)
	mux.HandleFunc("POST /api/agent/knowledge-search", api.HandleKnowledgeSearch)
	mux.HandleFunc("POST /api/mockups", api.HandlePostMockup)
	mux.HandleFunc("GET /m/", api.HandleServeMockup)
	api.RegisterArtifactRoutes(mux)
	mux.HandleFunc("POST /api/screenshots", api.HandlePostScreenshot)
	mux.HandleFunc("GET /s/", api.HandleServeScreenshot)
	mux.HandleFunc("POST /api/agent-config", api.HandlePostAgentConfig)
	mux.HandleFunc("GET /api/agent-config", api.HandleGetAgentConfig)
	mux.HandleFunc("POST /api/session/credentials/access", api.HandleManagedCredentialAccess)
	mux.HandleFunc("GET /api/session/plugin-marketplace-policy", api.HandlePodPluginPolicy)
	mux.HandleFunc("POST /api/messages", api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/recover", api.HandleRecoverQuestions)
	mux.HandleFunc("GET /api/messages", api.HandleGetMessages)
	mux.HandleFunc("GET /api/messages/{id}/answer", api.HandleGetMessageAnswer)
	mux.HandleFunc("POST /api/messages/{id}/reply", api.HandlePostMessageReply)
	mux.HandleFunc("GET /api/messages/{id}/thread", api.HandleGetQuestionThread)
	mux.HandleFunc("POST /api/messages/{id}/thread", api.HandlePostQuestionThread)
	mux.HandleFunc("POST /api/messages/{id}/expire", api.HandlePostMessageExpire)
	mux.HandleFunc("POST /api/messages/{id}/dismiss", api.HandlePostMessageDismiss)
	mux.HandleFunc("GET /api/boards", api.HandleGetBoards)
	mux.HandleFunc("POST /api/boards", api.HandlePostBoards)
	mux.HandleFunc("DELETE /api/boards/{id}", api.HandleDeleteBoard)
	mux.HandleFunc("GET /api/boards/{id}", api.HandleGetBoard)
	mux.HandleFunc("GET /api/boards/{id}/order", api.HandleGetBoardOrder)
	mux.HandleFunc("POST /api/boards/{id}/columns", api.HandlePostColumn)
	mux.HandleFunc("PATCH /api/columns/{id}", api.HandlePatchColumn)
	mux.HandleFunc("DELETE /api/columns/{id}", api.HandleDeleteColumn)
	mux.HandleFunc("POST /api/boards/{id}/tickets", api.HandlePostTicket)
	mux.HandleFunc("GET /api/boards/{id}/tickets", api.HandleListTickets)
	mux.HandleFunc("GET /api/tickets/{id}", api.HandleGetTicket)
	mux.HandleFunc("PATCH /api/tickets/{id}", api.HandlePatchTicket)
	mux.HandleFunc("POST /api/tickets/{id}/split", api.HandleSplitTicket)
	mux.HandleFunc("POST /api/tickets/{id}/archive", api.HandleArchiveTicket)
	mux.HandleFunc("POST /api/tickets/{id}/dependencies", api.HandleAddDependency)
	mux.HandleFunc("DELETE /api/tickets/{id}/dependencies/{depId}", api.HandleRemoveDependency)
	mux.HandleFunc("GET /api/tickets/{id}/events", api.HandleGetTicketEvents)
	mux.HandleFunc("GET /api/boards/{id}/archive", api.HandleGetBoardArchive)

	// Serve frontend static files (must be last for catch-all behavior).
	mux.Handle("/", gzipHandler(spaHandler(webDist)))

	if liveTLS != nil {
		privateTLS, e := liveTLS.Listen(ctx, ":8443", mux, func() { srv.SetWarning("live_data_tls_unavailable") })
		if e != nil {
			srv.SetWarning("live_data_tls_unavailable")
			failurelog.Default.Event("live_data_tls_listener_unavailable", slog.LevelWarn)
		} else {
			defer func() { _ = privateTLS.Close() }()
		}
	}
	srv.SetHandler(server.ServerTimingMiddleware(failurelog.Default.HTTP("runner", mux)))
	log.Printf("blerg-runner-server listening on %s", ":"+cfg.Port)
	// Deliberately no SIGTERM handling, drain or graceful shutdown (unlike core and the board):
	// the runner keeps its routing in memory (daemon, session and browser sockets, ask and
	// delegation waiters), so during a rolling update the old pod must exit at once. Its sockets
	// drop, everything reconnects to the one pod left, and its disconnect cleanup never runs
	// after the new pod has recorded the reconnects. A drain would keep two pods each holding
	// half the connections. Move routing state out of process before adding one
	// (install/k8s/runner-deployment.yaml).
	return srv.Wait()
}

// healthzHandler answers 200 "ok", and — when BLERG_CORE_PUBLIC_URL is set — allows core's
// landing page to probe it cross-origin.
func healthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if o := strings.TrimRight(os.Getenv("BLERG_CORE_PUBLIC_URL"), "/"); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
