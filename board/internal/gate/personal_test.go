package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

var gateAccount = coreauth.AgentToken{TokenID: "tok-1", AccountID: "acct-1", Aud: "blerg-core"}

// fetchStub is core's vault: it hands out cred (or err) and counts calls.
type fetchStub struct {
	mu    sync.Mutex
	cred  string
	err   error
	calls int
	got   [3]string // accountID, engine, tokenID of the last call
}

func (f *fetchStub) fetch(_ context.Context, accountID, engine, tokenID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = [3]string{accountID, engine, tokenID}
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.cred), nil
}

func (f *fetchStub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func okVerify(string) (coreauth.AgentToken, error) { return gateAccount, nil }

func reviewInput() Input {
	return Input{Board: db.Board{Name: "b"}, Operation: "create", Payload: []byte(`{"title":"x"}`)}
}

const acceptJSON = `{"decision":"accept","reason":"fine"}`

// fakeOpenAI is a person's own OpenAI-compatible box (the Hermes case).
type fakeOpenAI struct {
	srv       *httptest.Server
	mu        sync.Mutex
	auths     []string
	models    []string
	chatPaths []string
	status    int
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"hermes-70b"},{"id":"other"}]}`))
		case "/v1/chat/completions":
			f.chatPaths = append(f.chatPaths, r.URL.Path)
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.models = append(f.models, body.Model)
			if f.status != http.StatusOK {
				http.Error(w, `{"error":"Incorrect API key provided: user-***key"}`, f.status)
				return
			}
			out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": acceptJSON}}}})
			_, _ = w.Write(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// Hermes: the gate talks to the endpoint in the PERSON'S credential (its own
// base_url and key), not the operator's BLERG_BOARD_INFER_URL, and names the
// model that endpoint serves.
func TestPersonalHermesUsesTheCredentialsOwnEndpoint(t *testing.T) {
	t.Setenv("BLERG_BOARD_INFER_URL", "http://operator-box.invalid")
	box := newFakeOpenAI(t)
	vault := &fetchStub{cred: "# my hermes\nexport OPENAI_BASE_URL=\"" + box.srv.URL + "/v1\"\nOPENAI_API_KEY=user-key\nOPENROUTER_API_KEY=unused\n"}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Verify: okVerify, Fetch: vault.fetch})

	v, err := b.Review(context.Background(), reviewInput())
	if err != nil || v.Decision != "accept" {
		t.Fatalf("Review = %+v, %v", v, err)
	}
	if vault.got != [3]string{"acct-1", "hermes", "tok-1"} {
		t.Fatalf("fetched %v, want the token owner's hermes credential", vault.got)
	}
	if len(box.chatPaths) != 1 || box.models[0] != "hermes-70b" {
		t.Fatalf("chat calls %v models %v, want one call on the listed model", box.chatPaths, box.models)
	}
	for _, a := range box.auths {
		if a != "Bearer user-key" {
			t.Fatalf("Authorization = %q, want the credential's own key", a)
		}
	}
	if b.Name() != "hermes" || b.ModelID() != "hermes-70b" {
		t.Fatalf("Name/ModelID = %s/%s", b.Name(), b.ModelID())
	}
	// cached: a second review fetches nothing
	if _, err := b.Review(context.Background(), reviewInput()); err != nil || vault.count() != 1 {
		t.Fatalf("second review: err=%v fetches=%d", err, vault.count())
	}
}

// A Hermes credential that doesn't say where its endpoint is fails closed —
// it never falls back to the operator's local-inference box.
func TestPersonalHermesWithoutBaseURLFailsClosed(t *testing.T) {
	vault := &fetchStub{cred: "OPENROUTER_API_KEY=sk-or-secret\n"}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Verify: okVerify, Fetch: vault.fetch})
	_, err := b.Review(context.Background(), reviewInput())
	if err == nil || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
		t.Fatalf("err = %v, want one naming OPENAI_BASE_URL", err)
	}
	if strings.Contains(err.Error(), "sk-or-secret") {
		t.Fatal("error leaks the credential")
	}
}

// Claude: the account's own API key, and only it — the operator's
// ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN in board's env are not sent.
func TestPersonalClaudeUsesOnlyTheAccountsKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "operator-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "operator-token")
	var gotKey, gotAuth, gotModel string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("X-Api-Key"), r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": body.Model,
			"content":     []any{map[string]string{"type": "text", "text": acceptJSON}},
			"stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		_, _ = w.Write(out)
	}))
	defer api.Close()
	vault := &fetchStub{cred: "sk-ant-api03-personal\n"}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineClaude, Token: "raw", Verify: okVerify, Fetch: vault.fetch,
		ClaudeOptions: []option.RequestOption{option.WithBaseURL(api.URL), option.WithMaxRetries(0)}})
	v, err := b.Review(context.Background(), reviewInput())
	if err != nil || v.Decision != "accept" {
		t.Fatalf("Review = %+v, %v", v, err)
	}
	if gotKey != "sk-ant-api03-personal" || gotAuth != "" {
		t.Fatalf("sent x-api-key=%q authorization=%q, want only the account's key", gotKey, gotAuth)
	}
	if gotModel != "claude-opus-5" || vault.got[1] != "claude" {
		t.Fatalf("model %q engine %q", gotModel, vault.got[1])
	}
}

// A subscription (OAuth) token can't back a direct API call: fail closed with
// the reason, without quoting the token.
func TestPersonalClaudeRefusesSubscriptionToken(t *testing.T) {
	vault := &fetchStub{cred: "sk-ant-oat01-subscription"}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineClaude, Token: "raw", Verify: okVerify, Fetch: vault.fetch})
	_, err := b.Review(context.Background(), reviewInput())
	if err == nil || !strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), "sk-ant-oat01-subscription") {
		t.Fatalf("err = %v", err)
	}
}

// A failed fetch fails the call (the gate's gate_on_unavailable policy takes
// over), is not retried until the backoff passes, and then is.
func TestPersonalFetchFailureBacksOff(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	vault := &fetchStub{err: errors.New("blerg-core has no credential to give")}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Verify: okVerify, Fetch: vault.fetch,
		Backoff: 30 * time.Second, Now: func() time.Time { return now }})
	for i := 0; i < 3; i++ {
		if _, err := b.Review(context.Background(), reviewInput()); err == nil ||
			!strings.Contains(err.Error(), "gate account credential unavailable") {
			t.Fatalf("call %d: err = %v", i, err)
		}
	}
	if vault.count() != 1 {
		t.Fatalf("fetched %d times inside the backoff, want 1", vault.count())
	}
	now = now.Add(31 * time.Second)
	_, _ = b.Review(context.Background(), reviewInput())
	if vault.count() != 2 {
		t.Fatalf("fetched %d times after the backoff, want 2", vault.count())
	}
}

// A token that stops verifying (revoked, expired) drops the credential at
// once — no model call is made on it again.
func TestPersonalRevokedTokenDropsCredential(t *testing.T) {
	box := newFakeOpenAI(t)
	vault := &fetchStub{cred: "OPENAI_BASE_URL=" + box.srv.URL}
	revoked := false
	verify := func(string) (coreauth.AgentToken, error) {
		if revoked {
			return coreauth.AgentToken{}, errors.New("identity: revoked")
		}
		return gateAccount, nil
	}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Model: "m", Verify: verify, Fetch: vault.fetch})
	if _, err := b.Review(context.Background(), reviewInput()); err != nil {
		t.Fatal(err)
	}
	revoked = true
	if _, err := b.Review(context.Background(), reviewInput()); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v, want the revocation", err)
	}
	if n := len(box.chatPaths); n != 1 {
		t.Fatalf("%d model calls, want 1 — none after revocation", n)
	}
}

// The credential is fetched again after the TTL, and right after the model
// provider rejects it; the provider's error body never reaches the caller.
func TestPersonalRefetchOnTTLAndAuthFailure(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	box := newFakeOpenAI(t)
	vault := &fetchStub{cred: "OPENAI_BASE_URL=" + box.srv.URL + "\nOPENAI_API_KEY=user-key"}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Model: "m", Verify: okVerify,
		Fetch: vault.fetch, TTL: time.Hour, Now: func() time.Time { return now }})
	_, _ = b.Review(context.Background(), reviewInput())
	now = now.Add(61 * time.Minute)
	_, _ = b.Review(context.Background(), reviewInput())
	if vault.count() != 2 {
		t.Fatalf("fetches after TTL = %d, want 2", vault.count())
	}
	box.mu.Lock()
	box.status = http.StatusUnauthorized
	box.mu.Unlock()
	_, err := b.Review(context.Background(), reviewInput())
	if err == nil || strings.Contains(err.Error(), "user-") {
		t.Fatalf("err = %v — want a failure that does not echo the provider body", err)
	}
	box.mu.Lock()
	box.status = http.StatusOK
	box.mu.Unlock()
	if _, err := b.Review(context.Background(), reviewInput()); err != nil {
		t.Fatal(err)
	}
	if vault.count() != 3 {
		t.Fatalf("fetches after a 401 = %d, want 3", vault.count())
	}
}

// The board-visible "curator unavailable" reason is generic plus a status
// code at most — never the backend error's text (a provider body, a URL, what
// kind of credential was refused).
func TestUnavailableReasonIsViewerSafe(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("gate account credential unavailable: the account's Claude credential is a subscription token (claude setup-token)"), "curator unavailable"},
		{errors.New(`Post "http://192.0.2.5:8000/v1/chat/completions": dial tcp: connection refused`), "curator unavailable"},
		{&statusError{401, "hermes curator: server rejected the call (HTTP 401)"}, "curator unavailable (HTTP 401)"},
		{fmt.Errorf("wrapped: %w", &statusError{503, "x"}), "curator unavailable (HTTP 503)"},
		{errNoBackends, "curator unavailable: no curator backends configured"},
	} {
		if got := unavailableReason(tc.err); got != tc.want {
			t.Errorf("unavailableReason(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// and PersonalBackend's own provider failures keep their status for it
	box := newFakeOpenAI(t)
	box.status = http.StatusUnauthorized
	vault := &fetchStub{cred: "OPENAI_BASE_URL=" + box.srv.URL}
	b := NewPersonalBackend(PersonalConfig{Engine: GateEngineHermes, Token: "raw", Model: "m", Verify: okVerify, Fetch: vault.fetch})
	_, err := b.Review(context.Background(), reviewInput())
	if got := unavailableReason(err); got != "curator unavailable (HTTP 401)" {
		t.Fatalf("reason for a provider 401 = %q", got)
	}
}

func TestParseGateEngine(t *testing.T) {
	for _, ok := range []string{"claude", "hermes"} {
		if got, err := ParseGateEngine(ok); err != nil || got != ok {
			t.Errorf("%s: %q %v", ok, got, err)
		}
	}
	if _, err := ParseGateEngine("codex"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("codex: %v", err)
	}
	for _, bad := range []string{"", "gpt", "Claude"} {
		if _, err := ParseGateEngine(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
