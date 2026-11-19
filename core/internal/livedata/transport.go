package livedata

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// Transport is registered only on the dedicated TLS mux. Enabled stays false in
// production until polling admission has passed review. Provider arguments remain
// in the approved revision. Credential release is separately bound to
// a committed Runner attempt receipt, over this dedicated identity only.
type Transport struct {
	Issuer   *Issuer
	Verify   func(*http.Request) error
	Enabled  bool
	Provider func(context.Context, ld.AttemptReceipt) (ld.ProviderCredential, error)
}

func (t Transport) Register(mux *http.ServeMux) {
	for _, op := range []string{"mint", "keys", "current", "invalidate", "provider"} {
		mux.Handle("POST "+ld.IssuerPath+op, t.handler(op))
	}
}
func (t Transport) handler(op string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if t.Issuer == nil || t.Verify == nil || t.Verify(r) != nil {
			t.refuse(w)
			return
		}
		identity, e := ld.AuthenticatedRunner(r)
		if e != nil || identity.Installation != t.Issuer.Installation || identity.Runner != t.Issuer.Runner {
			t.refuse(w)
			return
		}
		for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
			if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
				t.refuse(w)
				return
			}
		}
		operation := "check"
		if op == "mint" {
			operation = "mint"
		}
		if op == "invalidate" {
			operation = "revoke"
		}
		fail := func(ctx context.Context, err error) {
			_ = t.Issuer.record(ctx, "", 0, operation, err)
			http.Error(w, "Unavailable.", serviceFailureStatus(err))
		}
		var mint ld.MintRequest
		if op == "mint" {
			if decodeService(w, r, &mint) != nil {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			if mint.Renew {
				operation = "renew"
			}
		}
		if !t.Enabled {
			fail(r.Context(), ErrUnavailable)
			return
		}
		var out any
		switch op {
		case "provider":
			var body ld.ProviderRequest
			if decodeService(w, r, &body) != nil || !uuidID.MatchString(body.Attempt) || t.Provider == nil || (body.Stage != "list" && body.Stage != "call") {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			keys, err := t.Issuer.PublicKeys(r.Context())
			if err != nil {
				fail(r.Context(), err)
				return
			}
			claims, err := ld.Verify(body.Token, keys, identity.Installation, identity.Runner, t.Issuer.now())
			if err != nil {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			if _, err = t.Issuer.Current(r.Context(), claims.Scope); err != nil {
				fail(r.Context(), err)
				return
			}
			keyID, epoch := t.Issuer.Keys.auditIdentity()
			audit := AuditMetadata{Installation: identity.Installation, Runner: identity.Runner, KeyID: keyID, Epoch: epoch, AttemptID: body.Attempt}
			if err = t.Issuer.record(r.Context(), claims.Scope.Binding, claims.Scope.Revision, "check", nil, audit); err != nil {
				fail(r.Context(), err)
				return
			}
			credential, err := t.Provider(r.Context(), ld.AttemptReceipt{Scope: claims.Scope, Attempt: body.Attempt, Stage: body.Stage})
			if err != nil {
				_ = t.Issuer.record(r.Context(), claims.Scope.Binding, claims.Scope.Revision, "check", err, audit)
				fail(r.Context(), err)
				return
			}
			if _, err = t.Issuer.Current(r.Context(), claims.Scope); err != nil {
				fail(r.Context(), err)
				return
			}
			if _, err = ld.Verify(body.Token, keys, identity.Installation, identity.Runner, t.Issuer.now()); err != nil {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			expires := time.Unix(claims.ExpiresAt, 0)
			if credential.ExpiresAt.IsZero() || credential.ExpiresAt.After(expires) {
				credential.ExpiresAt = expires
			}
			out = credential
		case "mint":
			token, err := t.Issuer.Mint(r.Context(), mint.Binding, mint.Revision, mint.Renew)
			if err != nil {
				http.Error(w, "Unavailable.", serviceFailureStatus(err))
				return
			}
			out = ld.MintResponse{Token: token}
		case "keys":
			var body struct{}
			if decodeService(w, r, &body) != nil {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			keys, err := t.Issuer.PublicKeys(r.Context())
			if err != nil {
				fail(r.Context(), err)
				return
			}
			out = ld.KeysResponse{Keys: keys}
		case "current", "invalidate":
			var scope ld.Scope
			if decodeService(w, r, &scope) != nil || !scope.Valid() || scope.Installation != identity.Installation || scope.Runner != identity.Runner {
				fail(r.Context(), ld.ErrDenied)
				return
			}
			if op == "current" {
				state, err := t.Issuer.Current(r.Context(), scope)
				if err != nil {
					http.Error(w, "Unavailable.", serviceFailureStatus(err))
					return
				}
				out = state
			} else {
				if err := t.Issuer.Invalidate(r.Context(), scope); err != nil {
					http.Error(w, "Unavailable.", serviceFailureStatus(err))
					return
				}
				out = ld.RevokeResponse{Revoked: true}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if json.NewEncoder(w).Encode(out) != nil {
			failurelog.Default.Event("live_data_issuer_response_failed", slog.LevelWarn)
		}
	})
}
func (t Transport) refuse(w http.ResponseWriter) {
	failurelog.Default.Event("live_data_issuer_identity_refused", slog.LevelWarn)
	http.Error(w, "Unavailable.", http.StatusUnauthorized)
}
func decodeService(w http.ResponseWriter, r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return ld.ErrDenied
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return ld.ErrDenied
	}
	return nil
}

func serviceFailureStatus(err error) int {
	if outcome(err) == "denied" || outcome(err) == "unsafe_host" {
		return http.StatusForbidden
	}
	return http.StatusServiceUnavailable
}
