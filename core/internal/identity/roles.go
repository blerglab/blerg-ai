package identity

import (
	"context"

	"github.com/blerglab/blerg-ai/core/internal/db"
)

// PlatformRoleCaps maps a platform-level account role to its capability set.
// Deliberately a SEPARATE map from projects.RoleCaps — that map already has
// a "member" key scoped to project membership with a different capability
// set; reusing it here would silently collide the two "member" meanings.
//
// "admin" is a strict SUPERSET of "member": an admin can do everything a
// member can, plus the admin-only capabilities. Before this was enforced,
// "admin" lacked card.read/card.write, so the bootstrap admin — the only
// account on a fresh install — could not read a single board card while a
// plain member could. TestAdminIsSupersetOfMember guards the invariant.
var PlatformRoleCaps = map[string][]string{
	"admin": dedupeCaps(append([]string{
		"board.admin", "gate.bypass", "membership.write", "secrets.read", "account.manage",
	}, memberCaps...)),
	"member": memberCaps,
}

// memberCaps is the "member" capability set (spec §4: read/write on boards and
// tickets — which on board means card.* AND column.write, since column
// create/rename/move/delete are gated on column.write for every principal —
// plus session.start for runner). Named so "admin" is defined in terms of it.
var memberCaps = []string{"card.read", "card.write", "column.write", "session.start"}

// dedupeCaps returns caps with duplicates removed, preserving first-seen
// order — so admin's own list and memberCaps may overlap freely without
// producing a capability twice.
func dedupeCaps(caps []string) []string {
	seen := make(map[string]bool, len(caps))
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// AccountRole resolves an account's platform role from the accounts table.
func AccountRole(ctx context.Context, st db.Store, accountID string) (string, error) {
	var role string
	err := st.Pool().QueryRow(ctx,
		`SELECT role FROM accounts WHERE id = $1`, accountID).Scan(&role)
	return role, err
}
