package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func TestLiveDataBootOutageRetriesAndRecovers(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deps := api.Deps{}
	boot := func(_ context.Context, d *api.Deps) (*livedata.Issuer, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New("fixture authority unavailable")
		}
		d.LiveDataAuthorityEpoch = func() int64 { return 7 }
		return &livedata.Issuer{}, nil
	}
	r := newIssuerBoot(&deps, boot)
	r.handler = func(*livedata.Issuer) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	r.attempt(ctx)
	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, httptest.NewRequest(http.MethodPost, "/", nil))
	if denied.Code != http.StatusServiceUnavailable || !r.Unavailable() {
		t.Fatal("boot outage did not refuse transport")
	}
	if r.current.Load() != nil || deps.LiveDataAuthorityEpoch() != 0 {
		t.Fatal("outage admitted issuer")
	}
	ticks := make(chan time.Time, 2)
	ticks <- time.Time{}
	ticks <- time.Time{}
	r.retry(ctx, ticks)
	recovered := httptest.NewRecorder()
	r.ServeHTTP(recovered, httptest.NewRequest(http.MethodPost, "/", nil))
	if recovered.Code != http.StatusNoContent || r.Unavailable() {
		t.Fatal("recovered issuer was not published to transport")
	}
	if r.current.Load() == nil || calls.Load() != 3 || deps.LiveDataAuthorityEpoch() != 7 {
		t.Fatal("boot authority outage never recovered", calls.Load())
	}
}

func TestLiveDataBootRealAuthorityOutageRecovers(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	root := t.TempDir()
	if _, e := livedata.StartAuthority(root, true); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, "authority.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, "authority.json")); e != nil {
		t.Fatal(e)
	}
	tlsRoot := t.TempDir()
	if e = livetls.Provision(tlsRoot); e != nil {
		t.Fatal(e)
	}
	identity, e := livetls.Load(filepath.Join(tlsRoot, "core"), "core")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND", "")
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", root)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deps := api.Deps{}
	r := newIssuerBoot(&deps, liveDataBootstrap(identity, pool))
	r.attempt(ctx)
	if r.current.Load() != nil || deps.LiveDataAuthorityEpoch() != 0 {
		t.Fatal("outage admitted authority")
	}
	if e = os.WriteFile(filepath.Join(root, "authority.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	r.retry(ctx, ticks)
	if r.current.Load() == nil || deps.LiveDataAuthorityEpoch() != 1 {
		t.Fatal("boot outage never recovered")
	}
	if _, e = r.current.Load().issuer.PublicKeys(ctx); e != nil {
		t.Fatal("recovered boot had no usable issuer", e)
	}
}

func TestLiveDataBootCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deps := api.Deps{}
	r := newIssuerBoot(&deps, func(context.Context, *api.Deps) (*livedata.Issuer, error) {
		t.Error("canceled boot retried")
		return nil, errors.New("fixture")
	})
	r.retry(ctx, make(chan time.Time))
	if !r.Unavailable() {
		t.Fatal("canceled boot became ready")
	}
}
