package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/core/internal/landing"
	"github.com/blerglab/blerg-ai/core/internal/releasenotes"
)

type whatsNewResponse struct {
	Current  string                 `json:"current"`
	Since    string                 `json:"since"`
	Pending  bool                   `json:"pending"`
	Releases []releasenotes.Release `json:"releases"`
}

// The runner authenticates the browser, then supplies its live human-session
// proof over the internal connection. Reads never acknowledge an upgrade.
func (d Deps) handleInternalWhatsNew(w http.ResponseWriter, r *http.Request) {
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		Account string `json:"account_id"`
		Session string `json:"session_id"`
		Release string `json:"release"`
		Dismiss bool   `json:"dismiss"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || !uuidShape.MatchString(in.Account) || !uuidShape.MatchString(in.Session) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	_, _, live, err := d.principalLiveness(r.Context(), in.Account, "", in.Session)
	if err != nil {
		http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	if !live {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	current := landing.Release()
	if in.Release != current {
		http.Error(w, "release changed", http.StatusConflict)
		return
	}

	releases, err := releasenotes.Through(current)
	if err != nil {
		http.Error(w, "release notes unavailable", http.StatusInternalServerError)
		return
	}
	out := whatsNewResponse{Current: current, Releases: releases}
	if !releasenotes.Valid(current) {
		writeJSON(w, out)
		return
	}
	if d.Store == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	tx, err := d.Store.Pool().Begin(ctx)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var seen *string
	err = tx.QueryRow(ctx, "SELECT whats_new_seen FROM accounts WHERE id=$1 FOR UPDATE", in.Account).Scan(&seen)
	if err != nil {
		http.Error(w, "account unavailable", http.StatusInternalServerError)
		return
	}
	if seen != nil {
		out.Since = *seen
	}
	if seen != nil && releasenotes.Valid(*seen) && releasenotes.Compare(*seen, current) < 0 {
		for _, release := range releases {
			if releasenotes.Compare(release.Version, *seen) > 0 {
				out.Pending = true
			}
		}
	}
	if seen == nil || (in.Dismiss && out.Pending) {
		if _, err = tx.Exec(ctx, "UPDATE accounts SET whats_new_seen=$2 WHERE id=$1", in.Account, current); err != nil {
			http.Error(w, "could not save release", http.StatusInternalServerError)
			return
		}
	}
	if in.Dismiss {
		out.Pending = false
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "could not save release", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
