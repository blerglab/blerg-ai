package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

var usageNoticeSettingKeys = [3]string{"usage_notice_warning", "usage_notice_danger", "usage_notice_critical"}
var defaultUsageNoticeThresholds = [3]int{80, 90, 95}

type usageNoticeSettings struct {
	Thresholds []int `json:"thresholds"`
}

func validUsageNoticeThresholds(t []int) bool {
	return len(t) == 3 && t[0] >= 1 && t[0] < t[1] && t[1] < t[2] && t[2] <= 100
}

func (a *API) usageNoticeThresholds(ctx context.Context) ([3]int, error) {
	t := defaultUsageNoticeThresholds
	settings, err := db.GetSettings(ctx, a.dbPool)
	if err != nil {
		return t, err
	}
	for i, k := range usageNoticeSettingKeys {
		if v, ok := settings[k]; ok {
			t[i] = int(v)
		}
	}
	if !validUsageNoticeThresholds(t[:]) {
		return t, fmt.Errorf("invalid stored usage notice thresholds")
	}
	return t, nil
}

// HandleUsageNoticeSettings exposes install-wide runner settings to administrators, including on
// desktop-only installs. All three values are validated and saved together.
func (a *API) HandleUsageNoticeSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authDaemonOrCoreCap(w, r, "account.manage")
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "no database to store settings in")
		return
	}
	if r.Method == http.MethodPut {
		var req usageNoticeSettings
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if !validUsageNoticeThresholds(req.Thresholds) {
			writeError(w, http.StatusUnprocessableEntity, "thresholds must contain three increasing whole percentages between 1 and 100")
			return
		}
		by := "daemon"
		if actor.Core != nil {
			by = actor.Core.Sub
		}
		tx, err := a.dbPool.Begin(r.Context())
		if err != nil {
			writeError(w, 500, "could not save settings")
			return
		}
		defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
		for i, k := range usageNoticeSettingKeys {
			if _, err = tx.Exec(r.Context(), `INSERT INTO runner_settings(key,value,updated_by) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now(),updated_by=EXCLUDED.updated_by`, k, req.Thresholds[i], by); err != nil {
				writeError(w, 500, "could not save settings")
				return
			}
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "could not save settings")
			return
		}
	}
	t, err := a.usageNoticeThresholds(r.Context())
	if err != nil {
		writeError(w, 500, "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, usageNoticeSettings{Thresholds: t[:]})
}
