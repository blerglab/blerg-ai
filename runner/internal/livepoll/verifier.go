package livepoll

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
)

// Authority performs authenticated, fresh Core reads, not credential-cache hits.
type Authority interface {
	PublicKeys(context.Context) (map[string]ed25519.PublicKey, error)
	Current(context.Context, ld.Scope) (ld.Snapshot, error)
	Invalidate(context.Context, ld.Scope) error
}
type AttemptStore interface {
	ObserveEpoch(context.Context, ld.Scope) error
	Consume(context.Context, string, ld.Scope, int) error
	Finish(context.Context, string, bool) error
	RevokeRevision(context.Context, ld.Scope, string) error
}

// Definition must use a new tools/list each time, bypassing HashCacheTTL.
// The server-side adapter resolves credentials; no caller selects URLs or tools.
type Provider interface {
	Definition(context.Context, ld.Scope) (hash string, readOnly bool, err error)
	Call(context.Context, ld.Scope, json.RawMessage) (json.RawMessage, error)
	// Release drops the exact attempt's pending listing exchange on every exit.
	Release(context.Context, ld.Scope)
}

type Verifier struct {
	// The production scheduler constructor preserves the disabled rollout gate.
	Enabled      bool
	Desktop      bool
	Installation string
	Runner       string
	Authority    Authority
	Attempts     AttemptStore
	Provider     Provider
	Now          func() time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}
func (v *Verifier) current(ctx context.Context, scope ld.Scope) (ld.Snapshot, error) {
	start := v.now()
	state, err := v.Authority.Current(ctx, scope)
	end := v.now()
	if err != nil {
		if errors.Is(err, ld.ErrDenied) {
			return ld.Snapshot{}, refuse("authority_revoked")
		}
		return ld.Snapshot{}, refuse("authority_unavailable")
	}
	rtt := end.Sub(start)
	offset := state.Grant.CoreTime.Sub(start.Add(rtt / 2))
	if offset < 0 {
		offset = -offset
	}
	if rtt < 0 || rtt > 30*time.Second || offset+rtt/2 > 30*time.Second {
		return ld.Snapshot{}, refuse("clock_skew")
	}
	if v.Desktop && !state.Grant.UnsafeHostOptIn {
		return ld.Snapshot{}, refuse("unsafe_host_opt_in_required")
	}
	if state.Grant.Scope != scope || !state.Grant.ExpiresAt.After(state.Grant.CoreTime) {
		return ld.Snapshot{}, refuse("authority_mismatch")
	}
	if _, err := state.Grant.ApprovedRevision(); err != nil {
		return ld.Snapshot{}, refuse("configuration_mismatch")
	}
	if err := v.Attempts.ObserveEpoch(ctx, scope); err != nil {
		return ld.Snapshot{}, err
	}
	return state, nil
}

// Execute is in-process only. There is intentionally no request arguments
// parameter: invocation uses only the durable approved Core revision's arguments.
func (v *Verifier) Execute(ctx context.Context, token, attemptID string) (result json.RawMessage, resultErr error) {
	if !v.Enabled || v.Authority == nil || v.Attempts == nil || v.Provider == nil {
		return nil, refuse("polling_disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	keys, err := v.Authority.PublicKeys(ctx)
	if err != nil {
		if errors.Is(err, ld.ErrDenied) {
			return nil, refuse("credential_revoked")
		}
		return nil, refuse("keys_unavailable")
	}
	claims, err := ld.Verify(token, keys, v.Installation, v.Runner, v.now())
	if err != nil {
		return nil, refuse("credential_invalid")
	}
	state, err := v.current(ctx, claims.Scope)
	if err != nil {
		return nil, err
	}
	if _, err = ld.Verify(token, state.Keys, v.Installation, v.Runner, state.Grant.CoreTime); err != nil {
		return nil, refuse("credential_revoked")
	}
	if claims.ExpiresAt > state.Grant.ExpiresAt.Unix() {
		return nil, refuse("grant_expired")
	}
	ctx = ld.WithProviderRequest(ctx, ld.ProviderRequest{Token: token, Attempt: attemptID, Stage: "list"})
	defer v.Provider.Release(ctx, claims.Scope)
	hash, readOnly, err := v.Provider.Definition(ctx, claims.Scope)
	if err != nil {
		failure := providerFailure(err, "tool_listing_unavailable")
		if FailureCause(failure) == "connection_needs_sign_in" {
			return nil, v.fence(ctx, claims.Scope, "connection_needs_sign_in")
		}
		return nil, failure
	}
	if !readOnly || hash != claims.Scope.Hash {
		return nil, v.fence(ctx, claims.Scope, "tool_changed")
	}
	// A listing performs I/O: reauthorize again after it and before dispatch.
	state, err = v.current(ctx, claims.Scope)
	if err != nil {
		return nil, err
	}
	if _, err = ld.Verify(token, state.Keys, v.Installation, v.Runner, state.Grant.CoreTime); err != nil {
		return nil, refuse("credential_revoked")
	}
	if ctx.Err() != nil {
		return nil, refuse("call_canceled")
	}
	approved, err := state.Grant.ApprovedRevision()
	if err != nil {
		return nil, refuse("configuration_mismatch")
	}
	if err = v.Attempts.Consume(ctx, attemptID, claims.Scope, approved.IntervalSeconds); err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := v.Attempts.Finish(finishCtx, attemptID, succeeded); err != nil {
			result = nil
			resultErr = err
		}
	}()
	if ctx.Err() != nil {
		return nil, refuse("call_canceled")
	}
	ctx = ld.WithProviderRequest(ctx, ld.ProviderRequest{Token: token, Attempt: attemptID, Stage: "call"})
	result, err = v.Provider.Call(ctx, claims.Scope, append(json.RawMessage(nil), state.Grant.Arguments...))
	if err != nil {
		failure := providerFailure(err, "provider_unavailable")
		if FailureCause(failure) == "connection_needs_sign_in" {
			return nil, v.fence(ctx, claims.Scope, "connection_needs_sign_in")
		}
		return nil, failure
	}
	if ctx.Err() != nil {
		return nil, refuse("call_canceled")
	}
	if len(result) > 5*1024*1024 || !json.Valid(result) {
		return nil, refuse("invalid_result")
	}
	if _, err = v.current(ctx, claims.Scope); err != nil {
		return nil, err
	}
	rows, err := livemcp.Map(approved.Mapping, result)
	if err != nil || !datasetRowsFit(rows) {
		return nil, refuse("mapping_failed")
	}
	result, err = json.Marshal(rows)
	if err != nil {
		return nil, refuse("mapping_failed")
	}
	succeeded = true
	return result, nil
}

// Keep normalization within the existing chunk-2 socket/card row budget.
func datasetRowsFit(rows []livemcp.Row) bool {
	for _, row := range rows {
		if len(row) > 16 {
			return false
		}
		key, ok := row["key"].(string)
		if !ok || len(key) > 128 {
			return false
		}
		for _, value := range row {
			if text, ok := value.(string); ok && len(text) > 256 {
				return false
			}
		}
	}
	return true
}

// fence persists known authority loss before the remote invalidation attempt.
func (v *Verifier) fence(ctx context.Context, scope ld.Scope, cause string) error {
	fenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	localErr := v.Attempts.RevokeRevision(fenceCtx, scope, cause)
	cancel()
	if err := v.Authority.Invalidate(ctx, scope); err != nil {
		_ = refuse("grant_revoke_failed")
		if localErr != nil {
			return localErr
		}
	}
	return refuse(cause)
}
