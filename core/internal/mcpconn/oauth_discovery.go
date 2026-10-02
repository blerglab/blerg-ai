package mcpconn

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/netguard"
)

// Discovery of an MCP server's OAuth setup (docs/design/ai-crons.md 4.3): RFC 9728 protected
// resource metadata (from the 401's resource_metadata hint, else the well-known locations), then
// RFC 8414 / OIDC authorization server metadata, then RFC 7591 dynamic client registration.
// Every request goes through the netguard client: https only (unless the operator lists the host),
// no private addresses, no redirects, capped bodies and timeouts. What a remote server says is
// never copied into an error shown to the person; failures are described in fixed words.

const (
	clientName          = "Blerg"
	maxClientIDLen      = 256
	maxScopeLen         = 1024
	maxAuthServers      = 8
	mcpProbeInitialize  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"blerg","version":"1"}}}`
	wellKnownPRM        = "oauth-protected-resource"
	wellKnownASMetadata = "oauth-authorization-server"
	wellKnownOIDC       = "openid-configuration"
)

// protectedResource is the RFC 9728 document.
type protectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServer is the subset of RFC 8414 / OIDC discovery metadata core uses.
type authServer struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	GrantTypes            []string `json:"grant_types_supported"`
	ResponseTypes         []string `json:"response_types_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	// IssParamSupported is RFC 9207's authorization_response_iss_parameter_supported.
	IssParamSupported bool `json:"authorization_response_iss_parameter_supported"`
}

// discovery is everything the start of a flow learned about a server.
type discovery struct {
	resource string
	as       authServer
	// scope is the scope to request when the person gave none: the 401's hint, else the
	// protected resource's scopes_supported (MCP authorization, scope selection).
	scope string
}

var challengeParamRe = regexp.MustCompile(`(?i)(?:^|[\s,])(resource_metadata|scope)\s*=\s*(?:"([^"]*)"|([^\s,]+))`)

// parseChallenges extracts resource_metadata and scope from WWW-Authenticate header values.
func parseChallenges(values []string) (resourceMetadata, scope string) {
	for _, v := range values {
		for _, m := range challengeParamRe.FindAllStringSubmatch(v, -1) {
			val := m[2]
			if val == "" {
				val = m[3]
			}
			switch strings.ToLower(m[1]) {
			case "resource_metadata":
				if resourceMetadata == "" {
					resourceMetadata = val
				}
			case "scope":
				if scope == "" {
					scope = val
				}
			}
		}
	}
	return resourceMetadata, scope
}

// netErr turns a fetch error into a ValidationError in fixed words: a policy refusal says so, and
// nothing the remote server returned is repeated.
func netErr(what string, err error) error {
	if errors.Is(err, netguard.ErrBlockedAddress) || errors.Is(err, netguard.ErrURLNotPermitted) {
		return invalid("%s is not permitted by the network policy", what)
	}
	log.Printf("mcpconn: oauth discovery: %s: %v", what, err)
	return invalid("could not reach %s", what)
}

// wellKnown builds the RFC 8615 location for a metadata suffix with the resource path inserted
// (RFC 9728 3.1, RFC 8414 3.1): https://h/.well-known/<suffix>/<path>.
func wellKnown(base, suffix string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("unparsable url")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = "/.well-known/" + suffix + strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func (s *Service) getJSON(ctx context.Context, rawURL string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("not json")
	}
	return nil
}

// probe asks the MCP endpoint without credentials and returns the 401 challenge's hints.
func (s *Service) probe(ctx context.Context, mcpURL string) (resourceMetadata, scope string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader([]byte(mcpProbeInitialize)))
	if err != nil {
		return "", "", invalid("url is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := s.httpc.Do(req)
	if err != nil {
		return "", "", netErr("the MCP server", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", nil
	}
	rm, sc := parseChallenges(resp.Header.Values("WWW-Authenticate"))
	return rm, sc, nil
}

func sameResource(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// fetchResourceMetadata tries the 401 hint, then the path-suffixed and root well-known
// locations, and returns the first document that describes mcpURL.
func (s *Service) fetchResourceMetadata(ctx context.Context, mcpURL, hint string) (protectedResource, error) {
	var candidates []string
	if hint != "" {
		candidates = append(candidates, hint)
	}
	if u, err := wellKnown(mcpURL, wellKnownPRM); err == nil {
		candidates = append(candidates, u)
	}
	if pu, err := url.Parse(mcpURL); err == nil && strings.Trim(pu.Path, "/") != "" {
		pu.Path, pu.RawPath = "", ""
		if u, err := wellKnown(pu.String(), wellKnownPRM); err == nil {
			candidates = append(candidates, u)
		}
	}
	candidates = slices.Compact(candidates)
	var lastErr error
	for _, cand := range candidates {
		if err := s.policy.CheckURL(cand); err != nil {
			return protectedResource{}, netErr("the resource metadata", err)
		}
		var prm protectedResource
		if err := s.getJSON(ctx, cand, &prm); err != nil {
			if errors.Is(err, netguard.ErrBlockedAddress) || errors.Is(err, netguard.ErrURLNotPermitted) {
				return protectedResource{}, netErr("the resource metadata", err)
			}
			lastErr = err
			continue
		}
		// RFC 9728 3.3: the document must name the resource it was fetched for; one that names
		// none proves nothing about who it speaks for.
		if prm.Resource == "" {
			return protectedResource{}, invalid("the server's resource metadata does not name its resource")
		}
		if !sameResource(prm.Resource, mcpURL) {
			return protectedResource{}, invalid("the server's resource metadata does not match its URL (resource mismatch)")
		}
		if len(prm.AuthorizationServers) == 0 {
			lastErr = errors.New("no authorization servers")
			continue
		}
		return prm, nil
	}
	if lastErr != nil {
		log.Printf("mcpconn: oauth discovery: resource metadata: %v", lastErr)
	}
	return protectedResource{}, invalid("the server does not advertise OAuth (no protected resource metadata found)")
}

// fetchAuthServer tries RFC 8414 then the two OIDC discovery forms for issuer.
func (s *Service) fetchAuthServer(ctx context.Context, issuer string) (authServer, error) {
	if err := s.policy.CheckURL(issuer); err != nil {
		return authServer{}, netErr("the authorization server", err)
	}
	if strings.ContainsAny(issuer, "?#") {
		return authServer{}, invalid("the authorization server identifier is not valid")
	}
	var candidates []string
	for _, suffix := range []string{wellKnownASMetadata, wellKnownOIDC} {
		if u, err := wellKnown(issuer, suffix); err == nil {
			candidates = append(candidates, u)
		}
	}
	candidates = append(candidates, strings.TrimRight(issuer, "/")+"/.well-known/"+wellKnownOIDC)
	var lastErr error
	for _, cand := range slices.Compact(candidates) {
		var m authServer
		if err := s.getJSON(ctx, cand, &m); err != nil {
			if errors.Is(err, netguard.ErrBlockedAddress) || errors.Is(err, netguard.ErrURLNotPermitted) {
				return authServer{}, netErr("the authorization server", err)
			}
			lastErr = err
			continue
		}
		// RFC 8414 3.3: the issuer in the document must be the one it was fetched for.
		if m.Issuer != issuer {
			lastErr = errors.New("issuer mismatch")
			continue
		}
		return m, nil
	}
	if lastErr != nil {
		log.Printf("mcpconn: oauth discovery: authorization server metadata: %v", lastErr)
	}
	return authServer{}, invalid("could not read the authorization server's metadata")
}

// checkAuthServer validates the metadata and drops optional endpoints the policy refuses.
func (s *Service) checkAuthServer(m *authServer) error {
	for _, ep := range []struct{ name, val string }{{"authorization endpoint", m.AuthorizationEndpoint}, {"token endpoint", m.TokenEndpoint}} {
		if ep.val == "" {
			return invalid("the authorization server did not publish its %s", ep.name)
		}
		if err := s.policy.CheckURL(ep.val); err != nil {
			return invalid("the authorization server's %s is not permitted by the network policy", ep.name)
		}
		if strings.Contains(ep.val, "#") {
			return invalid("the authorization server's %s is not valid", ep.name)
		}
	}
	if m.RegistrationEndpoint != "" && s.policy.CheckURL(m.RegistrationEndpoint) != nil {
		m.RegistrationEndpoint = ""
	}
	if m.RevocationEndpoint != "" && s.policy.CheckURL(m.RevocationEndpoint) != nil {
		m.RevocationEndpoint = ""
	}
	// MCP authorization: a server that does not advertise S256 is refused, never downgraded.
	if !slices.Contains(m.CodeChallengeMethods, "S256") {
		return invalid("the authorization server does not advertise PKCE (S256) support")
	}
	if len(m.ResponseTypes) > 0 && !slices.Contains(m.ResponseTypes, "code") {
		return invalid("the authorization server does not support the authorization code flow")
	}
	if len(m.GrantTypes) > 0 && !slices.Contains(m.GrantTypes, "authorization_code") {
		return invalid("the authorization server does not support the authorization code grant")
	}
	return nil
}

// discover runs the whole discovery for mcpURL.
func (s *Service) discover(ctx context.Context, mcpURL string) (*discovery, error) {
	hint, hintScope, err := s.probe(ctx, mcpURL)
	if err != nil {
		return nil, err
	}
	prm, err := s.fetchResourceMetadata(ctx, mcpURL, hint)
	if err != nil {
		return nil, err
	}
	if len(prm.AuthorizationServers) > maxAuthServers {
		prm.AuthorizationServers = prm.AuthorizationServers[:maxAuthServers]
	}
	// The first authorization server is used; choosing among several is a person's decision
	// that this flow has no UI for.
	as, err := s.fetchAuthServer(ctx, prm.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	if err := s.checkAuthServer(&as); err != nil {
		return nil, err
	}
	scope := hintScope
	if scope == "" {
		scope = strings.Join(prm.ScopesSupported, " ")
	}
	if len(scope) > maxScopeLen || validateScopes(scope) != nil {
		scope = ""
	}
	return &discovery{resource: mcpURL, as: as, scope: scope}, nil
}

// validateScopes checks RFC 6749 3.3 scope syntax: space-separated tokens of printable ASCII
// without '"' or '\'.
func validateScopes(v string) error {
	if len(v) > maxScopeLen {
		return invalid("scopes must be at most %d characters", maxScopeLen)
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c != ' ' && (c < 0x21 || c > 0x7e || c == '"' || c == '\\') {
			return invalid("scopes must be space separated scope tokens")
		}
	}
	return nil
}

// registeredClient is the outcome of dynamic client registration.
type registeredClient struct {
	id, secret, method string
	// secretExpiresAt is client_secret_expires_at (unix seconds; 0 = the secret does not expire).
	secretExpiresAt int64
}

// effectiveAuthMethods is token_endpoint_auth_methods_supported with RFC 8414 2's default applied:
// an absent (or empty) list means client_secret_basic.
func effectiveAuthMethods(supported []string) []string {
	if len(supported) == 0 {
		return []string{"client_secret_basic"}
	}
	return supported
}

// authMethodFor picks the token endpoint authentication method to ask for: a public client
// ("none") whenever the server allows it, else a secret-based one.
func authMethodFor(supported []string) (string, error) {
	supported = effectiveAuthMethods(supported)
	if slices.Contains(supported, "none") {
		return "none", nil
	}
	for _, m := range []string{"client_secret_basic", "client_secret_post"} {
		if slices.Contains(supported, m) {
			return m, nil
		}
	}
	return "", invalid("the authorization server requires a client authentication method this client does not support")
}

// register performs RFC 7591 dynamic client registration.
func (s *Service) register(ctx context.Context, as authServer, redirectURI, scope string) (registeredClient, error) {
	method, err := authMethodFor(as.TokenAuthMethods)
	if err != nil {
		return registeredClient{}, err
	}
	req := map[string]any{
		"client_name": clientName, "redirect_uris": []string{redirectURI},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if scope != "" {
		req["scope"] = scope
	}
	body, err := json.Marshal(req)
	if err != nil {
		return registeredClient{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return registeredClient{}, invalid("the registration endpoint is not valid")
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	resp, err := s.httpc.Do(hreq)
	if err != nil {
		return registeredClient{}, netErr("the registration endpoint", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || (resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK) {
		log.Printf("mcpconn: oauth discovery: registration: status %d err %v", resp.StatusCode, err)
		return registeredClient{}, invalid("the authorization server refused to register a client")
	}
	var out struct {
		ClientID        string `json:"client_id"`
		ClientSecret    string `json:"client_secret"`
		Method          string `json:"token_endpoint_auth_method"`
		SecretExpiresAt int64  `json:"client_secret_expires_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" || len(out.ClientID) > maxClientIDLen || len(out.ClientSecret) > maxSecretLen {
		return registeredClient{}, invalid("the authorization server returned an invalid client registration")
	}
	rc := registeredClient{id: out.ClientID, secret: out.ClientSecret, method: out.Method, secretExpiresAt: max(out.SecretExpiresAt, 0)}
	if rc.method == "" {
		// RFC 7591 2: the default is client_secret_basic.
		rc.method = "client_secret_basic"
		if rc.secret == "" {
			rc.method = "none"
		}
	}
	if rc.secret == "" {
		rc.method = "none"
	}
	if rc.method != "none" && rc.method != "client_secret_basic" && rc.method != "client_secret_post" {
		return registeredClient{}, invalid("the authorization server chose an unsupported client authentication method")
	}
	return rc, nil
}

// encryptDraftSecret seals a client secret for the state row's draft (the row is plain jsonb).
func (s *Service) encryptDraftSecret(ctx context.Context, secret string) (ct, keyID string, err error) {
	if secret == "" {
		return "", "", nil
	}
	b, kid, err := s.backend.Encrypt(ctx, []byte(secret))
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(b), kid, nil
}

func (s *Service) decryptDraftSecret(ctx context.Context, ct, keyID string) (string, error) {
	if ct == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		return "", err
	}
	plain, err := s.backend.Decrypt(ctx, raw, keyID)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
