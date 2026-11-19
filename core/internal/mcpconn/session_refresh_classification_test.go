package mcpconn

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/netguard"
)

func TestSessionRefreshConfirmedNoSendKeepsCredential(t *testing.T) {
	for _, kind := range []string{"build", "dial", "dns", "policy", "tls"} {
		t.Run(kind, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			sessionNearExpiry(e)
			ctx := context.Background()
			original := e.svc.httpc
			switch kind {
			case "build":
				var ct []byte
				var key *string
				if err := e.pool.QueryRow(ctx, `SELECT secret_ciphertext,key_id FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&ct, &key); err != nil {
					t.Fatal(err)
				}
				bundle, err := e.svc.openBundle(ctx, ct, key)
				if err != nil {
					t.Fatal(err)
				}
				bundle.TokenEndpoint = ":"
				sealed, kid, err := e.svc.sealBundle(ctx, bundle)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET secret_ciphertext=$2,key_id=$3 WHERE id=$1`, in.ConnectionID, sealed, kid); err != nil {
					t.Fatal(err)
				}
			default:
				e.svc.httpc = &http.Client{Transport: reviewAuthorityTransport(func(*http.Request) (*http.Response, error) {
					switch kind {
					case "dial":
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
					case "tls":
						return nil, &tls.CertificateVerificationError{Err: errors.New("synthetic certificate verification failure")}
					case "dns":
						return nil, &net.DNSError{Err: "synthetic lookup failure", Name: "provider.example", IsNotFound: true}
					default:
						return nil, netguard.ErrBlockedAddress
					}
				})}
			}
			if _, err := e.svc.FetchSessionSecret(ctx, in); !errors.Is(err, ErrUnavailable) {
				t.Fatal("confirmed no-send failure must remain retryable", err)
			}
			var state, status string
			var generation int64
			var released bool
			if err := e.pool.QueryRow(ctx, `SELECT lifecycle,status,credential_generation,session_refresh_lease_until IS NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&state, &status, &generation, &released); err != nil {
				t.Fatal(err)
			}
			if state != "active" || status != "ok" || generation != in.CredentialGeneration || !released {
				t.Fatal("no-send failure suspended or stranded its credential")
			}
			if spent, _, revokes, _ := e.provider.Stats(); spent != 0 || revokes != 0 {
				t.Fatal("no-send failure spent or revoked tokens")
			}
			e.svc.httpc = original
			if kind != "build" {
				if _, err := e.svc.FetchSessionSecret(ctx, in); err != nil {
					t.Fatal("safe subsequent retry failed", err)
				}
			}
		})
	}
}

func TestSessionRefreshKnownRejectionIsTerminalNotUncertain(t *testing.T) {
	for _, code := range []string{"invalid_grant", "invalid_client", "unauthorized_client"} {
		t.Run(code, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			sessionNearExpiry(e)
			e.provider.RefreshStatus = 400
			e.provider.RefreshError = code
			_, err := e.svc.FetchSessionSecret(context.Background(), in)
			if !errors.Is(err, ErrNotFound) || FailureCause(err) != "scoped_refresh_rejected" {
				t.Fatal("known rejection was not classified terminal", err)
			}
			var state string
			if err := e.pool.QueryRow(context.Background(), `SELECT lifecycle FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&state); err != nil || state != "suspended" {
				t.Fatal("rejected credential remained live", err)
			}
		})
	}
}

func TestSessionRefreshNoSendCannotReleaseSupersededClaim(t *testing.T) {
	for _, kind := range []string{"fence", "ciphertext", "generation", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			sessionNearExpiry(e)
			ctx := context.Background()
			e.svc.httpc = &http.Client{Transport: reviewAuthorityTransport(func(*http.Request) (*http.Response, error) {
				var query string
				switch kind {
				case "fence":
					query = `UPDATE mcp_connections SET refresh_fence=refresh_fence+1 WHERE id=$1`
				case "ciphertext":
					query = `UPDATE mcp_connections SET secret_ciphertext=secret_ciphertext||decode('00','hex') WHERE id=$1`
				case "generation":
					query = `UPDATE mcp_connections SET credential_generation=credential_generation+1 WHERE id=$1`
				case "expiry":
					query = `UPDATE mcp_connections SET session_refresh_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`
				}
				if _, err := e.pool.Exec(ctx, query, in.ConnectionID); err != nil {
					t.Error(err)
				}
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			})}
			if _, err := e.svc.FetchSessionSecret(ctx, in); !errors.Is(err, ErrNotFound) {
				t.Fatal("superseded claim was released", err)
			}
			var retained bool
			if err := e.pool.QueryRow(ctx, `SELECT session_refresh_lease_until IS NOT NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&retained); err != nil || !retained {
				t.Fatal("cleared a superseded or expired lease", err)
			}
		})
	}
}

func TestSessionRefreshReadFailureAfterSpendRemainsUncertain(t *testing.T) {
	e, in := sessionDispatchReady(t)
	sessionNearExpiry(e)
	original := e.svc.httpc.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	e.svc.httpc = &http.Client{Transport: reviewAuthorityTransport(func(r *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		_ = response.Body.Close()
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: &net.DNSError{Err: "synthetic nested lookup cause", Name: "provider.example"}}
	})}
	if _, err := e.svc.FetchSessionSecret(context.Background(), in); !errors.Is(err, ErrNotFound) || FailureCause(err) != "scoped_refresh_uncertain" {
		t.Fatal("post-send failure was treated as safe retry", err)
	}
	if spent, reuse, _, _ := e.provider.Stats(); spent != 1 || reuse != 0 {
		t.Fatal("test did not spend exactly one refresh token")
	}
}

type reviewGrantAuthority struct {
	SessionOAuthAuthority
	hash  string
	calls int
}

func (a *reviewGrantAuthority) ValidateSessionDispatch(_ context.Context, in SessionDispatchRequest, _ SessionOAuthAuthorization) error {
	a.calls++
	if in.GrantTokenHash != a.hash {
		return ErrNotFound
	}
	return nil
}
func TestSessionDispatchGrantHashMustReachRunnerAuthority(t *testing.T) {
	e, in := sessionDispatchReady(t)
	authority := &reviewGrantAuthority{SessionOAuthAuthority: e.svc.sessionAuthority, hash: in.GrantTokenHash}
	e.svc.sessionAuthority = authority
	if err := e.svc.CheckSessionDispatch(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.GrantTokenHash = strings.Repeat("b", 64)
	if err := e.svc.CheckSessionDispatch(context.Background(), in); !errors.Is(err, ErrNotFound) || authority.calls != 2 {
		t.Fatal("different grant hash reused cached authority or bypassed Runner check", err)
	}
}
