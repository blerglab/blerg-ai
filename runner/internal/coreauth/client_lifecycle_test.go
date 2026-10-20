package coreauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForCoreAuthLifecycle(t *testing.T, description string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not complete", description)
	}
}

func TestClientRefreshesRevocationsAndStopsWithOwningContext(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	var requestStarts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks":
			_ = json.NewEncoder(w).Encode(map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)})
		case "/revocations":
			entries := []revocationEntry{}
			if revoked.Load() {
				entries = append(entries, revocationEntry{Kind: "sub", Value: "fixture-subject"})
			}
			_ = json.NewEncoder(w).Encode(entries)
		default:
			t.Error("unexpected Core request")
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	// Observe client initiation, not when an already-sent request's server
	// handler is eventually scheduled after cancellation.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { requestStarts.Add(1) }})
	c := newClient(ctx, srv.URL, time.Millisecond, time.Millisecond, httpTimeout)
	t.Cleanup(c.Close)
	if c.RevokedFor("", "", "fixture-subject", "", 0) {
		t.Fatal("initial empty revocations revoked subject")
	}
	revoked.Store(true)
	deadline := time.Now().Add(15 * time.Second)
	for !c.RevokedFor("", "", "fixture-subject", "", 0) {
		if time.Now().After(deadline) {
			t.Fatal("periodic revocation refresh did not revoke subject")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	joined := make(chan struct{})
	go func() { c.Close(); close(joined) }()
	waitForCoreAuthLifecycle(t, "cancelled refresh loops", joined)
	// Joining is the proof that a later timer cannot send to a reused receiver.
	// Cache reads retain the real revocation verdict after shutdown.
	before := requestStarts.Load()
	if !c.RevokedFor("", "", "fixture-subject", "", 0) {
		t.Fatal("shutdown lost cached revocation")
	}
	_ = c.KeySet()
	c.Close() // shutdown is idempotent
	if requestStarts.Load() != before {
		t.Fatal("request started after refresh loops joined")
	}
}

func TestClientCloseCancelsInflightRefreshAndJoinsBothPollers(t *testing.T) {
	var requests [2]atomic.Int32
	entered := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	cancelled := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := 0
		if r.URL.Path == "/revocations" {
			index = 1
		}
		if requests[index].Add(1) == 1 {
			if index == 0 {
				_, _ = w.Write([]byte(`{}`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
			return
		}
		close(entered[index])
		select {
		case <-r.Context().Done():
			close(cancelled[index])
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)
	// Cancellation must finish the request; its hypothetical HTTP timeout
	// is deliberately longer than the completion watchdog.
	c := newClient(t.Context(), srv.URL, time.Millisecond, time.Millisecond, time.Hour)
	// Always release handlers before a red regression attempts its final join.
	t.Cleanup(func() { unblock(); c.Close() })
	waitForCoreAuthLifecycle(t, "JWKS refresh entry", entered[0])
	waitForCoreAuthLifecycle(t, "revocation refresh entry", entered[1])
	joined := make(chan struct{})
	go func() { c.Close(); close(joined) }()
	waitForCoreAuthLifecycle(t, "JWKS request cancellation", cancelled[0])
	waitForCoreAuthLifecycle(t, "revocation request cancellation", cancelled[1])
	waitForCoreAuthLifecycle(t, "both refresh loops joining", joined)
	if requests[0].Load() != 2 || requests[1].Load() != 2 {
		t.Fatalf("requests=%d,%d; want only initial and one in-flight refresh", requests[0].Load(), requests[1].Load())
	}
}

func TestClientCloseWaitsForRunningRefreshCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c := &Client{cancel: cancel}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	c.refreshes.Add(1)
	go c.loop(ctx, time.Millisecond, func(context.Context) { close(entered); <-release })
	t.Cleanup(func() { unblock(); c.Close() })
	waitForCoreAuthLifecycle(t, "refresh callback entry", entered)
	joined := make(chan struct{})
	go func() { c.Close(); close(joined) }()
	waitForCoreAuthLifecycle(t, "owner cancellation", ctx.Done())
	select {
	case <-joined:
		t.Fatal("Close returned before its running refresh callback finished")
	case <-time.After(50 * time.Millisecond): // Semantic negative observation while callback is explicitly blocked.
	}
	unblock()
	waitForCoreAuthLifecycle(t, "running refresh callback shutdown", joined)
}
