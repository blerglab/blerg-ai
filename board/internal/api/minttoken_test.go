package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// mintResponse is POST /api/tokens's success body.
type mintResponse struct {
	Token struct {
		Capabilities []string `json:"capabilities"`
	} `json:"token"`
	Secret string `json:"secret"`
}

// TestMintToken_MemberCannotEscalate is the regression guard for the
// privilege-escalation hole where handleMintToken's only check was
// `Kind != KindAgent`. A blerg-core "member" token maps to KindService (see
// auth.corePrincipal), so it sailed past that check and could name any
// capability it liked in the request body — including board.admin and
// gate.bypass, neither of which a member holds.
func TestMintToken_MemberCannotEscalate(t *testing.T) {
	srv, _ := testServer(t)

	// Exactly identity.PlatformRoleCaps["member"], as a core token would carry.
	member := testHumanToken(t, []string{"card.read", "card.write", "session.start"})

	resp := request(t, srv, "POST", "/api/tokens", member, nil, map[string]any{
		"label":        "escalate",
		"capabilities": []string{"board.admin", "gate.bypass", "membership.write"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member minting board.admin: %d, want 403", resp.StatusCode)
	}

	// Not even a default-capability mint: minting at all requires board.admin.
	resp2 := request(t, srv, "POST", "/api/tokens", member, nil, map[string]any{"label": "sneaky"})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("member minting with default caps: %d, want 403", resp2.StatusCode)
	}
}

// TestMintToken_AdminClampedToOwnCapabilities proves the clamp applies even to
// a caller that legitimately holds board.admin: it may delegate what it has,
// and nothing more.
func TestMintToken_AdminClampedToOwnCapabilities(t *testing.T) {
	srv, _ := testServer(t)

	admin := testHumanToken(t, []string{"card.read", "card.write", "column.write", "board.admin"})

	// Subset of its own capabilities → allowed.
	resp := request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{
		"label":        "scoped agent",
		"capabilities": []string{"card.read", "card.write"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin minting a subset: %d, want 201", resp.StatusCode)
	}
	var body mintResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Token.Capabilities) != 2 {
		t.Fatalf("minted caps = %v, want the two requested", body.Token.Capabilities)
	}

	// A capability the caller does NOT itself hold → refused, even though the
	// caller is board.admin.
	resp2 := request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{
		"label":        "over-privileged",
		"capabilities": []string{"card.read", "secrets.read"},
	})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("admin minting secrets.read it doesn't hold: %d, want 403", resp2.StatusCode)
	}

	// The default capability set is intersected with the caller's, never
	// expanded past it: this caller holds no gate.bypass, so no minted token
	// ever carries one.
	resp3 := request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "defaults"})
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("admin minting with default caps: %d, want 201", resp3.StatusCode)
	}
	var defBody mintResponse
	if err := json.NewDecoder(resp3.Body).Decode(&defBody); err != nil {
		t.Fatal(err)
	}
	for _, c := range defBody.Token.Capabilities {
		if c == "gate.bypass" || c == "secrets.read" {
			t.Fatalf("default mint leaked %q past the caller's own capabilities: %v", c, defBody.Token.Capabilities)
		}
	}
}
