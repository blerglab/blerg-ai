package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type previewBody struct {
	HTML string `json:"html"`
}

// HandlePreview handles POST /api/preview.
// It validates the daemon bearer token, stores the HTML in the hub, and
// broadcasts a preview_updated message to all connected browsers.
func (h *Hub) HandlePreview(daemonToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Validate Authorization header.
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || !tokenEqual(token, daemonToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body previewBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		h.SetPreview(body.HTML)
		h.BroadcastJSON(protocol.PreviewUpdated{
			Type: "preview_updated",
			HTML: body.HTML,
		})

		w.WriteHeader(http.StatusOK)
	}
}
