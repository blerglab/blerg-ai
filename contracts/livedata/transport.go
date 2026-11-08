package livedata

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"io"
	"log/slog"
	"net/http"
)

var ErrAuthorityUnavailable = errors.New("live_data_authority_unavailable")

const IssuerPath = "/internal/live-data/issuer/"

type MintRequest struct {
	Binding  string `json:"binding"`
	Revision int64  `json:"revision"`
	Renew    bool   `json:"renew"`
}
type MintResponse struct {
	Token string `json:"token"`
}
type RevokeResponse struct {
	Revoked bool `json:"revoked"`
}
type KeysResponse struct {
	Keys map[string]ed25519.PublicKey `json:"keys"`
}

// ServiceClient uses only an explicitly supplied dedicated mTLS client and base
// URL. It is not an agent API, and never falls back to a default HTTP transport.
type ServiceClient struct {
	URL  string
	HTTP *http.Client
}

func (c *ServiceClient) request(ctx context.Context, path string, body, out any) (resultErr error) {
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("live_data_issuer_client_failed", slog.LevelWarn, "operation", path)
		}
	}()
	if c.HTTP == nil || c.HTTP.Transport == nil || c.URL == "" {
		return ErrAuthorityUnavailable
	}
	raw, e := json.Marshal(body)
	if e != nil {
		return ErrAuthorityUnavailable
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.URL+IssuerPath+path, bytes.NewReader(raw))
	if e != nil {
		return ErrAuthorityUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := client.Do(req)
	if e != nil {
		if livetls.Refused(e) {
			return ErrDenied
		}
		return ErrAuthorityUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized {
		return ErrDenied
	}
	if res.StatusCode != http.StatusOK {
		return ErrAuthorityUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 128*1024+1))
	if e != nil || len(data) > 128*1024 {
		return ErrAuthorityUnavailable
	}
	if out == nil {
		return nil
	}
	if json.Unmarshal(data, out) != nil {
		return ErrAuthorityUnavailable
	}
	return nil
}
func (c *ServiceClient) Mint(ctx context.Context, binding string, revision int64, renew bool) (string, error) {
	var out MintResponse
	e := c.request(ctx, "mint", MintRequest{binding, revision, renew}, &out)
	if e != nil {
		return "", e
	}
	if out.Token == "" {
		return "", ErrAuthorityUnavailable
	}
	return out.Token, nil
}
func (c *ServiceClient) PublicKeys(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	var out KeysResponse
	e := c.request(ctx, "keys", struct{}{}, &out)
	if e != nil {
		return nil, e
	}
	if len(out.Keys) == 0 {
		return nil, ErrAuthorityUnavailable
	}
	return out.Keys, nil
}
func (c *ServiceClient) Current(ctx context.Context, scope Scope) (Snapshot, error) {
	var out Snapshot
	e := c.request(ctx, "current", scope, &out)
	return out, e
}
func (c *ServiceClient) Invalidate(ctx context.Context, scope Scope) error {
	var out RevokeResponse
	if e := c.request(ctx, "invalidate", scope, &out); e != nil {
		return e
	}
	if !out.Revoked {
		failurelog.Default.Event("live_data_issuer_invalid_ack", slog.LevelWarn)
		return ErrAuthorityUnavailable
	}
	return nil
}
