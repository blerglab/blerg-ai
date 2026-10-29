package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// shellLimitsRefresh is how often the stored shell limits are read again, so a change made
// through another runner replica (or straight in the database) reaches running shells.
const shellLimitsRefresh = 30 * time.Second

type shellLimitPair struct {
	DetachedSeconds int64 `json:"detached_seconds"`
	IdleSeconds     int64 `json:"idle_seconds"`
}

type shellSettingsReply struct {
	Desktop  shellLimitPair `json:"desktop"`
	Cluster  shellLimitPair `json:"cluster"`
	Defaults struct {
		Desktop shellLimitPair `json:"desktop"`
		Cluster shellLimitPair `json:"cluster"`
	} `json:"defaults"`
	// Stored holds the administrator's overrides; null means the runtime default applies.
	Stored struct {
		Desktop storedShellLimits `json:"desktop"`
		Cluster storedShellLimits `json:"cluster"`
	} `json:"stored"`
	MinSeconds int64 `json:"min_seconds"`
	MaxSeconds int64 `json:"max_seconds"`
}

type storedShellLimits struct {
	DetachedSeconds *int64 `json:"detached_seconds"`
	IdleSeconds     *int64 `json:"idle_seconds"`
}

// shellLimitValue is one field of a change: absent leaves the limit alone, null restores the
// runtime default, and a number sets it.
type shellLimitValue struct {
	present, reset bool
	seconds        int64
}

func (v *shellLimitValue) UnmarshalJSON(b []byte) error {
	v.present = true
	if string(b) == "null" {
		v.reset = true
		return nil
	}
	return json.Unmarshal(b, &v.seconds)
}

type shellLimitChange struct {
	DetachedSeconds shellLimitValue `json:"detached_seconds"`
	IdleSeconds     shellLimitValue `json:"idle_seconds"`
}

type shellSettingsRequest struct {
	Desktop *shellLimitChange `json:"desktop"`
	Cluster *shellLimitChange `json:"cluster"`
}

func pairOf(l ownershell.Limits) shellLimitPair {
	return shellLimitPair{DetachedSeconds: int64(l.Detached / time.Second), IdleSeconds: int64(l.Idle / time.Second)}
}

// validShellLimitSeconds is zero (until the session ends) or 10 minutes to 87,600 hours.
func validShellLimitSeconds(v int64) bool {
	return v >= 0 && v <= int64(ownershell.MaxLimit/time.Second) && ownershell.ValidLimit(time.Duration(v)*time.Second)
}

// shellLimitsFromSettings overlays stored overrides on the defaults. A stored value outside the
// bounds is ignored (the default applies), never stretched or clamped.
func shellLimitsFromSettings(stored map[string]int64) ownershell.ShellLimits {
	l := ownershell.DefaultLimits()
	for key, dst := range map[string]*time.Duration{
		db.SettingShellDesktopDetachedSeconds: &l.Desktop.Detached,
		db.SettingShellDesktopIdleSeconds:     &l.Desktop.Idle,
		db.SettingShellClusterDetachedSeconds: &l.Cluster.Detached,
		db.SettingShellClusterIdleSeconds:     &l.Cluster.Idle,
	} {
		if v, ok := stored[key]; ok && validShellLimitSeconds(v) {
			*dst = time.Duration(v) * time.Second
		}
	}
	return l
}

// refreshShellLimits applies the stored limits to the shell manager. An unreadable database
// keeps the limits in force.
func (a *API) refreshShellLimits(ctx context.Context) error {
	if a.dbPool == nil || a.shells == nil {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stored, err := db.GetSettings(call, a.dbPool)
	if err != nil {
		return err
	}
	a.shells.SetLimits(shellLimitsFromSettings(stored))
	return nil
}

// watchShellLimits loads the stored limits at start and every shellLimitsRefresh.
func (a *API) watchShellLimits(ctx context.Context) {
	ticker := time.NewTicker(shellLimitsRefresh)
	defer ticker.Stop()
	for {
		if err := a.refreshShellLimits(ctx); err != nil && ctx.Err() == nil {
			log.Printf("shell limits: keeping the limits in force: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) shellSettingsReply(stored map[string]*int64) shellSettingsReply {
	current := ownershell.DefaultLimits()
	if a.shells != nil {
		current = a.shells.Limits()
	}
	defaults := ownershell.DefaultLimits()
	var out shellSettingsReply
	out.Desktop, out.Cluster = pairOf(current.Desktop), pairOf(current.Cluster)
	out.Defaults.Desktop, out.Defaults.Cluster = pairOf(defaults.Desktop), pairOf(defaults.Cluster)
	out.MinSeconds, out.MaxSeconds = int64(ownershell.MinLimit/time.Second), int64(ownershell.MaxLimit/time.Second)
	out.Stored.Desktop = storedShellLimits{stored[db.SettingShellDesktopDetachedSeconds], stored[db.SettingShellDesktopIdleSeconds]}
	out.Stored.Cluster = storedShellLimits{stored[db.SettingShellClusterDetachedSeconds], stored[db.SettingShellClusterIdleSeconds]}
	return out
}

// storedShellOverrides reads the valid stored overrides (the ones in force), keyed by setting.
func (a *API) storedShellOverrides(ctx context.Context) (map[string]*int64, error) {
	out := map[string]*int64{}
	if a.dbPool == nil {
		return out, nil
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	all, err := db.GetSettings(call, a.dbPool)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{db.SettingShellDesktopDetachedSeconds, db.SettingShellDesktopIdleSeconds, db.SettingShellClusterDetachedSeconds, db.SettingShellClusterIdleSeconds} {
		if v, ok := all[key]; ok && validShellLimitSeconds(v) {
			out[key] = &v
		}
	}
	return out, nil
}

// HandleGetShellSettings is GET /api/shells/settings: the owner-shell inactivity limits per
// runtime class. Administrators only (account.manage), like every install-wide setting.
func (a *API) HandleGetShellSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authDaemonOrCoreCap(w, r, "account.manage"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	stored, err := a.storedShellOverrides(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the settings")
		return
	}
	writeJSON(w, http.StatusOK, a.shellSettingsReply(stored))
}

// HandlePutShellSettings is PUT /api/shells/settings: an administrator sets how long a shell
// may have no browser attached, and how long it may go without typing, for desktop and
// cluster shells. Each value is 0 (until the session ends), null (back to the runtime
// default) or 10 minutes to 87,600 hours.
// The change applies to running shells; a shortened limit gives each of them at least
// 10 minutes from now.
func (a *API) HandlePutShellSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authDaemonOrCoreCap(w, r, "account.manage")
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "no database to store settings in")
		return
	}
	var req shellSettingsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	changes := map[string]shellLimitValue{}
	for _, c := range []struct {
		change         *shellLimitChange
		detached, idle string
	}{
		{req.Desktop, db.SettingShellDesktopDetachedSeconds, db.SettingShellDesktopIdleSeconds},
		{req.Cluster, db.SettingShellClusterDetachedSeconds, db.SettingShellClusterIdleSeconds},
	} {
		if c.change == nil {
			continue
		}
		if v := c.change.DetachedSeconds; v.present {
			changes[c.detached] = v
		}
		if v := c.change.IdleSeconds; v.present {
			changes[c.idle] = v
		}
	}
	if len(changes) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	for _, v := range changes {
		if !v.reset && !validShellLimitSeconds(v.seconds) {
			writeError(w, http.StatusUnprocessableEntity, "each shell limit must be 0 (until the session ends), null (the default) or between 10 minutes and 87,600 hours")
			return
		}
	}
	by := "daemon"
	if actor.Core != nil {
		by = actor.Core.Sub
	}
	for key, v := range changes {
		var err error
		if v.reset {
			err = db.DeleteSetting(r.Context(), a.dbPool, key)
		} else {
			err = db.SetSetting(r.Context(), a.dbPool, key, v.seconds, by)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not save the setting")
			return
		}
	}
	if err := a.refreshShellLimits(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "saved, but running shells could not be updated yet; they will be within a minute")
		return
	}
	stored, err := a.storedShellOverrides(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "saved, but the settings could not be read back")
		return
	}
	writeJSON(w, http.StatusOK, a.shellSettingsReply(stored))
}
