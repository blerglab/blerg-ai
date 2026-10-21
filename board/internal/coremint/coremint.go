// Package coremint is the board's client for "Run as me": it asks blerg-core to mint a
// run-sessions agent token for the person signed in to the board (POST
// /internal/tokens/board-automation), and to revoke one it minted earlier (.../revoke).
//
// Authentication is BLERG_CORE_BOARD_MINT_KEY, a key that opens only those two routes on core
// (not the internal key, which reads engine credentials). The person is proved by the
// board-audience access token their browser sent the board on the same request; core verifies
// it as its own token and checks that its browser session is live. The board never sees the
// person's core cookie or refresh token.
//
// The minted token is a live credential. It travels only in the response body of a direct,
// redirect-free call to core; this package never logs a value or a response body, and none of
// its errors carries one.
package coremint

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

var (
	// ErrNotConfigured: no core URL or no mint key.
	ErrNotConfigured = errors.New("run as me needs BLERG_CORE_URL and BLERG_CORE_BOARD_MINT_KEY on blerg-board")
	// ErrUnauthorized: core refused the board's key.
	ErrUnauthorized = errors.New("blerg-core refused the board's mint key (BLERG_CORE_BOARD_MINT_KEY must be the same on core and board)")
	// ErrDisabled: core has no mint key configured.
	ErrDisabled = errors.New("blerg-core has run as me turned off (BLERG_CORE_BOARD_MINT_KEY is not set on core)")
	// ErrSwitchedOff: an operator switched new mints off on core (its 503 carries
	// mintDisabledHeader). Revoking still works.
	ErrSwitchedOff = errors.New("blerg-core has new run as me tokens switched off (BLERG_CORE_BOARD_MINT_DISABLED is set on core)")
	// ErrRefused: core did not accept the person's proof — the browser session ended, the
	// account changed, or the token is not a person's. Signing in again fixes the usual case.
	ErrRefused = errors.New("blerg-core did not accept your sign-in as live")
	// ErrRateLimited: too many attempts for this account; try again later.
	ErrRateLimited = errors.New("too many run-as-me attempts; try again later")
	// ErrTooManyTokens: the account holds the maximum number of live agent tokens.
	ErrTooManyTokens = errors.New("you have the maximum number of active agent tokens (50); revoke one in blerg-core Settings → Agent tokens")
	// ErrForbidden: the account's role cannot start sessions.
	ErrForbidden = errors.New("your account's role cannot start sessions")
	// ErrUnavailable: core could not be asked or failed to answer.
	ErrUnavailable = errors.New("blerg-core could not be reached to mint the token")
)

// Minted is a token core minted. Token is a live bearer credential: never log it.
type Minted struct {
	ID        string
	Token     string
	Name      string
	ExpiresAt time.Time
}

// Client calls core's board mint routes.
type Client struct {
	coreURL string
	key     string
	client  *http.Client
}

// New builds a Client. client may be nil: a 10-second client that never follows a redirect (the
// key and the person's token must reach core itself, nowhere else) is used.
func New(coreURL, key string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Client{coreURL: strings.TrimRight(coreURL, "/"), key: key, client: client}
}

// Configured reports whether the client can be used at all.
func (c *Client) Configured() bool { return c != nil && c.coreURL != "" && c.key != "" }

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.coreURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, errClass(err))
	}
	return resp, nil
}

// errClass names a transport failure without echoing anything the server said.
func errClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timed out"
	}
	return "connection failed"
}

// mintDisabledHeader is set on core's 503 when BLERG_CORE_BOARD_MINT_DISABLED switched minting
// off, which tells it apart from a core with no mint key. Core sets the same name.
const mintDisabledHeader = "Blerg-Mint-Disabled"

// statusErr maps core's answer to a sentinel. The response body is never read into an error.
func statusErr(code int) error {
	switch code {
	case http.StatusServiceUnavailable:
		return ErrDisabled
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrRefused
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusConflict:
		return ErrTooManyTokens
	case http.StatusForbidden:
		return ErrForbidden
	}
	return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, code)
}

// Mint asks core for a run-sessions token for accountID, minted for boardID and named after
// boardName, proved by accessToken: the person's own board-audience access token from this
// request.
func (c *Client) Mint(ctx context.Context, accountID, boardID, boardName, accessToken string) (Minted, error) {
	resp, err := c.post(ctx, "/internal/tokens/board-automation", map[string]string{
		"account_id": accountID, "board_id": boardID, "board_name": boardName, "access_token": accessToken,
	})
	if err != nil {
		return Minted{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(mintDisabledHeader) != "" {
			return Minted{}, ErrSwitchedOff
		}
		return Minted{}, statusErr(resp.StatusCode)
	}
	var out struct {
		ID        string `json:"id"`
		Token     string `json:"token"`
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil || out.ID == "" || out.Token == "" {
		return Minted{}, fmt.Errorf("%w: unreadable answer", ErrUnavailable)
	}
	exp, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		return Minted{}, fmt.Errorf("%w: unreadable answer", ErrUnavailable)
	}
	return Minted{ID: out.ID, Token: out.Token, Name: out.Name, ExpiresAt: exp}, nil
}

// Revoke asks core to revoke tokenID, a token it minted for boardID on accountID's behalf. A
// token core no longer knows as that board's (already gone, or never minted here) is not an
// error: there is nothing left to revoke.
func (c *Client) Revoke(ctx context.Context, accountID, tokenID, boardID string) error {
	resp, err := c.post(ctx, "/internal/tokens/board-automation/revoke", map[string]string{
		"account_id": accountID, "token_id": tokenID, "board_id": boardID,
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return nil
	}
	return statusErr(resp.StatusCode)
}
