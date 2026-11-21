package server

// BLG-479: a child's not-yet-imported result bundles, and the bookkeeping `cmd_session_import`
// uses to pick its branch number (design §4 step 9) and to answer `session import --list` /
// the worker card's outstanding count. This is pure bookkeeping, never a trust decision — the
// import's safety comes entirely from Main's own ancestor check and CAS, run locally; losing or
// re-sending a report here can only delay a branch number or leave a bundle listed as
// outstanding a little longer, never corrupt an import.
//
//	GET  /api/sessions/{id}/children/{child}/imports   next_n to try, and the outstanding list
//	POST /api/sessions/{id}/children/{child}/imports   report one completed import

import (
	"encoding/json"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/fileview"
)

func (a *API) handleChildImportsStatus(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok || !a.sessionFilesFor(w, r, p, child) {
		return
	}
	next, err := db.NextImportNumber(r.Context(), a.dbPool, child)
	if err != nil {
		writeDatabaseFailure(w, "read the import count", "imports status next", err)
		return
	}
	bundles, err := db.UnimportedBundles(r.Context(), a.dbPool, child)
	if err != nil {
		writeDatabaseFailure(w, "list outstanding bundles", "imports status list", err)
		return
	}
	out := make([]artifactInfo, 0, len(bundles))
	for _, b := range bundles {
		out = append(out, infoOf(b))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"next_n": next, "outstanding": out})
}

type reportImportRequest struct {
	ArtifactID string `json:"artifact_id"`
	Version    int    `json:"version"`
	N          int    `json:"n"`
}

func (a *API) handleChildImportsReport(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok || !a.sessionFilesFor(w, r, p, child) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body reportImportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}
	if body.ArtifactID == "" || body.Version <= 0 || body.N <= 0 {
		writeError(w, http.StatusBadRequest, "artifact_id, version and n are required")
		return
	}
	row, err := db.GetArtifact(r.Context(), a.dbPool, child, body.ArtifactID)
	if err != nil {
		writeDatabaseFailure(w, "find the artifact", "imports report get", err)
		return
	}
	if row == nil || row.ContentType != fileview.GitBundle || row.Version != body.Version {
		writeError(w, http.StatusNotFound, "no such result bundle at that version")
		return
	}
	if err := db.ReportImport(r.Context(), a.dbPool, child, body.ArtifactID, body.Version, body.N); err != nil {
		writeDatabaseFailure(w, "record the import", "imports report insert", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
