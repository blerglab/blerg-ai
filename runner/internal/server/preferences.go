package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Per-account preferences: GET/PUT /api/me/preferences. Only the signed-in person's browser may read
// or write them (a human core token with the browser capability); an agent token acting for the
// person is refused, and one account can never reach another's, because the account comes from the
// token, never the request. Keys include code_colours and compact_folders.

const (
	preferencesBodyLimit = 16 << 10
	prefCodeColours      = "code_colours"
	prefCompactFolders   = "compact_folders"
)

// codeThemeIDs and codeTokenKeys mirror packages/chat/src/theme/codeThemes.ts; a test reads that file
// and fails when the two drift.
var codeThemeIDs = map[string]bool{
	"blerg": true, "github-dark": true, "github-light": true, "one-dark": true, "dracula": true,
	"monokai": true, "solarized-dark": true, "solarized-light": true, "nord": true, "gruvbox-dark": true,
}

var codeTokenKeys = map[string]bool{
	"fg": true, "bg": true, "linenum": true, "keyword": true, "string": true, "number": true, "comment": true,
	"title": true, "type": true, "variable": true, "builtin": true, "operator": true, "diffAdd": true, "diffDel": true,
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type codeColours struct {
	Dark      string                       `json:"dark"`
	Light     string                       `json:"light"`
	Overrides map[string]map[string]string `json:"overrides"`
}

// validateCodeColours decodes strictly (unknown fields refused) and checks every theme id, mode,
// token key and colour; the returned value is what is stored, normalised so overrides always has both modes.
func validateCodeColours(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var c codeColours
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("code_colours: %w", err)
	}
	if !codeThemeIDs[c.Dark] || !codeThemeIDs[c.Light] {
		return nil, errors.New("code_colours: unknown theme")
	}
	norm := map[string]map[string]string{"dark": {}, "light": {}}
	for mode, toks := range c.Overrides {
		if mode != "dark" && mode != "light" {
			return nil, fmt.Errorf("code_colours: unknown mode %q", mode)
		}
		for k, v := range toks {
			if !codeTokenKeys[k] {
				return nil, fmt.Errorf("code_colours: unknown token %q", k)
			}
			if !hexColour.MatchString(v) {
				return nil, fmt.Errorf("code_colours: %s must be #rrggbb", k)
			}
			norm[mode][k] = strings.ToLower(v)
		}
	}
	c.Overrides = norm
	out, err := json.Marshal(c)
	return out, err
}

// personAccount authenticates a browser request from the person themselves and returns their account.
func (a *API) personAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return "", false
	}
	if p.Kind != string(identity.Human) || p.OnBehalfOf != "" || p.Sub == "" {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return "", false
	}
	return p.Sub, true
}

// HandleGetMyPreferences returns the person's stored preferences: {"code_colours": {...}} with each
// key present only when it has been set.
func (a *API) HandleGetMyPreferences(w http.ResponseWriter, r *http.Request) {
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	a.writePreferences(w, r, account)
}

func (a *API) writePreferences(w http.ResponseWriter, r *http.Request, account string) {
	out := map[string]json.RawMessage{}
	for _, key := range []string{prefCodeColours, prefCompactFolders} {
		v, err := db.GetAccountPreference(r.Context(), a.dbPool, account, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read preferences")
			return
		}
		if v != nil {
			out[key] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleRunnerGetPreferences is GET /api/runner/preferences?account=<id>, the read an app's chat proxy
// makes for the person signed in to the app (the board, whose chat shows code in their colours). It is
// read-only and holds no editor: the person changes their colours in the runner. The runner key and a
// service token name the account; a human token gets its own, whatever it asks for; an agent token is
// refused, as it is not the person. An account that has set nothing, or none named, gets {}.
func (a *API) HandleRunnerGetPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	if preferencesRefused(p) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	account := r.URL.Query().Get("account")
	if p.Kind == string(identity.Human) {
		account = p.Sub
	}
	if account == "" || len(account) > 256 {
		writeJSON(w, http.StatusOK, map[string]json.RawMessage{})
		return
	}
	a.writePreferences(w, r, account)
}

// HandlePutMyPreferences stores the preference keys in the body; the stored value comes back.
func (a *API) HandlePutMyPreferences(w http.ResponseWriter, r *http.Request) {
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, preferencesBodyLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil || len(in) == 0 {
		writeError(w, http.StatusBadRequest, "body must be an object with a preference to set")
		return
	}
	stored := map[string]json.RawMessage{}
	for k, raw := range in {
		norm, err := validatePreference(k, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		stored[k] = norm
	}
	for k, v := range stored {
		if err := db.PutAccountPreference(r.Context(), a.dbPool, account, k, v); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save preferences")
			return
		}
	}
	writeJSON(w, http.StatusOK, stored)
}

// preferencesRefused: an agent token is not the person, and neither is a parent session acting for
// its children (unreachable through authRunner, refused here as well).
func preferencesRefused(p runnerPrincipal) bool {
	return p.Kind == agentPrincipalKind || p.Kind == sessionDelegateKind
}

func validatePreference(key string, raw json.RawMessage) (json.RawMessage, error) {
	switch key {
	case prefCodeColours:
		return validateCodeColours(raw)
	case prefCompactFolders:
		if string(raw) != "true" && string(raw) != "false" {
			return nil, errors.New("compact_folders must be a boolean")
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unknown preference %s", key)
	}
}
