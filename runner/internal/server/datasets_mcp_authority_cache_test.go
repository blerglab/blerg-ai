package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func TestMCPAuthorityCacheScopeSequenceExpiryAndBound(t *testing.T) {
	now := time.Now()
	cache := mcpAuthorityCache{now: func() time.Time { return now }}
	scope := ld.Scope{Binding: "00000000-0000-4000-8000-000000000514", Digest: strings.Repeat("a", 64)}
	calls := 0
	cause := ""
	fetch := func() mcpAuthorityDecision { calls++; return mcpAuthorityDecision{cause: cause} }
	check := func(s ld.Scope, seq int64, wantCalls int, wantCause string) {
		t.Helper()
		if got := cache.check(t.Context(), s, seq, fetch); got.cause != wantCause || calls != wantCalls {
			t.Fatal("incorrect shared authority decision", got, calls)
		}
	}
	check(scope, 0, 1, "")
	check(scope, 0, 1, "")
	changed := scope
	changed.Digest = strings.Repeat("b", 64)
	check(changed, 0, 2, "")
	check(scope, 0, 3, "")
	check(scope, 1, 4, "")
	now = now.Add(time.Second)
	check(scope, 1, 5, "")
	now = now.Add(time.Second)
	cause = "access_removed"
	check(scope, 1, 6, "access_removed")
	check(scope, 1, 6, "access_removed")
	now = now.Add(time.Second)
	cause = ""
	check(scope, 1, 7, "")
	for i := 0; i < 4100; i++ {
		scope.Binding = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		_ = cache.check(t.Context(), scope, 0, fetch)
	}
	if len(cache.entries) > 4096 {
		t.Fatal("authority metadata cache grew without a bound")
	}
}
func TestMCPAuthorityCacheWaiterCancellation(t *testing.T) {
	cache := mcpAuthorityCache{}
	scope := ld.Scope{Binding: "00000000-0000-4000-8000-000000000514"}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan mcpAuthorityDecision, 1)
	go func() {
		done <- cache.check(t.Context(), scope, 0, func() mcpAuthorityDecision { close(entered); <-release; return mcpAuthorityDecision{} })
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cause := cache.check(ctx, scope, 0, func() mcpAuthorityDecision {
		t.Error("canceled waiter fetched authority")
		return mcpAuthorityDecision{}
	})
	close(release)
	first := <-done
	if cause.cause != "authority_unavailable" || first.cause != "" {
		t.Fatal("canceled waiter did not leave the shared request", cause, first)
	}
	if got := cache.check(ctx, scope, 0, func() mcpAuthorityDecision { return mcpAuthorityDecision{} }); got.cause != "authority_unavailable" {
		t.Fatal("canceled request reused a warm authority result")
	}
}
