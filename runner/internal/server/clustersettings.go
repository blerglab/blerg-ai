package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Bounds an admin may set. The idle timeout may be 0 (never); the lifetime cap
// may not, or a forgotten pod would run forever.
const (
	minPodIdleSeconds = 5 * 60
	maxPodSeconds     = 30 * 24 * 3600
	minPodTTLSeconds  = 3600
	// The session cap: at least one pod, and a ceiling so a typo cannot ask the cluster for hundreds.
	minMaxSessions = 1
	maxMaxSessions = 64
)

// SettingsOverrides is the JobManager.Overrides source: the stored admin
// settings, or nil without a database or when it cannot be read (the
// environment defaults then apply).
func (a *API) SettingsOverrides() func() map[string]int64 {
	if a == nil || a.dbPool == nil {
		return nil
	}
	return func() map[string]int64 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		m, err := db.GetSettings(ctx, a.dbPool)
		if err != nil {
			return nil
		}
		return m
	}
}

type clusterSettingsRequest struct {
	PodIdleTimeoutSeconds *int64 `json:"pod_idle_timeout_seconds"`
	PodTTLSeconds         *int64 `json:"pod_ttl_seconds"`
	// MaxSessions stays raw so "absent" (leave it alone) is told apart from
	// null (remove the override), which a *int64 cannot do.
	MaxSessions json.RawMessage `json:"max_sessions"`
}

// maxSessionsChange reads the raw max_sessions field. present is false when the
// field was left out; remove is true for null or 0 (back to the environment default).
func (r clusterSettingsRequest) maxSessionsChange() (value int64, present, remove bool, err error) {
	if len(r.MaxSessions) == 0 {
		return 0, false, false, nil
	}
	if string(r.MaxSessions) == "null" {
		return 0, true, true, nil
	}
	if err = json.Unmarshal(r.MaxSessions, &value); err != nil {
		return 0, true, false, err
	}
	return value, true, value == 0, nil
}

// HandlePutClusterSettings is PUT /api/cluster/settings: an administrator
// changes how long cluster session pods may sit idle, how long any pod may
// live and how many session pods may run at once. Applies to pods started after the change. Needs the account.manage
// capability (the platform admin role), or the daemon token.
func (a *API) HandlePutClusterSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authDaemonOrCoreCap(w, r, "account.manage")
	if !ok {
		return
	}
	if a.dbPool == nil || a.hub.JobManager() == nil {
		writeError(w, http.StatusConflict, "no cluster runtime or database to store settings in")
		return
	}
	var req clusterSettingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	maxSessions, maxSessionsSet, maxSessionsRemove, err := req.maxSessionsChange()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.PodIdleTimeoutSeconds == nil && req.PodTTLSeconds == nil && !maxSessionsSet {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if maxSessionsSet && !maxSessionsRemove && (maxSessions < minMaxSessions || maxSessions > maxMaxSessions) {
		writeError(w, http.StatusUnprocessableEntity, "max_sessions must be between 1 and 64 (or 0 / null to use the default)")
		return
	}
	if v := req.PodIdleTimeoutSeconds; v != nil && *v != 0 && (*v < minPodIdleSeconds || *v > maxPodSeconds) {
		writeError(w, http.StatusUnprocessableEntity, "pod_idle_timeout_seconds must be 0 (never) or between 5 minutes and 30 days")
		return
	}
	if v := req.PodTTLSeconds; v != nil && (*v < minPodTTLSeconds || *v > maxPodSeconds) {
		writeError(w, http.StatusUnprocessableEntity, "pod_ttl_seconds must be between 1 hour and 30 days")
		return
	}
	by := "daemon"
	if actor.Core != nil {
		by = actor.Core.Sub
	}
	if v := req.PodIdleTimeoutSeconds; v != nil {
		if err := db.SetSetting(r.Context(), a.dbPool, db.SettingPodIdleTimeoutSeconds, *v, by); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save the setting")
			return
		}
	}
	if v := req.PodTTLSeconds; v != nil {
		if err := db.SetSetting(r.Context(), a.dbPool, db.SettingPodTTLSeconds, *v, by); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save the setting")
			return
		}
	}
	if maxSessionsSet {
		if maxSessionsRemove {
			err = db.DeleteSetting(r.Context(), a.dbPool, db.SettingMaxSessions)
		} else {
			err = db.SetSetting(r.Context(), a.dbPool, db.SettingMaxSessions, maxSessions, by)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not save the setting")
			return
		}
	}
	writeJSON(w, http.StatusOK, a.hub.JobManager().Status()) //nolint:contextcheck // bounded by the JobManager client timeout, like the status read it mirrors
}
