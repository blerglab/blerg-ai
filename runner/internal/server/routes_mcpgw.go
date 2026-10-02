package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MCPGatewayHandle is a running MCP gateway: the gateway itself (so later features can
// install a proposal sink) and the address its listener is bound to.
type MCPGatewayHandle struct {
	Gateway *mcpgw.Gateway
	Addr    net.Addr
}

// StartMCPGateway starts the MCP gateway (spec 5.5) on its own listener and http.Server,
// separate from the runner's main mux, so it is never reachable through the public
// Ingress and a hostile upstream or a looping agent cannot starve the control plane. It is
// disabled, and returns nil, unless BLERG_RUNNER_MCP_GW_ADDR is set and both the database
// and core (BLERG_CORE_URL plus the internal key) are configured: without them there is
// nothing to authenticate against or fetch credentials from. Sessions with a grant are
// refused elsewhere while it is disabled.
//
// The server stops with ctx. Configuration errors are logged, not fatal: the runner still
// serves everything else.
func StartMCPGateway(ctx context.Context, pool *pgxpool.Pool, coreURL, coreInternalKey string) *MCPGatewayHandle {
	return startMCPGateway(ctx, os.Getenv, pool, coreURL, coreInternalKey)
}

func startMCPGateway(ctx context.Context, env func(string) string, pool *pgxpool.Pool, coreURL, coreInternalKey string) *MCPGatewayHandle {
	addr := env("BLERG_RUNNER_MCP_GW_ADDR")
	if addr == "" {
		log.Println("MCP gateway disabled (BLERG_RUNNER_MCP_GW_ADDR not set)")
		return nil
	}
	if pool == nil || coreURL == "" || coreInternalKey == "" {
		log.Println("MCP gateway disabled: it needs the database and core (BLERG_CORE_URL, BLERG_RUNNER_CORE_INTERNAL_KEY)")
		return nil
	}
	cfg := mcpgw.Config{
		DB:   pool,
		Core: &mcpgw.HTTPCoreClient{BaseURL: coreURL, InternalKey: coreInternalKey},
		Policy: netguard.Policy{
			AllowHTTPHosts:    netguard.ParseHostList(env("BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS")),
			AllowPrivateHosts: netguard.ParseHostList(env("BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS")),
		},
		// Propose-mode tools are frozen into proposals (spec 9). The agent is told the proposal's
		// page on the runner's public address; without BLERG_RUNNER_SELF_URL, only its id.
		Sink: mcpgw.NewProposalSink(pool, env("BLERG_RUNNER_SELF_URL"), 0),
		// The built-in board connection (spec 10.2): core's token exchange and the board's MCP address
		// (BLERG_RUNNER_BOARD_MCP_URL, or BLERG_RUNNER_BOARD_URL + /mcp). Unset: the board is unavailable.
		Board:          &mcpgw.HTTPBoardExchanger{BaseURL: coreURL, InternalKey: coreInternalKey},
		BoardMCPURL:    BoardMCPURLFromEnv(env),
		CallTimeout:    time.Duration(envPositiveInt(env, "BLERG_RUNNER_MCP_CALL_TIMEOUT_SECONDS")) * time.Second,
		MaxConcurrent:  envPositiveInt(env, "BLERG_RUNNER_MCP_MAX_CONCURRENT"),
		MaxResultBytes: envPositiveInt(env, "BLERG_RUNNER_MCP_MAX_RESULT_BYTES"),
	}
	gw := mcpgw.New(cfg)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		log.Printf("MCP gateway disabled: listen on %s: %v", addr, err)
		return nil
	}
	callTimeout := cfg.CallTimeout
	if callTimeout <= 0 {
		callTimeout = mcpgw.DefaultCallTimeout
	}
	// Its own timeouts, unlike the main server (which streams WebSockets): a request is one
	// bounded JSON-RPC exchange, so the whole request and reply get a deadline that covers the
	// per-call timeout plus the upstream handshake.
	srv := &http.Server{
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      callTimeout + 60*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("MCP gateway: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go gw.Run(ctx) // prunes mcp_call_log (90 days) and idle per-grant state
	log.Printf("MCP gateway listening on %s", ln.Addr())
	return &MCPGatewayHandle{Gateway: gw, Addr: ln.Addr()}
}

// envPositiveInt reads a positive integer variable; unset, malformed or non-positive is 0,
// which the gateway reads as "use the default".
func envPositiveInt(env func(string) string, key string) int {
	n, err := strconv.Atoi(env(key))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
