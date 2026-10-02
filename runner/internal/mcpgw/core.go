package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Proof names the live thing that authorises a credential fetch at core: exactly one of
// the agent (cron) token id or the human login session id, plus the account it acts for.
// It is what the grant row stores (proof_kind, proof_value).
type Proof struct {
	AccountID string
	TokenID   string
	SessionID string
}

// Valid reports whether exactly one liveness proof is named.
func (p Proof) Valid() bool { return (p.TokenID != "") != (p.SessionID != "") }

// Kind and Value are the proof as stored in session_mcp_grants.
func (p Proof) Kind() string {
	if p.TokenID != "" {
		return "token_id"
	}
	return "session_id"
}

// Value is the id of the token or session.
func (p Proof) Value() string {
	if p.TokenID != "" {
		return p.TokenID
	}
	return p.SessionID
}

// ProofFromGrant rebuilds a Proof from stored grant columns.
func ProofFromGrant(accountID, kind, value string) Proof {
	p := Proof{AccountID: accountID}
	if kind == "token_id" {
		p.TokenID = value
	} else {
		p.SessionID = value
	}
	return p
}

// Credential is what core hands the gateway for one connection: where to call and the
// header carrying the secret. HeaderName and Value are empty for a connection with no auth.
type Credential struct {
	URL        string
	HeaderName string
	Value      string
	ExpiresAt  time.Time // zero: no expiry known
}

// ErrConnectionGone means core does not know the connection for this proof: it was
// deleted, belongs to someone else, or the login that started the session has ended.
// Core answers all of these with one uniform 404, so they cannot be told apart here.
var ErrConnectionGone = errors.New("connection no longer available")

// CoreClient fetches a fresh credential for a connection from blerg-core.
type CoreClient interface {
	// Token returns the credential for connectionID. ErrConnectionGone when core
	// answers not found.
	Token(ctx context.Context, proof Proof, connectionID string) (Credential, error)
}

// HTTPCoreClient implements CoreClient against core's
// POST /internal/mcp/connections/token (spec 4.5), guarded by the internal key.
type HTTPCoreClient struct {
	BaseURL     string
	InternalKey string
	HTTP        *http.Client // nil: a client with a 10 second timeout
}

type coreTokenRequest struct {
	AccountID    string `json:"account_id"`
	ConnectionID string `json:"connection_id"`
	TokenID      string `json:"token_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

type coreTokenResponse struct {
	URL        string     `json:"url"`
	HeaderName string     `json:"header_name"`
	Value      string     `json:"value"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// Token implements CoreClient.
func (c *HTTPCoreClient) Token(ctx context.Context, proof Proof, connectionID string) (Credential, error) {
	if !proof.Valid() {
		return Credential{}, errors.New("no liveness proof for the credential fetch")
	}
	if c.BaseURL == "" || c.InternalKey == "" {
		return Credential{}, errors.New("core is not configured")
	}
	raw, err := json.Marshal(coreTokenRequest{
		AccountID: proof.AccountID, ConnectionID: connectionID, TokenID: proof.TokenID, SessionID: proof.SessionID,
	})
	if err != nil {
		return Credential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/internal/mcp/connections/token", bytes.NewReader(raw))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	// Never follow a redirect: the request carries the internal key.
	client := &http.Client{Timeout: 10 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		client = &cp
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return Credential{}, fmt.Errorf("core unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out coreTokenResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
			return Credential{}, fmt.Errorf("decode core response: %w", err)
		}
		cred := Credential{URL: out.URL, HeaderName: out.HeaderName, Value: out.Value}
		if out.ExpiresAt != nil {
			cred.ExpiresAt = *out.ExpiresAt
		}
		return cred, nil
	case http.StatusNotFound:
		// Core's own uniform answer says "not found"; a router's 404 for a route an older core
		// lacks says something else and is a plain failure, not a removed connection.
		if !isCoreNotFound(resp) {
			return Credential{}, errors.New("core has no MCP credential route: is it up to date?")
		}
		return Credential{}, ErrConnectionGone
	default:
		return Credential{}, fmt.Errorf("core returned %d", resp.StatusCode)
	}
}

// isCoreNotFound reports whether a 404 is core's own uniform answer (a handler's http.Error "not
// found"), as opposed to a router's or a proxy's 404 for a route core lacks (a different body, or
// none). Only the former means "this proof or object is gone"; the latter is an error.
func isCoreNotFound(resp *http.Response) bool {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.TrimSpace(string(raw)) == "not found"
}
