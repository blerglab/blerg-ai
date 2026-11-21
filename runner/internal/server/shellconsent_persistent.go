package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"time"
)

// A missing accepted row is a denial; inability to read it only suspends access.
func persistentConsentReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ownershell.ErrDenied
	}
	return ownershell.ErrUnknown
}

type consentChallengeRate struct {
	start time.Time
	count int
}

// Admission precedes runtime RPC and database work. Owners have independent
// bounded buckets; inactive buckets disappear after a minute.
func (r *remoteShell) admitConsentChallenge(owner string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, bucket := range r.challengeRates {
		if now.Sub(bucket.start) >= time.Minute {
			delete(r.challengeRates, id)
		}
	}
	count := 0
	for id, c := range r.consents {
		if c.cleanupComplete || !c.consumed && now.Unix() >= c.value.ExpiresAt {
			delete(r.consents, id)
			continue
		}
		if c.value.Pins.Owner == owner {
			count++
		}
	}
	if count >= 32 || len(r.consents) >= 1024 {
		return false
	}
	bucket := r.challengeRates[owner]
	if bucket.count >= 4 || bucket.count == 0 && len(r.challengeRates) >= 1024 {
		return false
	}
	if bucket.count == 0 {
		bucket.start = now
	}
	bucket.count++
	if r.challengeRates == nil {
		r.challengeRates = map[string]consentChallengeRate{}
	}
	r.challengeRates[owner] = bucket
	return true
}

type brokerRetainedConsent struct {
	value                         protocol.ShellConsentV2
	accepted, consumed, withdrawn bool
	cleanupComplete               bool
}

func consentMatchesBinding(c protocol.ShellConsentV2, a ownershell.Actor, b ownershell.Binding) bool {
	p, w := c.Pins, b.ShellLink
	return c.Version == 2 && a.Kind == "human" && p.Owner == a.Owner && p.Login == a.Login && p.Owner == b.Owner && p.Session == b.Session && p.Launch == b.Launch && p.Runtime == b.Runtime && p.Container == b.Container && p.RuntimeGeneration == b.RuntimeGeneration && p.Root == b.Root && p.Manifest == b.ManifestDigest && p.Identity == b.IdentityDigest && p.Desktop == w.Desktop.DesktopID && p.Credential == w.Desktop.TokenID && p.OwnershipRevision == w.Desktop.Revision && p.Runner == w.Runner && p.Daemon == w.Daemon
}
func (r *remoteShell) readyPersistentConsent(a ownershell.Actor, b ownershell.Binding) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.consents {
		if c.accepted && !c.consumed && !c.withdrawn && time.Now().Unix() < c.value.ExpiresAt && consentMatchesBinding(c.value, a, b) {
			return true
		}
	}
	return false
}
func (r *remoteShell) ConsumePersistentConsent(ctx context.Context, a ownershell.Actor, b ownershell.Binding) (string, error) {
	r.mu.Lock()
	var selected *brokerRetainedConsent
	for _, c := range r.consents {
		if c.accepted && !c.consumed && !c.withdrawn && time.Now().Unix() < c.value.ExpiresAt && consentMatchesBinding(c.value, a, b) {
			selected = c
			c.consumed = true
			break
		}
	}
	r.mu.Unlock()
	if selected == nil {
		return "", ownershell.Refuse(ownershell.ReasonRuntimeConsent)
	}
	committed := false
	defer func() {
		if !committed {
			r.mu.Lock()
			selected.consumed = false
			r.mu.Unlock()
		}
	}()
	// Database metadata can deny, but cannot supply missing affirmative memory.
	stored, err := db.ReadPersistentShellConsent(ctx, r.api.dbPool, a.Owner, a.Login, b.Session, selected.value.Host.ID)
	if err != nil {
		return "", persistentConsentReadError(err)
	}
	r.mu.Lock()
	valid := stored == selected.value && !selected.withdrawn
	r.mu.Unlock()
	if !valid {
		return "", ownershell.Refuse(ownershell.ReasonRuntimeConsent)
	}
	committed = true
	return selected.value.Host.ID, nil
}
func (a *API) handlePersistentConsent(w http.ResponseWriter, r *http.Request, actor ownershell.Actor, b ownershell.Binding) {
	authority := shellAuthority{a.coreURL, a.coreInternalKey, &http.Client{Timeout: 2 * time.Second}}
	if _, _, err := authority.AuthorizePersistent(r.Context(), actor); err != nil {
		shellError(w, err)
		return
	}
	remote := a.shellRuntime
	action := r.PathValue("action")
	if action == "challenge" {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 2))
		if err != nil || len(raw) != 0 {
			writeError(w, 400, "body must be empty")
			return
		}
		if !remote.admitConsentChallenge(actor.Owner, time.Now()) {
			writeError(w, 429, "Too many shell consent requests. Try again later.")
			return
		}
		reply, err := remote.persistentCall(r.Context(), b, shellruntime.RPC{Op: "consent_challenge", Actor: actor})
		if err != nil || reply.PersistentConsent == nil || !consentMatchesBinding(*reply.PersistentConsent, actor, b) {
			writeError(w, 503, "challenge unavailable")
			return
		}
		c := *reply.PersistentConsent
		remote.mu.Lock()
		for id, v := range remote.consents {
			if v.cleanupComplete || !v.consumed && time.Now().Unix() >= v.value.ExpiresAt {
				delete(remote.consents, id)
			}
		}
		full := len(remote.consents) >= 1024
		remote.mu.Unlock()
		if full || db.CreatePersistentShellChallenge(r.Context(), a.dbPool, c) != nil {
			writeError(w, 503, "challenge unavailable")
			return
		}
		remote.mu.Lock()
		remote.consents[c.Host.ID] = &brokerRetainedConsent{value: c}
		remote.mu.Unlock()
		writeJSON(w, 201, struct {
			protocol.HostShellConsent
			Version int `json:"version"`
		}{c.Host, 2})
		return
	}
	if action != "accept" {
		writeError(w, 404, "not found")
		return
	}
	var request struct {
		Version   int    `json:"version"`
		Challenge string `json:"challenge_id"`
		Accept    bool   `json:"accept"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Version != 2 || !request.Accept || request.Challenge == "" || decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "Current shell consent is required. Refresh Blerg.")
		return
	}
	remote.mu.Lock()
	var memory *brokerRetainedConsent
	for _, c := range remote.consents {
		if c.value.Host.ChallengeID == request.Challenge && !c.accepted && !c.withdrawn && consentMatchesBinding(c.value, actor, b) {
			memory = c
			break
		}
	}
	remote.mu.Unlock()
	if memory == nil {
		writeError(w, 403, "challenge expired or runtime changed")
		return
	}
	stored, err := db.AcceptPersistentShellChallenge(r.Context(), a.dbPool, actor.Owner, actor.Login, b.Session, request.Challenge)
	if err != nil || stored != memory.value {
		writeError(w, 403, "challenge expired or runtime changed")
		return
	}
	if _, err = remote.persistentCall(r.Context(), b, shellruntime.RPC{Op: "consent_accept", Actor: actor, PersistentConsent: &stored}); err != nil {
		writeError(w, 503, "consent could not be confirmed")
		return
	}
	remote.mu.Lock()
	valid := !memory.withdrawn
	if valid {
		memory.accepted = true
	}
	remote.mu.Unlock()
	if !valid {
		writeError(w, 403, "consent withdrawn")
		return
	}
	if a.shells != nil {
		a.shells.RecordConsent(stored, ownershell.ConsentAccepted, "accepted")
	}
	writeJSON(w, 200, stored.Host)
}
func (a *API) handlePersistentWithdrawal(w http.ResponseWriter, r *http.Request, actor ownershell.Actor) {
	var request struct {
		ID string `json:"consent_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.ID == "" || decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "consent required")
		return
	}
	// No daemon describe or adoption is needed for offline withdrawal. The row
	// supplies only exact-login ownership of the acceptance being denied.
	var snapshot func(protocol.ShellConsentV2) ([]byte, error)
	if a.shells != nil {
		snapshot = func(c protocol.ShellConsentV2) ([]byte, error) {
			return json.Marshal(a.shells.ConsentAuditSnapshot(c, ownershell.ConsentWithdrawn, "withdrawn"))
		}
	}
	stored, changed, err := db.WithdrawPersistentShellConsentAudited(r.Context(), a.dbPool, actor.Owner, actor.Login, r.PathValue("id"), request.ID, snapshot)
	if err != nil {
		writeError(w, 403, "consent unavailable")
		return
	}
	// Denial and the audit snapshot are durable now. The optional log is a
	// mirror, independently of physical cleanup and process survival.
	if changed && a.shells != nil {
		a.shells.RecordConsent(stored, ownershell.ConsentWithdrawn, "withdrawn")
	}
	var value *protocol.ShellConsentV2
	if remote := a.shellRuntime; remote != nil {
		remote.mu.Lock()
		if c := remote.consents[request.ID]; c != nil && c.value.Pins.Owner == actor.Owner && c.value.Pins.Login == actor.Login && c.value.Pins.Session == r.PathValue("id") {
			c.withdrawn = true
			v := c.value
			value = &v
		}
		remote.mu.Unlock()
	}
	knownClosed := a.shells != nil && a.shells.ConsentCleanupComplete(actor, r.PathValue("id"), request.ID)
	clean := (value != nil || knownClosed) && a.shells != nil && a.shells.WithdrawConsent(r.Context(), actor, r.PathValue("id"), request.ID)
	if value != nil && a.shellRuntime != nil && !knownClosed {
		if _, err := a.shellRuntime.persistentCall(r.Context(), ownershell.Binding{Session: r.PathValue("id")}, shellruntime.RPC{Op: "consent_withdraw", Actor: actor, PersistentConsent: value}); err != nil {
			clean = false
		}
	}
	if clean && a.shellRuntime != nil {
		a.shellRuntime.mu.Lock()
		if c := a.shellRuntime.consents[request.ID]; c != nil {
			c.cleanupComplete = true
		}
		a.shellRuntime.mu.Unlock()
	}
	writeJSON(w, 200, map[string]bool{"withdrawn": true, "cleanup_pending": !clean})
}

func (a *API) HandlePersistentConsentList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := a.shellActor(w, r)
	if !ok {
		return
	}
	if err := a.checkShellSessionOwner(r.Context(), actor, r.PathValue("id")); err != nil {
		shellError(w, err)
		return
	}
	after := r.URL.Query().Get("after")
	if len(after) > 256 {
		writeError(w, 400, "invalid page")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, next, err := db.ListPersistentShellConsents(ctx, a.dbPool, actor.Owner, actor.Login, r.PathValue("id"), after)
	if err != nil {
		writeError(w, 503, "consent unavailable")
		return
	}
	type summary struct {
		ID             string `json:"id"`
		Account        string `json:"account"`
		Folder         string `json:"folder"`
		Withdrawn      bool   `json:"withdrawn"`
		CleanupPending bool   `json:"cleanup_pending"`
	}
	summaries := make([]summary, 0, len(rows))
	for _, state := range rows {
		c := state.Consent
		clean := a.shells != nil && a.shells.ConsentCleanupComplete(actor, r.PathValue("id"), c.Host.ID)
		if remote := a.shellRuntime; remote != nil {
			remote.mu.Lock()
			if memory := remote.consents[c.Host.ID]; memory != nil {
				clean = clean || memory.cleanupComplete
			}
			remote.mu.Unlock()
		}
		summaries = append(summaries, summary{ID: c.Host.ID, Account: c.Host.Account, Folder: c.Host.Cwd, Withdrawn: state.Withdrawn, CleanupPending: state.Withdrawn && !clean})
	}
	writeJSON(w, 200, struct {
		Consents []summary `json:"consents"`
		Next     string    `json:"next"`
	}{summaries, next})
}

// Called only before allocation or after a correlated definite no-allocation
// reply. A timeout or failed bootstrap cannot make an acceptance reusable.
func (r *remoteShell) ReleasePersistentConsent(_ context.Context, a ownershell.Actor, b ownershell.Binding, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.consents[id]; c != nil && consentMatchesBinding(c.value, a, b) && !c.withdrawn {
		c.consumed = false
	}
}
