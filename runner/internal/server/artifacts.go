package server

// Session artifacts: a file an agent hands the user (`blerg-runner publish <file>`).
//
//	POST   /api/sessions/{id}/artifacts               the agent: raw body, its per-session token
//	GET    /api/sessions/{id}/artifacts               a person: the session's files, newest first
//
// A file whose name already exists in the session, from the same origin, is the next VERSION of
// it (db.InsertArtifactCapped numbers it under the per-session lock; at most db.MaxArtifactVersions
// per name). The newest version downloads under the plain name, an older one as <stem>-vN<ext>.
//
//	GET    /api/sessions/{id}/artifacts/{aid}/download  a person: the bytes as an attachment
//	GET    /api/sessions/{id}/artifacts/{aid}/raw       a person: the bytes for the in-app viewer
//	DELETE /api/sessions/{id}/artifacts/{aid}         a person who can see the session
//
// The bytes are agent-authored and are served from the app origin, so every
// read path is hardened (see artifactServedType): the stored content type comes
// from our own table, never from the uploader; nothing renderable is sent with
// its own type; every response is nosniff + `Content-Security-Policy: sandbox`
// + no-store. A person sees exactly the sessions canSeeAccount lets them see
// (a private session's files are its owner's alone) and everything else is one
// uniform 404.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Limits. Vars rather than consts so a test can exercise them without moving 25 MiB.
var (
	maxArtifactBytes       int64 = 25 << 20
	maxArtifactsPerSession       = 50
)

const versionLimitMsg = "This file already has 20 versions; delete an old one first."

const (
	maxArtifactName    = 120
	artifactEventKind  = "artifact"
	artifactPruneEvery = time.Hour
)

// artifactsDir is where every session's files live, inside the runner data directory.
func artifactsDir() string { return publishDataDir("artifacts") }

// validSessionID accepts only a canonical lower- or upper-case UUID: the value
// becomes a directory name, so nothing else may get near a path.
func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

// artifactDir is one artifact's directory; both ids must already be validated.
func artifactDir(sessionID, id string) string {
	return filepath.Join(artifactsDir(), sessionID, id)
}

type artifactsHandler struct{ api *API }

// artifactInfo is the JSON shape of one artifact (upload reply, list entry, event payload).
type artifactInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	View        string `json:"view"`
	Origin      string `json:"origin"`  // "agent" (published by the session) or "user" (attached by a person)
	Version     int    `json:"version"` // 1 for the first file of this name and origin, then 2, 3, ...
}

type artifactListEntry struct {
	artifactInfo
	LatestVersion int    `json:"latest_version"` // the highest version of this name and origin
	CreatedAt     string `json:"created_at"`
}

// latestVersions maps each (origin, name) of rows to its highest version.
func latestVersions(rows []db.ArtifactRow) map[string]int {
	out := map[string]int{}
	for _, a := range rows {
		if a.Version > out[latestKey(a)] {
			out[latestKey(a)] = a.Version
		}
	}
	return out
}

func latestKey(a db.ArtifactRow) string { return originOf(a.Origin) + "\x00" + a.Name }

// versionedDownloadName is the name a file is saved under: the plain name for the newest version
// (and for a file that has no other), <stem>-v<N><ext> for an older one (a name without an
// extension just gets -v<N> appended).
func versionedDownloadName(name string, version, latest int) string {
	if version <= 0 || version >= latest {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // a dotfile such as .env has no extension
		stem, ext = name, ""
	}
	return fmt.Sprintf("%s-v%d%s", stem, version, ext)
}

func originOf(o string) string {
	if o == db.OriginUser {
		return db.OriginUser
	}
	return db.OriginAgent
}

func infoOf(a db.ArtifactRow) artifactInfo {
	return artifactInfo{ID: a.ID, Name: a.Name, Size: a.Size, ContentType: a.ContentType, View: viewForContentType(a.ContentType), Origin: originOf(a.Origin), Version: a.Version}
}

// ─── upload ───────────────────────────────────────────────────────────────────

func (h *artifactsHandler) upload(w http.ResponseWriter, r *http.Request) {
	a := h.api
	sid := r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	// The per-session messaging token (or the daemon token): the same credential the
	// `blerg-runner` CLI already sends for ask/update/note. A token is bound to ONE
	// session, and that must be the session in the path.
	tokenSession, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if tokenSession != "" && tokenSession != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	info := h.store(w, r, sid, artifactIntake{origin: db.OriginAgent, maxFiles: maxArtifactsPerSession, noun: "files"})
	if info == nil {
		return
	}
	a.recordArtifactEvent(r.Context(), sid, info.artifactInfo)
	writeJSON(w, http.StatusCreated, struct {
		storedArtifact
		URL string `json:"url"`
	}{*info, "/sessions/" + sid + "?artifact=" + info.ID})
}

// storedArtifact is what store() returns: the new file, the version it follows (null for the
// first of its name) and the latest version of the name (always the new file's own). Only the
// upload replies carry `previous` and `latest_version`; the event payload does not.
type storedArtifact struct {
	artifactInfo
	LatestVersion int  `json:"latest_version"`
	Previous      *int `json:"previous"`
}

// artifactIntake says how store() treats one upload: who it is from and which caps apply.
type artifactIntake struct {
	origin     string
	uploadedBy string
	maxFiles   int    // per session, among files of the same origin
	maxTotal   int64  // total bytes per session among files of the same origin; 0 = no cap
	noun       string // "files" / "uploaded files", for the 409 message
}

// store reads the request body into a new file of the session and indexes it. The caller has
// authenticated the request and validated sid. On failure it has written the error reply and
// returns nil.
func (h *artifactsHandler) store(w http.ResponseWriter, r *http.Request, sid string, in artifactIntake) *storedArtifact {
	a := h.api
	limitMsg := fmt.Sprintf("this session already has %d %s; delete one first", in.maxFiles, in.noun)
	ctx := r.Context()
	sess, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		log.Printf("artifacts upload GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return nil
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return nil
	}
	if terminalSessionStatus(sess.Status) {
		writeError(w, http.StatusConflict, "session has ended")
		return nil
	}
	name := artifactNameFromRequest(r)
	if n, err := db.CountArtifactVersions(ctx, a.dbPool, sid, in.origin, name); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return nil
	} else if n >= db.MaxArtifactVersions {
		writeError(w, http.StatusConflict, versionLimitMsg)
		return nil
	}
	if n, _, err := db.CountArtifactsOrigin(ctx, a.dbPool, sid, in.origin); err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return nil
	} else if n >= in.maxFiles {
		writeError(w, http.StatusConflict, limitMsg)
		return nil
	}
	if r.ContentLength > maxArtifactBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than %d MiB", maxArtifactBytes>>20))
		return nil
	}

	id := newPublishID()
	dir := artifactDir(sid, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Printf("artifacts upload mkdir: %v", err)
		writeError(w, http.StatusInternalServerError, "storage error")
		return nil
	}
	fail := func(status int, msg string) *storedArtifact {
		_ = os.RemoveAll(dir)
		writeError(w, status, msg)
		return nil
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is built from a validated uuid and a generated id; name is a sanitised base name
	if err != nil {
		log.Printf("artifacts upload create: %v", err)
		return fail(http.StatusInternalServerError, "storage error")
	}
	sum := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(f, sum), http.MaxBytesReader(w, r.Body, maxArtifactBytes))
	closeErr := f.Close()
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(copyErr, &tooBig):
		return fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than %d MiB", maxArtifactBytes>>20))
	case copyErr != nil:
		return fail(http.StatusBadRequest, "upload interrupted")
	case closeErr != nil:
		return fail(http.StatusInternalServerError, "storage error")
	}
	head := make([]byte, 512)
	if hf, err := os.Open(path); err == nil { //nolint:gosec // same path as above
		n, _ := io.ReadFull(hf, head)
		head = head[:n]
		_ = hf.Close()
	} else {
		head = nil
	}
	contentType, view := classifyArtifact(name, head, size)

	row, err := db.InsertArtifactCapped(ctx, a.dbPool, db.ArtifactRow{
		ID: id, SessionID: sid, Name: name, Size: size, ContentType: contentType,
		SHA256: hex.EncodeToString(sum.Sum(nil)), Origin: in.origin, UploadedBy: in.uploadedBy,
	}, in.maxFiles, in.maxTotal)
	if errors.Is(err, db.ErrArtifactVersionLimit) {
		return fail(http.StatusConflict, versionLimitMsg)
	}
	if errors.Is(err, db.ErrArtifactLimit) {
		return fail(http.StatusConflict, limitMsg)
	}
	if errors.Is(err, db.ErrArtifactBytesLimit) {
		return fail(http.StatusConflict, fmt.Sprintf("this session's %s are already at the %d MiB total; delete one first", in.noun, in.maxTotal>>20))
	}
	if err != nil {
		log.Printf("artifacts upload insert: %v", err)
		return fail(http.StatusInternalServerError, "store failed")
	}
	out := storedArtifact{artifactInfo: artifactInfo{
		ID: row.ID, Name: row.Name, Size: row.Size, ContentType: row.ContentType, View: view, Origin: originOf(row.Origin), Version: row.Version,
	}, LatestVersion: row.Version}
	if row.Previous > 0 {
		out.Previous = &row.Previous
	}
	return &out
}

// artifactNameFromRequest reads the name the agent gave: X-Artifact-Name (percent-encoded,
// since an HTTP header is not UTF-8 safe), else ?name=. Always sanitised.
func artifactNameFromRequest(r *http.Request) string {
	raw := r.Header.Get("X-Artifact-Name")
	if raw != "" {
		if dec, err := url.PathUnescape(raw); err == nil {
			raw = dec
		}
	} else {
		raw = r.URL.Query().Get("name")
	}
	return sanitizeArtifactName(raw)
}

// recordArtifactEvent persists an `artifact` agent event and fans it out to the
// session's watchers, the same way the start-stage events are: the chat shows a
// card for it live, and it is still there after a reload.
func (a *API) recordArtifactEvent(ctx context.Context, sessionID string, info artifactInfo) {
	raw, err := json.Marshal(info)
	if err != nil {
		return
	}
	ev := protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(),
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: artifactEventKind, Payload: raw,
	}
	seq, _, err := db.AppendAgentEvent(ctx, a.dbPool, sessionID, ev.ClientEventID, ev.Kind, string(raw))
	if err != nil {
		log.Printf("artifact event %s: %v", sessionID, err)
		return
	}
	ev.Seq = seq
	if msg, err := json.Marshal(ev); err == nil {
		a.hub.FanOutSessionOutput(sessionID, msg)
	}
}

// ─── reads and delete (a signed-in person) ───────────────────────────────────

// person authenticates a signed-in person who may see the session in the path:
// 401 without a credential, 403 for an agent token or the runner key, and one
// uniform 404 for a session that is unknown, malformed, or private to someone else.
func (h *artifactsHandler) person(w http.ResponseWriter, r *http.Request) (sessionID string, ok bool) {
	sid, _, ok := h.personAccount(w, r)
	return sid, ok
}

// personAccount is person plus the signed-in account id.
func (h *artifactsHandler) personAccount(w http.ResponseWriter, r *http.Request) (sessionID, account string, ok bool) {
	a := h.api
	p, ok := a.authBrowser(w, r)
	if !ok {
		return "", "", false
	}
	if p.Kind != "human" {
		writeError(w, http.StatusForbidden, "files can only be opened by a signed-in person, not by an agent token")
		return "", "", false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return "", "", false
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return "", "", false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		log.Printf("artifacts GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return "", "", false
	}
	if row == nil || !canSeeAccount(p.Sub, row) {
		writeError(w, http.StatusNotFound, "not found")
		return "", "", false
	}
	return sid, p.Sub, true
}

// artifact loads the artifact named by the path, or writes the uniform 404.
func (h *artifactsHandler) artifact(w http.ResponseWriter, r *http.Request, sid string) *db.ArtifactRow {
	id := r.PathValue("aid")
	if !validPublishID(id) {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	row, err := db.GetArtifact(r.Context(), h.api.dbPool, sid, id)
	if err != nil {
		log.Printf("artifacts get: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return nil
	}
	if row == nil || row.Name != filepath.Base(row.Name) || row.Name == "." || row.Name == ".." {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return row
}

// The read, download and delete bodies below take the session id their caller has already
// authenticated: a signed-in person on the browser routes (person), or the runner contract's
// principal on the agent-token routes (runner_files.go). Same bytes, same headers, same 404s,
// whichever credential opened the door.

func (h *artifactsHandler) list(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.person(w, r); ok {
		h.listSession(w, r, sid)
	}
}

func (h *artifactsHandler) listSession(w http.ResponseWriter, r *http.Request, sid string) {
	rows, err := db.ListArtifacts(r.Context(), h.api.dbPool, sid)
	if err != nil {
		log.Printf("artifacts list: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	latest := latestVersions(rows)
	out := make([]artifactListEntry, 0, len(rows))
	for _, a := range rows {
		out = append(out, artifactListEntry{infoOf(a), latest[latestKey(a)], a.CreatedAt.UTC().Format(time.RFC3339)})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}

func (h *artifactsHandler) download(w http.ResponseWriter, r *http.Request) { h.serve(w, r, false) }
func (h *artifactsHandler) raw(w http.ResponseWriter, r *http.Request)      { h.serve(w, r, true) }

func (h *artifactsHandler) serve(w http.ResponseWriter, r *http.Request, raw bool) {
	if sid, ok := h.person(w, r); ok {
		h.serveSession(w, r, sid, raw)
	}
}

func (h *artifactsHandler) serveSession(w http.ResponseWriter, r *http.Request, sid string, raw bool) {
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	latest := row.Version
	if !raw {
		var err error
		if latest, err = db.LatestArtifactVersion(r.Context(), h.api.dbPool, sid, originOf(row.Origin), row.Name); err != nil {
			log.Printf("artifacts latest version: %v", err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
	}
	f, err := os.Open(filepath.Join(artifactDir(sid, row.ID), row.Name))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = f.Close() }()
	hd := w.Header()
	hd.Set("Content-Type", artifactServedType(row.ContentType, raw))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "sandbox")
	hd.Set("Cache-Control", "private, no-store")
	if !raw {
		hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+escapeFilename(versionedDownloadName(row.Name, row.Version, latest)))
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (h *artifactsHandler) remove(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.person(w, r); ok {
		h.removeSession(w, r, sid)
	}
}

func (h *artifactsHandler) removeSession(w http.ResponseWriter, r *http.Request, sid string) {
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	if _, err := db.DeleteArtifact(r.Context(), h.api.dbPool, sid, row.ID); err != nil {
		log.Printf("artifacts delete: %v", err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if err := os.RemoveAll(artifactDir(sid, row.ID)); err != nil {
		log.Printf("artifacts delete files %s/%s: %v", sid, row.ID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// escapeFilename percent-encodes a file name for an RFC 5987 ext-value.
func escapeFilename(name string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(name) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ─── cleanup ─────────────────────────────────────────────────────────────────

// removeSessionArtifactFiles deletes a session's files (best effort). The index rows go with the
// session by cascade; this is the half the database cannot do. Anything that is not a session id
// is ignored, so it can never walk a path.
func removeSessionArtifactFiles(sessionID string) {
	if !validSessionID(sessionID) {
		return
	}
	if err := os.RemoveAll(filepath.Join(artifactsDir(), sessionID)); err != nil { //nolint:gosec // sessionID passed validSessionID above: a canonical uuid, no separators
		log.Printf("artifacts: remove files of %s: %v", sessionID, err)
	}
}

// pruneOrphanArtifacts removes the directories of sessions that no longer exist (deleted while
// the server was down, or a removal that failed). Only directories named like a session id are
// considered. It reports how many it removed.
func (a *API) pruneOrphanArtifacts(ctx context.Context) (int, error) {
	if a.dbPool == nil {
		return 0, nil
	}
	ents, err := os.ReadDir(artifactsDir())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && validSessionID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	live, err := db.ExistingSessionIDs(ctx, a.dbPool, ids)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		if live[id] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(artifactsDir(), id)); err != nil {
			log.Printf("artifacts prune %s: %v", id, err)
			continue
		}
		removed++
	}
	return removed, nil
}

// runArtifactPrune prunes once at start and then every hour until ctx ends.
func (a *API) runArtifactPrune(ctx context.Context) {
	t := time.NewTicker(artifactPruneEvery)
	defer t.Stop()
	for {
		if n, err := a.pruneOrphanArtifacts(ctx); err != nil {
			log.Printf("artifacts prune: %v", err)
		} else if n > 0 {
			log.Printf("artifacts prune: removed %d orphaned director(ies)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
