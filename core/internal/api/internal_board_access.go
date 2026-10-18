package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

const boardResponseLimit = 1 << 20

type accessibleBoard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Board uses global card.read for people; Core projects are unrelated. Discovery delegates
// that existing permission, never a project membership or caller-selected destination.
func (d Deps) operatorBoardURL(ctx context.Context) (*url.URL, error) {
	if d.Registry == nil {
		return nil, errors.New("board unavailable")
	}
	manifest, err := d.Registry.Aggregate(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range manifest.Components {
		if c.Name != "blerg-board" || c.Stale {
			continue
		}
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("invalid board registration")
		}
		return u, nil
	}
	return nil, errors.New("board unavailable")
}

func validBoardID(id string) bool {
	if !validExchangeString(id, maxExchangeBoardIDLen) || strings.TrimSpace(id) != id || strings.ContainsAny(id, "/\\%?#") || id == "." || id == ".." {
		return false
	}
	return true
}

// Registered components are operator-trusted hosts, including private desktop/cluster addresses.
// The exact host is allowlisted; redirects and environment proxies remain disabled by netguard.
func fetchBoard(ctx context.Context, base *url.URL, path, token string, out any) int {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	policy := netguard.Policy{AllowHTTPHosts: []string{u.Hostname()}, AllowPrivateHosts: []string{u.Hostname()}}
	client := policy.Client(5*time.Second, boardResponseLimit)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return http.StatusNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return http.StatusServiceUnavailable
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(out); err != nil {
		return http.StatusServiceUnavailable
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

func (d Deps) handleInternalBoardsList(w http.ResponseWriter, r *http.Request) {
	var body internalMCPProof
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	deny := func(code int) { http.Error(w, http.StatusText(code), code) }
	if body.TokenID != "" {
		kind, err := svc.AgentTokenKindOf(r.Context(), body.AccountID, body.TokenID)
		if err != nil {
			deny(http.StatusServiceUnavailable)
			return
		}
		if kind != identity.AgentTokenKindToken && kind != identity.AgentTokenKindCron {
			deny(http.StatusNotFound)
			return
		}
	}
	_, _, live, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	if !live {
		deny(http.StatusNotFound)
		return
	}
	state, err := svc.AccountState(r.Context(), body.AccountID)
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	if state.Disabled || state.MustChangePassword || !slices.Contains(identity.PlatformRoleCaps[state.Role], "card.read") {
		deny(http.StatusNotFound)
		return
	}
	if body.TokenID != "" {
		caps, err := svc.AgentTokenCaps(r.Context(), body.AccountID, body.TokenID)
		if err != nil {
			deny(http.StatusNotFound)
			return
		}
		if !slices.Contains(caps, "card.read") && !slices.Contains(caps, "session.start") {
			deny(http.StatusNotFound)
			return
		}
	}
	base, err := d.operatorBoardURL(r.Context())
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	// This read-only probe stays within Core and is never persisted, returned, or logged.
	// Human-session revocation is checked both before and after the request, rather than
	// pretending a synthetic agent credential carries a human sid. Token proofs reuse their sub.
	sub := body.TokenID
	if sub == "" {
		sub = body.SessionID
	}
	raw, err := svc.MintAgentToken(r.Context(), identity.AgentTokenInput{Sub: sub, Aud: "blerg-board", Caps: []string{"card.read"}, OnBehalfOf: body.AccountID, Lineage: body.AccountID, ExpiresAt: time.Now().Add(30 * time.Second)})
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	var boards []accessibleBoard
	if code := fetchBoard(r.Context(), base, "/api/boards", raw, &boards); code != http.StatusOK {
		deny(code)
		return
	}
	if len(boards) > 1000 {
		deny(http.StatusServiceUnavailable)
		return
	}
	seen := map[string]bool{}
	for _, b := range boards {
		if !validBoardID(b.ID) || seen[b.ID] || b.Name == "" || !utf8.ValidString(b.Name) || utf8.RuneCountInString(b.Name) > 200 || strings.ContainsFunc(b.Name, unicode.IsControl) {
			deny(http.StatusServiceUnavailable)
			return
		}
		seen[b.ID] = true
	}
	_, _, live, err = d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	if !live {
		deny(http.StatusNotFound)
		return
	}
	if boards == nil {
		boards = []accessibleBoard{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, struct {
		Boards []accessibleBoard `json:"boards"`
	}{boards})
}
