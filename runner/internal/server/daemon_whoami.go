package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// HandleDaemonWhoami is GET /api/daemon/whoami: is this bearer a token /ws/daemon would
// accept? The installer asks it with the token it is about to put into the daemon's service,
// before it replaces the running daemon, so a token the server would refuse never replaces
// one that works. It touches no connection and registers nothing.
//
// 200 mode="desktop" for a person-bound credential (including a new revision-1
// identity), mode="daemon" for the explicitly enabled legacy master token, {"ok":true,"mode":"runtime"} for
// a session pod's current runtime token; 401 for anything else. The same tokens, and the same
// checks, as the websocket hello, which has no rate limit either: the tokens are long random
// strings, so a second place to try one teaches nothing. The reply never holds the token.
func (a *API) HandleDaemonWhoami(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if strings.HasPrefix(raw, "bdt_") {
		b, err := a.authenticateDesktop(r.Context(), raw)
		if err != nil || a.dbPool == nil {
			writeError(w, 401, "unauthorized")
			return
		}
		err = db.WithDesktopFence(r.Context(), a.dbPool, b.DesktopID, func(ctx context.Context) error {
			d, err := db.GetDesktop(ctx, a.dbPool, b.DesktopID)
			if err != nil {
				return err
			}
			if (d == nil && b.Revision == 1) || (d != nil && d.OwnerID == b.OwnerID && d.Revision == b.Revision) || db.PendingDesktopBinding(ctx, a.dbPool, b) {
				return nil
			}
			return db.ErrDesktopAuthority
		})
		if err != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "mode": "desktop", "desktop_id": b.DesktopID, "ownership_revision": b.Revision})
		return
	}
	compatible := false
	if a.isDaemonToken(raw) && a.dbPool != nil && r.URL.Query().Get("name") != "" {
		d, err := db.FindUnownedDesktopByName(r.Context(), a.dbPool, strings.ToLower(r.URL.Query().Get("name")))
		compatible = err == nil && d != nil && d.Compatibility
	}
	if a.isDaemonToken(raw) && legacyDesktopAllowed(compatible) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "daemon"})
		return
	}
	if _, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw); err != nil {
		if errors.Is(err, db.ErrTokenInvalid) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// The database could not answer: that says nothing about the token.
		log.Printf("daemon whoami: check runtime token: %v", err)
		writeError(w, http.StatusServiceUnavailable, "could not check the token now; try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "runtime"})
}
