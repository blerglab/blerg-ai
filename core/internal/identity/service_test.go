package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/signing"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

// openStore provides a fresh database cloned from the migrated package template.
func openStore(t *testing.T) db.Store {
	t.Helper()
	return db.NewPgStore(testsupport.Pool(t, db.Migrate))
}

// staticRev is an in-memory snapshot of the revocation table for a single Verify call.
type staticRevChecker struct {
	kids     map[string]bool
	lineages map[string]bool
	subs     map[string]bool
}

func (r staticRevChecker) Revoked(kid, lineage, sub, _ string) bool {
	return r.kids[kid] || r.lineages[lineage] || r.subs[sub]
}

func (r staticRevChecker) StaleBeyondCeiling() bool { return false }

func staticRev(ctx context.Context, t *testing.T, svc *Service) cid.RevocationChecker {
	t.Helper()
	revs, err := svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatalf("revocation snapshot: %v", err)
	}
	r := staticRevChecker{kids: map[string]bool{}, lineages: map[string]bool{}, subs: map[string]bool{}}
	for _, rev := range revs {
		switch rev.Kind {
		case "kid":
			r.kids[rev.Value] = true
		case "lineage":
			r.lineages[rev.Value] = true
		case "sub":
			r.subs[rev.Value] = true
		}
	}
	return r
}

// TestLoadOrGenerateSigningKeySerializesViaAdvisoryLock proves the fix for IMPORTANT 5: two
// replicas booting concurrently against an empty signing_keys table must not each generate and
// persist a different key. We can't cheaply force an "empty table" race against the shared,
// persistent test DB, so instead we verify the actual mechanism directly: hold the same
// Postgres advisory lock key LoadOrGenerateSigningKey uses (signingKeyLockKey, package-private
// so only reachable from an in-package test) on a separate connection, and confirm the call
// blocks until that lock is released.
func TestLoadOrGenerateSigningKeySerializesViaAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	holder, err := st.Pool().Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", signingKeyLockKey); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	unlocked := false
	defer func() {
		if !unlocked {
			_, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", signingKeyLockKey)
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := LoadOrGenerateSigningKey(ctx, st)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("LoadOrGenerateSigningKey returned (err=%v) while another connection held the advisory lock — it is not serializing on it", err)
	case <-time.After(300 * time.Millisecond):
		// Still blocked on the lock, as expected.
	}

	if _, err := holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", signingKeyLockKey); err != nil {
		t.Fatalf("release advisory lock: %v", err)
	}
	unlocked = true

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoadOrGenerateSigningKey after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("LoadOrGenerateSigningKey did not complete after advisory lock was released")
	}
}

// TestLoadOrGenerateSigningKeyConcurrentCallsConverge fires many concurrent calls (against
// whatever state signing_keys is currently in) and asserts they all agree on one key — the
// property that matters for "stable kid across restarts" under concurrency. This complements
// the lock-blocking test above without needing to force the table empty in the shared DB.
func TestLoadOrGenerateSigningKeyConcurrentCallsConverge(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	const n = 8
	var wg sync.WaitGroup
	kps := make([]signing.KeyPair, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kps[i], errs[i] = LoadOrGenerateSigningKey(ctx, st)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	first := kps[0]
	for i, kp := range kps {
		if kp.Kid != first.Kid || !bytes.Equal(kp.Pub, first.Pub) || !bytes.Equal(kp.Priv, first.Priv) {
			t.Fatalf("call %d returned a different key than call 0: kid=%s vs %s", i, kp.Kid, first.Kid)
		}
	}
}

func TestMintedAgentTokenVerifiesAndRevokes(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	kp, err := LoadOrGenerateSigningKey(ctx, st) // persists the key so JWKS can serve it
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	svc := NewService(st, kp)

	// Unique per run so repeated test executions against the same persistent Postgres
	// don't collide with a revocation row left behind by a prior run.
	lineage := fmt.Sprintf("lin-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(),
			`DELETE FROM revocations WHERE kind = 'lineage' AND value = $1`, lineage)
	})

	tok, err := svc.MintAgentToken(ctx, AgentTokenInput{
		Sub: "sess-1", Aud: "blerg-board", Project: "proj-1",
		OnBehalfOf: "alice", Lineage: lineage, Caps: []string{"card.write"},
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	keys, _ := svc.JWKS(ctx)
	rev := staticRev(ctx, t, svc)
	p, err := cid.Verify(tok, "blerg-board", keys, rev, func(string) bool { return false })
	if err != nil {
		t.Fatalf("verify minted token: %v", err)
	}
	if p.Project != "proj-1" || !p.Has("card.write") || p.OnBehalfOf != "alice" {
		t.Fatalf("claims wrong: %+v", p.Claims)
	}
	// TTL is session-scale (<= ~15 min from now).
	if p.ExpiresAt-time.Now().Unix() > 16*60 {
		t.Fatalf("TTL too long: %d", p.ExpiresAt-time.Now().Unix())
	}
	// Revoke by lineage → verify now fails.
	if err := svc.Revoke(ctx, "lineage", lineage); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rev2 := staticRev(ctx, t, svc)
	if _, err := cid.Verify(tok, "blerg-board", keys, rev2, func(string) bool { return false }); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("post-revoke verify err = %v, want ErrRevoked", err)
	}
}
