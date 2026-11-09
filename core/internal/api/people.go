package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func (d Deps) registerPeopleRoutes(mux *http.ServeMux) {
	admin := func(h http.HandlerFunc) http.HandlerFunc { return requireHumanPrincipal(h, d, "account.manage") }
	mux.HandleFunc("GET /api/people", admin(d.handlePeople))
	mux.HandleFunc("POST /api/people", admin(d.handleInvitePerson))
	mux.HandleFunc("POST /api/people/{id}/disable", admin(d.handleDisablePerson))
	mux.HandleFunc("POST /auth/invite", d.handleAcceptInvite)
	mux.HandleFunc("GET /invite", func(w http.ResponseWriter, r *http.Request) {
		noFraming(w)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		d.serveApp(w, r)
	})
}
func (d Deps) handlePeople(w http.ResponseWriter, r *http.Request) {
	_, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	people, err := svc.People(r.Context())
	if err != nil {
		http.Error(w, "People are unavailable.", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"people": people})
}
func (d Deps) handleInvitePerson(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	if _, ok := d.localProvider(); !ok {
		http.Error(w, "Invites require local sign-in.", http.StatusConflict)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		http.Error(w, "Invalid person.", http.StatusBadRequest)
		return
	}
	id, code, err := svc.InvitePerson(r.Context(), p.Sub, p.Sid, body.Name, body.Email)
	if errors.Is(err, identity.ErrPersonInput) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Could not invite this person.", http.StatusForbidden)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "code": code, "expires_in_days": 7})
}
func (d Deps) handleDisablePerson(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	p, svc, ok := d.desktopPerson(w, r)
	if !ok {
		return
	}
	err := svc.DisablePerson(r.Context(), p.Sub, p.Sid, r.PathValue("id"))
	if errors.Is(identity.AsSessionBusy(err), identity.ErrSessionBusy) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Session busy. Try again.", http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, identity.ErrPersonSelf) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "Could not disable this person.", http.StatusForbidden)
		return
	}
	// Account disable is durable before contacting runner. A retry finishes desktop
	// invalidation if either service or the response was temporarily unavailable.
	status, _, callErr := d.runnerDesktopCall(r.Context(), p, http.MethodPost, "/internal/people/"+r.PathValue("id")+"/disable-desktops", nil)
	if callErr != nil || status != http.StatusOK {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"status": "disabled", "error": "Person disabled. Desktop disconnection is pending; retry this action."})
		return
	}
	writeJSON(w, map[string]string{"status": "disabled"})
}
func (d Deps) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	noFraming(w)
	w.Header().Set("Cache-Control", "no-store")
	if !d.requireSameOrigin(w, r) {
		return
	}
	if _, ok := d.localProvider(); !ok {
		http.Error(w, "Invites require local sign-in.", http.StatusNotFound)
		return
	}
	ip := d.clientIP(r)
	if ok, _ := d.LoginLimiter.Allow(ip, "invite:"+ip); !ok {
		http.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Code     string `json:"code"`
		Username string `json:"provider_subject"`
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "Invalid invitation.", http.StatusBadRequest)
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "Invites unavailable.", http.StatusServiceUnavailable)
		return
	}
	id, err := svc.AcceptPersonInvite(r.Context(), body.Code, body.Username, body.Password)
	if err != nil {
		d.LoginLimiter.Fail(ip, "invite:"+ip)
		if errors.Is(err, authprovider.ErrInvalidUsername) || errors.Is(err, authprovider.ErrWeakPassword) {
			http.Error(w, "Choose an available username and a password of 12 to 72 characters.", http.StatusBadRequest)
			return
		}
		http.Error(w, "Invitation refused. It may be expired, used, or disabled, or the username may be taken.", http.StatusForbidden)
		return
	}
	d.LoginLimiter.Reset(ip, "invite:"+ip)
	if d.startHumanSession(w, r, id, nil) {
		w.WriteHeader(http.StatusOK)
	}
}
