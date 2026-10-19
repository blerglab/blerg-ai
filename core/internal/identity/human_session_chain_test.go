package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Superseded rows are bookkeeping for responses that may arrive in any order.
// They must remain traversable regardless of age or the number of racing tabs.
func TestLostRotationHealsLongSupersededChain(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "long-superseded-chain")
	original, err := svc.IssueRefreshToken(ctx, acct, "original-agent", "original-ip")
	if err != nil {
		t.Fatal(err)
	}
	var final string
	for i := range 40 {
		access, next, err := svc.RefreshAccessToken(ctx, original, "blerg-core")
		if err != nil {
			t.Fatalf("lost-response replacement %d: %v", i, err)
		}
		if access == "" || next == "" || next == original || next == final {
			t.Fatalf("replacement %d did not issue fresh credentials", i)
		}
		final = next
	}
	var live, superseded, extended, changedMetadata int
	if err := st.Pool().QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE revoked_at IS NULL),
		count(*) FILTER (WHERE revoke_reason = 'superseded'),
		count(DISTINCT expires_at),
		count(*) FILTER (WHERE user_agent <> 'original-agent' OR ip <> 'original-ip')
		FROM human_sessions WHERE account_id = $1`, acct).
		Scan(&live, &superseded, &extended, &changedMetadata); err != nil {
		t.Fatal(err)
	}
	if live != 1 || superseded != 39 || extended != 1 || changedMetadata != 0 {
		t.Fatalf("chain invariant: live=%d superseded=%d expiryValues=%d changedMetadata=%d", live, superseded, extended, changedMetadata)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, final, "blerg-core"); err != nil {
		t.Fatalf("latest replacement must work: %v", err)
	}
}

// Finding a used rotation after several superseded rows must stop the walk:
// reaching a live unused descendant is not by itself sufficient to heal.
func TestLostRotationWalkStopsAtPresentedIntermediate(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "superseded-before-rotated")
	original, err := svc.IssueRefreshToken(ctx, acct, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var replacement string
	for i := range 4 {
		_, replacement, err = svc.RefreshAccessToken(ctx, original, "blerg-core")
		if err != nil {
			t.Fatalf("replacement %d: %v", i, err)
		}
	}
	_, newest, err := svc.RefreshAccessToken(ctx, replacement, "blerg-core")
	if err != nil {
		t.Fatalf("presenting the replacement: %v", err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, original, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("used intermediate must cause replay rejection: %v", err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, newest, "blerg-core"); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("replay must revoke latest descendant too: %v", err)
	}
}

func TestLostRotationWalkDoesNotReviveEndedHead(t *testing.T) {
	for _, end := range []string{"logout", "logout_all", "expiry", "disabled"} {
		t.Run(end, func(t *testing.T) {
			svc, st := newTestService(t)
			ctx := context.Background()
			acct := insertAccount(t, st, "ended-superseded-head")
			original, err := svc.IssueRefreshToken(ctx, acct, "", "")
			if err != nil {
				t.Fatal(err)
			}
			var head string
			for i := range 3 {
				_, head, err = svc.RefreshAccessToken(ctx, original, "blerg-core")
				if err != nil {
					t.Fatalf("replacement %d: %v", i, err)
				}
			}
			switch end {
			case "logout":
				err = svc.RevokeHumanSession(ctx, head)
			case "logout_all":
				err = svc.RevokeAllSessions(ctx, acct)
			case "expiry":
				_, err = st.Pool().Exec(ctx, `UPDATE human_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, acct)
			case "disabled":
				_, err = st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, acct)
			}
			if err != nil {
				t.Fatal(err)
			}
			var before int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1`, acct).Scan(&before); err != nil {
				t.Fatal(err)
			}
			access, raw, err := svc.RefreshAccessToken(ctx, original, "blerg-core")
			if err == nil || access != "" || raw != "" {
				t.Fatalf("ended head must not mint credentials: error=%v accessPresent=%v refreshPresent=%v", err, access != "", raw != "")
			}
			var after int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1`, acct).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("rejected refresh persisted a replacement: rows %d -> %d", before, after)
			}
		})
	}
}
