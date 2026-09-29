package identity_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestPlatformRoleCapsDistinctFromProjectRoleCaps(t *testing.T) {
	admin, ok := identity.PlatformRoleCaps["admin"]
	if !ok || len(admin) == 0 {
		t.Fatal("admin role must exist with capabilities")
	}
	found := false
	for _, c := range admin {
		if c == "session.start" {
			found = true
		}
	}
	if !found {
		t.Error("admin must carry session.start")
	}
	member, ok := identity.PlatformRoleCaps["member"]
	if !ok {
		t.Fatal("member role must exist")
	}
	for _, c := range member {
		if c == "membership.write" || c == "board.admin" {
			t.Errorf("member must not carry admin-only capability %q", c)
		}
	}
}

// TestAdminIsSupersetOfMember guards the invariant that was broken before the
// final-review fix wave: "admin" lacked card.read/card.write, so the bootstrap
// admin — the only account on a fresh install — could not read a single board
// card, while a plain "member" could.
func TestAdminIsSupersetOfMember(t *testing.T) {
	admin := identity.PlatformRoleCaps["admin"]
	has := make(map[string]bool, len(admin))
	for _, c := range admin {
		if has[c] {
			t.Errorf("admin lists capability %q twice", c)
		}
		has[c] = true
	}
	for _, c := range identity.PlatformRoleCaps["member"] {
		if !has[c] {
			t.Errorf("admin is missing member capability %q", c)
		}
	}
	// And it must still be a STRICT superset — admin-only capabilities intact.
	for _, c := range []string{"board.admin", "membership.write", "secrets.read", "account.manage"} {
		if !has[c] {
			t.Errorf("admin lost admin-only capability %q", c)
		}
	}
}

func TestAccountRoleResolvesFromDB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := db.NewPgStore(pool)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var accountID string
	// Use unique provider_subject to avoid duplicate key constraint violations on repeated runs.
	uniqueSubject := fmt.Sprintf("u-%d", time.Now().UnixNano())
	err := pool.QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local',$1,'admin') RETURNING id::text`,
		uniqueSubject,
	).Scan(&accountID)
	if err != nil {
		t.Fatal(err)
	}
	role, err := identity.AccountRole(ctx, st, accountID)
	if err != nil || role != "admin" {
		t.Fatalf("AccountRole = %q, %v; want admin, nil", role, err)
	}
}

func TestMemberAndAdminCanWriteColumns(t *testing.T) {
	for _, role := range []string{"member", "admin"} {
		if !slices.Contains(identity.PlatformRoleCaps[role], "column.write") {
			t.Errorf("%s lacks column.write — board's column handlers require it for every principal", role)
		}
	}
	if !slices.Contains(identity.PlatformRoleCaps["admin"], "gate.bypass") {
		t.Error("admin lacks gate.bypass")
	}
	if slices.Contains(identity.PlatformRoleCaps["member"], "board.admin") {
		t.Error("member must not carry board.admin")
	}
}
