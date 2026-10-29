package server

// Session attachment storage (#231): a session's attachments together are at most its own limit
// (sessions.upload_limit_bytes) or else the install default; an administrator can raise one
// session in steps up to the install maximum. Both install values are core admin settings
// (POST /internal/session-storage); before an administrator saves them, or while core cannot be
// read, the runner uses its environment (BLERG_RUNNER_MAX_SESSION_UPLOADS_MB for the default).
//
//	POST /api/sessions/{id}/storage          an administrator: raise this session's attachment total
//	POST /api/runner/sessions/{id}/storage   the same on the runner contract (the board's chat)

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/sessionstorage"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// sessionStorageTTL bounds how often the runner asks core: a space check polls the file list.
const sessionStorageTTL = 30 * time.Second

type sessionStorageCache struct {
	limits    sessionStorageBytes
	fromCore  bool
	fetchedAt time.Time
}

// sessionStorageBytes are the install values in bytes.
type sessionStorageBytes struct{ defaultBytes, maxBytes int64 }

func sessionStorageFromEnv() sessionStorageBytes {
	def := maxUploadBytesPerSession
	return sessionStorageBytes{defaultBytes: def, maxBytes: max(def, int64(sessionstorage.DefaultMaxUploadMB)<<20)}
}

// effectiveSessionStorage is the install default and maximum: core's saved settings when there
// are any, else the environment. A failed read keeps the last good value.
func (a *API) effectiveSessionStorage(ctx context.Context) sessionStorageBytes {
	a.sessionStorageMu.Lock()
	defer a.sessionStorageMu.Unlock()
	c := &a.sessionStorage
	if c.fetchedAt.IsZero() && !c.fromCore {
		c.limits = sessionStorageFromEnv()
	}
	if a.coreURL == "" || a.coreInternalKey == "" || (!c.fetchedAt.IsZero() && time.Since(c.fetchedAt) < sessionStorageTTL) {
		return c.limits
	}
	l, set, err := a.fetchCoreSessionStorage(ctx)
	c.fetchedAt = time.Now()
	switch {
	case err != nil:
	case set && l.Validate() == nil:
		c.limits, c.fromCore = sessionStorageBytes{defaultBytes: l.DefaultUploadMB << 20, maxBytes: l.MaxUploadMB << 20}, true
	case !set:
		c.limits, c.fromCore = sessionStorageFromEnv(), false
	}
	return c.limits
}

// forgetSessionStorage makes the next read ask core again (tests, and after a raise).
func (a *API) forgetSessionStorage() {
	a.sessionStorageMu.Lock()
	a.sessionStorage.fetchedAt = time.Time{}
	a.sessionStorageMu.Unlock()
}

func (a *API) fetchCoreSessionStorage(ctx context.Context) (sessionstorage.Limits, bool, error) {
	var out struct {
		sessionstorage.Limits
		Set bool `json:"set"`
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/session-storage", bytes.NewBufferString("{}"))
	if err != nil {
		return out.Limits, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := coreHTTPClient(nil).Do(req) // #nosec G704 -- Target is the operator-configured Core URL.
	if err != nil {
		return out.Limits, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out.Limits, false, errors.New("session storage could not be read from core")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return out.Limits, false, err
	}
	err = json.Unmarshal(raw, &out)
	return out.Limits, out.Set, err
}

// sessionUploadLimit is what this session's attachments may weigh together, and the install
// maximum an administrator may raise it to.
func (a *API) sessionUploadLimit(ctx context.Context, sid string) (limit, maximum int64, err error) {
	install := a.effectiveSessionStorage(ctx)
	own, err := db.SessionUploadLimit(ctx, a.dbPool, sid)
	if err != nil {
		return 0, 0, err
	}
	if own > 0 {
		return own, install.maxBytes, nil
	}
	return install.defaultBytes, install.maxBytes, nil
}

// sessionFileLimits is currentFileLimits with this session's attachment total.
func (a *API) sessionFileLimits(ctx context.Context, sid string) (sessionFileLimitsView, error) {
	limit, maximum, err := a.sessionUploadLimit(ctx, sid)
	if err != nil {
		return sessionFileLimitsView{}, err
	}
	l := currentFileLimits()
	l.MaxSessionUploadBytes = limit
	return sessionFileLimitsView{FileLimits: l, MaxSessionUploadCapBytes: maximum}, nil
}

// raiseStorage raises one session's attachment total by a step, for an administrator.
func (a *API) raiseStorage(w http.ResponseWriter, r *http.Request, sid string, actor fileActor) {
	if !actor.admin || actor.account == "" {
		writeError(w, http.StatusForbidden, "Only an administrator can increase this session's storage. Ask an admin to increase it.")
		return
	}
	install := a.effectiveSessionStorage(r.Context())
	prev, next, err := db.RaiseSessionUploadLimit(r.Context(), a.dbPool, sid, actor.account, install.defaultBytes, int64(sessionstorage.StepMB)<<20, install.maxBytes)
	if errors.Is(err, db.ErrSessionStorageAtMaximum) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This session already has the most storage this install allows (" + sizeText(install.maxBytes) + "). An administrator can raise the maximum in Settings.", "code": "storage_at_maximum"})
		return
	}
	if err != nil {
		writeDatabaseFailure(w, "increase the session's storage", "session storage raise", err)
		return
	}
	log.Printf("session storage: %s raised session %s attachments from %s to %s", strings.TrimSpace(actor.account), sid, sizeText(prev), sizeText(next))
	limits, err := a.sessionFileLimits(r.Context(), sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's storage", "session storage read", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"limits": limits, "can_increase": next < install.maxBytes})
}

// increaseStorage is POST /api/sessions/{id}/storage.
func (h *artifactsHandler) increaseStorage(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.person(w, r)
	if !ok {
		return
	}
	p, ok := h.api.authBrowser(w, r)
	if !ok {
		return
	}
	h.api.raiseStorage(w, r, sid, browserFileActor(p))
}

// HandleRunnerIncreaseStorage is POST /api/runner/sessions/{id}/storage.
func (a *API) HandleRunnerIncreaseStorage(w http.ResponseWriter, r *http.Request) {
	if p, sid, ok := a.runnerSessionFiles(w, r); ok {
		a.raiseStorage(w, r, sid, a.runnerFileActor(r, p, sid))
	}
}
