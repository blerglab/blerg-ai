package auth

import (
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// TestRequireBoard_CoreServiceKindDeniedWithoutCapability guards against the
// privilege-escalation bug where a core token with Claims.Kind == "service"
// (or anything other than "agent") was mapped to board's KindService and
// then hit RequireBoard's native-trusted-actor bypass, granting it full
// board-admin on every board regardless of its actual capability claims.
func TestRequireBoard_CoreServiceKindDeniedWithoutCapability(t *testing.T) {
	p := corePrincipal(coreauth.Synthesized{
		Sub:  "svc-1",
		Kind: "service", // NOT "agent"
		Caps: nil,       // no capabilities at all
	})
	if p.Kind != KindService {
		t.Fatalf("corePrincipal(kind=service) => Kind = %v, want KindService", p.Kind)
	}
	if !p.FromCore {
		t.Fatalf("corePrincipal() => FromCore = false, want true")
	}

	if err := p.RequireBoard("board-1", "board.admin"); err == nil {
		t.Fatalf("RequireBoard() = nil, want error: a core token with kind=service and no caps must not get board-admin")
	}

	// A native (non-core) service principal must still be unaffected — the
	// blanket bypass is preserved for board's own trusted actors.
	native := Principal{Kind: KindService, Token: &db.Token{ID: "svc-native", Capabilities: nil}}
	if err := native.RequireBoard("board-1", "board.admin"); err != nil {
		t.Fatalf("native RequireBoard() = %v, want nil (native service key must keep its blanket bypass)", err)
	}
}

// TestRequireBoard_CoreServiceKindAllowedWithCapability confirms the fix is
// a capability check, not an outright ban: a core "service"-kind token that
// does carry the required capability is still authorized.
func TestRequireBoard_CoreServiceKindAllowedWithCapability(t *testing.T) {
	p := corePrincipal(coreauth.Synthesized{
		Sub:  "svc-2",
		Kind: "service",
		Caps: []string{"card.write"},
	})
	if err := p.RequireBoard("board-1", "card.write"); err != nil {
		t.Fatalf("RequireBoard() = %v, want nil (token carries the required capability)", err)
	}
	if err := p.RequireBoard("board-1", "board.admin"); err == nil {
		t.Fatalf("RequireBoard() = nil, want error (token lacks board.admin)")
	}
}

// TestRequireBoard_CoreAgentKindStillCapabilityChecked is a regression guard
// that the pre-existing agent-kind path (already correct) keeps working.
func TestRequireBoard_CoreAgentKindStillCapabilityChecked(t *testing.T) {
	p := corePrincipal(coreauth.Synthesized{
		Sub:  "agent-1",
		Kind: "agent",
		Caps: []string{"card.read"},
	})
	if p.Kind != KindAgent {
		t.Fatalf("corePrincipal(kind=agent) => Kind = %v, want KindAgent", p.Kind)
	}
	if err := p.RequireBoard("board-1", "card.write"); err == nil {
		t.Fatalf("RequireBoard() = nil, want error (token lacks card.write)")
	}
	if err := p.RequireBoard("board-1", "card.read"); err != nil {
		t.Fatalf("RequireBoard() = %v, want nil", err)
	}
}

// TestRequireBoardEnforcesCapabilityForCoreHumanToken is the exact scenario
// the spec's adversarial review caught: a core-issued human token must be
// capability-checked like any other core-issued principal, never given a
// blanket bypass just because its Kind claim ("human") maps to KindService.
func TestRequireBoardEnforcesCapabilityForCoreHumanToken(t *testing.T) {
	p := corePrincipal(coreauth.Synthesized{
		Sub:  "human-1",
		Kind: "human",
		Caps: []string{"card.read", "card.write"},
	})
	if err := p.RequireBoard("b1", "card.read"); err != nil {
		t.Errorf("member should have card.read: %v", err)
	}
	if err := p.RequireBoard("b1", "board.admin"); err == nil {
		t.Error("member must NOT have board.admin — this is the bug the spec review caught")
	}
}

// TestIsHuman confirms IsHuman recognizes a core-issued human token (via the
// raw Kind claim preserved in Token.Kind) but not a core-issued non-human
// token that happens to map to the same board Kind (KindService).
func TestIsHuman(t *testing.T) {
	human := corePrincipal(coreauth.Synthesized{Sub: "human-1", Kind: "human"})
	if !human.IsHuman() {
		t.Error("core-issued human token: IsHuman() = false, want true")
	}
	svc := corePrincipal(coreauth.Synthesized{Sub: "svc-1", Kind: "service"})
	if svc.IsHuman() {
		t.Error("core-issued service token: IsHuman() = true, want false")
	}
	agent := corePrincipal(coreauth.Synthesized{Sub: "agent-1", Kind: "agent"})
	if agent.IsHuman() {
		t.Error("core-issued agent token: IsHuman() = true, want false")
	}
}

// TestIsNativeServiceExcludesCoreTokens pins the model that replaces the
// `p.Kind == KindAgent`-only gates (audit C1): the env service key is the
// ONLY blanket-trust principal, and a core-issued token that happens to map
// to KindService (a human "member", say) is never native and never admin
// unless its capability set actually carries board.admin.
func TestIsNativeServiceExcludesCoreTokens(t *testing.T) {
	native := Principal{Kind: KindService, Token: &db.Token{Capabilities: []string{"board.admin"}}}
	core := Principal{Kind: KindService, FromCore: true, Token: &db.Token{Kind: "human", Capabilities: []string{"card.read"}}}
	if !native.IsNativeService() || core.IsNativeService() {
		t.Fatal("IsNativeService must be true only for the env service key")
	}
	if core.IsAdmin() || core.RequireAdmin("b") == nil {
		t.Fatal("a core member must not be admin")
	}
	if !native.IsAdmin() || native.RequireAdmin("b") != nil {
		t.Fatal("native service key is admin")
	}
	// A native service key with NO token row at all (direct-call tests, or a
	// key whose row failed to resolve) is still the env key.
	bare := Principal{Kind: KindService}
	if !bare.IsNativeService() || !bare.IsAdmin() || bare.RequireAdmin("") != nil {
		t.Fatal("a bare native KindService principal is the env key")
	}
}

// TestIsAdminFollowsCapability: admin is a capability, not a Kind. A core
// admin (board.admin in its caps) is admin on every board; a board-scoped
// native agent token holding board.admin is admin on its board only, and
// never instance-wide (RequireAdmin("")).
func TestIsAdminFollowsCapability(t *testing.T) {
	coreAdmin := corePrincipal(coreauth.Synthesized{Sub: "admin-1", Kind: "human",
		Caps: []string{"card.read", "card.write", "column.write", "session.start", "board.admin", "gate.bypass", "membership.write"}})
	if !coreAdmin.IsAdmin() || coreAdmin.RequireAdmin("b1") != nil || coreAdmin.RequireAdmin("") != nil {
		t.Fatal("a core admin carries board.admin and is admin everywhere")
	}
	member := corePrincipal(coreauth.Synthesized{Sub: "member-1", Kind: "human",
		Caps: []string{"card.read", "card.write", "column.write", "session.start"}})
	if member.IsAdmin() || member.RequireAdmin("b1") == nil || member.RequireAdmin("") == nil {
		t.Fatal("a core member is not admin anywhere")
	}
	b1 := "b1"
	scoped := Principal{Kind: KindAgent, Token: &db.Token{BoardID: &b1, Capabilities: []string{"card.read", "board.admin"}}}
	if !scoped.IsAdmin() || scoped.RequireAdmin("b1") != nil {
		t.Fatal("a board-scoped agent token with board.admin is admin on its board")
	}
	if scoped.RequireAdmin("b2") == nil || scoped.RequireAdmin("") == nil {
		t.Fatal("a board-scoped agent token is never admin off its board or instance-wide")
	}
	if (Principal{Kind: KindHuman}).RequireAdmin("b1") == nil {
		t.Fatal("a token-less non-native principal must be rejected, not bypassed")
	}
}

// TestRequireGlobalRead: cross-board reads (overview, global sessions, the
// unfiltered review list) are open to the native key and to any UNSCOPED
// principal carrying card.read; a board-scoped token is refused outright.
func TestRequireGlobalRead(t *testing.T) {
	if err := (Principal{Kind: KindService}).RequireGlobalRead(); err != nil {
		t.Fatalf("native: %v", err)
	}
	member := corePrincipal(coreauth.Synthesized{Sub: "m", Kind: "human", Caps: []string{"card.read"}})
	if err := member.RequireGlobalRead(); err != nil {
		t.Fatalf("unscoped core member with card.read: %v", err)
	}
	noRead := corePrincipal(coreauth.Synthesized{Sub: "m", Kind: "human", Caps: []string{"card.write"}})
	if noRead.RequireGlobalRead() == nil {
		t.Fatal("card.read is required")
	}
	b1 := "b1"
	scoped := Principal{Kind: KindAgent, Token: &db.Token{BoardID: &b1, Capabilities: []string{"card.read", "board.admin"}}}
	if scoped.RequireGlobalRead() == nil {
		t.Fatal("a board-scoped token must not read across boards")
	}
	unscopedAgent := Principal{Kind: KindAgent, Token: &db.Token{Capabilities: []string{"card.read"}}}
	if err := unscopedAgent.RequireGlobalRead(); err != nil {
		t.Fatalf("unscoped agent with card.read: %v", err)
	}
	if (Principal{Kind: KindHuman}).RequireGlobalRead() == nil {
		t.Fatal("a token-less non-native principal must be rejected")
	}
}
