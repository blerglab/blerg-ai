package api

import (
	"net/http"

	"github.com/blerglab/blerg-ai/board/internal/auth"
)

func (a *API) handleCardDiff(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	a.cardLocalGit(w, r, p, true)
}
