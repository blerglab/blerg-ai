package mcpgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// LiveCredentials resolves only an exact audited scope over dedicated Core TLS.
// No legacy session/token proof or five-minute credential cache is consulted.
type LiveCredentials interface {
	ProviderCredential(context.Context, ld.ProviderRequest) (ld.ProviderCredential, error)
}
type liveExchange struct {
	scope ld.Scope
	state *grantState
	grant *db.MCPGrant
	until time.Time
	url   string
}
type LiveProvider struct {
	gateway     *Gateway
	credentials LiveCredentials
	mu          sync.Mutex
	pending     map[string]liveExchange
}

func (g *Gateway) LiveProvider(credentials LiveCredentials) *LiveProvider {
	return &LiveProvider{gateway: g, credentials: credentials, pending: map[string]liveExchange{}}
}

var errLiveProvider = errors.New("live_provider_unavailable")

func (p *LiveProvider) credential(ctx context.Context, s ld.Scope, stage string) (Credential, ld.ProviderRequest, error) {
	proof, ok := ld.ProviderRequestFrom(ctx)
	if !ok || proof.Stage != stage || !s.Valid() || p.credentials == nil {
		return Credential{}, proof, errLiveProvider
	}
	value, err := p.credentials.ProviderCredential(ctx, proof)
	if err != nil {
		return Credential{}, proof, err
	}
	if !value.ExpiresAt.IsZero() && !value.ExpiresAt.After(p.gateway.now()) {
		return Credential{}, proof, errLiveProvider
	}
	return Credential{URL: value.URL, HeaderName: value.HeaderName, Value: value.Value, ExpiresAt: value.ExpiresAt, PinPolicy: PinPolicyStrict}, proof, nil
}
func (p *LiveProvider) Definition(ctx context.Context, s ld.Scope) (string, bool, error) {
	credential, proof, err := p.credential(ctx, s, "list")
	if err != nil {
		return "", false, err
	}
	g := p.gateway
	if err = g.checkCredential(credential, credential.URL); err != nil {
		return "", false, errLiveProvider
	}
	state := &grantState{liveOnly: true, g: g, sem: make(chan struct{}, 1), cred: &credential, credUntil: g.now().Add(30 * time.Second)}
	grant := &db.MCPGrant{ConnectionID: s.Connection, AccountID: s.Owner, URLSnapshot: credential.URL}
	tools, complete, rerr, err := state.listUpstream(ctx, grant, hashRefreshMaxPages, false)
	if err != nil || rerr != nil || !complete {
		return "", false, liveUpstreamFailure(err, 0, nil, g.now())
	}
	count := 0
	hash := ""
	readOnly := false
	for _, tool := range tools {
		if tool.Name == s.Tool {
			count++
			var annotations struct {
				ReadOnly bool `json:"readOnlyHint"`
			}
			canonical, pin := CanonicalTool(tool.Name, tool.Description, tool.InputSchema)
			if json.Unmarshal(tool.Annotations, &annotations) == nil && ClassifyDefinition(canonical) == "" {
				hash = pin
				readOnly = annotations.ReadOnly
			}
		}
	}
	if count != 1 {
		// The fresh complete listing proves the bound name is absent or
		// ambiguous. This is authority loss, not a retryable transport outage.
		return "", false, nil
	}
	if !readOnly || hash != s.Hash {
		return hash, readOnly, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, exchange := range p.pending {
		if !exchange.until.After(g.now()) {
			delete(p.pending, id)
		}
	}
	if len(p.pending) >= 4 {
		return "", false, errLiveProvider
	}
	p.pending[proof.Attempt] = liveExchange{scope: s, state: state, grant: grant, until: g.now().Add(30 * time.Second), url: credential.URL}
	return hash, true, nil
}
func (p *LiveProvider) Call(ctx context.Context, s ld.Scope, args json.RawMessage) (json.RawMessage, error) {
	proof, ok := ld.ProviderRequestFrom(ctx)
	if !ok || proof.Stage != "call" {
		return nil, errLiveProvider
	}
	p.mu.Lock()
	exchange, exists := p.pending[proof.Attempt]
	delete(p.pending, proof.Attempt)
	p.mu.Unlock()
	if !exists || exchange.scope != s || !exchange.until.After(p.gateway.now()) {
		return nil, errLiveProvider
	}
	credential, _, err := p.credential(ctx, s, "call")
	if err != nil {
		return nil, err
	}
	if p.gateway.checkCredential(credential, exchange.url) != nil {
		return nil, errLiveProvider
	}
	// Use exactly one tools/call POST: never retry an ambiguous outcome or 404.
	sid, proto := exchange.state.session()
	id := strconv.FormatInt(exchange.state.nextID.Add(1), 10)
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": "tools/call", "params": map[string]any{"name": s.Tool, "arguments": args}})
	if err != nil {
		return nil, errLiveProvider
	}
	message, headers, status, err := exchange.state.post(ctx, credential, payload, id, sid, proto)
	if err != nil || message.Error != nil {
		return nil, liveUpstreamFailure(err, status, headers, p.gateway.now())
	}
	var result struct {
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(message.Result, &result) != nil || result.IsError {
		return nil, errLiveProvider
	}
	if len(result.Structured) > 0 && string(result.Structured) != "null" {
		return result.Structured, nil
	}
	if len(result.Content) == 1 && result.Content[0].Type == "text" && json.Valid([]byte(result.Content[0].Text)) {
		return json.RawMessage(result.Content[0].Text), nil
	}
	return nil, errLiveProvider
}

// Release performs no I/O and works even after cancellation. A different scope
// or attempt cannot discard another caller's credential exchange.
func (p *LiveProvider) Release(ctx context.Context, s ld.Scope) {
	proof, ok := ld.ProviderRequestFrom(ctx)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if exchange, exists := p.pending[proof.Attempt]; exists && exchange.scope == s {
		delete(p.pending, proof.Attempt)
	}
}

// liveProviderFailure carries only fixed cause codes and a bounded delay.
type liveProviderFailure struct {
	cause string
	delay time.Duration
}

func (e *liveProviderFailure) Error() string                        { return "live_provider_unavailable" }
func (e *liveProviderFailure) LiveFailure() (string, time.Duration) { return e.cause, e.delay }

func liveRetryAfter(value string, now time.Time) time.Duration {
	if len(value) > 128 {
		return 0
	}
	const horizon = 30 * 24 * time.Hour // No approval can survive beyond this horizon.
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(horizon/time.Second) {
			return horizon
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(horizon, max(0, at.Sub(now)))
	}
	return 0
}
func liveUpstreamFailure(err error, status int, headers http.Header, now time.Time) error {
	var upstream *upstreamStatusError
	if errors.As(err, &upstream) {
		status = upstream.Status
		if headers == nil && status == http.StatusTooManyRequests {
			return &liveProviderFailure{cause: "provider_rate_limited", delay: upstream.RetryAfter}
		}
	}
	cause := "provider_unavailable"
	if status == http.StatusUnauthorized || status == http.StatusForbidden || errors.Is(err, errUpstreamAuth) {
		cause = "connection_needs_sign_in"
	}
	if status == http.StatusTooManyRequests {
		cause = "provider_rate_limited"
	}
	if errors.Is(err, errLiveResponseLimit) {
		cause = "invalid_result"
	}
	return &liveProviderFailure{cause: cause, delay: liveRetryAfter(headers.Get("Retry-After"), now)}
}
