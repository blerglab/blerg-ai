package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type desktopEnrollmentConnection struct {
	DesktopID      string    `json:"desktop_id"`
	Generation     string    `json:"connection_generation"`
	Hostname       string    `json:"hostname"`
	OSUser         string    `json:"os_user"`
	ConnectedSince time.Time `json:"connected_since"`
	SourceAddress  string    `json:"source_address"`
}

func enrollmentDisplayText(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 256 && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) < 0
}

func enrollmentConnectionDetails(dc *DaemonConn) (desktopEnrollmentConnection, bool) {
	if dc == nil || dc.BoundSessionID != "" || dc.Mode == "runner" || !enrollmentDisplayText(dc.Hostname) || !enrollmentDisplayText(dc.OSUser) || dc.ConnectedSince.IsZero() || dc.SourceAddress == "" {
		return desktopEnrollmentConnection{}, false
	}
	return desktopEnrollmentConnection{dc.ID, dc.DesktopGeneration, dc.Hostname, dc.OSUser, dc.ConnectedSince, dc.SourceAddress}, true
}

// These private connection details are shown only to a person eligible to enroll
// this desktop, under the same bounded final-write fence as desktop discovery.
func (a *API) HandleDesktopEnrollmentConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != string(identity.Human) || p.Sid == "" || p.OnBehalfOf != "" || !checkBrowserWSOrigin(r) {
		writeError(w, 403, "Sign in as a person on Runner to enroll this desktop.")
		return
	}
	a.desktopEnrollmentConnectionFor(w, r, p.Sub)
}

// desktopEnrollmentConnectionFor serves the connection details to actor, a person already
// authenticated by the browser route or by core on the internal route.
func (a *API) desktopEnrollmentConnectionFor(w http.ResponseWriter, r *http.Request, actor string) {
	id := r.PathValue("id")
	if a.dbPool == nil || !validSessionID(id) {
		writeError(w, 404, "Desktop unavailable.")
		return
	}
	started := false
	err := db.WithDesktopFence(r.Context(), a.dbPool, id, func(ctx context.Context) error {
		d, err := db.GetDesktop(ctx, a.dbPool, id)
		if err != nil || d == nil || (d.OwnerID != "" && d.OwnerID != actor) {
			return db.ErrDesktopAuthority
		}
		dc := a.hub.GetDaemon(id)
		details, ok := enrollmentConnectionDetails(dc)
		if !ok || !dc.DesktopEnrollment {
			return db.ErrDesktopAuthority
		}
		switch {
		case dc.LegacyDesktop:
			err = db.ValidateLegacyDesktop(ctx, a.dbPool, id, dc.DesktopGeneration, dc.LegacyRevision)
		case dc.DesktopBinding != nil:
			err = db.ValidateDesktopConnection(ctx, a.dbPool, *dc.DesktopBinding, dc.DesktopGeneration)
		default:
			return db.ErrDesktopAuthority
		}
		if err != nil {
			return err
		}
		started, err = writeDiscoverySnapshot(ctx, w, details)
		return err
	})
	if err != nil && !started {
		writeError(w, 409, "Connect this desktop and update its daemon, then review its connection details in Settings → Account → Your desktops → Enroll.")
	}
}

func (a *API) HandleEnrollDesktop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != string(identity.Human) || p.Sid == "" || p.OnBehalfOf != "" || !checkBrowserWSOrigin(r) {
		writeError(w, 403, "Sign in as a person on Runner to enroll this desktop.")
		return
	}
	a.enrollDesktopFor(w, r, p.Sub)
}

// enrollDesktopFor enrolls the desktop for actor, a person already authenticated by the
// browser route or by core on the internal route. Every #186 guard is here.
func (a *API) enrollDesktopFor(w http.ResponseWriter, r *http.Request, actor string) {
	if a.dbPool == nil {
		writeError(w, 503, "Desktop enrollment is unavailable.")
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeError(w, 404, "Desktop unavailable.")
		return
	}
	var confirmation struct {
		Confirmed  bool   `json:"confirmed"`
		Generation string `json:"connection_generation"`
	}
	// Decode before taking the fence; a missing/invalid confirmation is refused
	// after scope authorization so that the outcome can be audited safely.
	confirmationErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&confirmation)
	changed := false
	state := "enrolling"
	err := db.WithDesktopFence(r.Context(), a.dbPool, id, func(ctx context.Context) (enrollErr error) {
		d, err := db.GetDesktop(ctx, a.dbPool, id)
		if err != nil || d == nil || (d.OwnerID != "" && d.OwnerID != actor) {
			return db.ErrDesktopAuthority
		}
		// Existing issued/enrolled records are transactional. Record refused
		// attempts too, but only after authorizing this person's desktop scope.
		auditRevision := d.Revision
		defer func() {
			if enrollErr != nil {
				if err := db.RecordDesktopEnrollmentOutcome(ctx, a.dbPool, id, actor, "refused", confirmation.Generation, auditRevision); err != nil {
					enrollErr = err
				}
			}
		}()
		if confirmationErr != nil || !confirmation.Confirmed || !validSessionID(confirmation.Generation) {
			confirmation.Generation = "" // never persist an unvalidated claimed UUID
			changed = true
			return db.ErrDesktopAuthority
		}
		if err := db.RecordDesktopEnrollmentOutcome(ctx, a.dbPool, id, actor, "confirmed", confirmation.Generation, d.Revision); err != nil {
			return err
		}
		dc := a.hub.GetDaemon(id)
		if dc == nil || dc.BoundSessionID != "" || dc.Mode == "runner" {
			return db.ErrDesktopAuthority
		}
		if dc.DesktopGeneration != confirmation.Generation || d.Generation != confirmation.Generation {
			changed = true
			return db.ErrDesktopAuthority
		}
		if _, ok := enrollmentConnectionDetails(dc); !ok {
			return db.ErrDesktopAuthority
		}
		if dc.DesktopBinding != nil && d.OwnerID == actor && a.hub.desktopConnectionMatches(ctx, dc, *d, actor) {
			state = "enrolled"
			return db.RecordDesktopEnrollmentOutcome(ctx, a.dbPool, id, actor, "already_enrolled", confirmation.Generation, d.Revision)
		}
		if !dc.DesktopEnrollment || !dc.LegacyDesktop || db.ValidateLegacyDesktop(ctx, a.dbPool, id, dc.DesktopGeneration, dc.LegacyRevision) != nil {
			return db.ErrDesktopAuthority
		}
		pending, err := db.GetDesktopEnrollment(ctx, a.dbPool, id)
		if err != nil || (pending.Owner != "" && pending.TokenID != "" && pending.Owner != actor) {
			return db.ErrDesktopAuthority
		}
		revision := max(d.Revision, pending.Revision) + 1
		auditRevision = revision
		var issued protocol.DesktopEnroll
		if err := a.coreDesktopCall(ctx, "/internal/desktop-tokens/issue", map[string]any{"desktop_id": id, "owner_id": actor, "ownership_revision": revision}, &issued); err != nil {
			return err
		}
		if issued.Binding.DesktopID != id || issued.Binding.OwnerID != actor || issued.Binding.Revision != revision || issued.Binding.TokenID == "" || !issued.Binding.ExpiresAt.After(time.Now()) || !strings.HasPrefix(issued.Token, "bdt_") || len(issued.Token) != 68 {
			return errDesktopCredential
		}
		issued.Type = "desktop_enroll"
		issued.RequestID = newUUID()
		issued.ConnectionGeneration = dc.DesktopGeneration
		pending = db.DesktopEnrollment{Owner: actor, TokenID: issued.Binding.TokenID, Revision: revision, RequestID: issued.RequestID, Generation: dc.DesktopGeneration}
		if err := db.BeginDesktopEnrollment(ctx, a.dbPool, id, pending); err != nil {
			return err
		}
		raw, _ := json.Marshal(issued)
		if !a.hub.trySendToDaemon(dc, raw) {
			return db.ErrDesktopAuthority
		}
		return nil
	})
	if err != nil && changed {
		writeJSON(w, 409, map[string]string{"code": "connection_changed", "error": "This desktop's connection changed. Select Enroll again, review its current connection details, and confirm again."})
		return
	}
	if err != nil {
		writeError(w, 409, "Connect this desktop and update its daemon, then use Settings → Account → Your desktops → Enroll. An administrator can assign it first; alternatively import a person-issued credential with daemon/install.sh import-credential /path/to/credential.json.")
		return
	}
	writeJSON(w, 200, map[string]string{"status": state})
}

// The write pump checks this again under the desktop fence at delivery time.
func enrollmentDeliveryCurrent(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, raw []byte) bool {
	var m protocol.DesktopEnroll
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	p, err := db.GetDesktopEnrollment(ctx, pool, dc.ID)
	return err == nil && p.RequestID == m.RequestID && p.Generation == dc.DesktopGeneration && m.ConnectionGeneration == dc.DesktopGeneration && p.TokenID == m.Binding.TokenID
}
func acknowledgeDesktopEnrollment(ctx context.Context, h *Hub, dc *DaemonConn, raw []byte) bool {
	if !dc.LegacyDesktop || h.desktopPool == nil {
		return false
	}
	var ack protocol.DesktopEnrollAck
	if json.Unmarshal(raw, &ack) != nil || ack.ConnectionGeneration != dc.DesktopGeneration {
		return true
	}
	p, err := db.GetDesktopEnrollment(ctx, h.desktopPool, dc.ID)
	if err != nil || p.RequestID != ack.RequestID || p.Generation != dc.DesktopGeneration || h.GetDaemon(dc.ID) != dc {
		return true
	}
	if db.CompleteDesktopEnrollment(ctx, h.desktopPool, dc.ID, p) != nil {
		return true
	}
	return false // close this socket; the daemon reloads its private file on dial
}
