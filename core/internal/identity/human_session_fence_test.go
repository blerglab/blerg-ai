package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/signing"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

// fenceSvc is agentTokenSvc over a pool large enough for a held rotation, a revocation that
// waits on it, the access-token mint inside the rotation, and an observer, all at once.
func fenceSvc(t *testing.T) (*Service, db.Store) {
	t.Helper()
	small := testsupport.Pool(t, db.Migrate)
	cfg := small.Config()
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := db.NewPgStore(pool)
	kp, err := signing.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO signing_keys(kid, public_key, private_key, active) VALUES ($1,$2,$3,true)`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
	return NewService(st, kp), st
}

// pauseAt makes svc stop at the named commit point the first time it is reached. The returned
// channel closes once the point is reached; calling release lets that transaction commit.
func pauseAt(svc *Service, point string) (reached <-chan struct{}, release func()) {
	r := make(chan struct{})
	gate := make(chan struct{})
	var once, releaseOnce sync.Once
	svc.beforeSessionCommit = func(p string) {
		if p != point {
			return
		}
		once.Do(func() {
			close(r)
			<-gate
		})
	}
	return r, func() { releaseOnce.Do(func() { close(gate) }) }
}

// waitForLockWait returns once the specified query in this test's database is blocked on a
// lock. It polls a condition, so it establishes the ordering without depending on timing.
func waitForLockWait(t *testing.T, st db.Store, query string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND starts_with(query, $1) AND cardinality(pg_blocking_pids(pid)) > 0`, query).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no backend ever blocked on a lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

type refreshResult struct {
	access, refresh string
	err             error
}

func sessionRows(t *testing.T, st db.Store, accountID string) (total, live int) {
	t.Helper()
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE revoked_at IS NULL)
		FROM human_sessions WHERE account_id = $1`, accountID).Scan(&total, &live); err != nil {
		t.Fatal(err)
	}
	return total, live
}

// assertSessionEnded checks that nothing of accountID's sessions survives: no live row, the
// chain is not live, and the successor issued by the interleaved rotation mints nothing.
func assertSessionEnded(t *testing.T, svc *Service, st db.Store, accountID, chainID, successor string) {
	t.Helper()
	ctx := context.Background()
	svc.beforeSessionCommit = nil
	total, live := sessionRows(t, st, accountID)
	if live != 0 {
		t.Fatalf("%d session rows still live after revocation", live)
	}
	if _, ok, err := svc.HumanSessionLive(ctx, accountID, chainID); err != nil || ok {
		t.Fatalf("chain still live after revocation: live=%v err=%v", ok, err)
	}
	if successor != "" {
		access, raw, err := svc.RefreshAccessToken(ctx, successor, "blerg-core")
		if err == nil || access != "" || raw != "" {
			t.Fatalf("successor must not mint credentials: err=%v accessPresent=%v refreshPresent=%v", err, access != "", raw != "")
		}
	}
	after, live := sessionRows(t, st, accountID)
	if after != total || live != 0 {
		t.Fatalf("presenting the successor wrote rows: before=%d after=%d live=%d", total, after, live)
	}
}

func issueChain(t *testing.T, svc *Service, st db.Store, subject string) (accountID, raw, chainID string) {
	t.Helper()
	ctx := context.Background()
	accountID = insertAccount(t, st, subject, "member")
	raw, err := svc.IssueRefreshToken(ctx, accountID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE account_id = $1`, accountID).
		Scan(&chainID); err != nil {
		t.Fatal(err)
	}
	return accountID, raw, chainID
}

// A rotation in flight when a chain is revoked must not leave its successor live: revocation
// waits for the rotation and then ends the successor too.
func TestChainRevocationEndsConcurrentRotation(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acct, raw, chainID := issueChain(t, svc, st, "revoke-during-rotation")

	reached, release := pauseAt(svc, "rotate")
	defer release()
	refreshed := make(chan refreshResult, 1)
	go func() {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		refreshed <- refreshResult{a, r, err}
	}()
	waitFor(t, reached, "the rotation to reach its commit")

	revoked := make(chan error, 1)
	go func() { revoked <- svc.RevokeChain(ctx, chainID, "reuse") }()
	waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
	release()

	res := waitFor(t, refreshed, "the rotation")
	if res.err != nil || res.refresh == "" {
		t.Fatalf("the rotation that committed first should succeed: %v", res.err)
	}
	if err := waitFor(t, revoked, "the chain revocation"); err != nil {
		t.Fatal(err)
	}
	assertSessionEnded(t, svc, st, acct, chainID, res.refresh)
}

// "Log out everywhere" has the same shape: a rotation in flight must not survive it.
func TestRevokeAllSessionsEndsConcurrentRotation(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acct, raw, chainID := issueChain(t, svc, st, "logout-all-during-rotation")

	reached, release := pauseAt(svc, "rotate")
	defer release()
	refreshed := make(chan refreshResult, 1)
	go func() {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		refreshed <- refreshResult{a, r, err}
	}()
	waitFor(t, reached, "the rotation to reach its commit")

	revoked := make(chan error, 1)
	go func() { revoked <- svc.RevokeAllSessions(ctx, acct) }()
	waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
	release()

	res := waitFor(t, refreshed, "the rotation")
	if res.err != nil || res.refresh == "" {
		t.Fatalf("the rotation that committed first should succeed: %v", res.err)
	}
	if err := waitFor(t, revoked, "the revocation"); err != nil {
		t.Fatal(err)
	}
	assertSessionEnded(t, svc, st, acct, chainID, res.refresh)
}

// The other order: a refresh that arrives while the revocation is committing waits for it and
// is then refused, writing nothing.
func TestRotationWaitingOnChainRevocationIsRefused(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acct, raw, chainID := issueChain(t, svc, st, "rotate-during-revocation")

	reached, release := pauseAt(svc, "revoke")
	defer release()
	revoked := make(chan error, 1)
	go func() { revoked <- svc.RevokeChain(ctx, chainID, "reuse") }()
	waitFor(t, reached, "the revocation to reach its commit")

	refreshed := make(chan refreshResult, 1)
	go func() {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		refreshed <- refreshResult{a, r, err}
	}()
	waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
	release()

	if err := waitFor(t, revoked, "the chain revocation"); err != nil {
		t.Fatal(err)
	}
	res := waitFor(t, refreshed, "the refresh")
	if !errors.Is(res.err, ErrSessionRevoked) || res.access != "" || res.refresh != "" {
		t.Fatalf("refresh after revocation must be refused: err=%v", res.err)
	}
	assertSessionEnded(t, svc, st, acct, chainID, "")
}

// Logging out one device ends that browser session's whole chain, even when a refresh of the
// same cookie is committing at that moment. The person's other devices are untouched.
func TestLogoutEndsConcurrentRotationOfTheSameCookie(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acct, raw, chainID := issueChain(t, svc, st, "logout-during-rotation")
	otherRaw, err := svc.IssueRefreshToken(ctx, acct, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var otherChain string
	if err := st.Pool().QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE token_hash = $1`, hashToken(otherRaw)).
		Scan(&otherChain); err != nil {
		t.Fatal(err)
	}

	reached, release := pauseAt(svc, "rotate")
	defer release()
	refreshed := make(chan refreshResult, 1)
	go func() {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		refreshed <- refreshResult{a, r, err}
	}()
	waitFor(t, reached, "the rotation to reach its commit")

	loggedOut := make(chan error, 1)
	go func() { loggedOut <- svc.RevokeHumanSession(ctx, raw) }()
	waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
	release()

	res := waitFor(t, refreshed, "the rotation")
	if res.err != nil || res.refresh == "" {
		t.Fatalf("the rotation that committed first should succeed: %v", res.err)
	}
	if err := waitFor(t, loggedOut, "the logout"); err != nil {
		t.Fatal(err)
	}
	svc.beforeSessionCommit = nil

	var live int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE chain_id = $1 AND revoked_at IS NULL`, chainID).
		Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d rows of the logged-out chain still live", live)
	}
	if _, ok, err := svc.HumanSessionLive(ctx, acct, chainID); err != nil || ok {
		t.Fatalf("logged-out chain still live: live=%v err=%v", ok, err)
	}
	var sidRevoked bool
	if err := st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM revocations WHERE kind = 'sid' AND value = $1)`, chainID).
		Scan(&sidRevoked); err != nil {
		t.Fatal(err)
	}
	if !sidRevoked {
		t.Fatal("logout must revoke the chain's sid")
	}
	if a, r, err := svc.RefreshAccessToken(ctx, res.refresh, "blerg-core"); err == nil || a != "" || r != "" {
		t.Fatalf("successor must not mint credentials after logout: err=%v", err)
	}
	if _, ok, err := svc.HumanSessionLive(ctx, acct, otherChain); err != nil || !ok {
		t.Fatalf("another device's session must survive this logout: live=%v err=%v", ok, err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, otherRaw, "blerg-core"); err != nil {
		t.Fatalf("another device must still refresh: %v", err)
	}
}

// Every fenced session path finishes on one pooled connection: the transaction holding the fence
// never needs a second connection, so refreshes queued on the fence cannot starve it of one.
func TestFencedSessionPathsNeedOnlyOneConnection(t *testing.T) {
	small := testsupport.Pool(t, db.Migrate)
	cfg := small.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := db.NewPgStore(pool)
	kp, err := signing.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO signing_keys(kid, public_key, private_key, active) VALUES ($1,$2,$3,true)`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, kp)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acct, raw, chainID := issueChain(t, svc, st, "one-connection")

	access, next, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
	if err != nil || access == "" || next == "" {
		t.Fatalf("refresh on a one-connection pool: %v", err)
	}
	// The browser never got next: presenting raw again redoes the rotation.
	if _, _, err := svc.RefreshAccessToken(ctx, raw, "blerg-core"); err != nil {
		t.Fatalf("lost-rotation redo on a one-connection pool: %v", err)
	}
	if err := svc.RevokeHumanSession(ctx, raw); err != nil {
		t.Fatalf("logout on a one-connection pool: %v", err)
	}
	if err := svc.RevokeChain(ctx, chainID, "reuse"); err != nil {
		t.Fatalf("chain revocation on a one-connection pool: %v", err)
	}
	if err := svc.RevokeAllSessions(ctx, acct); err != nil {
		t.Fatalf("revoke all on a one-connection pool: %v", err)
	}
}

// A refresh that cannot get the account's session fence in time gives up with ErrSessionBusy,
// writes nothing, and leaves its token good for the next try.
func TestSessionFenceWaitGivesUpRetryably(t *testing.T) {
	svc, st := fenceSvc(t)
	svc.SetSessionLockTimeout(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acct, raw, _ := issueChain(t, svc, st, "fence-timeout")
	otherRaw, err := svc.IssueRefreshToken(ctx, acct, "", "")
	if err != nil {
		t.Fatal(err)
	}

	reached, release := pauseAt(svc, "rotate")
	defer release()
	held := make(chan refreshResult, 1)
	go func() {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		held <- refreshResult{a, r, err}
	}()
	waitFor(t, reached, "the rotation to reach its commit")
	before, _ := sessionRows(t, st, acct)

	start := time.Now()
	access, next, err := svc.RefreshAccessToken(ctx, otherRaw, "blerg-core")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrSessionBusy) || access != "" || next != "" {
		t.Fatalf("a refresh behind a held fence = %v, want ErrSessionBusy", err)
	}
	if errors.Is(err, ErrSessionRevoked) || errors.Is(err, ErrSessionReplayed) {
		t.Fatalf("a busy refresh must not read as a dead session: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the waiter gave up after %s, want about the lock timeout", elapsed)
	}
	if after, _ := sessionRows(t, st, acct); after != before {
		t.Fatalf("a busy refresh wrote rows: before=%d after=%d", before, after)
	}

	release()
	if res := waitFor(t, held, "the held rotation"); res.err != nil {
		t.Fatal(res.err)
	}
	svc.beforeSessionCommit = nil
	if _, _, err := svc.RefreshAccessToken(ctx, otherRaw, "blerg-core"); err != nil {
		t.Fatalf("the busy token must still refresh afterwards: %v", err)
	}
}

// Revocation rows for browser sessions go once every access token they could cover has expired.
func TestPruneSessionRevocations(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx := context.Background()
	acct, raw, endedChain := issueChain(t, svc, st, "prune-sid")
	if err := svc.RevokeHumanSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	_, _, liveChain := issueChain(t, svc, st, "prune-sid-live")
	_, raw2, recentChain := issueChain(t, svc, st, "prune-sid-recent")
	if err := svc.RevokeHumanSession(ctx, raw2); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-(humanAccessTokenTTL + SessionRevocationMargin + time.Minute))
	for _, r := range []struct{ kind, value string }{
		{"sid", endedChain}, {"sid", liveChain}, {"sub", acct},
	} {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO revocations(kind, value, revoked_at) VALUES ($1,$2,$3)
			ON CONFLICT (kind, value) DO UPDATE SET revoked_at = $3`, r.kind, r.value, old); err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.PruneSessionRevocations(ctx, SessionRevocationMargin)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	present := func(kind, value string) bool {
		var ok bool
		if err := st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM revocations WHERE kind=$1 AND value=$2)`, kind, value).
			Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if present("sid", endedChain) {
		t.Error("an old revocation of an ended session must be pruned")
	}
	if !present("sid", recentChain) {
		t.Error("a revocation still inside the access-token lifetime must stay")
	}
	if !present("sid", liveChain) {
		t.Error("a revocation naming a session with a live row must stay")
	}
	if !present("sub", acct) {
		t.Error("other kinds of revocation must not be touched")
	}
}

// A sub-millisecond session lock timeout still bounds the wait: Postgres reads 0 as "forever".
func TestSessionLockTimeoutIsAtLeastOneMillisecond(t *testing.T) {
	svc, st := fenceSvc(t)
	svc.SetSessionLockTimeout(time.Microsecond)
	ctx := context.Background()
	acct := insertAccount(t, st, "tiny-timeout", "member")
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := svc.lockAccountSessions(ctx, tx, acct); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := tx.QueryRow(ctx, `SHOW lock_timeout`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1ms" {
		t.Fatalf("lock_timeout = %q, want 1ms", got)
	}
}

// A lost response redone while a second tab presents the original cookie must leave
// one unused live head, and both responses must remain recoverable by that browser.
func TestLostRotationRedoInterleaving(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx := t.Context()
	_, raw, _ := issueChain(t, svc, st, "redo-interleave")
	if _, _, err := svc.RefreshAccessToken(ctx, raw, "blerg-core"); err != nil {
		t.Fatal(err)
	}
	reached, release := pauseAt(svc, "redo")
	defer release()
	first, second := make(chan refreshResult, 1), make(chan refreshResult, 1)
	run := func(ch chan<- refreshResult) {
		a, r, err := svc.RefreshAccessToken(ctx, raw, "blerg-core")
		ch <- refreshResult{a, r, err}
	}
	go run(first)
	waitFor(t, reached, "redo before commit")
	go run(second)
	waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
	release()
	a, b := waitFor(t, first, "first redo"), waitFor(t, second, "second redo")
	if a.err != nil || b.err != nil || a.refresh == b.refresh {
		t.Fatalf("redo results: %v %v", a.err, b.err)
	}
	svc.beforeSessionCommit = nil
	if _, _, err := svc.RefreshAccessToken(ctx, a.refresh, "blerg-core"); err != nil {
		t.Fatalf("out-of-order response must heal: %v", err)
	}
}

// The serialization point is the account fence. A login holding it commits first
// and is ended by a concurrent logout; a login queued after logout is a new login.
func TestLoginAndLogoutEverywhereOrder(t *testing.T) {
	for _, first := range []string{"issue", "logout_all"} {
		t.Run(first, func(t *testing.T) {
			svc, st := fenceSvc(t)
			acct, _, _ := issueChain(t, svc, st, "login-logout-order")
			reached, release := pauseAt(svc, first)
			defer release()
			login, logout := make(chan refreshResult, 1), make(chan error, 1)
			issue := func() {
				raw, err := svc.IssueRefreshToken(t.Context(), acct, "", "")
				login <- refreshResult{refresh: raw, err: err}
			}
			revoke := func() { logout <- svc.RevokeAccountEverywhere(t.Context(), acct) }
			if first == "issue" {
				go issue()
			} else {
				go revoke()
			}
			waitFor(t, reached, "first writer before commit")
			if first == "issue" {
				go revoke()
			} else {
				go issue()
			}
			waitForLockWait(t, st, "SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:'")
			release()
			result := waitFor(t, login, "login")
			if result.err != nil {
				t.Fatal(result.err)
			}
			if err := waitFor(t, logout, "logout"); err != nil {
				t.Fatal(err)
			}
			svc.beforeSessionCommit = nil
			_, _, err := svc.RefreshAccessToken(t.Context(), result.refresh, "blerg-core")
			if first == "issue" && !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("login before logout survived: %v", err)
			}
			if first == "logout_all" && err != nil {
				t.Fatalf("fresh login after logout failed: %v", err)
			}
			var sub int
			if err := st.Pool().QueryRow(t.Context(), `SELECT count(*) FROM revocations WHERE kind='sub' AND value=$1`, acct).Scan(&sub); err != nil {
				t.Fatal(err)
			}
			if first == "logout_all" && sub != 0 {
				t.Fatal("fresh login did not clear sub revocation atomically")
			}
		})
	}
}
