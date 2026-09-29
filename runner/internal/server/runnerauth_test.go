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

// mintRunnerToken signs a core-minted-style token: header {"alg":"EdDSA","kid":kid}
// plus the given claims, exactly like blerg-core would produce.
func mintRunnerToken(t *testing.T, priv ed25519.PrivateKey, kid string, c identity.Claims) string {
	t.Helper()
	hb, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newTestCoreAuthClient starts a fake blerg-core serving jwks/revocations and
// returns a coreauth.Client wired against it (coreauth.New fetches both
// synchronously before returning, so the client is immediately usable).
func newTestCoreAuthClient(t *testing.T, pub ed25519.PublicKey, kid string) *coreauth.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			kid: base64.RawURLEncoding.EncodeToString(pub),
		})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return coreauth.New(srv.URL)
}

// TestAuthRunner_CoreTokenWithoutCapabilityRejected is a regression guard for
// the vulnerability where any valid aud:"blerg-runner" core token was
// authorized with no capability check at all. A token that only asserts the
// audience (no coreAuthRunnerCap) must now be rejected.
func TestAuthRunner_CoreTokenWithoutCapabilityRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := newTestCoreAuthClient(t, pub, "core-1")
	api := &API{runnerKey: "static-runner-key", coreAuth: client}

	now := time.Now().Unix()
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "svc-1", Aud: "blerg-runner", Kind: "service",
		Caps:      []string{"card.read"}, // NOT coreAuthRunnerCap
		ExpiresAt: now + 60,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/runner/start", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()

	if _, ok := api.authRunner(w, req); ok {
		t.Fatalf("authRunner() = true, want false: token lacks %q", coreAuthRunnerCap)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestAuthRunner_CoreTokenWithCapabilityAccepted confirms the fix is a
// capability check, not an outright ban on core tokens: a token that does
// carry coreAuthRunnerCap is still authorized.
func TestAuthRunner_CoreTokenWithCapabilityAccepted(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := newTestCoreAuthClient(t, pub, "core-1")
	api := &API{runnerKey: "static-runner-key", coreAuth: client}

	now := time.Now().Unix()
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "svc-1", Aud: "blerg-runner", Kind: "service",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: now + 60,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/runner/start", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()

	if _, ok := api.authRunner(w, req); !ok {
		t.Fatalf("authRunner() = false, want true: token carries %q", coreAuthRunnerCap)
	}
}

// TestCoreAuthSensitiveCaps_RunnerCapAndSecretsFailClosed confirms
// coreAuthSensitiveCaps flags the runner capability (and secrets.read) as
// sensitive, so identity.Verify's StaleBeyondCeiling branch fails closed on
// them instead of accepting a runner-capable token indefinitely while
// blerg-core's revocation list is stale/unreachable.
func TestCoreAuthSensitiveCaps_RunnerCapAndSecretsFailClosed(t *testing.T) {
	if !coreAuthSensitiveCaps(coreAuthRunnerCap) {
		t.Fatalf("coreAuthSensitiveCaps(%q) = false, want true", coreAuthRunnerCap)
	}
	if !coreAuthSensitiveCaps("secrets.read") {
		t.Fatalf(`coreAuthSensitiveCaps("secrets.read") = false, want true`)
	}
	// Writes fail closed too: a revoked owner must not keep deleting boards
	// or approving rules while core is unreachable.
	if !coreAuthSensitiveCaps("board.admin") {
		t.Fatalf(`coreAuthSensitiveCaps("board.admin") = false, want true`)
	}
	if !coreAuthSensitiveCaps("card.write") {
		t.Fatalf(`coreAuthSensitiveCaps("card.write") = false, want true`)
	}
	if coreAuthSensitiveCaps("card.read") {
		t.Fatalf(`coreAuthSensitiveCaps("card.read") = true, want false`)
	}
}

// TestAuthRunner_StaticKeyUnchanged confirms the static BLERG_RUNNER_KEY path
// is untouched by the core-auth capability check.
func TestAuthRunner_StaticKeyUnchanged(t *testing.T) {
	api := &API{runnerKey: "static-runner-key"}
	req := httptest.NewRequest(http.MethodPost, "/api/runner/start", nil)
	req.Header.Set("Authorization", "Bearer static-runner-key")
	w := httptest.NewRecorder()
	if _, ok := api.authRunner(w, req); !ok {
		t.Fatalf("authRunner() = false, want true for the static runner key")
	}
}
