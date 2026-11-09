package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/persontime"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (a *API) accountTime(ctx context.Context, account string) (*persontime.Account, error) {
	raw, err := db.GetAccountPreference(ctx, a.dbPool, account, prefPersonTime)
	if err != nil || raw == nil {
		return nil, err
	}
	var settings persontime.Account
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// This shared input boundary covers queued turns and startup/recovery metadata for every engine.
// Strip caller-supplied metadata even when the account has no preference yet.
func (a *API) attachPersonTime(raw []byte) []byte {
	var in protocol.AgentUserMessage
	if json.Unmarshal(raw, &in) != nil || in.Type != "agent_user_message" {
		return raw
	}
	in.PersonTime = nil
	if a.dbPool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		row, err := db.GetSession(ctx, a.dbPool, in.SessionID)
		if err != nil {
			logPersonTimeFailure("input_session", err)
		} else if row != nil && row.SpawningAccountID != nil {
			in.PersonTime, err = a.accountTime(ctx, *row.SpawningAccountID)
			if err != nil {
				logPersonTimeFailure("input_account", err)
			}
		}
	}
	out, _ := json.Marshal(in)
	return out
}

// Core's Account panel reaches this route with its service key and authenticated human account.
func (a *API) HandleInternalAccountTime(w http.ResponseWriter, r *http.Request) {
	account, _, ok := a.internalDesktopActor(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPut {
		var in struct {
			persontime.Account
			Initialize bool `json:"initialize"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || !in.Valid() {
			writeError(w, 400, "Choose a valid IANA time zone and a quiet-hours window with different start and end times.")
			return
		}
		raw, _ := json.Marshal(in.Account)
		var err error
		if in.Initialize {
			_, err = a.dbPool.Exec(r.Context(), `INSERT INTO account_preferences(account_id,key,value) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, account, prefPersonTime, raw)
		} else {
			_, err = a.dbPool.Exec(r.Context(), `INSERT INTO account_preferences(account_id,key,value) VALUES($1,$2,$3)
 ON CONFLICT(account_id,key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, account, prefPersonTime, raw)
		}
		if err != nil {
			logPersonTimeFailure("save_account", err)
			failurelog.SetCause(w, err)
			writeError(w, 500, "Could not save your time zone.")
			return
		}
		if !in.Initialize {
			a.releaseClockPushLeases(r.Context(), account)
		}
	}
	settings, err := a.accountTime(r.Context(), account)
	if err != nil {
		logPersonTimeFailure("read_account", err)
		failurelog.SetCause(w, err)
		writeError(w, 500, "Could not read your time zone.")
		return
	}
	if settings == nil {
		settings = &persontime.Account{}
	}
	writeJSON(w, 200, settings)
}

// inQuietHours is the shared server policy seam, also used by the upcoming inbox.
func inQuietHours(account persontime.Account, now time.Time) bool { return account.InQuietHours(now) }

// Stage labels are fixed; typed causes retain diagnostics without logging account data or SQL.
func logPersonTimeFailure(stage string, err error) {
	failurelog.Default.Event("person_time:"+stage, slog.LevelWarn, "component", "runner", "stage", stage, "cause", failurelog.Cause(err))
}

func (a *API) releaseClockPushLeases(ctx context.Context, account string) {
	if _, err := a.dbPool.Exec(ctx, `UPDATE held_pushes SET lease_until=LEAST(lease_until,now()) WHERE account_id=$1`, account); err != nil {
		logPersonTimeFailure("push_lease_refresh", err)
	}
}
