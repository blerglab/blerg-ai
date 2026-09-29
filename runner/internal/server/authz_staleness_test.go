package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
)

// newStaleCoreAuthClient is newTestCoreAuthClient with a blerg-core whose
// revocation feed never answers: the client's revocation snapshot is never
// taken, so RevocationChecker.StaleBeyondCeiling() is true from the start —
// the same state a live runner reaches once core has been unreachable for
// longer than the staleness ceiling.
func newStaleCoreAuthClient(t *testing.T, pub ed25519.PublicKey, kid string) *coreauth.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			kid: base64.RawURLEncoding.EncodeToString(pub),
		})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "core down", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := coreauth.New(srv.URL)
	if !c.StaleBeyondCeiling() {
		t.Fatal("test setup: client is not stale past the ceiling")
	}
	return c
}

// TestAuthz_StaleRevocations_WritesFailClosed: once the revocation snapshot is
// past its ceiling, a token carrying board.admin or card.write is refused on
// the write gates even though its signature is valid — a revoked owner's
// token must not keep deleting boards or enabling rules while core is down.
// A token whose ONLY capability is card.read keeps reading (a core outage
// degrades such a token to read-only); any realistic member/admin token also
// carries session.start, a sensitive capability, so it is refused outright —
// writes and reads — once stale, since identity.Verify's staleness check
// runs over the token's full capability list.
func TestAuthz_StaleRevocations_WritesFailClosed(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{coreAuth: newStaleCoreAuthClient(t, pub, "core-1")}
	mint := func(caps ...string) string {
		return mintRunnerToken(t, priv, "core-1", identity.Claims{
			Sub: "owner-1", Aud: coreAuthAudience, Kind: "human",
			Caps: caps, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		})
	}
	req := func(tok string) *http.Request {
		r := httptest.NewRequest(http.MethodDelete, "/api/boards/b1", nil)
		r.SetPathValue("id", "b1")
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}

	admin := mint("card.read", "card.write", "board.admin")

	t.Run("board.admin via authBrowserOrBoard", func(t *testing.T) {
		w := httptest.NewRecorder()
		if _, ok := api.authBrowserOrBoard(w, req(admin), "b1", "board.admin"); ok {
			t.Fatal("stale revocations: board.admin token accepted, want refused")
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("board.admin via authDaemonOrCoreCap (rules)", func(t *testing.T) {
		w := httptest.NewRecorder()
		if _, ok := api.authDaemonOrCoreCap(w, req(admin), "board.admin"); ok {
			t.Fatal("stale revocations: board.admin token accepted on rules gate, want refused")
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("card.write via authBrowserOrBoard", func(t *testing.T) {
		w := httptest.NewRecorder()
		if _, ok := api.authBrowserOrBoard(w, req(mint("card.read", "card.write")), "b1", "card.write"); ok {
			t.Fatal("stale revocations: card.write token accepted, want refused")
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("HandleDeleteBoard end to end", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.HandleDeleteBoard(w, req(admin))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("DELETE /api/boards with stale revocations → %d, want 401", w.Code)
		}
	})

	t.Run("card.read still readable", func(t *testing.T) {
		w := httptest.NewRecorder()
		actor, ok := api.authBrowserOrBoard(w, req(mint("card.read")), "b1", "card.read")
		if !ok {
			t.Fatalf("stale revocations: card.read token refused with %d, want accepted", w.Code)
		}
		if actor.Core == nil {
			t.Fatal("expected a core principal")
		}
	})
}
