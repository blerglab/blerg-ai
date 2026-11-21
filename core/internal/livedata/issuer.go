package livedata

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

type Grant = ld.Grant
type Snapshot = ld.Snapshot

// Store.Current must read current durable state on every invocation, without a
// credential cache. It must deny disabled owners, revoked/expired grants,
// mismatched account/connection epochs and policies other than exact pinned allow.
type Store interface {
	Current(context.Context, string, int64) (Grant, error)
	Audit(context.Context, string, int64, string, string, AuditMetadata) error
	Revoke(context.Context, ld.Scope) error
}

// Issuer is an internal service seam; it is not an authentication endpoint.
// Installation/Runner identity comes from verified service transport, never a
// request body. Unknown host posture is unsafe. No approval mutation exists here.
type Issuer struct {
	readAuditMu sync.Mutex
	readAudits  map[string]ld.Scope

	Store        Store
	Keys         *Keyring
	Desktop      bool
	Installation string
	Runner       string
	// Posture is the trusted installation-wide assessment, not agent-supplied.
	// It is called per invocation so a newly privileged runtime fences polling.
	Posture        func(context.Context) (safe bool, err error)
	AuthorityEpoch func() int64
	Now            func() time.Time
}

func (s *Issuer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Issuer) current(ctx context.Context, binding string, revision int64) (Snapshot, error) {
	if s.Store == nil || s.Keys == nil || s.Posture == nil || !s.epochCurrent() {
		return Snapshot{}, ErrUnavailable
	}
	g, err := s.Store.Current(ctx, binding, revision)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := g.ApprovedRevision(); err != nil {
		return Snapshot{}, ld.ErrDenied
	}
	now := s.now()
	keys, epoch := s.Keys.public(now)
	if !g.Scope.Valid() || g.Scope.Binding != binding || g.Scope.Revision != revision || g.Scope.Installation != s.Installation || g.Scope.Runner != s.Runner || g.Scope.AuthorityEpoch != epoch || !now.Before(g.ExpiresAt) {
		return Snapshot{}, ld.ErrDenied
	}
	offset := now.Sub(g.CoreTime)
	if offset < -30*time.Second || offset > 30*time.Second {
		return Snapshot{}, errClockSkew
	}
	safe, err := s.Posture(ctx)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	if (!safe || s.Desktop) && !g.UnsafeHostOptIn {
		return Snapshot{}, errUnsafeHost
	}
	if !s.epochCurrent() {
		return Snapshot{}, ErrUnavailable
	}
	return Snapshot{Grant: g, Keys: keys}, nil
}

var errClockSkew = errors.New("clock_skew")
var errUnsafeHost = errors.New("unsafe_host")

func (s *Issuer) record(ctx context.Context, binding string, revision int64, operation string, resultErr error, signed ...AuditMetadata) error {
	if resultErr != nil {
		failurelog.Default.Event("live_data_issuer:"+outcome(resultErr), slog.LevelWarn, "component", "core", "operation", operation, "cause", outcome(resultErr))
	}
	if s.Store == nil {
		return ErrUnavailable
	}
	metadata := AuditMetadata{Installation: s.Installation, Runner: s.Runner}
	if s.Keys != nil {
		metadata.KeyID, metadata.Epoch = s.Keys.auditIdentity()
	}
	if len(signed) == 1 && signed[0].KeyID != "" {
		metadata.AttemptID = signed[0].AttemptID
		metadata.KeyID = signed[0].KeyID
		metadata.Epoch = signed[0].Epoch
	}
	if err := s.Store.Audit(ctx, binding, revision, operation, outcome(resultErr), metadata); err != nil {
		failurelog.Default.Event("live_data_audit", slog.LevelWarn, "component", "core", "cause", "audit_unavailable")
		if resultErr != nil && !errors.Is(resultErr, ErrUnavailable) && !errors.Is(resultErr, errClockSkew) {
			return resultErr
		}
		return ErrUnavailable
	}
	return resultErr
}

func outcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errUnsafeHost):
		return "unsafe_host"
	case errors.Is(err, errClockSkew):
		return "clock_skew"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	default:
		return "denied"
	}
}

func (s *Issuer) PublicKeys(_ context.Context) (map[string]ed25519.PublicKey, error) {
	if s.Keys == nil || !s.epochCurrent() {
		return nil, ErrUnavailable
	}
	keys, _ := s.Keys.public(s.now())
	return keys, nil
}

// Current reauthorizes even when the caller has a valid five-minute token.
func (s *Issuer) Current(ctx context.Context, scope ld.Scope) (Snapshot, error) {
	out, err := s.current(ctx, scope.Binding, scope.Revision)
	if err == nil && out.Grant.Scope != scope {
		err = ld.ErrDenied
	}
	if err = s.recordRead(ctx, scope, err); err != nil {
		return Snapshot{}, err
	}
	if !s.epochCurrent() {
		return Snapshot{}, s.recordRead(ctx, scope, ErrUnavailable)
	}
	return out, nil
}

// Mint and renewal use identical authority checks and a durable audit. The
// service adapter must authenticate mTLS before calling this internal method.
func (s *Issuer) Mint(ctx context.Context, binding string, revision int64, renew bool) (string, error) {
	op := "mint"
	if renew {
		op = "renew"
	}
	out, err := s.current(ctx, binding, revision)
	token := ""
	var signed AuditMetadata
	if err == nil && !s.epochCurrent() {
		err = ErrUnavailable
	}
	if err == nil {
		now := s.now()
		end := now.Add(ld.Lifetime)
		if out.Grant.ExpiresAt.Before(end) {
			end = out.Grant.ExpiresAt
		}
		scope := out.Grant.Scope
		token, signed, err = s.Keys.sign(ld.Claims{Scope: scope, Issuer: "blerg-core-live-data", Audience: ld.Audience(scope.Installation, scope.Runner), Operation: "poll", IssuedAt: now.Unix(), NotBefore: now.Unix(), ExpiresAt: end.Unix()}, now)
	}
	if err = s.record(ctx, binding, revision, op, err, signed); err != nil {
		return "", err
	}
	if !s.epochCurrent() {
		return "", s.record(ctx, binding, revision, op, ErrUnavailable)
	}
	return token, nil
}

// Invalidate never approves authority. Provider hash/read-only drift revokes the
// exact revision permanently, including if a later listing restores the old hash.
func (s *Issuer) Invalidate(ctx context.Context, scope ld.Scope) error {
	if s.Store == nil {
		return ErrUnavailable
	}
	if !scope.Valid() || scope.Installation != s.Installation || scope.Runner != s.Runner || !s.epochCurrent() {
		return s.record(ctx, scope.Binding, scope.Revision, "revoke", ld.ErrDenied)
	}
	err := s.Store.Revoke(ctx, scope)
	return s.record(ctx, scope.Binding, scope.Revision, "revoke", err)
}

type AuditMetadata struct {
	AttemptID    string
	Installation string
	Runner       string
	KeyID        string
	Epoch        int64
}

func (s *Issuer) epochCurrent() bool {
	if s.AuthorityEpoch == nil {
		return true
	}
	if s.Keys == nil {
		return false
	}
	_, epoch := s.Keys.auditIdentity()
	return s.AuthorityEpoch() == epoch
}

// This deduplicates audit writes, never authority reads. Refusals and recovery
// remain durable, and mint/renew/provider-call audit paths always write. Scope
// includes the approved digest and all authority generations.
func (s *Issuer) recordRead(ctx context.Context, scope ld.Scope, result error) error {
	s.readAuditMu.Lock()
	defer s.readAuditMu.Unlock()
	if result == nil && s.readAudits[scope.Binding] == scope {
		return nil
	}
	if result != nil {
		delete(s.readAudits, scope.Binding)
	}
	if e := s.record(ctx, scope.Binding, scope.Revision, "check", result); e != nil {
		return e
	}
	if s.readAudits == nil {
		s.readAudits = make(map[string]ld.Scope)
	}
	if len(s.readAudits) >= 4096 {
		// Bounded metadata only; eviction can add a subsequent state audit but can
		// never authorize a request or reuse credentials.
		for binding := range s.readAudits {
			delete(s.readAudits, binding)
			break
		}
	}
	s.readAudits[scope.Binding] = scope
	return nil
}
