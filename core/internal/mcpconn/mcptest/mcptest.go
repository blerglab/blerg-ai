// Package mcptest is a fake MCP server and OAuth authorization server for tests: one loopback
// HTTP listener that serves an MCP endpoint answering 401, the protected-resource and
// authorization-server metadata documents, dynamic client registration, an authorization code
// grant with PKCE S256, a rotating refresh token and RFC 7009 revocation. It records what it
// was asked so a test can assert on the exact requests core made. Tests reach it through a
// netguard policy that lists 127.0.0.1 as an allowed http and private host.
package mcptest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Server is the fake. Set the option fields before the first request (they are read under mu).
type Server struct {
	*httptest.Server

	mu sync.Mutex

	// Hint makes the MCP endpoint's 401 carry a resource_metadata hint (at /custom-prm) and a
	// scope; otherwise the plain 401 forces discovery through the well-known locations.
	Hint bool
	// PRMResource overrides the "resource" the protected-resource metadata claims.
	PRMResource string
	// AdvertiseIss sets authorization_response_iss_parameter_supported in the AS metadata.
	AdvertiseIss bool
	// NoRegistration removes the registration_endpoint from the AS metadata.
	NoRegistration bool
	// NoPKCE removes code_challenge_methods_supported.
	NoPKCE bool
	// Confidential makes dynamic registration hand out a client secret (client_secret_basic).
	Confidential bool
	// AccessTTL is expires_in of issued access tokens (seconds; default 3600).
	AccessTTL int
	// RefreshDelay widens the window in which two concurrent refreshes could both spend a token.
	RefreshDelay time.Duration
	// RefreshStatus, when non-zero, is the HTTP status the token endpoint answers a refresh with
	// (400 = invalid_grant, 500 = transient). RefreshError is the RFC 6749 error code of a 4xx
	// answer (default invalid_grant).
	RefreshStatus int
	RefreshError  string
	// RefreshRaw, when set, is answered to a refresh as a 200 with exactly this body (no token is
	// spent): a non-bearer or unparseable success.
	RefreshRaw string
	// ExpiresInString sends expires_in as a JSON string ("120") instead of a number.
	ExpiresInString bool
	// PRMOmitResource leaves "resource" out of the protected-resource metadata.
	PRMOmitResource bool
	// AuthMethods overrides token_endpoint_auth_methods_supported; OmitAuthMethods drops the field.
	AuthMethods     []string
	OmitAuthMethods bool
	// ClientSecretExpiresAt is returned by dynamic registration when non-zero (RFC 7591).
	ClientSecretExpiresAt int64
	// RevokeDelay slows the revocation endpoint.
	RevokeDelay time.Duration
	// RedirectWellKnown, when set, makes the protected-resource metadata endpoints answer 302 to
	// it (a path on this server); Hits counts requests that reach it.
	RedirectWellKnown string
	// IssuerPath is the path component of the issuer (default "/as").
	IssuerPath string
	// PRMAuthServer overrides the authorization server the protected-resource metadata names.
	PRMAuthServer string
	// RevokeStatus, when non-zero, is the status the revocation endpoint answers with.
	RevokeStatus int

	clients      map[string]client
	codes        map[string]codeGrant
	refresh      map[string]bool // live refresh tokens
	access       map[string]bool
	seq          int
	Registered   []map[string]any
	TokenForms   []url.Values
	Revoked      []url.Values
	RefreshSpent int
	RefreshReuse int
	Hits         map[string]int
}

type client struct{ secret string }

type codeGrant struct {
	challenge, redirectURI, clientID, resource string
}

// New starts the fake.
func New() *Server {
	s := &Server{
		clients: map[string]client{}, codes: map[string]codeGrant{}, refresh: map[string]bool{},
		access: map[string]bool{}, Hits: map[string]int{}, IssuerPath: "/as", AccessTTL: 3600,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Lock guards option changes made while requests may be in flight.
func (s *Server) Lock() { s.mu.Lock() }

// Unlock releases Lock.
func (s *Server) Unlock() { s.mu.Unlock() }

// MCPURL is the protected resource.
func (s *Server) MCPURL() string { return s.URL + "/mcp" }

// Issuer is the authorization server identifier.
func (s *Server) Issuer() string { return s.URL + s.IssuerPath }

// Stats is a consistent snapshot of the counters.
func (s *Server) Stats() (spent, reuse, revokes, registrations int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.RefreshSpent, s.RefreshReuse, len(s.Revoked), len(s.Registered)
}

// LastTokenForm returns the last form posted to the token endpoint with the given grant_type.
func (s *Server) LastTokenForm(grant string) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.TokenForms) - 1; i >= 0; i-- {
		if s.TokenForms[i].Get("grant_type") == grant {
			return s.TokenForms[i]
		}
	}
	return nil
}

// Approve plays the browser and the user: it validates the authorization URL core built,
// records the PKCE challenge and returns the query the authorization server would redirect back
// with (code, state and, when advertised, iss).
func (s *Server) Approve(authURL string) (url.Values, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme+"://"+u.Host+u.Path != s.Issuer()+"/authorize" {
		return nil, fmt.Errorf("authorization url points at %s", u.Scheme+"://"+u.Host+u.Path)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("redirect_uri") == "" || q.Get("client_id") == "" {
		return nil, fmt.Errorf("authorization request is missing a parameter: %v", q)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[q.Get("client_id")]; !ok {
		return nil, fmt.Errorf("unknown client %q", q.Get("client_id"))
	}
	s.seq++
	code := fmt.Sprintf("code-%d", s.seq)
	s.codes[code] = codeGrant{challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), clientID: q.Get("client_id"), resource: q.Get("resource")}
	out := url.Values{"code": {code}, "state": {q.Get("state")}}
	if s.AdvertiseIss {
		out.Set("iss", s.Issuer())
	}
	return out, nil
}

// AddClient registers a client id out of band (the "user supplied a client id" case).
func (s *Server) AddClient(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = client{}
}

// SetAccessTTL changes expires_in of tokens issued from now on.
func (s *Server) SetAccessTTL(sec int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AccessTTL = sec
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Hits[r.URL.Path]++
	s.mu.Unlock()
	switch {
	case r.URL.Path == "/mcp":
		s.mcp(w, r)
	case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource") || r.URL.Path == "/custom-prm":
		s.prm(w, r)
	case r.URL.Path == "/.well-known/oauth-authorization-server"+s.IssuerPath,
		r.URL.Path == "/.well-known/openid-configuration"+s.IssuerPath:
		s.asMetadata(w)
	case r.URL.Path == s.IssuerPath+"/register":
		s.register(w, r)
	case r.URL.Path == s.IssuerPath+"/token":
		s.token(w, r)
	case r.URL.Path == s.IssuerPath+"/revoke":
		s.revoke(w, r)
	case r.URL.Path == "/elsewhere":
		s.prm(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	hint := s.Hint
	s.mu.Unlock()
	if hint {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp", resource_metadata="`+s.URL+`/custom-prm", scope="notes.read"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.mu.Lock()
		ok := s.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		s.mu.Unlock()
		if ok {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *Server) prm(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	redir, hint, res, as := s.RedirectWellKnown, s.Hint, s.PRMResource, s.PRMAuthServer
	s.mu.Unlock()
	if r.URL.Path != "/custom-prm" && r.URL.Path != "/elsewhere" {
		if hint {
			// With a hint the well-known locations are not served: prove the hint was used.
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/.well-known/oauth-protected-resource" && r.URL.Path != "/.well-known/oauth-protected-resource/mcp" {
			http.NotFound(w, r)
			return
		}
		if redir != "" {
			http.Redirect(w, r, redir, http.StatusFound)
			return
		}
	}
	if res == "" {
		res = s.MCPURL()
	}
	if as == "" {
		as = s.Issuer()
	}
	doc := map[string]any{
		"resource": res, "authorization_servers": []string{as}, "scopes_supported": []string{"notes.read", "notes.write"},
	}
	s.mu.Lock()
	if s.PRMOmitResource {
		delete(doc, "resource")
	}
	s.mu.Unlock()
	writeJSON(w, doc)
}

func (s *Server) asMetadata(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]any{
		"issuer":                                         s.Issuer(),
		"authorization_endpoint":                         s.Issuer() + "/authorize",
		"token_endpoint":                                 s.Issuer() + "/token",
		"revocation_endpoint":                            s.Issuer() + "/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic"},
		"authorization_response_iss_parameter_supported": s.AdvertiseIss,
	}
	if s.AuthMethods != nil {
		m["token_endpoint_auth_methods_supported"] = s.AuthMethods
	}
	if s.OmitAuthMethods {
		delete(m, "token_endpoint_auth_methods_supported")
	}
	if !s.NoPKCE {
		m["code_challenge_methods_supported"] = []string{"S256"}
	}
	if !s.NoRegistration {
		m["registration_endpoint"] = s.Issuer() + "/register"
	}
	writeJSON(w, m)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Registered = append(s.Registered, req)
	s.seq++
	id := fmt.Sprintf("client-%d", s.seq)
	resp := map[string]any{"client_id": id, "redirect_uris": req["redirect_uris"], "token_endpoint_auth_method": "none"}
	c := client{}
	if s.Confidential {
		c.secret = fmt.Sprintf("client-secret-%d", s.seq)
		resp["client_secret"] = c.secret
		resp["token_endpoint_auth_method"] = "client_secret_basic"
		if s.ClientSecretExpiresAt != 0 {
			resp["client_secret_expires_at"] = s.ClientSecretExpiresAt
		}
	}
	s.clients[id] = c
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, resp)
}

func (s *Server) authClient(r *http.Request) (string, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), ""
	}
	c, ok := s.clients[id]
	if !ok || c.secret != secret {
		return "", false
	}
	return id, true
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.TokenForms = append(s.TokenForms, r.PostForm)
	grant := r.PostForm.Get("grant_type")
	delay, forced, forcedCode, raw := s.RefreshDelay, s.RefreshStatus, s.RefreshError, s.RefreshRaw
	s.mu.Unlock()

	if grant == "refresh_token" && delay > 0 {
		// The provider decides (and spends the token) when the request arrives, then takes its
		// time to answer: what a slow real provider does.
		rec := httptest.NewRecorder()
		s.mu.Lock()
		s.tokenLocked(rec, r, grant, forced, forcedCode, raw)
		s.mu.Unlock()
		time.Sleep(delay)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenLocked(w, r, grant, forced, forcedCode, raw)
}

func (s *Server) tokenLocked(w http.ResponseWriter, r *http.Request, grant string, forced int, forcedCode, raw string) {
	clientID, ok := s.authClient(r)
	if !ok {
		oauthErr(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	switch grant {
	case "authorization_code":
		g, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code")) // single use
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || g.clientID != clientID || g.redirectURI != r.PostForm.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			oauthErr(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		s.issue(w, clientID)
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		if raw != "" {
			writeRaw(w, raw)
			return
		}
		if forced != 0 {
			if forced >= 500 {
				http.Error(w, "boom", forced)
			} else {
				if forcedCode == "" {
					forcedCode = "invalid_grant"
				}
				oauthErr(w, forced, forcedCode)
			}
			return
		}
		if !s.refresh[rt] {
			s.RefreshReuse++
			oauthErr(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		delete(s.refresh, rt) // rotating: a refresh token is spent by use
		s.RefreshSpent++
		s.issue(w, clientID)
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (s *Server) issue(w http.ResponseWriter, _ string) {
	s.seq++
	at, rt := fmt.Sprintf("at-%d", s.seq), fmt.Sprintf("rt-%d", s.seq)
	s.access[at] = true
	s.refresh[rt] = true
	w.Header().Set("Cache-Control", "no-store")
	var expires any = s.AccessTTL
	if s.ExpiresInString {
		expires = fmt.Sprint(s.AccessTTL)
	}
	writeJSON(w, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": expires, "refresh_token": rt})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	delay := s.RevokeDelay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	s.mu.Lock()
	s.Revoked = append(s.Revoked, r.PostForm)
	status := s.RevokeStatus
	// Like a real provider, a revoked token stops working.
	delete(s.refresh, r.PostForm.Get("token"))
	delete(s.access, r.PostForm.Get("token"))
	s.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func oauthErr(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": code, "error_description": "fake-provider-text <script>alert(1)</script>"})
}

func writeRaw(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
