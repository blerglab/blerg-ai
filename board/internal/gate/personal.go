package gate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

// ── The gate on a person's own credential ────────────────────────────────────
//
// By default the gate runs on credentials the operator put in board's
// environment (ANTHROPIC_API_KEY, BLERG_BOARD_INFER_URL). An install whose
// credentials are self-service opts into running it on ONE named account's
// own connected credential instead, instance-wide:
//
//	BLERG_BOARD_GATE_ACCOUNT_TOKEN  a blerg-core agent token that account's
//	                                owner minted for themselves
//	BLERG_BOARD_GATE_ENGINE         claude | hermes
//
// Board verifies the token locally (whose is it, is it still live), then
// fetches that account's credential for the engine from core's vault
// (internal/corecred) and builds the backend from it. The operator never
// handles the credential; the account's owner can revoke the token at any
// time and the gate stops using their credential within a revocation poll.
//
// Once opted in there is NO fallback to the operator's env credentials: a
// failed fetch makes the gate unavailable, and each gated board's
// gate_on_unavailable policy applies, exactly as when a backend is down.
//
// Codex is not offered here: see ParseGateEngine.

// Gate engines a person's credential can back.
const (
	GateEngineClaude = "claude"
	GateEngineHermes = "hermes"
)

// ParseGateEngine validates BLERG_BOARD_GATE_ENGINE.
//
// codex is refused on purpose. A Codex credential is the contents of
// ~/.codex/auth.json, the codex CLI's login. With a ChatGPT sign-in (the
// usual case) it holds OAuth tokens for the CLI's own backend, not an API key
// for a completions endpoint: using them from here would mean driving an
// undocumented private API, and refreshing them would rotate the refresh token
// out from under the copy in the person's vault (breaking their sessions). The
// only supported way to spend that login is the codex CLI itself — a
// `codex exec` process per gate call, which is seconds of latency on a
// synchronous write path and a much bigger change than this backend.
func ParseGateEngine(v string) (string, error) {
	switch v {
	case GateEngineClaude, GateEngineHermes:
		return v, nil
	case "codex":
		return "", errors.New("BLERG_BOARD_GATE_ENGINE=codex is not supported: a Codex credential is the codex CLI's " +
			"login (auth.json), not an API key the gate can call directly — use claude or hermes")
	}
	return "", fmt.Errorf("BLERG_BOARD_GATE_ENGINE=%q: want claude or hermes", v)
}

// PersonalConfig configures a PersonalBackend. Token is the raw agent token;
// Verify and Fetch are wired to coreauth and corecred in production.
type PersonalConfig struct {
	Engine string // GateEngineClaude | GateEngineHermes
	Token  string // BLERG_BOARD_GATE_ACCOUNT_TOKEN — never logged
	// Model overrides the model: the Claude model (default claude-opus-5), or
	// the model named to a Hermes endpoint (default: the first one the
	// endpoint lists at /v1/models).
	Model string

	// Verify checks Token locally and says whose it is.
	Verify func(raw string) (coreauth.AgentToken, error)
	// Fetch returns the account's credential for engine from core's vault.
	Fetch func(ctx context.Context, accountID, engine, tokenID string) ([]byte, error)

	// TTL: how long a fetched credential is used before it is fetched again,
	// so a credential the owner rotates in Settings is picked up. Default 1h.
	TTL time.Duration
	// Backoff: after a failed fetch, how long calls fail fast before the
	// next attempt. Default 30s.
	Backoff time.Duration

	// Test seams.
	Now           func() time.Time
	HTTPClient    *http.Client           // Hermes endpoint calls
	ClaudeOptions []option.RequestOption // appended to the Claude client's
}

// PersonalBackend is a Backend that runs on the configured account's own
// credential, fetched lazily and re-fetched on expiry or an auth failure.
//
// Refetch strategy — a long-lived server making many calls, so neither
// "fetch per call" (a vault read and an audit row per card write) nor "fetch
// once forever" (a rotated or revoked credential lingers):
//   - The token is re-verified locally on EVERY call (no network: cached JWKS
//     and revocation list). Revoked or expired ⇒ the cached credential is
//     dropped at once and the gate is unavailable.
//   - The credential is fetched on first use and kept for TTL (1h), then
//     fetched again on the next call.
//   - A 401/403 from the model provider drops it, so the next call refetches
//     (the owner replaced a dead key).
//   - A failed fetch is not retried for Backoff (30s); calls in between fail
//     fast. Nothing polls in the background.
//
// The credential lives only inside the backend it builds; it is never logged
// and never appears in an error (errors are rebuilt from status codes).
type PersonalBackend struct {
	cfg PersonalConfig

	mu        sync.Mutex
	cur       Backend
	model     string
	fetchedAt time.Time
	failedAt  time.Time
	lastErr   error
}

// NewPersonalBackend builds the backend. Nothing is fetched until the first
// Review, so board boots even while core is still starting.
func NewPersonalBackend(cfg PersonalConfig) *PersonalBackend {
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Engine == GateEngineClaude && cfg.Model == "" {
		cfg.Model = "claude-opus-5"
	}
	return &PersonalBackend{cfg: cfg, model: cfg.Model}
}

func (p *PersonalBackend) Name() string { return p.cfg.Engine }

func (p *PersonalBackend) ModelID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}

// errUnavailable prefixes every "could not get a credential" error, so a
// gate log line reads as the gate's configuration and not as a model failure.
func errUnavailable(format string, args ...any) error {
	return fmt.Errorf("gate account credential unavailable: "+format, args...)
}

// backend returns a backend built from a live credential, fetching one when
// needed.
func (p *PersonalBackend) backend(ctx context.Context) (Backend, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.Now()

	tok, err := p.cfg.Verify(p.cfg.Token)
	if err != nil {
		if p.cur != nil {
			log.Printf("admission gate: BLERG_BOARD_GATE_ACCOUNT_TOKEN no longer verifies (%v) — dropping the account credential", err)
		}
		p.cur = nil
		return nil, errUnavailable("BLERG_BOARD_GATE_ACCOUNT_TOKEN does not verify: %v", err)
	}
	if p.cur != nil && now.Sub(p.fetchedAt) < p.cfg.TTL {
		return p.cur, nil
	}
	if !p.failedAt.IsZero() && now.Sub(p.failedAt) < p.cfg.Backoff {
		return nil, p.lastErr
	}

	b, model, err := p.build(ctx, tok)
	if err != nil {
		p.cur, p.failedAt, p.lastErr = nil, now, errUnavailable("%v", err)
		log.Printf("admission gate: %v", p.lastErr)
		return nil, p.lastErr
	}
	if p.cur == nil {
		log.Printf("admission gate: running on account %s's own %s credential (agent token %s, model %q)",
			tok.AccountID, p.cfg.Engine, tok.TokenID, model)
	}
	p.cur, p.model, p.fetchedAt, p.failedAt, p.lastErr = b, model, now, time.Time{}, nil
	return b, nil
}

// build fetches the credential and turns it into a backend. Errors name what
// is wrong and never quote the credential.
func (p *PersonalBackend) build(ctx context.Context, tok coreauth.AgentToken) (Backend, string, error) {
	raw, err := p.cfg.Fetch(ctx, tok.AccountID, p.cfg.Engine, tok.TokenID)
	if err != nil {
		return nil, "", err
	}
	switch p.cfg.Engine {
	case GateEngineClaude:
		key := strings.TrimSpace(string(raw))
		if strings.HasPrefix(key, "sk-ant-oat") {
			// A `claude setup-token` subscription token is for Claude Code
			// itself; the gate calls the Messages API directly and needs an
			// API key billed to an organisation.
			return nil, "", errors.New("the account's Claude credential is a subscription token (claude setup-token); " +
				"the gate calls the Anthropic API directly and needs an API key (sk-ant-api…) — " +
				"connect one in blerg-core Settings, or use BLERG_BOARD_GATE_ENGINE=hermes")
		}
		opts := append([]option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithAPIKey(key)},
			p.cfg.ClaudeOptions...)
		return &ClaudeBackend{client: anthropic.NewClient(opts...), model: p.cfg.Model}, p.cfg.Model, nil
	case GateEngineHermes:
		env := parseDotenv(string(raw))
		base := strings.TrimSpace(env["OPENAI_BASE_URL"])
		if base == "" {
			return nil, "", errors.New("the account's Hermes configuration has no OPENAI_BASE_URL — the gate needs " +
				"the OpenAI-compatible endpoint Hermes talks to (e.g. OPENAI_BASE_URL=http://my-gpu-box:8000/v1)")
		}
		client := localinfer.New(localinfer.Config{
			BaseURL:    strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"),
			APIKey:     strings.TrimSpace(env["OPENAI_API_KEY"]),
			ChatModel:  p.cfg.Model,
			HTTPClient: p.cfg.HTTPClient,
		})
		model := p.cfg.Model
		if model == "" {
			ids, err := client.ListModels(ctx)
			if err != nil || len(ids) == 0 {
				return nil, "", fmt.Errorf("the account's Hermes endpoint did not list a model (%s) — set BLERG_BOARD_GATE_MODEL",
					scrubbed(err))
			}
			model = ids[0]
		}
		return &OpenAIBackend{client: client, model: model}, model, nil
	}
	return nil, "", fmt.Errorf("unsupported gate engine %q", p.cfg.Engine)
}

// Review runs the curator on the account's credential.
func (p *PersonalBackend) Review(ctx context.Context, in Input) (Verdict, error) {
	b, err := p.backend(ctx)
	if err != nil {
		return Verdict{}, err
	}
	v, err := b.Review(ctx, in)
	if err == nil {
		return v, nil
	}
	if authFailure(err) {
		p.mu.Lock()
		if p.cur == b {
			p.cur = nil // refetch on the next call
		}
		p.mu.Unlock()
		log.Printf("admission gate: %s rejected the account credential — it will be fetched again", p.cfg.Engine)
	}
	msg := fmt.Sprintf("%s curator: %s", p.cfg.Engine, scrubbed(err))
	var ae *anthropic.Error
	var le *localinfer.Error
	switch {
	case errors.As(err, &ae) && ae.StatusCode != 0:
		return Verdict{}, &statusError{ae.StatusCode, msg}
	case errors.As(err, &le) && le.StatusCode != 0:
		return Verdict{}, &statusError{le.StatusCode, msg}
	}
	return Verdict{}, errors.New(msg)
}

// authFailure: the provider refused the credential itself.
func authFailure(err error) bool {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		return ae.StatusCode == http.StatusUnauthorized || ae.StatusCode == http.StatusForbidden
	}
	var le *localinfer.Error
	if errors.As(err, &le) {
		return le.StatusCode == http.StatusUnauthorized || le.StatusCode == http.StatusForbidden
	}
	return false
}

// scrubbed describes a provider error without its response body or request
// URL — either could carry a fragment of the credential (an OpenAI-style
// server echoes a masked key; a URL could embed one). Status and kind only.
func scrubbed(err error) string {
	if err == nil {
		return "no models listed"
	}
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		return fmt.Sprintf("HTTP %d from the Anthropic API", ae.StatusCode)
	}
	var le *localinfer.Error
	if errors.As(err, &le) {
		if le.StatusCode != 0 {
			return fmt.Sprintf("%s (HTTP %d)", le.Detail, le.StatusCode)
		}
		return le.Detail
	}
	// Verdict parsing and refusals carry no provider text.
	if strings.HasPrefix(err.Error(), "no JSON object") || strings.HasPrefix(err.Error(), "curator output") ||
		strings.HasPrefix(err.Error(), "claude declined") {
		return err.Error()
	}
	return "request failed"
}

// unavailableReason is the viewer-facing text for "every backend failed":
// generic, plus the provider's HTTP status when there was one. Never the
// error's own text (see the call site in Check).
func unavailableReason(err error) string {
	const base = "curator unavailable"
	if errors.Is(err, errNoBackends) {
		return base + ": no curator backends configured"
	}
	var ae *anthropic.Error
	if errors.As(err, &ae) && ae.StatusCode != 0 {
		return fmt.Sprintf("%s (HTTP %d)", base, ae.StatusCode)
	}
	var le *localinfer.Error
	if errors.As(err, &le) && le.StatusCode != 0 {
		return fmt.Sprintf("%s (HTTP %d)", base, le.StatusCode)
	}
	var se *statusError
	if errors.As(err, &se) {
		return fmt.Sprintf("%s (HTTP %d)", base, se.status)
	}
	return base
}

// statusError carries a provider status code through PersonalBackend's
// scrubbed errors, so unavailableReason can still report it.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

// parseDotenv reads KEY=VALUE lines (the ~/.hermes/.env a Hermes credential
// is): blank lines and # comments skipped, an optional "export " prefix and
// matching surrounding quotes removed. Nothing else is interpreted.
func parseDotenv(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}
