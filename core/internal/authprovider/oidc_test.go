package authprovider_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

// fakeOIDCServer wires up a real discovery document + JWKS endpoint (via
// go-oidc's own oidctest.Server, so the exact JSON shape go-oidc expects is
// produced by the library itself, not guessed at here) plus a hand-rolled
// /token endpoint returning a real signed id_token. This means
// go-oidc's actual verification code path — fetching the JWKS, checking the
// signature, iss, aud, exp — genuinely runs end-to-end in every test below,
// rather than being mocked out.
//
// idToken is the raw (already-signed) id_token string the /token endpoint
// should hand back; tests that want a forged/malformed token construct one
// themselves and pass it in.
type fakeOIDCServer struct {
	srv     *httptest.Server
	idToken string
}

func newFakeOIDCServer(t *testing.T, priv *rsa.PrivateKey, keyID string) *fakeOIDCServer {
	t.Helper()
	oidcSrv := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{
			{PublicKey: priv.Public(), KeyID: keyID, Algorithm: oidc.RS256},
		},
	}

	f := &fakeOIDCServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-123",
			"token_type":   "Bearer",
			"id_token":     f.idToken,
		})
	})
	// Everything else (discovery + JWKS) is handled by go-oidc's own test
	// server.
	mux.Handle("/", oidcSrv)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oidcSrv.SetIssuer(srv.URL)
	f.srv = srv
	return f
}

// signClaims builds a real signed id_token for issuer/clientID/subject/email
// using go-oidc's own test-signing helper, so the token this fake IdP hands
// back is exactly the kind of thing a real IdP would produce.
func signClaims(priv *rsa.PrivateKey, keyID, issuer, clientID, subject, email string) string {
	claims := fmt.Sprintf(`{
		"iss": %q,
		"aud": %q,
		"sub": %q,
		"exp": %d,
		"iat": %d,
		"email": %q,
		"email_verified": true
	}`, issuer, clientID, subject, time.Now().Add(time.Hour).Unix(), time.Now().Unix(), email)
	return oidctest.SignIDToken(priv, keyID, oidc.RS256, claims)
}

func callbackWithCode() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=y", nil)
}

func TestOIDCCallbackCreatesMemberAccount(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")
	f.idToken = signClaims(priv, "key-1", f.srv.URL, clientID, "user-42", "someone@example.com")

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	acc, err := provider.Callback(ctx, callbackWithCode())
	if err != nil {
		t.Fatalf("expected valid id_token to be accepted, got err: %v", err)
	}
	if acc.Provider != "oidc" {
		t.Errorf("expected Provider %q, got %q", "oidc", acc.Provider)
	}
	if acc.ProviderSubject != "user-42" {
		t.Errorf("expected ProviderSubject %q, got %q", "user-42", acc.ProviderSubject)
	}
	if acc.Email != "someone@example.com" {
		t.Errorf("expected Email %q, got %q", "someone@example.com", acc.Email)
	}
	if acc.ID == "" {
		t.Error("expected non-empty Account.ID")
	}
}

func TestOIDCCallbackUpsertsOnRepeatLogin(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")
	f.idToken = signClaims(priv, "key-1", f.srv.URL, clientID, "user-42", "someone@example.com")

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	acc1, err := provider.Callback(ctx, callbackWithCode())
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	acc2, err := provider.Callback(ctx, callbackWithCode())
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if acc1.ID != acc2.ID {
		t.Errorf("expected same account id across repeat logins, got %q then %q", acc1.ID, acc2.ID)
	}
}

func TestOIDCCallbackDefaultsRoleToMember(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")
	f.idToken = signClaims(priv, "key-1", f.srv.URL, clientID, "user-42", "someone@example.com")

	pool, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	if _, err := provider.Callback(ctx, callbackWithCode()); err != nil {
		t.Fatalf("callback: %v", err)
	}

	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM accounts WHERE provider='oidc' AND provider_subject='user-42'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "member" {
		t.Errorf("expected role %q, got %q", "member", role)
	}
}

// TestOIDCCallbackRejectsForgedSignature proves go-oidc's real signature
// verification is actually wired in: it signs the id_token with a DIFFERENT
// private key than the one advertised in the fake IdP's JWKS, so the
// signature cannot validate against the public key the verifier fetches.
// This is the single most important test in this file — if it passes for
// the wrong reason (e.g. verification silently skipped), a forged token
// would be accepted in production.
func TestOIDCCallbackRejectsForgedSignature(t *testing.T) {
	const clientID = "my-client-id"
	realKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forgedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// The fake IdP only ever advertises realKey's public half in its JWKS.
	f := newFakeOIDCServer(t, realKey, "key-1")
	// But the token handed back is signed with a key the IdP never
	// published — same "kid", different actual key material, simulating a
	// forged/tampered token.
	f.idToken = signClaims(forgedKey, "key-1", f.srv.URL, clientID, "user-42", "someone@example.com")

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	if _, err := provider.Callback(ctx, callbackWithCode()); err == nil {
		t.Error("expected a forged id_token signature to be rejected, but Callback succeeded")
	}
}

// TestOIDCCallbackRejectsWrongAudience proves the verifier checks "aud"
// against this provider's own clientID, not just any valid signature.
func TestOIDCCallbackRejectsWrongAudience(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")
	f.idToken = signClaims(priv, "key-1", f.srv.URL, "someone-elses-client-id", "user-42", "someone@example.com")

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	if _, err := provider.Callback(ctx, callbackWithCode()); err == nil {
		t.Error("expected an id_token issued for a different audience to be rejected")
	}
}

// TestOIDCCallbackRejectsExpiredToken proves the verifier enforces "exp".
func TestOIDCCallbackRejectsExpiredToken(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")

	claims := `{
		"iss": "` + f.srv.URL + `",
		"aud": "` + clientID + `",
		"sub": "user-42",
		"exp": ` + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10) + `,
		"iat": ` + strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10) + `,
		"email": "someone@example.com"
	}`
	f.idToken = oidctest.SignIDToken(priv, "key-1", oidc.RS256, claims)

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	if _, err := provider.Callback(ctx, callbackWithCode()); err == nil {
		t.Error("expected an expired id_token to be rejected")
	}
}

// TestOIDCCallbackRejectsMissingSubClaim proves Callback fails closed when
// a validly-signed token nonetheless has no sub claim.
func TestOIDCCallbackRejectsMissingSubClaim(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")

	claims := `{
		"iss": "` + f.srv.URL + `",
		"aud": "` + clientID + `",
		"exp": ` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `,
		"iat": ` + strconv.FormatInt(time.Now().Unix(), 10) + `,
		"email": "someone@example.com"
	}`
	f.idToken = oidctest.SignIDToken(priv, "key-1", oidc.RS256, claims)

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	if _, err := provider.Callback(ctx, callbackWithCode()); err == nil {
		t.Error("expected an id_token with no sub claim to be rejected")
	}
}

func TestOIDCCallbackMissingCode(t *testing.T) {
	const clientID = "my-client-id"
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOIDCServer(t, priv, "key-1")

	_, st := freshStore(t)
	ctx := context.Background()
	provider, err := authprovider.NewOIDC(ctx, st, f.srv.URL, clientID, "csecret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=y", nil)
	if _, err := provider.Callback(ctx, req); err == nil {
		t.Error("expected missing code to be rejected")
	}
}

func TestNewOIDCFailsFastOnUnreachableIssuer(t *testing.T) {
	// Nothing is listening on this port (server never started / already
	// closed), so discovery must fail immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	badURL := srv.URL
	srv.Close()

	if _, err := authprovider.NewOIDC(context.Background(), nil, badURL, "cid", "csecret"); err == nil {
		t.Error("expected NewOIDC to fail fast when the issuer's discovery document is unreachable")
	}
}
