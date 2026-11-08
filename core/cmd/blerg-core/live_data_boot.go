package main

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Immutable state is published once. Copied API dependencies and the private
// transport both observe recovery without mutating an issuer used by requests.
type issuerBootState struct {
	issuer  *livedata.Issuer
	epoch   func() int64
	handler http.Handler
}
type issuerBoot struct {
	handler func(*livedata.Issuer) http.Handler
	current atomic.Pointer[issuerBootState]
	deps    api.Deps
	boot    func(context.Context, *api.Deps) (*livedata.Issuer, error)
}

func newIssuerBoot(deps *api.Deps, boot func(context.Context, *api.Deps) (*livedata.Issuer, error)) *issuerBoot {
	r := &issuerBoot{deps: *deps, boot: boot}
	deps.LiveDataAuthorityEpoch = func() int64 {
		s := r.current.Load()
		if s == nil || s.epoch == nil {
			return 0
		}
		return s.epoch()
	}
	r.deps = *deps
	return r
}
func (r *issuerBoot) attempt(ctx context.Context) {
	deps := r.deps
	deps.LiveDataAuthorityEpoch = nil
	issuer, e := r.boot(ctx, &deps)
	if e != nil {
		issuerUnavailable()
		return
	}
	s := &issuerBootState{issuer: issuer, epoch: deps.LiveDataAuthorityEpoch}
	if r.handler != nil {
		s.handler = r.handler(issuer)
	}
	r.current.Store(s)
}
func (r *issuerBoot) retry(ctx context.Context, ticks <-chan time.Time) {
	for r.current.Load() == nil {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			r.attempt(ctx)
		}
	}
}

func configureRecoveringLiveDataIssuer(ctx context.Context, deps *api.Deps, identity *livetls.Identity, pool *pgxpool.Pool) *issuerBoot {
	deps.LiveDataDesktop = os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true"
	deps.LiveDataPosture = liveDataPosture(*deps)
	r := newIssuerBoot(deps, liveDataBootstrap(identity, pool))
	base := *deps
	r.handler = func(issuer *livedata.Issuer) http.Handler { return liveDataPrivateMux(base, identity, issuer) }
	r.attempt(ctx)
	if r.current.Load() == nil {
		go func() { ticker := time.NewTicker(100 * time.Millisecond); defer ticker.Stop(); r.retry(ctx, ticker.C) }()
	}
	return r
}
func (r *issuerBoot) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s := r.current.Load(); s != nil && s.handler != nil {
		s.handler.ServeHTTP(w, req)
		return
	}
	http.Error(w, "Unavailable.", http.StatusServiceUnavailable)
}

func liveDataBootstrap(identity *livetls.Identity, pool *pgxpool.Pool) func(context.Context, *api.Deps) (*livedata.Issuer, error) {
	return func(ctx context.Context, d *api.Deps) (*livedata.Issuer, error) {
		return configureLiveDataIssuer(ctx, d, identity, pool)
	}
}

func (r *issuerBoot) Unavailable() bool {
	s := r.current.Load()
	return s == nil || s.issuer != nil && (s.epoch == nil || s.epoch() == 0)
}
