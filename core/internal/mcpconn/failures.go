package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/blerglab/blerg-ai/contracts/netguard"

	"github.com/jackc/pgx/v5/pgconn"
)

// FailureCause is a bounded diagnostic vocabulary. Never return Error() from a
// transport, provider or database error: it can contain a URL or secret body.
func FailureCause(err error) string {
	var authority *sessionAuthorityError
	var callback *CallbackError
	var token *tokenError
	var revocation *revocationError
	var validation *ValidationError
	var database *pgconn.PgError
	var transport *url.Error
	var syntax *json.SyntaxError
	switch {
	case errors.Is(err, errSessionRefreshNotSent):
		return "scoped_refresh_not_sent"
	case errors.Is(err, errSessionRefreshRejected):
		return "scoped_refresh_rejected"
	case errors.Is(err, ErrSessionRefreshBusy):
		return "scoped_refresh_busy"
	case errors.Is(err, errSessionRefreshAbandoned):
		return "scoped_refresh_abandoned"
	case errors.Is(err, errSessionRefreshUncertain):
		return "scoped_refresh_uncertain"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.As(err, &revocation):
		switch revocation.cause {
		case "endpoint", "transport", "http_status":
			return "revocation_" + revocation.cause
		default:
			return "revocation_response"
		}
	case errors.As(err, &token):
		switch token.cause {
		case "endpoint", "transport", "http_status", "response":
			return "token_" + token.cause
		default:
			return "token_response"
		}
	case errors.As(err, &authority):
		switch authority.cause {
		case "status", "response", "transport", "configuration":
			return "authority_" + authority.cause
		default:
			return "authority_unavailable"
		}
	case errors.As(err, &callback):
		switch callback.Reason {
		case "state", "session", "redirect_uri", "issuer", "denied", "exchange", "gone", "internal":
			return "oauth_" + callback.Reason
		default:
			return "oauth_callback"
		}
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrLimit):
		return "limit"
	case errors.Is(err, ErrNameTaken):
		return "name_taken"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.As(err, &validation):
		return "validation"
	case errors.Is(err, netguard.ErrBlockedAddress), errors.Is(err, netguard.ErrURLNotPermitted):
		return "network_policy"
	case errors.As(err, &transport):
		return "transport"
	case errors.As(err, &syntax):
		return "response"
	case errors.As(err, &database):
		return "database"
	default:
		return "internal"
	}
}

func tokenErrorCode(code string) string {
	switch code {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "temporarily_unavailable", "server_error":
		return code
	default:
		return "other"
	}
}
