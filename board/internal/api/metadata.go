package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"io"
	"net/http"
)

func (a *API) handleApprovedMetadata(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.FromCore || p.Token == nil || p.Token.Kind != "agent" || p.Token.BoardID == nil || *p.Token.BoardID != r.PathValue("id") || !uuidRe.MatchString(p.CoreAccountID) || p.CoreAccountID != p.Token.LineageID {
		writeError(w, 403, "A signed board metadata approval is required.")
		return
	}
	intent, digest, ok := boardfiling.ParseMetadataCapability(p.Token.Capabilities)
	if !ok || p.Token.ID != "core:"+intent {
		writeError(w, 403, "A signed board metadata approval is required.")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	var d boardfiling.MetadataPatch
	if dec.Decode(&d) != nil || dec.Decode(new(any)) != io.EOF || boardfiling.ValidateMetadataPatch(d) != nil {
		writeError(w, 400, "Invalid metadata patch.")
		return
	}
	if d.BoardID != r.PathValue("id") || subtle.ConstantTimeCompare([]byte(boardfiling.MetadataDigest(d)), []byte(digest)) != 1 {
		writeError(w, 403, "The patch differs from the person's approval.")
		return
	}
	err := db.UpdateApprovedMetadata(r.Context(), a.Pool, d, p.CoreAccountID, intent, digest)
	if errors.Is(err, db.ErrBoardIntentUsed) {
		writeError(w, 409, "This approval was already used.")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(d.BoardID, "board_changed")
	writeJSON(w, 200, map[string]string{"board_id": d.BoardID, "status": "updated"})
}
