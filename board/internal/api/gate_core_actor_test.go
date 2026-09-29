package api_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// testCoreServiceToken mints a fake blerg-core-issued Kind:"service" bearer
// token carrying exactly caps — deliberately NOT Kind:"human" (see
// testHumanToken), because IsHuman() bypasses the gate entirely for a
// human-kind core token and would never reach the buggy code path. A
// service-kind (or agent-kind) core token is FromCore but not human, so it
// is gated like any agent token — this is exactly the principal shape that
// used to synthesize "core:"+sub into admission_reviews.actor_token_id (a
// uuid FK) and 500.
func testCoreServiceToken(t *testing.T, caps []string) string {
	t.Helper()
	if corePriv == nil {
		t.Fatal("testCoreServiceToken: no fake core wired — call testServer/testServerHeld first")
	}
	now := time.Now().Unix()
	claims := identity.Claims{
		Sub: "test-service", Aud: "blerg-board", Kind: "service",
		Caps: caps, ExpiresAt: now + 3600,
	}
	hb, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	sig := ed25519.Sign(corePriv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// A core-issued token without gate.bypass on a gate-enabled board used to 500:
// gateCheck passed the synthesized "core:<sub>" Token.ID into admission_reviews.
// actor_token_id (a uuid FK). It must be gated like any agent, never 500.
func TestGatedWriteByCoreTokenDoesNotFiveHundred(t *testing.T) {
	srv, _, board, _, _ := testServerHeld(t) // gate-enabled board, unavailable curator, hold policy
	tok := testCoreServiceToken(t, []string{"card.read", "card.write"})
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/cards", tok, nil,
		map[string]any{"title": "gated by core token"})
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("gated write by a core token returned 500 (uuid actor bug)")
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 202 (held) or 201", resp.StatusCode)
	}
}
