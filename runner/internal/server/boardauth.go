package server

import (
	"net/http"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// authMessaging validates the bearer token for session→user messaging endpoints
// (HandlePostMessages, HandleGetMessageAnswer, HandlePostMessageExpire).
//
// The daemon master token grants full access and returns ("", true) so the
// handler reads session_id from the request body as before. A board token is
// accepted when it carries the "message" capability; in that case
// boardSessionID is set to the token's session_id so the caller can override
// the request body's session_id field (per spec: the sending session is derived
// from the token, not the body).
//
// Token resolution order matches rawBearer:
//  1. Authorization: Bearer <raw>
//  2. X-Blerg-Runner-Board-Token: <raw>
//
// On failure writes 401 (missing/invalid token) or 403 (valid token, wrong
// cap) and returns ("", false). No response is written on success.
func (a *API) authMessaging(w http.ResponseWriter, r *http.Request) (boardSessionID string, ok bool) {
	// Extract raw token.
	var raw string
	if auth := r.Header.Get("Authorization"); auth != "" {
		if t, cut := strings.CutPrefix(auth, "Bearer "); cut {
			raw = t
		}
	}
	if raw == "" {
		raw = r.Header.Get("X-Blerg-Runner-Board-Token")
	}
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}

	// Daemon-token path.
	if a.isDaemonToken(raw) {
		return "", true
	}

	// Board-token path.
	row, err := db.ValidateBoardToken(r.Context(), a.dbPool, raw)
	if err != nil {
		// Invalid or expired token — not the daemon token either.
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}

	for _, cap := range row.Capabilities {
		if cap == "message" {
			return row.SessionID, true
		}
	}

	writeError(w, http.StatusForbidden, "forbidden")
	return "", false
}

// boardActor is who authenticated a board-surface request. Exactly one of
// Core / Board / Daemon is set.
type boardActor struct {
	Core   *identity.Principal // set when a core access token authenticated the call
	Board  *db.BoardTokenRow   // set when a board-scoped token did
	Daemon bool                // the daemon master token
}

// rawBearer returns the credential a request carries: Authorization: Bearer
// first, X-Blerg-Runner-Board-Token as the fallback. Empty when neither.
func (a *API) rawBearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if t, ok := strings.CutPrefix(h, "Bearer "); ok {
			return t
		}
	}
	return r.Header.Get("X-Blerg-Runner-Board-Token")
}

// isDaemonToken reports whether raw is the daemon master token. A blank
// configured token never matches, so an unset deployment cannot be walked
// into with an empty header.
func (a *API) isDaemonToken(raw string) bool { return tokenEqual(raw, a.daemonToken) }

// authBrowserOrBoard accepts, in order: the daemon token; a core access token
// (aud blerg-runner) holding coreCap; a board token scoped to boardID with the
// "board" capability. Writes 401/403 and returns ok=false otherwise.
//
// There is deliberately no "no header = trusted" branch: the ingress-only
// tier this replaced (authBoardOptional) was the anonymous surface audit C2
// closed. A request without a credential is refused, full stop.
//
// boardID == "" means the route has no board to scope a board token to. The
// board token is then accepted here only so the caller can refuse it itself
// via requireCoreOrDaemon (board create/delete, a reply on a no-board session).
func (a *API) authBrowserOrBoard(w http.ResponseWriter, r *http.Request, boardID, coreCap string) (boardActor, bool) {
	raw := a.rawBearer(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return boardActor{}, false
	}
	if a.isDaemonToken(raw) {
		return boardActor{Daemon: true}, true
	}
	if a.coreAuth != nil {
		if p, err := identity.Verify(raw, coreAuthAudience, a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps); err == nil {
			if !p.Has(coreCap) {
				writeError(w, http.StatusForbidden, "missing capability: "+coreCap)
				return boardActor{}, false
			}
			return boardActor{Core: &p}, true
		}
	}
	if a.dbPool == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return boardActor{}, false
	}
	row, err := db.ValidateBoardToken(r.Context(), a.dbPool, raw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return boardActor{}, false
	}
	if boardID != "" && row.BoardID != boardID {
		writeError(w, http.StatusForbidden, "forbidden")
		return boardActor{}, false
	}
	if !slices.Contains(row.Capabilities, "board") {
		writeError(w, http.StatusForbidden, "forbidden")
		return boardActor{}, false
	}
	return boardActor{Board: &row}, true
}

// requireCoreOrDaemon refuses a board-token actor on routes a board token can
// never use (creating or deleting boards, replying to a message whose session
// has no board). Writes 403 and returns false in that case.
func requireCoreOrDaemon(w http.ResponseWriter, actor boardActor) bool {
	if actor.Core == nil && !actor.Daemon {
		writeError(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// authDaemonOrCoreCap accepts the daemon token or a core token holding coreCap.
// Board tokens are not a credential here. Writes 401 (no / unverifiable
// credential) or 403 (core token without coreCap) and returns ok=false.
func (a *API) authDaemonOrCoreCap(w http.ResponseWriter, r *http.Request, coreCap string) (boardActor, bool) {
	raw := a.rawBearer(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return boardActor{}, false
	}
	if a.isDaemonToken(raw) {
		return boardActor{Daemon: true}, true
	}
	if a.coreAuth != nil {
		if p, err := identity.Verify(raw, coreAuthAudience, a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps); err == nil {
			if !p.Has(coreCap) {
				writeError(w, http.StatusForbidden, "missing capability: "+coreCap)
				return boardActor{}, false
			}
			return boardActor{Core: &p}, true
		}
	}
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return boardActor{}, false
}
