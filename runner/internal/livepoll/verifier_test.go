package livepoll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func scope() ld.Scope {
	return ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: "00000000-0000-4000-8000-000000000003", Binding: "00000000-0000-4000-8000-000000000004", Connection: "00000000-0000-4000-8000-000000000005", Tool: "quotes", Hash: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), Revision: 1, ConnectionGeneration: 1, OwnerEpoch: 1, AuthorityEpoch: 1}
}

type fakeAuthority struct {
	snapshot      ld.Snapshot
	calls         int
	denied        bool
	unavailable   bool
	revokeAt      int
	invalidations int
}

func (a *fakeAuthority) PublicKeys(context.Context) (map[string]ed25519.PublicKey, error) {
	return a.snapshot.Keys, nil
}
func (a *fakeAuthority) Current(context.Context, ld.Scope) (ld.Snapshot, error) {
	a.calls++
	if a.unavailable {
		return ld.Snapshot{}, ld.ErrAuthorityUnavailable
	}
	if a.denied || a.revokeAt > 0 && a.calls >= a.revokeAt {
		return ld.Snapshot{}, ld.ErrDenied
	}
	return a.snapshot, nil
}
func (a *fakeAuthority) Invalidate(context.Context, ld.Scope) error {
	a.invalidations++
	a.denied = true
	return nil
}

type fakeAttempts struct {
	revoked  bool
	used     bool
	calls    int
	finished bool
	highest  int64
}

func (a *fakeAttempts) ObserveEpoch(_ context.Context, s ld.Scope) error {
	if s.AuthorityEpoch < a.highest {
		return ld.ErrDenied
	}
	a.highest = s.AuthorityEpoch
	return nil
}
func (a *fakeAttempts) Consume(context.Context, string, ld.Scope, int) error {
	a.calls++
	if a.used || a.revoked {
		return ld.ErrDenied
	}
	a.used = true
	return nil
}
func (a *fakeAttempts) Finish(_ context.Context, _ string, ok bool) error {
	a.finished = ok
	return nil
}

type fakeProvider struct {
	releases    int
	pending     bool
	hash        string
	readOnly    bool
	calls       int
	definitions int
	arguments   string
	fail        bool
	afterList   func()
}

func (p *fakeProvider) Definition(context.Context, ld.Scope) (string, bool, error) {
	p.definitions++
	p.pending = true
	if p.afterList != nil {
		p.afterList()
	}
	return p.hash, p.readOnly, nil
}
func (p *fakeProvider) Call(_ context.Context, _ ld.Scope, args json.RawMessage) (json.RawMessage, error) {
	p.calls++
	p.arguments = string(args)
	if p.fail {
		return nil, ld.ErrDenied
	}
	return json.RawMessage(`{"quotes":[{"timestamp":"2026-11-02T14:30:00Z","close":185.25}],"simulated":true}`), nil
}
func fixture(t *testing.T, selected ...ld.Scope) (*Verifier, *fakeAuthority, *fakeAttempts, *fakeProvider, string) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	s := scope()
	if len(selected) == 1 {
		s = selected[0]
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grant := approvedFixture(s, now)
	s = grant.Scope
	a := &fakeAuthority{snapshot: ld.Snapshot{Grant: grant, Keys: map[string]ed25519.PublicKey{"key1": pub}}}
	attempts := &fakeAttempts{}
	p := &fakeProvider{hash: s.Hash, readOnly: true}
	token, err := ld.Sign(ld.Claims{Scope: s, Issuer: "blerg-core-live-data", Audience: ld.Audience(s.Installation, s.Runner), Operation: "poll", IssuedAt: now.Unix(), NotBefore: now.Unix(), ExpiresAt: now.Add(ld.Lifetime).Unix()}, "key1", key, now)
	if err != nil {
		t.Fatal(err)
	}
	return &Verifier{Enabled: true, Installation: s.Installation, Runner: s.Runner, Authority: a, Attempts: attempts, Provider: p, Now: func() time.Time { return now }}, a, attempts, p, token
}
func TestVerifierFreshAuthorityAndApprovedArguments(t *testing.T) {
	v, a, attempts, p, token := fixture(t)
	ctx := context.Background()
	if _, err := v.Execute(ctx, token, "attempt1"); err != nil {
		t.Fatal(err)
	}
	if a.calls != 3 || p.definitions != 1 || p.calls != 1 || p.arguments != `{"symbol":"NVDA"}` || !attempts.finished {
		t.Fatal("missing fresh check, listing, approved arguments or audit")
	}
	if _, err := v.Execute(ctx, token, "attempt1"); err == nil || p.calls != 1 {
		t.Fatal("replayed attempt executed")
	}
}
func TestVerifierFailureGuards(t *testing.T) {
	cases := map[string]func(*Verifier, *fakeAuthority, *fakeAttempts, *fakeProvider){
		"disabled":          func(v *Verifier, _ *fakeAuthority, _ *fakeAttempts, _ *fakeProvider) { v.Enabled = false },
		"authority revoked": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, _ *fakeProvider) { a.denied = true },
		"scope changed": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, _ *fakeProvider) {
			a.snapshot.Grant.Scope.Owner = a.snapshot.Grant.Scope.Runner
		},
		"clock skew": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, _ *fakeProvider) {
			a.snapshot.Grant.CoreTime = a.snapshot.Grant.CoreTime.Add(31 * time.Second)
		},
		"retired key": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, p *fakeProvider) {
			p.afterList = func() { a.snapshot.Keys = nil }
		},
		"epoch rollback": func(_ *Verifier, _ *fakeAuthority, a *fakeAttempts, _ *fakeProvider) { a.highest = 2 },
		"hash drift": func(_ *Verifier, _ *fakeAuthority, _ *fakeAttempts, p *fakeProvider) {
			p.hash = strings.Repeat("c", 64)
		},
		"read-only lost": func(_ *Verifier, _ *fakeAuthority, _ *fakeAttempts, p *fakeProvider) { p.readOnly = false },
		"revoke during listing": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, p *fakeProvider) {
			p.afterList = func() { a.denied = true }
		},
		"grant shorter": func(_ *Verifier, a *fakeAuthority, _ *fakeAttempts, _ *fakeProvider) {
			a.snapshot.Grant.ExpiresAt = a.snapshot.Grant.CoreTime.Add(time.Second)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v, a, attempts, p, token := fixture(t)
			change(v, a, attempts, p)
			if rows, err := v.Execute(context.Background(), token, "attempt1"); err == nil || rows != nil || p.calls != 0 {
				t.Fatal("guard allowed call or payload")
			}
			if name == "hash drift" || name == "read-only lost" {
				if a.invalidations != 1 {
					t.Fatal("drift did not revoke grant")
				}
			}
		})
	}
}
func TestVerifierRevocationDuringCall(t *testing.T) {
	v, a, attempts, p, token := fixture(t)
	a.revokeAt = 3
	if result, err := v.Execute(context.Background(), token, "attempt1"); err == nil || result != nil || p.calls != 1 || attempts.finished {
		t.Fatal("revoked in-flight result accepted")
	}
}

func approvedFixture(s ld.Scope, now time.Time) ld.Grant {
	definition := json.RawMessage(`{"description":"Simulated market quotes","inputSchema":{"additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"],"type":"object"},"name":"quotes"}`)
	sum := sha256.Sum256(definition)
	s.Hash = hex.EncodeToString(sum[:])
	r := ld.Revision{Scope: s, ToolDefinition: definition, Arguments: json.RawMessage(`{"symbol":"NVDA"}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 60, ExpiresAt: now.Add(24 * time.Hour), OwnerOnly: true}
	frozen, raw, err := r.Canonical(now)
	if err != nil {
		panic("invalid test revision")
	}
	return ld.Grant{Scope: frozen.Scope, Arguments: frozen.Arguments, Configuration: raw, ExpiresAt: frozen.ExpiresAt, CoreTime: now}
}

func TestVerifierRejectsUnapprovedConfiguration(t *testing.T) {
	for _, field := range []string{"interval_seconds", "mapping", "arguments"} {
		t.Run(field, func(t *testing.T) {
			v, a, _, p, token := fixture(t)
			var config ld.Revision
			_ = json.Unmarshal(a.snapshot.Grant.Configuration, &config)
			switch field {
			case "interval_seconds":
				config.IntervalSeconds = 15
			case "mapping":
				config.Mapping.Fields[0].Path = "/volume"
			case "arguments":
				config.Arguments = json.RawMessage(`{"symbol":"AMD"}`)
				a.snapshot.Grant.Arguments = config.Arguments
			}
			_, a.snapshot.Grant.Configuration, _ = config.Canonical(a.snapshot.Grant.ExpiresAt.Add(-time.Hour))
			if _, err := v.Execute(t.Context(), token, "attempt"); err == nil || p.calls != 0 {
				t.Fatal("unapproved configuration executed")
			}
		})
	}
}
func TestVerifierUsesApprovedMapping(t *testing.T) {
	v, _, _, _, token := fixture(t)
	result, err := v.Execute(t.Context(), token, "attempt")
	if err != nil || string(result) != `[{"key":"[185.25]","value":185.25}]` {
		t.Fatalf("approved mapping not applied: %s %v", result, err)
	}
}

func TestVerifierDesktopAlwaysRequiresConsent(t *testing.T) {
	v, authority, _, provider, token := fixture(t)
	v.Desktop = true
	if _, e := v.Execute(context.Background(), token, "attempt1"); e == nil || provider.calls != 0 {
		t.Fatal("desktop dispatched without Settings consent")
	}
	authority.snapshot.Grant.UnsafeHostOptIn = true
	if _, e := v.Execute(context.Background(), token, "attempt1"); e != nil {
		t.Fatal("explicit consent refused")
	}
}

func TestVerifierDistinguishesRevocationAndOutage(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		v, a, attempts, p, token := fixture(t)
		a.denied = !unavailable
		a.unavailable = unavailable
		_, e := v.Execute(context.Background(), token, "attempt1")
		want := "authority_revoked"
		if unavailable {
			want = "authority_unavailable"
		}
		if FailureCause(e) != want || p.calls != 0 || attempts.calls != 0 {
			t.Fatal("authority loss and transient failure were not distinguished", FailureCause(e))
		}
	}
}

func (p *fakeProvider) Release(context.Context, ld.Scope) { p.releases++; p.pending = false }
func TestVerifierReleasesUnusedExchangeOnEveryExit(t *testing.T) {
	for _, mode := range []string{"success", "revoked after listing", "hash drift", "attempt refused", "canceled", "provider failed"} {
		t.Run(mode, func(t *testing.T) {
			v, a, attempts, p, token := fixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "revoked after listing":
				p.afterList = func() { a.denied = true }
			case "hash drift":
				p.hash = strings.Repeat("c", 64)
			case "attempt refused":
				attempts.used = true
			case "canceled":
				p.afterList = cancel
			case "provider failed":
				p.fail = true
			}
			_, e := v.Execute(ctx, token, "attempt")
			if mode == "success" && e != nil {
				t.Fatal(e)
			}
			if mode != "success" && e == nil {
				t.Fatal("expected refusal", mode)
			}
			if p.pending || p.releases != 1 {
				t.Fatal("unused listing credential retained after verifier exit", p.releases)
			}
		})
	}
}

func (a *fakeAttempts) RevokeRevision(ctx context.Context, _ ld.Scope, _ string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.revoked = true
	return nil
}
func TestVerifierToolLossPersistsFenceAfterCancellation(t *testing.T) {
	v, _, attempts, p, token := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.readOnly = false
	p.afterList = cancel
	if _, e := v.Execute(ctx, token, "attempt"); e == nil || !attempts.revoked {
		t.Fatal("known tool loss was lost with caller cancellation")
	}
}
