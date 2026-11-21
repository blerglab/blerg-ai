package api

import (
	"encoding/json"
	"io"
	"net/http"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/theme"
)

// This endpoint alone accepts the three first-party browser audiences. Board and
// runner relay the original bearer over their server connection; no CORS or
// audience override is enabled on any other Core endpoint. Core derives the
// account from that verified bearer and checks its live human-session proof.
func (d Deps) handleAccountTheme(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	audience := r.Header.Get("X-Blerg-Theme-Audience")
	if audience == "" {
		audience = d.Audience
	}
	switch audience {
	case "blerg-core", "blerg-board", "blerg-runner":
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	verifier := d
	verifier.Audience = audience
	p, ok := verifyBearer(w, r, verifier)
	if !ok {
		return
	}
	if p.Kind != string(cid.Human) || p.OnBehalfOf != "" || p.Sid == "" || !p.Has("card.read") {
		http.Error(w, "human sign-in required", http.StatusForbidden)
		return
	}
	_, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid)
	if err != nil {
		http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	if !live {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if d.Store == nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	var out struct {
		Theme string `json:"theme"`
	}
	if r.Method == http.MethodPut {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || !theme.Valid(out.Theme) {
			http.Error(w, "invalid theme", http.StatusBadRequest)
			return
		}
		_, err = d.Store.Pool().Exec(r.Context(), "UPDATE accounts SET theme=$2 WHERE id=$1", p.Sub, out.Theme)
	} else {
		err = d.Store.Pool().QueryRow(r.Context(), "SELECT theme FROM accounts WHERE id=$1", p.Sub).Scan(&out.Theme)
	}
	if err != nil {
		http.Error(w, "theme unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, out)
}
