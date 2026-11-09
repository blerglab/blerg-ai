package identity

import (
	"encoding/json"
	"testing"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

func TestArchiveReviewExchangeProof(t *testing.T) {
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "archive-proof", "member")
	for _, proof := range [][]string{nil, {"session.start"}, {"card.read"}} {
		_, raw, err := svc.CreateExchangeToken(t.Context(), acct, "board", "board-a", proof)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := svc.JWKS(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		p, err := cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(p.Claims)
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			PersonBehind bool `json:"person_behind"`
		}
		if err = json.Unmarshal(b, &claims); err != nil {
			t.Fatal(err)
		}
		if claims.PersonBehind != (proof == nil) {
			t.Errorf("proof %v person marker=%v", proof, claims.PersonBehind)
		}
	}
}
