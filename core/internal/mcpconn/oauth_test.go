package mcpconn_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn/mcptest"
)

const (
	redirectURI = "https://core.example.com/auth/mcp/callback"
	liveSid     = "00000000-0000-4000-8000-0000000000aa"
)

type oauthEnv struct {
	svc  *mcpconn.Service
	pool *pgxpool.Pool
	as   *mcptest.Server
	acct string
}

func loopbackPolicy() netguard.Policy {
	return netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	pool := testPool(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	as := mcptest.New()
	t.Cleanup(as.Close)
	return &oauthEnv{
		svc:  mcpconn.NewService(db.NewPgStore(pool), backend, loopbackPolicy()),
		pool: pool, as: as, acct: newAccount(t, pool),
	}
}

func (e *oauthEnv) start(name string) mcpconn.OAuthStartInput {
	return mcpconn.OAuthStartInput{AccountID: e.acct, SessionID: liveSid, Name: name, URL: e.as.MCPURL(), RedirectURI: redirectURI}
}

func alwaysLive(context.Context, string, string) (bool, error) { return true, nil }

func neverLive(context.Context, string, string) (bool, error) { return false, nil }

// callback builds the callback input the provider's redirect would produce.
func callback(q url.Values, live func(context.Context, string, string) (bool, error)) mcpconn.OAuthCallbackInput {
	_, issPresent := q["iss"]
	return mcpconn.OAuthCallbackInput{
		State: q.Get("state"), Code: q.Get("code"), Iss: q.Get("iss"), IssPresent: issPresent,
		RedirectURI: redirectURI, SessionLive: live,
	}
}

// connect runs the whole flow and returns the stored connection.
func (e *oauthEnv) connect(t *testing.T, name string) mcpconn.Connection {
	t.Helper()
	ctx := context.Background()
	res, err := e.svc.StartOAuth(ctx, e.start(name))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	q, err := e.as.Approve(res.AuthorizationURL)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	c, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	return c
}

func (e *oauthEnv) fetch(t *testing.T, id string) (mcpconn.SecretResult, error) {
	t.Helper()
	return e.svc.FetchSecret(context.Background(), e.acct, id, mcpconn.Proof{SessionID: sessA})
}

func stateOf(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func stateRows(t *testing.T, e *oauthEnv) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM mcp_oauth_state`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	var ce *mcpconn.CallbackError
	if !errors.As(err, &ce) || ce.Reason != reason {
		t.Fatalf("err = %v, want callback reason %q", err, reason)
	}
}

func TestOAuthDiscoveryViaWWWAuthenticateHint(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.Hint = true // the well-known locations 404: only the 401 hint leads to the metadata
	res, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	if err != nil {
		t.Fatal(err)
	}
	if e.as.Hits["/custom-prm"] == 0 {
		t.Fatal("the resource_metadata hint was not used")
	}
	u, _ := url.Parse(res.AuthorizationURL)
	if !strings.HasPrefix(res.AuthorizationURL, e.as.Issuer()+"/authorize?") {
		t.Fatalf("authorization url %q not built from the discovered endpoint", res.AuthorizationURL)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "code_challenge_method": "S256", "redirect_uri": redirectURI,
		"resource": e.as.MCPURL(), "scope": "notes.read", "client_id": "client-1",
	} {
		if k == "client_id" {
			if q.Get(k) == "" {
				t.Error("client_id missing")
			}
			continue
		}
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("missing challenge/state: %v", q)
	}
}

func TestOAuthDiscoveryViaWellKnownAndDCR(t *testing.T) {
	e := newOAuthEnv(t)
	res, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	if err != nil {
		t.Fatal(err)
	}
	if e.as.Hits["/.well-known/oauth-protected-resource/mcp"] == 0 {
		t.Fatalf("path-suffixed well-known not fetched: %v", e.as.Hits)
	}
	if len(e.as.Registered) != 1 {
		t.Fatalf("registrations = %d, want 1", len(e.as.Registered))
	}
	reg := e.as.Registered[0]
	uris, _ := reg["redirect_uris"].([]any)
	if len(uris) != 1 || uris[0] != redirectURI || reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("registration = %v", reg)
	}
	u, _ := url.Parse(res.AuthorizationURL)
	if got := u.Query().Get("scope"); got != "notes.read notes.write" {
		t.Errorf("scope = %q, want the server's scopes_supported", got)
	}

	// The state row: keyed by SHA-256(state), single-use material for this account and session.
	state := stateOf(t, res.AuthorizationURL)
	sum := sha256.Sum256([]byte(state))
	var sid, issuer, resource, redirect, verifier string
	var expires time.Time
	var acct string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT account_id::text, sid, issuer, resource, redirect_uri, code_verifier, expires_at FROM mcp_oauth_state WHERE state_hash = $1`,
		sum[:]).Scan(&acct, &sid, &issuer, &resource, &redirect, &verifier, &expires); err != nil {
		t.Fatalf("state row: %v", err)
	}
	if acct != e.acct || sid != liveSid || issuer != e.as.Issuer() || resource != e.as.MCPURL() || redirect != redirectURI || len(verifier) < 43 {
		t.Fatalf("row = %s %s %s %s %s %d", acct, sid, issuer, resource, redirect, len(verifier))
	}
	if d := time.Until(expires); d < 9*time.Minute || d > 10*time.Minute+5*time.Second {
		t.Fatalf("expiry in %v, want ~10 minutes", d)
	}
	if res.StateCookie != hex.EncodeToString(sum[:]) {
		t.Fatalf("state cookie %q is not hex SHA-256(state)", res.StateCookie)
	}
	if strings.Contains(res.StateCookie, state) {
		t.Fatal("the cookie must not hold the state itself")
	}
}

func TestOAuthHappyPath(t *testing.T) {
	e := newOAuthEnv(t)
	c := e.connect(t, "notes")
	if c.AuthKind != "oauth" || c.Status != "ok" || c.Name != "notes" || !c.HasSecret || c.URL != e.as.MCPURL() {
		t.Fatalf("connection = %+v", c)
	}
	form := e.as.LastTokenForm("authorization_code")
	if form.Get("resource") != e.as.MCPURL() || form.Get("redirect_uri") != redirectURI || form.Get("code_verifier") == "" {
		t.Fatalf("token request = %v", form)
	}
	if stateRows(t, e) != 0 {
		t.Fatal("the state row must be consumed")
	}

	// oauth_meta carries endpoints and the client id but never a secret; the bundle is encrypted.
	var meta []byte
	var ct []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT oauth_meta, secret_ciphertext FROM mcp_connections WHERE id = $1`, c.ID).Scan(&meta, &ct); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"at-", "rt-", "client-secret", "code-"} {
		if bytes.Contains(meta, []byte(secret)) || bytes.Contains(ct, []byte(secret)) {
			t.Errorf("%q leaked into oauth_meta or plaintext ciphertext", secret)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil || m["issuer"] != e.as.Issuer() || m["client_id"] != "client-1" || m["resource"] != e.as.MCPURL() {
		t.Fatalf("oauth_meta = %s %v", meta, err)
	}
	pub, _ := json.Marshal(c)
	for _, secret := range []string{"at-", "rt-", "client-secret"} {
		if bytes.Contains(pub, []byte(secret)) {
			t.Errorf("public view leaks %q: %s", secret, pub)
		}
	}

	res, err := e.fetch(t, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.HeaderName != "Authorization" || !strings.HasPrefix(res.Value, "Bearer at-") || res.ExpiresAt == nil || res.URL != e.as.MCPURL() {
		t.Fatalf("secret = %+v", res)
	}
	if time.Until(*res.ExpiresAt) < 50*time.Minute {
		t.Fatalf("expiry %v", res.ExpiresAt)
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM mcp_secret_access_log WHERE connection_id = $1`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d %v", n, err)
	}
	// The bundle is decryptable by the key backend only: a fetch does not refresh a fresh token.
	if sp, _, _, _ := e.as.Stats(); sp != 0 {
		t.Fatalf("refreshed a fresh token (%d)", sp)
	}
}

func TestOAuthUserSuppliedClientID(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.NoRegistration = true
	e.as.AddClient("preset-client")

	_, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	wantValidation(t, err, "client_id")

	in := e.start("notes")
	in.ClientID = "preset-client"
	res, err := e.svc.StartOAuth(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.as.Registered) != 0 {
		t.Fatal("registered a client although one was supplied")
	}
	q, err := e.as.Approve(res.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.CompleteOAuth(context.Background(), callback(q, alwaysLive))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.fetch(t, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthRefusesServerWithoutS256(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.NoPKCE = true
	_, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	wantValidation(t, err, "PKCE")
	if stateRows(t, e) != 0 {
		t.Fatal("a state row was stored for a refused server")
	}
}

func TestOAuthStateIsSingleUse(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, err := e.svc.StartOAuth(ctx, e.start("notes"))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := e.as.Approve(res.AuthorizationURL)
	if _, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive)); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	wantReason(t, err, "state")
	if l, _ := e.svc.List(ctx, e.acct); len(l) != 1 {
		t.Fatalf("a replay created a second connection: %d", len(l))
	}
}

func TestOAuthExpiredStateRefusedAndConsumed(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_oauth_state SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	wantReason(t, err, "state")
	if stateRows(t, e) != 0 {
		t.Fatal("expired row not removed")
	}
	if l, _ := e.svc.List(ctx, e.acct); len(l) != 0 {
		t.Fatal("connection created from an expired state")
	}
	if f := e.as.LastTokenForm("authorization_code"); f != nil {
		t.Fatal("the code was exchanged although the state had expired")
	}
}

func TestOAuthUnknownStateRefused(t *testing.T) {
	e := newOAuthEnv(t)
	_, err := e.svc.CompleteOAuth(context.Background(), mcpconn.OAuthCallbackInput{
		State: "nope", Code: "x", RedirectURI: redirectURI, SessionLive: alwaysLive,
	})
	wantReason(t, err, "state")
}

func TestOAuthDeadLoginSessionRefused(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	var gotAcct, gotSid string
	live := func(_ context.Context, acct, sid string) (bool, error) {
		gotAcct, gotSid = acct, sid
		return false, nil
	}
	_, err := e.svc.CompleteOAuth(ctx, callback(q, live))
	wantReason(t, err, "session")
	if gotAcct != e.acct || gotSid != liveSid {
		t.Fatalf("liveness asked for %s/%s, want the recorded account and sid", gotAcct, gotSid)
	}
	if f := e.as.LastTokenForm("authorization_code"); f != nil {
		t.Fatal("exchanged a code for a dead session")
	}
	if stateRows(t, e) != 0 {
		t.Fatal("state must still be consumed")
	}
	if _, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive)); err == nil {
		t.Fatal("the consumed state was usable again")
	}
}

func TestOAuthIssChecks(t *testing.T) {
	ctx := context.Background()
	t.Run("mismatched iss", func(t *testing.T) {
		e := newOAuthEnv(t)
		res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
		q, _ := e.as.Approve(res.AuthorizationURL)
		q.Set("iss", "https://evil.example/as")
		_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
		wantReason(t, err, "issuer")
		if f := e.as.LastTokenForm("authorization_code"); f != nil {
			t.Fatal("exchanged a code whose iss did not match")
		}
	})
	t.Run("missing iss when advertised", func(t *testing.T) {
		e := newOAuthEnv(t)
		e.as.AdvertiseIss = true
		res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
		q, _ := e.as.Approve(res.AuthorizationURL)
		q.Del("iss")
		_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
		wantReason(t, err, "issuer")
	})
	t.Run("matching iss accepted", func(t *testing.T) {
		e := newOAuthEnv(t)
		e.as.AdvertiseIss = true
		e.connect(t, "notes")
	})
	t.Run("iss present though not advertised must still match", func(t *testing.T) {
		e := newOAuthEnv(t)
		res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
		q, _ := e.as.Approve(res.AuthorizationURL)
		q.Set("iss", "https://evil.example/as")
		_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
		wantReason(t, err, "issuer")
	})
	t.Run("missing iss tolerated when not advertised", func(t *testing.T) {
		e := newOAuthEnv(t)
		e.connect(t, "notes")
	})
}

func TestOAuthRedirectURIMustMatchExactly(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	in := callback(q, alwaysLive)
	in.RedirectURI = redirectURI + "/"
	_, err := e.svc.CompleteOAuth(ctx, in)
	wantReason(t, err, "redirect_uri")
}

func TestOAuthPKCEVerifierMismatchFailsAtProvider(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_oauth_state SET code_verifier = repeat('z', 64)`); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	wantReason(t, err, "exchange")
	if l, _ := e.svc.List(ctx, e.acct); len(l) != 0 {
		t.Fatal("connection created although PKCE failed")
	}
}

func TestOAuthProviderErrorConsumesStateAndIsNotReflected(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	in := callback(q, alwaysLive)
	in.Code, in.ProviderError = "", true
	_, err := e.svc.CompleteOAuth(ctx, in)
	wantReason(t, err, "denied")
	if stateRows(t, e) != 0 {
		t.Fatal("state not consumed")
	}
}

func TestOAuthNameTakenAndLimitAtCallback(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	q, _ := e.as.Approve(res.AuthorizationURL)
	// Someone takes the name while the person is at the provider.
	if _, err := e.svc.Create(ctx, e.acct, mcpconn.CreateInput{Name: "notes", URL: goodURL, AuthKind: "none"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	wantReason(t, err, "name_taken")
}

func TestOAuthStartValidatesLikeCreate(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	cases := map[string]func(*mcpconn.OAuthStartInput){
		"bad name":     func(in *mcpconn.OAuthStartInput) { in.Name = "Bad Name" },
		"reserved":     func(in *mcpconn.OAuthStartInput) { in.Name = "board" },
		"query string": func(in *mcpconn.OAuthStartInput) { in.URL += "?key=SECRET" },
		"fragment":     func(in *mcpconn.OAuthStartInput) { in.URL += "#x" },
		"userinfo":     func(in *mcpconn.OAuthStartInput) { in.URL = "http://u:p@127.0.0.1/mcp" },
		"bad scope":    func(in *mcpconn.OAuthStartInput) { in.Scopes = `a "b"` },
		"long scope":   func(in *mcpconn.OAuthStartInput) { in.Scopes = strings.Repeat("a ", 600) },
		"no sid":       func(in *mcpconn.OAuthStartInput) { in.SessionID = "" },
		"no redirect":  func(in *mcpconn.OAuthStartInput) { in.RedirectURI = "" },
	}
	for name, mut := range cases {
		in := e.start("notes")
		mut(&in)
		var ve *mcpconn.ValidationError
		if _, err := e.svc.StartOAuth(ctx, in); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want validation error", name, err)
		}
	}
	if _, err := e.svc.Create(ctx, e.acct, mcpconn.CreateInput{Name: "notes", URL: goodURL, AuthKind: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartOAuth(ctx, e.start("notes")); !errors.Is(err, mcpconn.ErrNameTaken) {
		t.Errorf("duplicate name = %v, want ErrNameTaken", err)
	}
	if stateRows(t, e) != 0 {
		t.Fatal("a refused start stored state")
	}
}

func TestOAuthSSRFDiscoveryRefused(t *testing.T) {
	ctx := context.Background()
	t.Run("literal private address as the mcp url", func(t *testing.T) {
		e := newOAuthEnv(t)
		svc := mcpconn.NewService(db.NewPgStore(e.pool), mustBackend(t), netguard.Policy{})
		in := e.start("notes")
		in.URL = "https://10.0.0.5/mcp" // scrub:allow (a range the guard must refuse)
		_, err := svc.StartOAuth(ctx, in)
		wantValidation(t, err, "not permitted")
	})
	t.Run("name resolving to loopback is refused at connect time", func(t *testing.T) {
		e := newOAuthEnv(t)
		svc := mcpconn.NewService(db.NewPgStore(e.pool), mustBackend(t), netguard.Policy{AllowHTTPHosts: []string{"localhost"}})
		in := e.start("notes")
		in.URL = strings.Replace(e.as.MCPURL(), "127.0.0.1", "localhost", 1)
		_, err := svc.StartOAuth(ctx, in)
		if err == nil {
			t.Fatal("discovery reached a loopback address through a name")
		}
		if n := e.as.Hits["/mcp"]; n != 0 {
			t.Fatalf("the fake server was contacted %d times", n)
		}
	})
	t.Run("metadata pointing at a private authorization server", func(t *testing.T) {
		e := newOAuthEnv(t)
		e.as.PRMAuthServer = "https://10.0.0.5/as" // scrub:allow (a range the guard must refuse)
		_, err := e.svc.StartOAuth(ctx, e.start("notes"))
		wantValidation(t, err, "not permitted")
	})
}

func mustBackend(t *testing.T) keybackend.Backend {
	t.Helper()
	b, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOAuthRedirectsAreNotFollowed(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.RedirectWellKnown = "/elsewhere"
	_, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	if err == nil {
		t.Fatal("discovery followed a redirect")
	}
	if n := e.as.Hits["/elsewhere"]; n != 0 {
		t.Fatalf("the redirect target was fetched %d times", n)
	}
}

func TestOAuthResourceMismatchRefused(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.PRMResource = "https://other.example/mcp"
	_, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	wantValidation(t, err, "resource")
}

func TestOAuthConcurrentRefreshSpendsRotatingTokenOnce(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.SetAccessTTL(30) // the initial token is inside the 60 s window: the next fetch must refresh
	c := e.connect(t, "notes")
	e.as.SetAccessTTL(3600) // what the refresh will hand out
	e.as.Lock()
	e.as.RefreshDelay = 150 * time.Millisecond
	e.as.Unlock()
	spentBefore, _, _, _ := e.as.Stats()

	const callers = 8
	var wg sync.WaitGroup
	values := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.fetch(t, c.ID)
			values[i], errs[i] = r.Value, err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if values[i] != values[0] {
			t.Fatalf("callers got different tokens: %q vs %q", values[i], values[0])
		}
	}
	spent, reuse, _, _ := e.as.Stats()
	if spent-spentBefore != 1 || reuse != 0 {
		t.Fatalf("refresh token spent %d times (reuse attempts %d), want exactly once", spent-spentBefore, reuse)
	}
}

func TestOAuthRefreshFailureNeedsAuth(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	e.as.SetAccessTTL(30)
	c := e.connect(t, "notes")
	e.as.Lock()
	e.as.RefreshStatus = 400
	e.as.Unlock()

	if _, err := e.fetch(t, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("fetch after a rejected refresh = %v, want ErrNotFound (the uniform 404)", err)
	}
	got, err := e.svc.Get(ctx, e.acct, c.ID)
	if err != nil || got.Status != "needs_auth" {
		t.Fatalf("status = %q %v, want needs_auth", got.Status, err)
	}
	// Once needs_auth, the provider is not asked again and nothing is decrypted.
	before := len(e.as.TokenForms)
	if _, err := e.fetch(t, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("second fetch = %v", err)
	}
	if len(e.as.TokenForms) != before {
		t.Fatal("a needs_auth connection hit the token endpoint again")
	}
	// The internal list still shows it, with its status.
	list, _ := e.svc.List(ctx, e.acct)
	if len(list) != 1 || list[0].Status != "needs_auth" {
		t.Fatalf("list = %+v", list)
	}
}

func TestOAuthTransientRefreshFailureKeepsConnection(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	e.as.SetAccessTTL(30) // valid for 30 s, inside the refresh window
	c := e.connect(t, "notes")
	e.as.Lock()
	e.as.RefreshStatus = 500
	e.as.Unlock()

	// Still unexpired: the current token is handed out, the connection is not condemned.
	res, err := e.fetch(t, c.ID)
	if err != nil || !strings.HasPrefix(res.Value, "Bearer at-") {
		t.Fatalf("fetch = %+v %v, want the still-valid token", res, err)
	}
	if got, _ := e.svc.Get(ctx, e.acct, c.ID); got.Status != "ok" {
		t.Fatalf("status = %q after a transient failure, want ok", got.Status)
	}
}

func TestOAuthExpiredTokenAndTransientFailureIsUnavailable(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	e.as.SetAccessTTL(1)
	c := e.connect(t, "notes")
	time.Sleep(1200 * time.Millisecond)
	e.as.Lock()
	e.as.RefreshStatus = 503
	e.as.Unlock()
	_, err := e.fetch(t, c.ID)
	if !errors.Is(err, mcpconn.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got, _ := e.svc.Get(ctx, e.acct, c.ID); got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
}

func TestOAuthConfidentialClientSecretSurvivesRefresh(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.Confidential = true
	e.as.SetAccessTTL(30)
	c := e.connect(t, "notes")
	e.as.SetAccessTTL(3600)
	res, err := e.fetch(t, c.ID) // refresh authenticates with the stored client secret (the fake enforces it)
	if err != nil {
		t.Fatalf("refresh with a confidential client: %v", err)
	}
	if sp, _, _, _ := e.as.Stats(); sp != 1 {
		t.Fatalf("spent = %d", sp)
	}
	if !strings.HasPrefix(res.Value, "Bearer ") {
		t.Fatal(res.Value)
	}
	var ct, meta []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT secret_ciphertext, oauth_meta FROM mcp_connections WHERE id = $1`, c.ID).Scan(&ct, &meta)
	if bytes.Contains(ct, []byte("client-secret")) || bytes.Contains(meta, []byte("client-secret")) {
		t.Fatal("client secret stored in the clear")
	}
	var state int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM mcp_oauth_state WHERE draft::text LIKE '%client-secret%'`).Scan(&state)
	if state != 0 {
		t.Fatal("client secret left in the clear in a state row")
	}
}

func TestOAuthDeleteRevokesUpstream(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	c := e.connect(t, "notes")
	if err := e.svc.Delete(ctx, e.acct, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, revokes, _ := e.as.Stats(); revokes == 0 {
		t.Fatal("no revocation reached the provider")
	}
	e.as.Lock()
	first := e.as.Revoked[0]
	e.as.Unlock()
	if !strings.HasPrefix(first.Get("token"), "rt-") || first.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revocation = %v, want the refresh token first", first)
	}
	if _, err := e.svc.Get(ctx, e.acct, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("connection survived delete: %v", err)
	}
}

func TestOAuthDeleteSucceedsWhenRevocationFails(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	c := e.connect(t, "notes")
	e.as.Lock()
	e.as.RevokeStatus = 500
	e.as.Unlock()
	if err := e.svc.Delete(ctx, e.acct, c.ID); err != nil {
		t.Fatalf("delete must not depend on the provider: %v", err)
	}
	if _, err := e.svc.Get(ctx, e.acct, c.ID); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatal("not deleted")
	}
}

func TestOAuthReconnectReplacesTokens(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	c := e.connect(t, "notes")
	tools := json.RawMessage(`{"search":{"mode":"allow","hash":"h1"}}`)
	if _, err := e.svc.Patch(ctx, e.acct, c.ID, mcpconn.PatchInput{DefaultTools: tools}); err != nil {
		t.Fatal(err)
	}
	old, _ := e.fetch(t, c.ID)
	// The grant goes bad.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET status = 'needs_auth' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}

	in := e.start("ignored")
	in.Name, in.URL, in.ReconnectID = "", "", c.ID
	res, err := e.svc.StartOAuth(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.as.Registered) != 1 {
		t.Fatalf("reconnect registered a new client (%d); the known client should be reused", len(e.as.Registered))
	}
	q, _ := e.as.Approve(res.AuthorizationURL)
	got, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.Status != "ok" || got.Name != "notes" || !strings.Contains(string(got.DefaultTools), "search") {
		t.Fatalf("reconnected = %+v", got)
	}
	fresh, err := e.fetch(t, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Value == old.Value {
		t.Fatal("tokens were not replaced")
	}
	if l, _ := e.svc.List(ctx, e.acct); len(l) != 1 {
		t.Fatalf("reconnect created a second connection: %d", len(l))
	}
}

func TestOAuthReconnectRefusedForOtherAccountAndNonOAuth(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	c := e.connect(t, "notes")
	other := newAccount(t, e.pool)
	in := mcpconn.OAuthStartInput{AccountID: other, SessionID: liveSid, ReconnectID: c.ID, RedirectURI: redirectURI}
	if _, err := e.svc.StartOAuth(ctx, in); !errors.Is(err, mcpconn.ErrNotFound) {
		t.Fatalf("other account's reconnect = %v, want ErrNotFound", err)
	}
	st, err := e.svc.Create(ctx, e.acct, mcpconn.CreateInput{Name: "plain", URL: goodURL, AuthKind: "none"})
	if err != nil {
		t.Fatal(err)
	}
	in = mcpconn.OAuthStartInput{AccountID: e.acct, SessionID: liveSid, ReconnectID: st.ID, RedirectURI: redirectURI}
	_, err = e.svc.StartOAuth(ctx, in)
	wantValidation(t, err, "oauth")
}

func TestOAuthStartRateLimit(t *testing.T) {
	e := newOAuthEnv(t)
	e.svc.SetOAuthStartLimit(3, time.Minute)
	ctx := context.Background()
	for i := range 3 {
		if _, err := e.svc.StartOAuth(ctx, e.start("notes")); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if _, err := e.svc.StartOAuth(ctx, e.start("notes")); !errors.Is(err, mcpconn.ErrRateLimited) {
		t.Fatalf("4th start = %v, want ErrRateLimited", err)
	}
	// Per account.
	other := e.start("notes")
	other.AccountID = newAccount(t, e.pool)
	if _, err := e.svc.StartOAuth(ctx, other); err != nil {
		t.Fatalf("another account was limited: %v", err)
	}
}

func TestOAuthStatePrunedWhenExpired(t *testing.T) {
	e := newOAuthEnv(t)
	ctx := context.Background()
	if _, err := e.svc.StartOAuth(ctx, e.start("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartOAuth(ctx, e.start("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_oauth_state SET expires_at = now() - interval '1 minute' WHERE state_hash = (SELECT state_hash FROM mcp_oauth_state LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.PruneOAuthState(ctx)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v, want 1", n, err)
	}
	if stateRows(t, e) != 1 {
		t.Fatal("the live row was pruned")
	}
}

func TestOAuthNeverLogsSecrets(t *testing.T) {
	e := newOAuthEnv(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	e.as.Confidential = true
	e.as.SetAccessTTL(30)
	ctx := context.Background()
	res, _ := e.svc.StartOAuth(ctx, e.start("notes"))
	state := stateOf(t, res.AuthorizationURL)
	q, _ := e.as.Approve(res.AuthorizationURL)
	c, err := e.svc.CompleteOAuth(ctx, callback(q, alwaysLive))
	if err != nil {
		t.Fatal(err)
	}
	var verifier string
	_ = e.pool.QueryRow(ctx, `SELECT 'x'`).Scan(&verifier)
	e.as.Lock()
	e.as.RefreshStatus = 400
	e.as.Unlock()
	_, _ = e.fetch(t, c.ID) // failing refresh logs
	_ = e.svc.Delete(ctx, e.acct, c.ID)
	_, _ = e.svc.CompleteOAuth(ctx, callback(q, neverLive)) // replay logs
	for _, secret := range []string{"at-", "rt-", "code-", "client-secret", state, q.Get("code"), "code_verifier"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q:\n%s", secret, logs.String())
		}
	}
}
