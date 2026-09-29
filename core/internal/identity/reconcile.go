package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// reconcileLockKey is a fixed Postgres advisory lock key, distinct from
// signingKeyLockKey (service.go) and db's migration lock key, guarding
// reconcileOnce the same way LoadOrGenerateSigningKey guards key generation
// — so that when blerg-core runs as multiple replicas, only one of them
// does a given reconcile pass's GitHub API calls and revocations at a time,
// instead of every replica hammering the GitHub API and racing each other
// on the same revoke. Unlike LoadOrGenerateSigningKey's *blocking*
// pg_advisory_lock (every replica must eventually get the same answer, so
// waiting is fine), reconcileOnce uses pg_try_advisory_lock: a replica that
// loses the race simply skips this tick rather than queuing up to redo the
// same work right after the winner finishes.
const reconcileLockKey int64 = 0x626c6572676b6579 + 1

// githubAPIBaseURL is a test seam: reconcileOnce lists org members against
// this base URL, which tests point at an httptest.Server fake instead of
// the real api.github.com. Package-private and mutated only from tests in
// this package (reconcile_test.go), which is why reconcile_test.go is
// `package identity` rather than `package identity_test`.
var githubAPIBaseURL = "https://api.github.com"

// StartReconcileLoop runs reconcileOnce on a ticker every interval until ctx
// is canceled. It returns immediately; the loop runs in its own goroutine.
//
// ghToken is required (a GitHub token with read:org): without authentication, GitHub answers
// an org members listing with 200 and only the PUBLIC members (I-3) — every account with a
// private org membership would look "gone" and get disabled. Callers (main.go) must not start
// this loop at all when no token is configured, rather than passing an empty string through.
//
// Fail-closed semantics here are the mirror image of github.go's
// Callback: a GitHub API error *during login* must reject that one login
// (never admit on an inconclusive check — see github.go's doc comment). A
// GitHub API error *during this periodic reconcile pass*, by contrast, must
// leave every existing account's access untouched and just log loudly — a
// transient GitHub API blip must never be treated as "the whole org's
// membership just vanished" and lock out every github-provider account at
// once. Both directions are "fail closed" in the sense of never acting on
// an inconclusive signal, but they produce opposite outcomes (reject one
// login vs. change nothing) because the blast radius of getting it wrong is
// opposite: one login vs. every existing session.
func StartReconcileLoop(ctx context.Context, st db.Store, svc *Service, org, ghToken string, ghClient *http.Client, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				reconcileOnce(ctx, st, svc, org, ghToken, ghClient)
			}
		}
	}()
}

// reconcileOnce performs one reconcile pass: bulk-list the org's current
// members (paginated — not just the first 100), then for every local
// "github"-provider account, flip accounts.disabled_at both ways to match:
// a member no longer listed gets disabled (RevokeAccountEverywhere, then
// disabled_at = now()); an account previously disabled that IS listed again
// gets re-enabled (Unrevoke "sub", then disabled_at = NULL). Each branch is
// gated on the account's *current* disabled state, so a tick that observes
// no change (still a member and still enabled, or still gone and already
// disabled) does nothing — re-running the same listing twice must not
// re-revoke or move disabled_at.
//
// It fails safe at every step: if the advisory lock isn't acquired (another
// replica, or an overlapping pass, is already running), if ghToken is
// empty, if the member listing errors or comes back malformed on any page,
// or if reading the local accounts fails, this returns WITHOUT touching
// anyone. A partial or failed member listing is not treated as "these
// members are gone" — that would turn a transient GitHub outage into a
// mass lockout.
func reconcileOnce(ctx context.Context, st db.Store, svc *Service, org, ghToken string, ghClient *http.Client) {
	conn, err := st.Pool().Acquire(ctx)
	if err != nil {
		log.Printf("identity: reconcile: acquire connection: %v", err)
		return
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", reconcileLockKey).Scan(&acquired); err != nil {
		log.Printf("identity: reconcile: advisory lock: %v", err)
		return
	}
	if !acquired {
		// Another replica (or an overlapping pass) is already reconciling.
		// Skip this tick rather than queue up behind it.
		return
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", reconcileLockKey); err != nil {
			log.Printf("identity: reconcile: advisory unlock: %v", err)
		}
	}()

	members, err := listOrgMembers(ctx, ghClient, org, ghToken)
	if err != nil {
		log.Printf("identity: reconcile: github org member listing failed, leaving all github-provider accounts untouched: %v", err)
		return
	}

	rows, err := st.Pool().Query(ctx,
		`SELECT id::text, email, disabled_at IS NOT NULL FROM accounts WHERE provider = 'github'`)
	if err != nil {
		log.Printf("identity: reconcile: list github-provider accounts: %v", err)
		return
	}
	type acct struct {
		id, login string
		disabled  bool
	}
	var toCheck []acct
	for rows.Next() {
		var id string
		var login *string
		var disabled bool
		if err := rows.Scan(&id, &login, &disabled); err != nil {
			rows.Close()
			log.Printf("identity: reconcile: scan account row: %v", err)
			return
		}
		l := ""
		if login != nil {
			l = *login
		}
		toCheck = append(toCheck, acct{id: id, login: l, disabled: disabled})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("identity: reconcile: iterate accounts: %v", err)
		return
	}
	rows.Close()

	for _, a := range toCheck {
		member := a.login != "" && members[a.login]
		switch {
		case member && a.disabled:
			// Un-revoke FIRST: if it fails we retry next tick because disabled_at is still set.
			if err := svc.Unrevoke(ctx, "sub", a.id); err != nil {
				log.Printf("identity: reconcile: un-revoke account %s: %v", a.id, err)
				continue
			}
			if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = NULL WHERE id = $1`, a.id); err != nil {
				log.Printf("identity: reconcile: re-enable account %s: %v", a.id, err)
				continue
			}
			log.Printf("identity: reconcile: re-enabled account %s (login %q) — back in org %s", a.id, a.login, org)
		case !member && !a.disabled:
			// Revoke FIRST for the same reason: a failed revoke leaves disabled_at NULL, so
			// the next tick retries instead of believing the job is done.
			if err := svc.RevokeAccountEverywhere(ctx, a.id); err != nil {
				log.Printf("identity: reconcile: revoke account %s (login %q): %v", a.id, a.login, err)
				continue
			}
			if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, a.id); err != nil {
				log.Printf("identity: reconcile: disable account %s: %v", a.id, err)
				continue
			}
			log.Printf("identity: reconcile: disabled account %s (login %q) — no longer a member of org %s", a.id, a.login, org)
		}
	}
}

// listOrgMembers fetches every page of GET /orgs/{org}/members (100 per
// page — GitHub's max) and returns the set of member logins. It stops when
// a page comes back with fewer than a full page's worth of results (the
// standard signal there is no next page), so an org with more than 100
// members is genuinely paginated through rather than silently truncated to
// its first 100. Any transport error, non-200 status, or malformed JSON on
// any page aborts the whole listing and returns an error — a partial
// listing must never be used, since "GitHub couldn't tell us about members
// on page 3" is not the same as "those members are gone."
func listOrgMembers(ctx context.Context, ghClient *http.Client, org, token string) (map[string]bool, error) {
	const perPage = 100
	members := map[string]bool{}
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/orgs/%s/members?per_page=%d&page=%d", githubAPIBaseURL, org, perPage, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := ghClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list members page %d: %w", page, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list members page %d: unexpected status %d", page, resp.StatusCode)
		}
		var batch []struct {
			Login string `json:"login"`
		}
		err = json.NewDecoder(resp.Body).Decode(&batch)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list members page %d: malformed response: %w", page, err)
		}
		for _, m := range batch {
			members[m.Login] = true
		}
		if len(batch) < perPage {
			break
		}
	}
	return members, nil
}
