package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Engine discovery after install. A daemon reports, per known engine, a
// status and the facts behind it (protocol.EngineStatus); this file bounds
// those facts, writes the reason and the exact fix a person reads, and serves
// "Check again" / "Re-detect PATH" (POST /api/daemons/{id}/engine-check).

const (
	maxStatusPath    = 512
	maxStatusVersion = 32
	maxSearchedDirs  = 32
)

var (
	engineStatusValues = []string{protocol.EngineAvailable, protocol.EngineNotInstalled, protocol.EngineNotLoggedIn, protocol.EngineMisconfigure, protocol.EngineOutdated}
	engineStatusDetail = []string{"keychain", "api_key_only", "no_provider", "not_onboarded", "found_elsewhere"}
	statusPathChars    = regexp.MustCompile(`^[A-Za-z0-9/._+@,~-]+$`)
	statusVersion      = regexp.MustCompile(`^\d+(\.\d+){0,3}$`)
)

func cleanStatusPath(p string) string {
	if len(p) > maxStatusPath || !statusPathChars.MatchString(p) || (!strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/")) {
		return ""
	}
	return p
}

func cleanStatusVersion(v string) string {
	if len(v) > maxStatusVersion || !statusVersion.MatchString(v) {
		return ""
	}
	return v
}

// sanitizeEngineStatuses keeps a daemon's report to known engines, known
// values and bounded paths; it drops any text a daemon put in Reason, Fix or
// Action, which only the server writes. nil in (an older daemon) is nil out.
func sanitizeEngineStatuses(in []protocol.EngineStatus) []protocol.EngineStatus {
	if in == nil {
		return nil
	}
	out := []protocol.EngineStatus{}
	for _, st := range in {
		if _, ok := engines.Lookup(st.Engine); !ok || st.Engine == "" || !slices.Contains(engineStatusValues, st.Status) {
			continue
		}
		if slices.ContainsFunc(out, func(o protocol.EngineStatus) bool { return o.Engine == st.Engine }) {
			continue
		}
		clean := protocol.EngineStatus{Engine: st.Engine, Status: st.Status, FoundAt: cleanStatusPath(st.FoundAt),
			Version: cleanStatusVersion(st.Version), MinVersion: cleanStatusVersion(st.MinVersion)}
		if slices.Contains(engineStatusDetail, st.Detail) {
			clean.Detail = st.Detail
		}
		for _, dir := range st.SearchedPath {
			if len(clean.SearchedPath) == maxSearchedDirs {
				break
			}
			if dir = cleanStatusPath(dir); dir != "" {
				clean.SearchedPath = append(clean.SearchedPath, dir)
			}
		}
		out = append(out, clean)
		if len(out) == 16 {
			break
		}
	}
	return out
}

// engineInstallHint is the install command, where one is stable enough to quote.
var engineInstallHint = map[string]string{
	"claude": "npm install -g @anthropic-ai/claude-code",
	"codex":  "npm install -g @openai/codex",
}

func engineDisplayName(id string) string {
	if d, ok := engines.Lookup(id); ok && d.Name != "" {
		return d.Name
	}
	return id
}

// describeEngineStatus writes the person-facing reason, the exact fix and the
// one-click action for st on the desktop called host.
func describeEngineStatus(st protocol.EngineStatus, host string) protocol.EngineStatus {
	name := engineDisplayName(st.Engine)
	if host == "" {
		host = "this desktop"
	}
	st.Reason, st.Fix, st.Action = "", "", ""
	switch st.Status {
	case protocol.EngineNotInstalled:
		st.Action = "redetect_path"
		if st.Detail == "found_elsewhere" && st.FoundAt != "" {
			st.Reason = fmt.Sprintf("%s is installed at %s on %s, but that folder isn't on the daemon's PATH.", name, st.FoundAt, host)
			st.Fix = "Re-detect PATH to give the daemon your login shell's PATH. If it's still missing, add that folder to PATH in your shell profile, then Re-detect PATH."
			return st
		}
		st.Reason = fmt.Sprintf("%s isn't on the daemon's PATH on %s.", name, host)
		if hint := engineInstallHint[st.Engine]; hint != "" {
			st.Fix = fmt.Sprintf("Install it on %s (`%s`). If your shell already finds it, Re-detect PATH.", host, hint)
		} else {
			st.Fix = fmt.Sprintf("Install %s on %s. If your shell already finds it, Re-detect PATH.", name, host)
		}
	case protocol.EngineNotLoggedIn:
		st.Action = "check_again"
		st.Reason = fmt.Sprintf("%s is installed on %s but not logged in.", name, host)
		switch st.Engine {
		case "claude":
			st.Fix = fmt.Sprintf("Run `claude` on %s and log in (or set CLAUDE_CODE_OAUTH_TOKEN for its daemon), then Check again.", host)
		case "codex":
			st.Fix = fmt.Sprintf("Run `codex login` on %s, then Check again.", host)
		default:
			st.Fix = fmt.Sprintf("Log in to %s on %s, then Check again.", name, host)
		}
	case protocol.EngineMisconfigure:
		st.Action = "check_again"
		switch st.Detail {
		case "no_provider":
			st.Reason = fmt.Sprintf("%s on %s has no model provider set up.", name, host)
			st.Fix = fmt.Sprintf("Run `hermes setup` on %s, then Check again.", host)
		case "not_onboarded":
			st.Reason = fmt.Sprintf("%s on %s isn't set up yet.", name, host)
			st.Fix = fmt.Sprintf("Run `openclaw onboard` on %s, then Check again.", host)
		default:
			st.Reason = fmt.Sprintf("%s on %s isn't set up to run.", name, host)
			st.Fix = fmt.Sprintf("Finish setting up %s on %s, then Check again.", name, host)
		}
	case protocol.EngineOutdated:
		st.Action = "check_again"
		version := st.Version
		if version == "" {
			version = "installed"
		}
		st.Reason = fmt.Sprintf("%s %s on %s is older than %s, the oldest Blerg supports.", name, version, host, st.MinVersion)
		st.Fix = fmt.Sprintf("Update %s on %s, then Check again.", name, host)
	}
	return st
}

// describedEngineStatuses is dc's last report, described for the browser.
func describedEngineStatuses(dc *DaemonConn) []protocol.EngineStatus {
	raw := dc.EngineStatuses()
	if raw == nil {
		return nil
	}
	out := make([]protocol.EngineStatus, 0, len(raw))
	for _, st := range raw {
		out = append(out, describeEngineStatus(st, dc.Name))
	}
	return out
}

// engineStatusOn is dc's described status for engine, if it reported one.
func engineStatusOn(dc *DaemonConn, engine string) (protocol.EngineStatus, bool) {
	for _, st := range dc.EngineStatuses() {
		if st.Engine == engine {
			return describeEngineStatus(st, dc.Name), true
		}
	}
	return protocol.EngineStatus{}, false
}

// ─── Check again / Re-detect PATH ────────────────────────────────────────────

// engineCheckTimeout bounds the wait for a daemon's answer; reading a login
// shell is itself bounded to 10 s twice on the daemon. A var for tests.
var engineCheckTimeout = 30 * time.Second

// engineCheckWorkBound is how long a check sent to a daemon counts against its
// desktop's limit when nobody waits for it any more (the browser canceled, or
// the wait timed out): until the daemon answers, disconnects, or this passes.
// The daemon bounds its own work below it. A var for tests.
var engineCheckWorkBound = 60 * time.Second

// engineChecksPerDesktop is how many checks may be outstanding on one desktop.
const engineChecksPerDesktop = 2

var (
	errEngineCheckGone    = errors.New("daemon disconnected")
	errEngineCheckTimeout = errors.New("daemon did not answer")
	errEngineCheckBusy    = errors.New("daemon send buffer full")
	errEngineCheckLimit   = errors.New("too many engine checks on this desktop")
)

type engineCheckWaiter struct {
	dc    *DaemonConn
	ch    chan protocol.EngineCheckResult // buffered 1: delivery never blocks, waited for or not
	until time.Time
}

// engineCheckRequests are the checks outstanding on a daemon. An entry is the
// daemon's work, not the HTTP request's: it leaves when the daemon answers or
// disconnects, or at its work bound, never when the requester stops waiting.
type engineCheckRequests struct {
	mu      sync.Mutex
	pending map[string]engineCheckWaiter
}

func (h *Hub) requestEngineCheck(ctx context.Context, dc *DaemonConn, redetectPath bool) (protocol.EngineCheckResult, error) {
	id := newUUID()
	now := time.Now()
	w := engineCheckWaiter{dc: dc, ch: make(chan protocol.EngineCheckResult, 1), until: now.Add(engineCheckWorkBound)}
	r := &h.engineCheckReqs
	r.mu.Lock()
	if r.pending == nil {
		r.pending = map[string]engineCheckWaiter{}
	}
	perDaemon := 0
	for key, p := range r.pending {
		if now.After(p.until) {
			delete(r.pending, key)
			continue
		}
		if p.dc == dc {
			perDaemon++
		}
	}
	if perDaemon >= engineChecksPerDesktop || len(r.pending) >= 256 {
		r.mu.Unlock()
		return protocol.EngineCheckResult{}, errEngineCheckLimit
	}
	r.pending[id] = w
	r.mu.Unlock()

	data, err := json.Marshal(protocol.EngineCheck{Type: "engine_check", RequestID: id, RedetectPath: redetectPath})
	if err != nil || !h.trySendToDaemon(dc, data) {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		if err != nil {
			return protocol.EngineCheckResult{}, err
		}
		return protocol.EngineCheckResult{}, errEngineCheckBusy
	}
	timer := time.NewTimer(engineCheckTimeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok {
			return protocol.EngineCheckResult{}, errEngineCheckGone
		}
		return res, nil
	case <-timer.C:
		return protocol.EngineCheckResult{}, errEngineCheckTimeout
	case <-ctx.Done():
		return protocol.EngineCheckResult{}, ctx.Err()
	}
}

// handleEngineCheckResult is the read pump's engine_check_result case: the
// fresh detection is applied whether or not anyone still waits for it.
func (h *Hub) handleEngineCheckResult(dc *DaemonConn, res protocol.EngineCheckResult) {
	if res.AvailableEngines != nil {
		dc.SetAvailableEngines(res.AvailableEngines)
	}
	if res.Statuses != nil {
		dc.SetEngineStatuses(res.Statuses)
	}
	r := &h.engineCheckReqs
	r.mu.Lock()
	w, ok := r.pending[res.RequestID]
	if ok && w.dc == dc {
		delete(r.pending, res.RequestID)
	}
	r.mu.Unlock()
	if ok && w.dc == dc {
		w.ch <- res
	}
}

func (h *Hub) abandonEngineChecks(dc *DaemonConn) {
	r := &h.engineCheckReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

type engineCheckRequest struct {
	RedetectPath bool `json:"redetect_path"`
}

type engineCheckResponse struct {
	EngineStatuses   []protocol.EngineStatus `json:"engine_statuses"`
	AvailableEngines []string                `json:"available_engines"`
	PathAdded        []string                `json:"path_added,omitempty"`
	PathError        string                  `json:"path_error,omitempty"`
}

// HandleEngineCheck is POST /api/daemons/{id}/engine-check: the owner's
// "Check again" (re-detect engines now) or, with redetect_path, "Re-detect
// PATH" (the daemon reads their login shell's PATH first). 200 with the
// described statuses; 404 when no such desktop of theirs is connected; 504
// when it did not answer (an older daemon).
func (a *API) HandleEngineCheck(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	var req engineCheckRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	dc := a.hub.ownedHostDaemon(r.Context(), principal.Sub, r.PathValue("id"))
	if dc == nil || !isHostDaemon(dc) {
		writeError(w, http.StatusNotFound, "desktop not connected")
		return
	}
	res, err := a.hub.requestEngineCheck(r.Context(), dc, req.RedetectPath)
	switch {
	case errors.Is(err, errEngineCheckBusy):
		writeError(w, http.StatusServiceUnavailable, "the desktop is busy; try again in a moment")
		return
	case errors.Is(err, errEngineCheckLimit):
		writeError(w, http.StatusTooManyRequests, "the desktop is already checking its engines; try again when that finishes")
		return
	case errors.Is(err, errEngineCheckGone):
		writeError(w, http.StatusBadGateway, "the desktop disconnected before answering")
		return
	case errors.Is(err, errEngineCheckTimeout):
		writeError(w, http.StatusGatewayTimeout, "the desktop did not answer; update its daemon (daemon/install.sh install) to check engines from here")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "request failed")
		return
	}
	if res.Busy {
		writeError(w, http.StatusTooManyRequests, "the desktop is already checking its engines; try again when that finishes")
		return
	}
	out := engineCheckResponse{EngineStatuses: describedEngineStatuses(dc), AvailableEngines: nonNilStrings(dc.AvailableEngines())}
	if out.EngineStatuses == nil {
		out.EngineStatuses = []protocol.EngineStatus{}
	}
	for _, dir := range res.PathAdded {
		if dir = cleanStatusPath(dir); dir != "" && len(out.PathAdded) < maxSearchedDirs {
			out.PathAdded = append(out.PathAdded, dir)
		}
	}
	if res.PathError != "" {
		out.PathError = "Couldn't read your login shell's PATH on " + dc.Name + ". Check that your shell starts without asking for input, or run `daemon/install.sh refresh-path` there."
	} else if res.PathSaveError != "" {
		out.PathError = "The PATH applies now, but " + dc.Name + " couldn't save it, so a daemon restart loses it. Check that the daemon's state folder is writable, or run `daemon/install.sh refresh-path` there."
	}
	// Engine details (CLI locations, the PATH searched) go only to the desktop's
	// owner at the moment of delivery: the response is written under the same
	// ownership fence as GET /api/daemons, so a transfer that committed while the
	// daemon was answering gets a 409, never the details.
	a.hub.deliverDesktopInventory(r.Context(), principal.Sub, []*DaemonConn{dc}, w, func(out2 http.ResponseWriter) {
		out2.Header().Set("Cache-Control", "private, no-store")
		writeJSON(out2, http.StatusOK, out)
	})
}
