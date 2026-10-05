// blerg-board-server: the blerg-board service — REST + MCP + WS + SPA in one binary.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/corecred"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/gate"
	"github.com/blerglab/blerg-ai/board/internal/localinfer"
	"github.com/blerglab/blerg-ai/board/internal/mcp"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/logsafe"
	"github.com/blerglab/blerg-ai/contracts/secrets"
)

// validateSecrets refuses to boot on placeholder or too-short shared secrets (R4) — a copied
// .env.example must never become a live credential.
func validateSecrets() error {
	for _, c := range []struct {
		name     string
		required bool
	}{
		{"BLERG_BOARD_SERVICE_KEY", true},
		{"BLERG_RUNNER_KEY", false},
		{"BLERG_BOARD_CORE_INTERNAL_KEY", false},
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

// runnerRuntimeFromEnv reads BLERG_BOARD_RUNNER_RUNTIME, the runtime the board
// asks the runner for on every start ("" = the runner's default).
func runnerRuntimeFromEnv() (string, error) {
	return runner.ParseRuntime(os.Getenv("BLERG_BOARD_RUNNER_RUNTIME"))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// version is stamped by the build (git describe / VERSION); "dev" otherwise.
var version = "dev"

func main() {
	logsafe.Install()
	if err := validateSecrets(); err != nil {
		log.Fatalf("%v", err)
	}
	runnerRuntime, err := runnerRuntimeFromEnv()
	if err != nil {
		log.Fatalf("%v", err)
	}

	ctx := context.Background()

	// The build version board reports in its agent manifest (GET /agents) and
	// in what it registers with core. Set before either can be read.
	api.SetComponentVersion(version)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL required")
	}
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	// No deferred pool.Close: main only ever leaves through log.Fatal, which
	// exits without running defers; the process exit releases the connections.

	// Automation tokens are encrypted at rest under BLERG_BOARD_SECRET_KEY.
	// Fail closed: a bad key, or tokens with no key, refuse to boot; plaintext
	// rows from before encryption are sealed here, once.
	sealed, err := db.InitAutomationEncryption(ctx, pool, os.Getenv(db.SecretKeyEnv))
	if err != nil {
		log.Fatalf("automation token encryption: %v", err)
	}
	switch {
	case db.AutomationKeyConfigured():
		log.Printf("automation tokens: encrypted at rest (%d plaintext token(s) sealed at boot)", sealed)
	default:
		log.Printf("automation tokens: %s is not set — boards cannot store an automation token until it is", db.SecretKeyEnv)
	}

	a := auth.New(pool, os.Getenv("BLERG_BOARD_SERVICE_KEY"))
	if err := a.EnsureServiceToken(ctx); err != nil {
		log.Fatalf("service token: %v", err)
	}

	// blerg-core integration (control plane): makes core the identity/
	// discovery authority for blerg-board. Both pieces degrade to absent
	// when unset — board keeps working standalone.
	//   - BLERG_CORE_URL alone: verify core-issued bearer tokens (additive,
	//     tried after board's own native auth paths).
	//   - BLERG_CORE_URL + BLERG_CORE_REGISTER_KEY: also self-register (and
	//     heartbeat) into core's component directory.
	if coreURL := os.Getenv("BLERG_CORE_URL"); coreURL != "" {
		core := coreauth.NewClient(coreURL)
		core.Start(ctx)
		a.SetCore(core)
		log.Printf("blerg-core: verifying bearer tokens against %s", coreURL)

		if registerKey := os.Getenv("BLERG_CORE_REGISTER_KEY"); registerKey != "" {
			selfURL := env("BLERG_BOARD_SELF_URL", env("BLERG_BOARD_PUBLIC_URL", "http://blerg-board:8080"))
			// The full manifest entry, not just a name and a base URL: core's
			// aggregate /agents documents board from exactly this payload.
			coreauth.StartRegistration(ctx, coreURL, registerKey, api.BoardManifest(selfURL))
			log.Printf("blerg-core: registering as %q with blerg-core at %s", selfURL, coreURL)
		}
	}

	// Local inference (an OpenAI-compatible endpoint) is read once here and
	// shared by every feature that offloads to it — the gate is only the
	// first. BLERG_BOARD_INFER_URL/BLERG_BOARD_INFER_MODEL are canonical; GATE_OPENAI_*
	// is read as a fallback alias so existing deployments keep working
	// untouched (see docs/CONFIG.md).
	infer := localinfer.NewFromEnv()
	if infer.Configured() {
		log.Printf("local inference: %s (chat model %q)", infer.BaseURL(), infer.ChatModel())
	}

	backends, tiebreak, err := gateBackends(os.Getenv, infer, a.Core())
	if err != nil {
		log.Fatalf("admission gate: %v", err)
	}
	var g *gate.Gate
	if len(backends) > 0 {
		g = gate.New(pool, backends, tiebreak)
		log.Printf("admission gate: %d backend(s)", len(backends))
	} else {
		log.Printf("admission gate: no backends configured — gated boards fall back to gate_on_unavailable policy")
		g = gate.New(pool, nil, nil)
	}

	apiSrv := api.New(pool, a, g)
	if dir := os.Getenv("BLERG_BOARD_PROMPTS_DIR"); dir != "" {
		if err := apiSrv.LoadPrompts(dir); err != nil {
			log.Fatalf("prompts: %v", err)
		}
		log.Printf("prompts: overrides loaded from %s", dir)
	}
	apiSrv.StartHeldSweep(ctx)
	apiSrv.StartStaleSweep(ctx)

	// Runner: blerg-runner driver, enabled when BLERG_RUNNER_URL/KEY are set.
	if runnerURL, runnerKey := os.Getenv("BLERG_RUNNER_URL"), os.Getenv("BLERG_RUNNER_KEY"); runnerURL != "" && runnerKey != "" {
		driver := runner.NewBlergRunner(runnerURL, runnerKey, os.Getenv("RUNNER_GIT_BASE"))
		driver.SetRuntime(runnerRuntime)
		apiSrv.SetRunner(api.RunnerConfig{
			Driver:           driver,
			PublicURL:        env("BLERG_BOARD_PUBLIC_URL", "https://blerg-board.example.com"),
			AgentURL:         env("BLERG_BOARD_AGENT_URL", "http://blerg-board.blerg-board.svc"),
			UIBase:           os.Getenv("RUNNER_UI_BASE"),
			InfraDocsURL:     os.Getenv("INFRA_DOCS_URL"),
			InfraDocsNote:    os.Getenv("INFRA_DOCS_NOTE"),
			GitCredentialEnv: os.Getenv("RUNNER_GIT_CREDENTIAL_ENV"),
		})
		apiSrv.StartRunnerIngest(ctx)
		apiSrv.StartBoardRuns(ctx)
		apiSrv.StartStandingAgents(ctx)
		log.Printf("runner: blerg-runner driver at %s", runnerURL)
	}

	mux := http.NewServeMux()
	// Discovery first: GET /agents and GET /openapi.json, both public.
	api.RegisterBoardContractRoutes(mux, apiSrv)
	apiSrv.Routes(mux)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
	})
	mux.Handle("/mcp", mcp.New(apiSrv))

	// SPA: serve web/dist with an index.html fallback for client routes.
	webDir := env("BLERG_BOARD_WEB_DIR", "web/dist")
	fs := http.FileServer(http.Dir(webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(webDir, "index.html"))
	})
	mux.HandleFunc("GET /healthz", healthzHandler(pool))

	addr := env("BLERG_BOARD_ADDR", ":8080")
	log.Printf("blerg-board-server listening on %s", addr)
	// ReadHeaderTimeout bounds how long a client may take to send its request
	// headers. There is deliberately no WriteTimeout: the event streams are
	// long-lived responses.
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// gateAccountAudiences are the agent-token audiences BLERG_BOARD_GATE_ACCOUNT_TOKEN
// may carry. Any of core's presets proves who the token belongs to and that it
// is live, which is all the gate needs; the least-privileged ("platform",
// aud blerg-core) is the one to recommend.
var gateAccountAudiences = []string{"blerg-core", "blerg-board", "blerg-runner"}

// gateBackends picks the admission gate's curator backends (ordered) and its
// tiebreak adjudicator from the environment.
//
// Two modes, never mixed:
//
//   - Account mode (BLERG_BOARD_GATE_ACCOUNT_TOKEN set): one backend on that
//     account's own connected credential, for BLERG_BOARD_GATE_ENGINE
//     (claude | hermes), fetched from blerg-core's vault. It is also the
//     tiebreak. The operator's ANTHROPIC_API_KEY and BLERG_BOARD_INFER_URL are
//     NOT consulted — once someone has opted in, a failed fetch makes the gate
//     unavailable (gate_on_unavailable applies) rather than silently running
//     on a different identity's credential. Missing wiring (engine, core URL,
//     internal key) refuses boot.
//   - Legacy mode (unset): BLERG_BOARD_GATE_BACKENDS="openai,claude" over the
//     operator's local inference endpoint and ANTHROPIC_API_KEY, as before.
func gateBackends(getenv func(string) string, infer *localinfer.Client, core *coreauth.Client) ([]gate.Backend, gate.Backend, error) {
	if token := strings.TrimSpace(getenv("BLERG_BOARD_GATE_ACCOUNT_TOKEN")); token != "" {
		engine, err := gate.ParseGateEngine(getenv("BLERG_BOARD_GATE_ENGINE"))
		if err != nil {
			return nil, nil, err
		}
		coreURL, internalKey := getenv("BLERG_CORE_URL"), getenv("BLERG_BOARD_CORE_INTERNAL_KEY")
		if core == nil || coreURL == "" {
			return nil, nil, errors.New("BLERG_BOARD_GATE_ACCOUNT_TOKEN needs BLERG_CORE_URL (the token is a blerg-core agent token)")
		}
		if internalKey == "" {
			return nil, nil, errors.New("BLERG_BOARD_GATE_ACCOUNT_TOKEN needs BLERG_BOARD_CORE_INTERNAL_KEY " +
				"(core's BLERG_CORE_INTERNAL_KEY) to fetch the account's credential")
		}
		model := getenv("BLERG_BOARD_GATE_MODEL")
		if model == "" && engine == gate.GateEngineClaude {
			model = getenv("BLERG_BOARD_CLAUDE_MODEL")
		}
		fetcher := corecred.New(coreURL, internalKey, nil)
		b := gate.NewPersonalBackend(gate.PersonalConfig{
			Engine: engine, Token: token, Model: model,
			Verify: func(raw string) (coreauth.AgentToken, error) {
				var firstErr error
				for _, aud := range gateAccountAudiences {
					t, err := core.VerifyAgentToken(raw, aud)
					if err == nil {
						return t, nil
					}
					if firstErr == nil || !errors.Is(err, identity.ErrWrongAudience) {
						firstErr = err
					}
				}
				return coreauth.AgentToken{}, firstErr
			},
			Fetch: fetcher.Fetch,
		})
		log.Printf("admission gate: account mode — %s on the credential of BLERG_BOARD_GATE_ACCOUNT_TOKEN's owner "+
			"(operator ANTHROPIC_API_KEY / local inference are not used by the gate)", engine)
		return []gate.Backend{b}, b, nil
	}
	if getenv("BLERG_BOARD_GATE_ENGINE") != "" {
		return nil, nil, errors.New("BLERG_BOARD_GATE_ENGINE is set but BLERG_BOARD_GATE_ACCOUNT_TOKEN is not — " +
			"set both to run the gate on a person's own credential, or neither")
	}

	// Legacy: BLERG_BOARD_GATE_BACKENDS="openai,claude" (ordered). The
	// OpenAI-compatible backend is default-first (local inference, per-write
	// cost ~0); the Claude API is the tiebreak adjudicator (stronger model).
	var backends []gate.Backend
	var tiebreak gate.Backend
	claudeKey := getenv("ANTHROPIC_API_KEY")
	gateOrder := getenv("BLERG_BOARD_GATE_BACKENDS")
	if gateOrder == "" {
		gateOrder = "openai,claude"
	}
	for _, name := range strings.Split(gateOrder, ",") {
		switch strings.TrimSpace(name) {
		case "openai":
			if infer.Configured() {
				backends = append(backends, gate.NewOpenAIBackend(infer, ""))
			}
		case "claude":
			if claudeKey != "" {
				backends = append(backends, gate.NewClaudeBackend(claudeKey, getenv("BLERG_BOARD_CLAUDE_MODEL")))
			}
		}
	}
	if claudeKey != "" {
		tiebreak = gate.NewClaudeBackend(claudeKey, getenv("BLERG_BOARD_CLAUDE_MODEL"))
	}
	return backends, tiebreak, nil
}

type pinger interface{ Ping(context.Context) error }

// healthzHandler answers 200 "ok" when Postgres is reachable, 503 otherwise, and — when
// BLERG_CORE_PUBLIC_URL is set — allows core's landing page to probe it cross-origin.
func healthzHandler(p pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := strings.TrimRight(os.Getenv("BLERG_CORE_PUBLIC_URL"), "/"); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
		}
		if err := p.Ping(r.Context()); err != nil {
			http.Error(w, "db unreachable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}
}
