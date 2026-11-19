package server

import (
	"encoding/json"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// cardTimeBudgetSettings is the budget interval (owner's decision 4, corrected: per account, not
// install-wide — account A must never read or change account B's threshold, M4).
type cardTimeBudgetSettings struct {
	IntervalHours int `json:"interval_hours"`
}

func validCardTimeBudgetInterval(hours int) bool { return hours >= 1 && hours <= 168 }

// HandleCardTimeBudgetSettings is GET/PUT /api/settings/card-time-budget: the signed-in person's
// own setting only, following HandlePlanNudges' shape (plansync.go) rather than
// HandleUsageNoticeSettings' (that one really is install-wide, by design; this one is not).
func (a *API) HandleCardTimeBudgetSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "no database to store settings in")
		return
	}
	if r.Method == http.MethodPut {
		var req cardTimeBudgetSettings
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if !validCardTimeBudgetInterval(req.IntervalHours) {
			writeError(w, http.StatusUnprocessableEntity, "interval_hours must be a whole number of hours from 1 to 168")
			return
		}
		if err := db.SetCardTimeBudgetIntervalHours(r.Context(), a.dbPool, p.Sub, req.IntervalHours, p.Sub); err != nil {
			writeError(w, 500, "could not save settings")
			return
		}
	}
	hours, err := db.CardTimeBudgetIntervalHours(r.Context(), a.dbPool, p.Sub)
	if err != nil {
		writeError(w, 500, "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, cardTimeBudgetSettings{IntervalHours: hours})
}
