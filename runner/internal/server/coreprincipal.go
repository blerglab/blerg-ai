package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var errCorePrincipalUnavailable = errors.New("the session owner's current authorization could not be verified")

// errCorePrincipalRefused is errCorePrincipalUnavailable when core answered and refused the proof
// (or there is no proof to ask with): the sign-in is not live. Every other failure (core unreachable,
// a 5xx, a malformed answer) is only "could not tell". errors.Is(…, errCorePrincipalUnavailable)
// holds for both.
var errCorePrincipalRefused = fmt.Errorf("%w: refused", errCorePrincipalUnavailable)

// verifyCorePrincipal checks the named principal without reading any credential.
// Unlike credential-fetch fallback, a revoked proof or unavailable core fails
// closed. The response and transport errors never escape this trust boundary.
func verifyCorePrincipal(parent context.Context, client *http.Client, coreURL, internalKey, accountID string, proof coreProof) error {
	_, err := listCorePersonalKinds(parent, client, coreURL, internalKey, accountID, proof)
	return err
}

// listCorePersonalKinds is verifyCorePrincipal's call, returning what it lists: the credential kinds
// the account has saved (names only, never values).
func listCorePersonalKinds(parent context.Context, client *http.Client, coreURL, internalKey, accountID string, proof coreProof) ([]string, error) {
	if coreURL == "" || internalKey == "" {
		return nil, errCorePrincipalUnavailable
	}
	if accountID == "" || !proof.valid() {
		return nil, errCorePrincipalRefused
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(internalCredentialListRequest{AccountID: accountID, TokenID: proof.TokenID, SessionID: proof.SessionID})
	if err != nil {
		return nil, errCorePrincipalUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/internal/credentials/list", bytes.NewReader(body)) // #nosec G704 -- Target is operator-configured Core URL; requester fields are JSON data and redirects are disabled.
	if err != nil {
		return nil, errCorePrincipalUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", internalKey)
	resp, err := coreHTTPClient(client).Do(req) // #nosec G704 -- Target is operator-configured Core URL; requester fields are JSON data and redirects are disabled.
	if err != nil {
		return nil, errCorePrincipalUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return nil, errCorePrincipalRefused
	case resp.StatusCode != http.StatusOK:
		return nil, errCorePrincipalUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10+1))
	var result internalCredentialListResponse
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &result) != nil || result.Engines == nil {
		return nil, errCorePrincipalUnavailable
	}
	return result.Engines, nil
}
