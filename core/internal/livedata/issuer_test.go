package livedata

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func testScope() ld.Scope {
	return ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: "00000000-0000-4000-8000-000000000003", Binding: "00000000-0000-4000-8000-000000000004", Connection: "00000000-0000-4000-8000-000000000005", Tool: "quotes", Hash: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), Revision: 1, ConnectionGeneration: 1, OwnerEpoch: 1, AuthorityEpoch: 1}
}
func keyFile(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "live-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type memoryStore struct {
	grant     Grant
	denied    bool
	auditDown bool
	reads     int
	afterRead func()
	outcomes  []string
	metadata  []AuditMetadata
}

func (m *memoryStore) Current(context.Context, string, int64) (Grant, error) {
	m.reads++
	if m.afterRead != nil {
		m.afterRead()
	}
	if m.denied {
		return Grant{}, ld.ErrDenied
	}
	return m.grant, nil
}
func (m *memoryStore) Audit(_ context.Context, _ string, _ int64, op, outcome string, metadata AuditMetadata) error {
	if m.auditDown {
		return ErrUnavailable
	}
	m.outcomes = append(m.outcomes, op+":"+outcome)
	m.metadata = append(m.metadata, metadata)
	return nil
}
func issuer(t *testing.T) (*Issuer, *memoryStore, *bool) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	s := testScope()
	store := &memoryStore{grant: approvedFixture(s, now)}
	keys, err := LoadKeyring("key1", keyFile(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	safe := true
	return &Issuer{Store: store, Keys: keys, Installation: s.Installation, Runner: s.Runner, Posture: func(context.Context) (bool, error) { return safe, nil }, Now: func() time.Time { return now }}, store, &safe
}
func TestIssuerUnsafeHostAndFreshAuthority(t *testing.T) {
	s, store, safe := issuer(t)
	ctx := context.Background()
	*safe = false
	if token, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false); err == nil || token != "" {
		t.Fatal("privileged desktop minted without opt-in")
	}
	if _, err := s.Current(ctx, store.grant.Scope); err == nil {
		t.Fatal("privileged desktop dispatched without opt-in")
	}
	store.grant.UnsafeHostOptIn = true
	if _, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false); err != nil {
		t.Fatal("explicit opt-in refused")
	}
	*safe = true
	store.grant.UnsafeHostOptIn = false
	token, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := s.PublicKeys(ctx)
	if _, err := ld.Verify(token, keys, s.Installation, s.Runner, s.now()); err != nil {
		t.Fatal(err)
	}
	store.denied = true
	if _, err := s.Current(ctx, store.grant.Scope); err == nil {
		t.Fatal("cached credential bypassed revocation")
	}
	if _, err := s.Mint(ctx, store.grant.Scope.Binding, 1, true); err == nil {
		t.Fatal("renewed revoked grant")
	}
	if store.reads != 6 {
		t.Fatalf("expected fresh durable read per operation; got %d", store.reads)
	}
	if strings.Join(store.outcomes, ",") != "mint:unsafe_host,check:unsafe_host,mint:ok,mint:ok,check:denied,renew:denied" {
		t.Fatal(store.outcomes)
	}
}
func TestIssuerAuditAndScopeRefusal(t *testing.T) {
	s, store, _ := issuer(t)
	ctx := context.Background()
	store.auditDown = true
	if token, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false); err == nil || token != "" {
		t.Fatal("audit failure minted")
	}
	store.auditDown = false
	bad := store.grant.Scope
	bad.Digest = strings.Repeat("c", 64)
	if _, err := s.Current(ctx, bad); err == nil || store.outcomes[len(store.outcomes)-1] != "check:denied" {
		t.Fatal("wrong revision digest accepted or unaudited")
	}
	store.grant.CoreTime = store.grant.CoreTime.Add(-31 * time.Second)
	if _, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false); !errors.Is(err, errClockSkew) {
		t.Fatal("clock skew accepted")
	}
}
func TestKeyRotationAndCompromise(t *testing.T) {
	s, store, _ := issuer(t)
	ctx := context.Background()
	token, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	if err := s.Keys.Rotate("key2", keyFile(t), 1, false, now); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.Keys.public(now)
	if _, err := ld.Verify(token, keys, s.Installation, s.Runner, now); err != nil {
		t.Fatal("ordinary rotation lost valid preceding key")
	}
	keys, _ = s.Keys.public(now.Add(ld.Lifetime))
	if _, exists := keys["key1"]; exists {
		t.Fatal("retired key remains trusted")
	}
	if err := s.Keys.Rotate("key3", keyFile(t), 1, true, now); err == nil {
		t.Fatal("compromise without epoch advance accepted")
	}
	if err := s.Keys.Rotate("key3", keyFile(t), 2, true, now); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.Keys.public(now)
	if len(keys) != 1 {
		t.Fatal("compromised keys retained")
	}
	if _, err := s.Mint(ctx, store.grant.Scope.Binding, 1, false); err == nil {
		t.Fatal("old authority survived compromise")
	}
	if err := s.Keys.Rotate("key4", keyFile(t), 1, false, now); err == nil {
		t.Fatal("authority epoch rollback accepted")
	}
}
func TestPrivateKeyMountRefusal(t *testing.T) {
	path := keyFile(t)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring("key1", path, 1); err == nil {
		t.Fatal("publicly readable private key loaded")
	}
	if _, err := LoadKeyring("key1", t.TempDir(), 1); err == nil {
		t.Fatal("directory loaded as key")
	}
}

func (m *memoryStore) Revoke(context.Context, ld.Scope) error { m.denied = true; return nil }

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

func TestIssuerDesktopAlwaysRequiresConsent(t *testing.T) {
	issuer, store, _ := issuer(t)
	issuer.Desktop = true
	if token, e := issuer.Mint(context.Background(), store.grant.Scope.Binding, 1, false); e == nil || token != "" {
		t.Fatal("desktop minted without Settings consent despite safe posture")
	}
	store.grant.UnsafeHostOptIn = true
	if _, e := issuer.Mint(context.Background(), store.grant.Scope.Binding, 1, false); e != nil {
		t.Fatal("explicit consent refused")
	}
}

func TestIssuerEpochChangesDuringRead(t *testing.T) {
	s, store, _ := issuer(t)
	epoch := int64(1)
	s.AuthorityEpoch = func() int64 { return epoch }
	store.afterRead = func() { epoch = 2 }
	if _, e := s.current(context.Background(), store.grant.Scope.Binding, 1); e == nil {
		t.Fatal("snapshot authorized an epoch changed during its durable read")
	}
	epoch = 1
	if token, e := s.Mint(context.Background(), store.grant.Scope.Binding, 1, false); e == nil || token != "" {
		t.Fatal("external epoch change during durable read minted")
	}
	if _, e := s.Current(context.Background(), store.grant.Scope); e == nil {
		t.Fatal("old Core process returned positive authority")
	}
}

func TestIssuerReadAuditsOnlyStateChanges(t *testing.T) {
	s, store, _ := issuer(t)
	scope := store.grant.Scope
	for range 20 {
		if _, e := s.Current(t.Context(), scope); e != nil {
			t.Fatal(e)
		}
	}
	if store.reads != 20 {
		t.Fatal("authority read was cached", store.reads)
	}
	if len(store.outcomes) != 1 || store.outcomes[0] != "check:ok" {
		t.Fatal("unchanged reads grew the durable audit", store.outcomes)
	}
	store.denied = true
	for range 2 {
		if _, e := s.Current(t.Context(), scope); e == nil {
			t.Fatal("revocation hidden by read audit deduplication")
		}
	}
	if len(store.outcomes) != 3 {
		t.Fatal("refusals were not audited", store.outcomes)
	}
	store.denied = false
	store.auditDown = true
	if _, e := s.Current(t.Context(), scope); e == nil {
		t.Fatal("authority recovery bypassed its state-change audit")
	}
	store.auditDown = false
	if _, e := s.Current(t.Context(), scope); e != nil {
		t.Fatal(e)
	}
	if len(store.outcomes) != 4 {
		t.Fatal("authority recovery not audited", store.outcomes)
	}
	for range 2 {
		if _, e := s.Mint(t.Context(), scope.Binding, scope.Revision, false); e != nil {
			t.Fatal(e)
		}
	}
	if len(store.outcomes) != 6 {
		t.Fatal("mint audits were deduplicated", store.outcomes)
	}
}

func TestIssuerProviderAuditKeepsAttempt(t *testing.T) {
	s, store, _ := issuer(t)
	key, epoch := s.Keys.auditIdentity()
	attempt := "00000000-0000-4000-8000-000000000514"
	if e := s.record(t.Context(), store.grant.Scope.Binding, 1, "check", nil, AuditMetadata{KeyID: key, Epoch: epoch, AttemptID: attempt}); e != nil {
		t.Fatal(e)
	}
	if len(store.metadata) != 1 || store.metadata[0].AttemptID != attempt {
		t.Fatal("provider credential audit lost its attempt attribution")
	}
}
