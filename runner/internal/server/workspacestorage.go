package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (a *API) RegisterWorkspaceStorageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/workspace/storage", a.handleWorkspaceStorage)
	mux.HandleFunc("POST /api/sessions/{id}/workspace/storage", a.handleWorkspaceStorage)
}

func (a *API) handleWorkspaceStorage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	cause := "authorization"
	success := false
	defer func() {
		if !success {
			failurelog.Default.Event("storage:"+cause, slog.LevelWarn, "component", "server", "operation", "release_storage", "cause", cause)
		}
	}()
	owner, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	row, ok := a.ownedSession(r.Context(), w, owner.Account, r.PathValue("id"))
	if !ok {
		return
	}
	cause = "unsupported_or_live_session"
	workspace, err := a.storageWorkspace(r, row.ID)
	if err != nil || workspace == nil || workspace.Runtime != "sandbox" || row.EndedAt == nil {
		writeError(w, 409, "Release storage is available only for a stopped desktop Local sandbox session.")
		return
	}
	cause = "desktop_unavailable"
	dc := a.hub.ownedHostDaemon(r.Context(), owner.Account, row.DaemonID)
	if dc == nil {
		writeError(w, 409, "The desktop running this session is offline.")
		return
	}
	operation, confirm := "preview", ""
	if r.Method == http.MethodPost {
		cause = "invalid_confirmation"
		var body struct {
			Confirm string `json:"confirm"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF || len(body.Confirm) != 64 {
			writeError(w, 400, "Confirm the current storage preview first.")
			return
		}
		operation, confirm = "release", body.Confirm
	}
	call := func(ctx context.Context, op string) (*protocol.WorkspaceStoragePreview, error) {
		raw, err := a.hub.requestExt(ctx, dc, owner.Account, row.ID, "workspace_storage_result", func(id string) any {
			return protocol.WorkspaceStorageRequest{Type: "workspace_storage", RequestID: id, SessionID: row.ID, OwnerID: owner.Account, Operation: op, Confirm: confirm}
		}, 2*time.Minute)
		if err != nil {
			return nil, err
		}
		var res protocol.WorkspaceStorageResult
		if json.Unmarshal(raw, &res) != nil || res.Error != "" || res.Preview == nil {
			return nil, errWorkspaceUnavailable
		}
		return res.Preview, nil
	}
	cause = "desktop_release_refused"
	preview, err := call(r.Context(), operation)
	if err != nil {
		writeError(w, 409, "Storage could not be released. Refresh the preview and try again; incomplete releases retain their journal for retry.")
		return
	}
	if preview.Released {
		cause = "release_acknowledgment_failed"
		if _, err := call(r.Context(), "acknowledge"); err != nil {
			writeError(w, 409, "Storage was released, but cleanup acknowledgment is pending. Try again to finish.")
			return
		}
	}
	success = true
	writeJSON(w, http.StatusOK, preview)
}

func (a *API) storageWorkspace(r *http.Request, id string) (*protocol.WorkspaceManifest, error) {
	w, err := db.GetSessionWorkspace(r.Context(), a.dbPool, id)
	if err != nil || w == nil {
		return nil, err
	}
	return w.Manifest, nil
}
