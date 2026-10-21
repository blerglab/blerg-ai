package identity

import (
	"context"
	"errors"
	"testing"
)

// A sign-in marked before a revocation everywhere is refused under the fence and leaves the
// revocation in place; one marked after it is a fresh sign-in. Both the password form (the
// account is named up front) and an identity provider's callback (it is not) are covered, and a
// second revocation over an existing "lineage" entry is caught too.
func TestLoginMarkRefusesSignInAcrossRevocation(t *testing.T) {
	for _, tc := range []struct{ name, provider string }{{"named account", "local"}, {"provider callback", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st := fenceSvc(t)
			ctx := context.Background()
			subject := "mark-" + map[string]string{"local": "local", "": "idp"}[tc.provider]
			acct := insertAccount(t, st, subject, "member")
			subjectArg := subject
			if tc.provider == "" {
				subjectArg = ""
			}
			subCount := func() int {
				var n int
				if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM revocations WHERE kind = 'sub' AND value = $1`, acct).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}

			for round := range 2 {
				mark, err := svc.BeginLogin(ctx, tc.provider, subjectArg)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.RevokeAccountEverywhere(ctx, acct); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.IssueRefreshTokenSince(ctx, mark, acct, "", ""); !errors.Is(err, ErrLoginSuperseded) {
					t.Fatalf("round %d: sign-in marked before the revocation: %v, want ErrLoginSuperseded", round, err)
				}
				if subCount() != 1 {
					t.Fatalf("round %d: the refused sign-in cleared the sub revocation", round)
				}
				fresh, err := svc.BeginLogin(ctx, tc.provider, subjectArg)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.IssueRefreshTokenSince(ctx, fresh, acct, "", ""); err != nil {
					t.Fatalf("round %d: sign-in marked after the revocation: %v", round, err)
				}
				if subCount() != 0 {
					t.Fatalf("round %d: a fresh sign-in did not clear the sub revocation", round)
				}
			}
		})
	}
}

// A mark taken for one account does not admit a session for another (the subject was renamed
// or reassigned while the credential was checked).
func TestLoginMarkForAnotherAccountIsRefused(t *testing.T) {
	svc, st := fenceSvc(t)
	ctx := context.Background()
	insertAccount(t, st, "mark-first", "member")
	other := insertAccount(t, st, "mark-other", "member")
	mark, err := svc.BeginLogin(ctx, "local", "mark-first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueRefreshTokenSince(ctx, mark, other, "", ""); !errors.Is(err, ErrLoginSuperseded) {
		t.Fatalf("mark for another account: %v, want ErrLoginSuperseded", err)
	}
}
