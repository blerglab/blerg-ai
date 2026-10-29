package credentials_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/credentials"
)

// A provider with two independent, single-use refresh-token families. Reusing
// any spent token fails, including a copy consumed by a different runtime.
type rotatingCodexAdapter struct {
	managedFixtureAdapter
	mu    sync.Mutex
	next  map[string]int
	calls int
}

func (a *rotatingCodexAdapter) token(family string, generation int) credentials.ManagedToken {
	return credentials.ManagedToken{AccessToken: fmt.Sprintf("fixture-%s-access-%d", family, generation), RefreshToken: fmt.Sprintf("fixture-%s-refresh-%d", family, generation), AccountID: "provider-account", ExpiresAt: time.Now().Add(time.Hour)}
}
func (a *rotatingCodexAdapter) PollDevice(context.Context, credentials.DeviceGrant) (credentials.ManagedToken, error) {
	return a.token("cluster", 0), nil
}
func (a *rotatingCodexAdapter) Refresh(_ context.Context, old credentials.ManagedToken) (credentials.ManagedToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for family, generation := range a.next {
		if old.RefreshToken == a.token(family, generation).RefreshToken {
			a.next[family]++
			a.calls++
			return a.token(family, generation+1), nil
		}
	}
	return credentials.ManagedToken{}, errors.New("fixture refresh token was already used")
}

func TestDedicatedClusterLoginSurvivesDesktopRotationAndPersistsEachRefresh(t *testing.T) {
	ctx := context.Background()
	a := &rotatingCodexAdapter{next: map[string]int{"desktop": 0, "cluster": 0}}
	s, replica, pool, account := setupManaged(t, a)
	old := connectManaged(t, s, account)
	desktop := a.token("desktop", 0)
	if _, err := a.Refresh(ctx, desktop); err != nil {
		t.Fatal("desktop refresh failed")
	}
	if _, err := a.Refresh(ctx, desktop); err == nil {
		t.Fatal("fixture accepted a copied spent token")
	}
	// Expired access is renewed before a model probe or pod can obtain it.
	for generation := 1; generation <= 2; generation++ {
		if _, err := pool.Exec(ctx, `UPDATE user_credentials SET expires_at=now()-interval '1 second' WHERE account_id=$1`, account); err != nil {
			t.Fatal(err)
		}
		access, err := replica.Obtain(ctx, account, "codex", false, "")
		if err != nil || access.AccessToken != a.token("cluster", generation).AccessToken || access.Generation == old.Generation {
			t.Fatal("cluster did not renew with its own durable refresh family")
		}
		// Another consumer of the rejected old generation must join the stored result.
		joined, err := s.Obtain(ctx, account, "codex", true, old.Generation)
		if err != nil || joined.Generation != access.Generation {
			t.Fatal("concurrent consumer spent refresh authority again")
		}
		old = access
	}
	if a.calls != 3 {
		t.Fatal("unexpected provider rotations")
	}
}
