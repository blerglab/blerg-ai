// Package corecred fetches a person's own engine credential from blerg-core's
// per-user vault (POST /internal/credentials/fetch), for the one board feature
// that makes model calls itself: the admission gate running on a named
// account's credential (BLERG_BOARD_GATE_ACCOUNT_TOKEN).
//
// It is the board twin of the runner's fetchCoreCredential
// (runner/internal/server/k8sjobs.go): same endpoint, same X-Internal-Key
// header (core's BLERG_CORE_INTERNAL_KEY), same body, and the same liveness
// rule on core's side — the agent token named in token_id must be one of the
// account's own, unrevoked and unexpired, or core answers 404 exactly as for a
// credential that was never stored. Core writes a credential_access_log row
// for every successful fetch.
//
// One deliberate difference: the runner degrades a failed fetch to "use the
// operator's shared credential". Board has no such fallback — an opted-in
// gate either runs on that person's credential or reports itself unavailable
// — so every failure here is an error the caller must act on.
//
// The plaintext returned is a live credential. Callers must never log it,
// never put it in an error or a response, and keep it only as long as they
// need it. Nothing in this package logs a value or a response body.
package corecred

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrNotFound: core has nothing to give — the account never stored a
	// credential for this engine, or the token is not (or no longer) a live
	// agent token of that account. Core makes the two indistinguishable on
	// purpose (anti-enumeration), so this says both.
	ErrNotFound = errors.New("blerg-core has no credential to give for this account and engine " +
		"(none stored, or the agent token is revoked or expired)")
	// ErrUnauthorized: core refused the internal key — board's
	// BLERG_BOARD_CORE_INTERNAL_KEY does not match core's BLERG_CORE_INTERNAL_KEY.
	ErrUnauthorized = errors.New("blerg-core refused board's internal key " +
		"(BLERG_BOARD_CORE_INTERNAL_KEY must equal core's BLERG_CORE_INTERNAL_KEY)")
	// ErrUnavailable: core could not be asked, or failed to answer.
	ErrUnavailable = errors.New("blerg-core credential fetch unavailable")
	// ErrNoTokenID: a fetch was attempted without the authorizing agent
	// token's id. Refused locally, never sent.
	ErrNoTokenID = errors.New("credential fetch refused: no agent token id to authorize it")
	// ErrNotConfigured: the fetcher has no core URL or internal key.
	ErrNotConfigured = errors.New("credential fetch needs BLERG_CORE_URL and BLERG_BOARD_CORE_INTERNAL_KEY")
)

// Fetcher calls core's internal credential endpoint.
type Fetcher struct {
	coreURL     string
	internalKey string
	client      *http.Client
}

// New builds a Fetcher. client may be nil (a 10s-timeout client is used).
func New(coreURL, internalKey string, client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Fetcher{coreURL: strings.TrimRight(coreURL, "/"), internalKey: internalKey, client: client}
}

// Fetch returns accountID's own credential for engine, authorized by the live
// agent token tokenID (the token's jti/sub, not the token itself — the raw
// token never leaves board). Errors are the sentinels above, wrapped with a
// status where useful; none carries a response body or a value.
func (f *Fetcher) Fetch(ctx context.Context, accountID, engine, tokenID string) ([]byte, error) {
	if f == nil || f.coreURL == "" || f.internalKey == "" {
		return nil, ErrNotConfigured
	}
	// Core requires exactly one named live proof (token_id or session_id) and
	// checks it belongs to the account. Board always names the agent token
	// that authorizes the fetch, so core itself confines the answer to that
	// token's live owner rather than trusting board to.
	if tokenID == "" {
		return nil, ErrNoTokenID
	}
	raw, err := json.Marshal(struct {
		AccountID string `json:"account_id"`
		Engine    string `json:"engine"`
		TokenID   string `json:"token_id,omitempty"`
	}{accountID, engine, tokenID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.coreURL+"/internal/credentials/fetch", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: bad core URL", ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", f.internalKey)
	resp, err := f.client.Do(req)
	if err != nil {
		// The transport error names the URL, never the key or the body.
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		return nil, fmt.Errorf("%w: core answered HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		PlaintextBase64 string `json:"plaintext_base64"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: undecodable response", ErrUnavailable)
	}
	plaintext, err := base64.StdEncoding.DecodeString(out.PlaintextBase64)
	if err != nil {
		return nil, fmt.Errorf("%w: undecodable credential", ErrUnavailable)
	}
	// An empty credential is not one (same rule as the runner's fetch).
	if len(bytes.TrimSpace(plaintext)) == 0 {
		return nil, ErrNotFound
	}
	return plaintext, nil
}
