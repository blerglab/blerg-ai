package server

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRound10KeeperMembershipWaitNamesKeeper(t *testing.T) {
	if got := membershipWaitingText(protocol.MembershipWaitKeeper); got != "Waiting for the shell keeper to reconnect. Blerg will retry this repository change." {
		t.Fatal("misleading keeper wait", got)
	}
}

func TestRound10MembershipViewKeepsKeeperWaitCode(t *testing.T) {
	for _, code := range []string{protocol.MembershipWaitKeeper, protocol.MembershipWaitShell, protocol.MembershipWaitRuntime} {
		view := operationView(&db.MembershipOperation{Waiting: membershipWaitingText(code)})
		if (view.WaitingCode == protocol.MembershipWaitKeeper) != (code == protocol.MembershipWaitKeeper) {
			t.Fatal("wrong recovery action", code, view.WaitingCode)
		}
	}
}

func TestRound11IncompatibleKeeperWaitKeepsItsCode(t *testing.T) {
	view := operationView(&db.MembershipOperation{Waiting: membershipWaitingText(protocol.MembershipWaitKeeperIncompatible)})
	if view.WaitingCode != protocol.MembershipWaitKeeperIncompatible {
		t.Fatal("incompatible keeper lost its remedy", view.WaitingCode)
	}
	got := membershipWaitingText(protocol.MembershipWaitKeeperIncompatible)
	for _, want := range []string{"incompatible", "Restart the keeper service", "ends its open shells"} {
		if !strings.Contains(got, want) {
			t.Fatal("incompatible keeper lost its explanation or remedy", got)
		}
	}
}
