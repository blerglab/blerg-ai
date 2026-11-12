package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// No configuration switch can enable polling in this checkpoint.
const liveDataIssuerEnabled = false

func configureLiveDataIssuer(ctx context.Context, deps *api.Deps, identity *livetls.Identity, pool *pgxpool.Pool) (*livedata.Issuer, error) {
	if pool == nil {
		return nil, livedata.ErrUnavailable
	}
	var history bool
	if e := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM live_data_grants) OR EXISTS(SELECT 1 FROM live_data_proposals) OR EXISTS(SELECT 1 FROM live_data_credential_audit)`).Scan(&history); e != nil {
		return nil, livedata.ErrUnavailable
	}
	var keys *livedata.Keyring
	var epoch func() int64
	var revocations livedata.RevocationAuthority
	if os.Getenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND") == "kubernetes" {
		backend, e := livedata.InClusterAuthority()
		if e != nil {
			return nil, e
		}
		state, e := backend.Start(ctx, !history)
		if e != nil {
			return nil, e
		}
		keys, epoch, revocations = state.Keys, state.Epoch, state
	} else {
		dir := os.Getenv("BLERG_LIVE_DATA_AUTHORITY_DIR")
		if dir == "" {
			return nil, nil
		}
		state, e := livedata.StartAuthority(dir, !history)
		if e != nil {
			return nil, e
		}
		keys, epoch, revocations = state.Keys, state.Epoch, state
	}
	current := epoch()
	if current < 1 {
		return nil, livedata.ErrUnavailable
	}
	checkpoint := livedata.RevocationCheckpoint{Pool: pool, Authority: revocations, Epoch: current}
	start, cancelStart := context.WithTimeout(ctx, 2*time.Second)
	startErr := checkpoint.Start(start)
	cancelStart()
	if e := startErr; e != nil {
		return nil, e
	}
	// Explicit restore fencing invalidates only grants from older epochs.
	// The keyring survives restarts; checkpoint startup invalidates prior approvals.
	if _, e := pool.Exec(ctx, `UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE revoked_at IS NULL AND (scope->>'authority_epoch')::bigint<>$1`, current); e != nil {
		return nil, livedata.ErrUnavailable
	}
	// Start background work only after initialization can no longer fail.
	go checkpoint.Run(ctx)
	// Startup invalidation can reserve a new ticket after Start. Complete it
	// before publishing a ready issuer; human actions still commit locally.
	var ready atomic.Bool
	initializeIssuerReadiness(ctx, checkpoint.Sync, &ready)
	if !ready.Load() {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			retryIssuerReadiness(ctx, checkpoint.Sync, &ready, ticker.C)
		}()
	}
	// Revocation durability also runs when an optional TLS bundle is missing:
	// loss of a live-data certificate must not strand account/connection changes.
	if identity == nil {
		return nil, nil
	}
	var fenced atomic.Bool
	// Human approval transactions use only the boot epoch and a one-way local
	// refusal flag: no Kubernetes network request while holding session locks.
	deps.LiveDataAuthorityEpoch = func() int64 {
		return issuerApprovalEpoch(current, &ready, &fenced)
	}
	freshEpoch := func() int64 {
		next := epoch()
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if checkpoint.Check(check) != nil {
			return 0
		}
		if next == 0 {
			return 0
		}
		if next != current {
			fenced.Store(true)
		}
		if fenced.Load() || !ready.Load() {
			return 0
		}
		return next
	}
	deps.LiveDataDesktop = os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true"
	deps.LiveDataPosture = liveDataPosture(*deps)
	return &livedata.Issuer{Store: livedata.Postgres{Pool: pool}, Keys: keys, Installation: identity.Installation, Runner: identity.Runner, Desktop: os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true", AuthorityEpoch: freshEpoch, Posture: deps.LiveDataPosture}, nil
}
func liveDataPrivateMux(deps api.Deps, identity *livetls.Identity, issuer *livedata.Issuer) *http.ServeMux {
	mux := http.NewServeMux()
	if identity != nil {
		livedata.Transport{Issuer: issuer, Verify: identity.VerifyRequest, Enabled: liveDataIssuerEnabled, Provider: liveDataProvider(deps)}.Register(mux)
	}
	mux.Handle("/", api.NewRouter(deps))
	return mux
}
func issuerUnavailable() {
	failurelog.Default.Event("live_data_issuer_unavailable", slog.LevelWarn, "cause", "issuer_bootstrap_unavailable")
}
