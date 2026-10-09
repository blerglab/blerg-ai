package main

import (
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/secrets"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcp"
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

// spaHandler serves static files from dir, falling back to index.html for any
// path that doesn't correspond to a real file — standard SPA routing support.
// index.html is served with Cache-Control: no-cache so browsers always
// re-validate it and pick up new hashed asset filenames after deploys.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
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
	if err := validateSecrets(); err != nil {
		log.Fatalf("%v", err)
	}
	cfg := loadConfig()

	ctx := context.Background()

	// Connect to Postgres (optional — server degrades gracefully without it).
	var dbPool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		var err error
		dbPool, err = pgxpool.New(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("connect to database: %v", err)
		}
		defer dbPool.Close()

		if err := db.RunMigrations(ctx, dbPool); err != nil {
			log.Fatalf("run migrations: %v", err) //nolint:gocritic // exitAfterDefer: the process is exiting, the pool goes with it
		}
		log.Println("database connected and migrations applied")
	} else {
		log.Println("BLERG_RUNNER_DATABASE_URL not set — running without database")
	}

	hub := server.NewHub()
	hub.SetServerVersion(version)
	// The same version the hub reports also goes in the agent manifest served at
	// GET /agents and sent to core on registration.
	server.SetComponentVersion(version)
	if jm := server.NewJobManagerFromEnv(cfg.GitHubOrg); jm != nil {
		hub.SetJobManager(jm)
		log.Println("cluster agent runtime enabled")
	}

	// The UI package the frontend build packed beside its dist (GET /packages/,
	// advertised as ui.chat_package in /agents and in the registration below). A
	// build without one is the normal case for a workstation install: nothing is
	// served, nothing is advertised.
	if err := server.LoadPackages("./frontend/dist/packages"); err != nil {
		log.Printf("ui packages: %v", err)
	}

	// Register with blerg-core (disabled unless BLERG_CORE_URL + BLERG_CORE_REGISTER_KEY are set).
	go server.StartCoreRegistration(ctx, cfg.CoreURL, cfg.CoreRegisterKey, cfg.RunnerSelfURL, version)

	// Build the GitHub repo lister (nil org → disabled).
	var repoLister *server.RepoLister
	if cfg.GitHubOrg != "" {
		repoLister = server.NewRepoLister(cfg.GitHubOrg, cfg.GitHubToken, hub)
	}

	api := server.NewAPI(hub, dbPool, cfg.DaemonToken, repoLister, cfg.VapidPublicKey)
	if jm := hub.JobManager(); jm != nil {
		jm.Overrides = api.SettingsOverrides()
	}
	// Close out cluster sessions whose Job ended without the pod reporting back
	// (deadline, OOM kill, image pull failure, node loss) so they reach a
	// terminal status and fire their completion webhook. Inert without a
	// cluster runtime or a database; stops with ctx.
	go api.StartClusterJobReconciler(ctx, 0)
	// Own listener; off unless BLERG_RUNNER_MCP_GW_ADDR is set. Session starts reach it (and the
	// address sessions dial, BLERG_RUNNER_MCP_GW_URL) through SetMCPGateway.
	api.SetMCPGateway(server.StartMCPGateway(ctx, dbPool, cfg.CoreURL, cfg.CoreInternalKey), server.MCPGatewayURLFromEnv(), cfg.CoreURL, cfg.CoreInternalKey)
	// The cron scheduler, watchdog and sweeper: on only with the database and blerg-core, and it
	// logs why when off. Must follow SetMCPGateway (its start path reads that configuration).
	server.StartCrons(ctx, api, cfg.CoreURL, cfg.CoreInternalKey)
	// Proposal expiry, lost approvals and pruning: independent of crons and of blerg-core.
	server.StartProposalMaintenance(ctx, api)
	if emb := server.NewEmbedderFromEnv(); emb != nil {
		api.SetEmbedder(emb)
		log.Println("knowledge-search embedder enabled (Voyage)")
	}

	mux := http.NewServeMux()

	// Health check endpoint.
	mux.HandleFunc("GET /healthz", healthzHandler())

	// WebSocket endpoints.
	mux.HandleFunc("/ws/daemon", hub.ServeDaemon(cfg.DaemonToken, dbPool))
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(dbPool, api.AuthorizeBrowserWS))

	// REST endpoints.
	mux.HandleFunc("GET /api/daemons", api.HandleGetDaemons)
	mux.HandleFunc("PUT /api/daemons/{id}/repos-root", api.HandlePutDaemonReposRoot)
	mux.HandleFunc("GET /api/cluster/status", api.HandleGetClusterStatus)
	mux.HandleFunc("PUT /api/cluster/settings", api.HandlePutClusterSettings)
	mux.HandleFunc("GET /api/insights", api.HandleGetInsights)
	mux.HandleFunc("GET /api/insights/prices", api.HandleGetInsightPrices)
	mux.HandleFunc("PUT /api/insights/prices", api.HandlePutInsightPrice)
	mux.HandleFunc("DELETE /api/insights/prices", api.HandleDeleteInsightPrice)
	// Prometheus metrics: off (404) unless BLERG_RUNNER_METRICS_TOKEN is set; then a scraper presents it as a bearer token.
	api.SetMetricsToken(os.Getenv("BLERG_RUNNER_METRICS_TOKEN"))
	mux.HandleFunc("GET /metrics", api.HandleMetrics)
	mux.HandleFunc("GET /api/me/credentials", api.HandleGetMyCredentials)
	mux.HandleFunc("GET /api/sessions", api.HandleGetSessions)
	mux.HandleFunc("POST /api/sessions", api.HandlePostSessions)
	mux.HandleFunc("DELETE /api/sessions/{id}", api.HandleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/pause", api.HandlePauseSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", api.HandlePatchSession)
	mux.HandleFunc("GET /api/repos", api.HandleGetRepos)
	mux.HandleFunc("GET /api/push/vapid-public-key", api.HandleGetVapidPublicKey)
	mux.HandleFunc("POST /api/push/subscribe", api.HandlePostPushSubscribe)
	mux.HandleFunc("GET /api/sessions/{id}/agent-events", api.HandleGetAgentEvents)
	mux.HandleFunc("GET /api/sessions/{id}/capabilities", api.HandleGetCapabilities)
	api.RegisterMCPRoutes(mux)
	api.RegisterCronRoutes(mux, cfg.CoreURL, cfg.CoreInternalKey)
	api.RegisterProposalRoutes(mux)
	// Runner contract (blerg-board) — enabled only when BLERG_RUNNER_KEY is set.
	api.SetRunnerKey(os.Getenv("BLERG_RUNNER_KEY"))
	// Additive: also accept blerg-core-issued tokens (aud "blerg-runner") once
	// BLERG_CORE_URL is set, so blerg-core can act as identity authority.
	api.SetCoreAuth(coreauth.New(cfg.CoreURL))
	// Lets GET /api/me/credentials ask core which personal credentials the
	// caller has; inert unless both env vars are set.
	api.SetCoreCredentials(cfg.CoreURL, cfg.CoreInternalKey)
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
	mux.HandleFunc("POST /api/agent/rules", api.HandlePostRule)
	mux.HandleFunc("GET /api/agent/rules", api.HandleGetRules)
	mux.HandleFunc("PATCH /api/agent/rules/{id}", api.HandlePatchRule)
	mux.HandleFunc("DELETE /api/agent/rules/{id}", api.HandleDeleteRule)
	mux.HandleFunc("POST /api/mockups", api.HandlePostMockup)
	mux.HandleFunc("GET /m/", api.HandleServeMockup)
	api.RegisterArtifactRoutes(mux)
	mux.HandleFunc("POST /api/screenshots", api.HandlePostScreenshot)
	mux.HandleFunc("GET /s/", api.HandleServeScreenshot)
	mux.HandleFunc("POST /api/agent-config", api.HandlePostAgentConfig)
	mux.HandleFunc("GET /api/agent-config", api.HandleGetAgentConfig)
	mux.HandleFunc("POST /api/messages", api.HandlePostMessages)
	mux.HandleFunc("GET /api/messages", api.HandleGetMessages)
	mux.HandleFunc("GET /api/messages/{id}/answer", api.HandleGetMessageAnswer)
	mux.HandleFunc("POST /api/messages/{id}/reply", api.HandlePostMessageReply)
	mux.HandleFunc("POST /api/messages/{id}/expire", api.HandlePostMessageExpire)
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
	mux.HandleFunc("POST /api/boards/{id}/assist", api.HandlePostAssist)

	// Serve frontend static files (must be last for catch-all behavior).
	mux.Handle("/", gzipHandler(spaHandler("./frontend/dist")))

	addr := ":" + cfg.Port
	log.Printf("blerg-runner-server listening on %s", addr)
	// No overall read/write timeout: the mux serves long-lived WebSocket and
	// streaming responses. Header and idle timeouts bound slow-loris clients.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
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
