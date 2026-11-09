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
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgconn"
)

// The size and count limits are in filelimits.go.

var versionLimitMsg = fmt.Sprintf("This file already has %d versions; delete an old one first.", db.MaxArtifactVersions)

func refuseArtifactVersion(w http.ResponseWriter, count int) bool {
	if count < db.MaxArtifactVersions {
		return false
	}
	writeError(w, http.StatusConflict, versionLimitMsg)
	return true
}

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
	// Live (BLG-470): this file is updated in place by every `publish --live` of the same name,
	// instead of a new version being added. Absent (false) for an ordinary versioned file.
	Live bool `json:"live,omitempty"`
	// UpdatedAt is when the row's bytes last changed: set only when Live, since a versioned file's
	// created_at already says that for each of its versions.
	UpdatedAt string `json:"updated_at,omitempty"`
	// SHA256 is computed by the server from the bytes it actually received (never a
	// client-supplied header). Always set now (BLG-479): `session import` checks a downloaded
	// result bundle against this before running any git command over it (design §4 step 2). Also
	// used, when Live, so a client applying a live push can break a tie between two updates that
	// happen to share the same updated_at deterministically.
	SHA256 string `json:"sha256"`
}

type artifactListEntry struct {
	artifactInfo
	LatestVersion int    `json:"latest_version"` // the highest version of this name and origin
	CreatedAt     string `json:"created_at"`
	// Missing: the row is here but its bytes are not on disk (a data directory that did not
	// survive a restart). The file is listed so it can be deleted; it cannot be viewed.
	Missing bool `json:"missing,omitempty"`
	// CanDelete: whether the person asking may delete this file (#231): the session's owner or an
	// administrator may delete any file, and a person their own attachments.
	CanDelete bool `json:"can_delete"`
}

// sessionFileLimitsView is a session's `limits` in its file list: the install limits with this
// session's own attachment total, and the most an administrator may raise that total to.
type sessionFileLimitsView struct {
	protocol.FileLimits
	MaxSessionUploadCapBytes int64 `json:"max_session_upload_cap_bytes"`
}

// fileGoneMsg is the body of a 410: the row exists, its bytes do not.
const fileGoneMsg = "This file is no longer available: the runner still lists it, but its bytes are gone."

// artifactPath is where one artifact's bytes live; sid and the row's id must already be validated.
func artifactPath(sid string, a db.ArtifactRow) string {
	return filepath.Join(artifactDir(sid, a.ID), a.Name)
}

// artifactFileMissing reports whether a listed file's bytes are gone from disk. Only a definite
// "does not exist" counts: any other error (permissions, a busy disk) leaves the file listed as
// present, and the read path answers for itself.
func artifactFileMissing(sid string, a db.ArtifactRow) bool {
	_, err := os.Stat(artifactPath(sid, a))
	return errors.Is(err, os.ErrNotExist)
}

// liveIntegrityShaCheckCap: above this size, serving a live file only compares its size against
// the row, never its content — hashing a large file on every read is not "cheap" the way the size
// check is. Most live files (charts, small snapshots) are well under it.
const liveIntegrityShaCheckCap = 8 << 20

// liveFileIntegrityMismatch reports whether a live row's bytes on disk (already open as f, left
// re-wound to the start on return) do not match what the row says (BLG-470 round 3 #3): a crash
// between UpdateLiveArtifactContent's commit and its rename can leave the row describing content
// the rename never actually wrote, with nothing else to notice. A size mismatch is always checked;
// a sha256 mismatch only when the file is small enough for that to stay cheap on every read.
func liveFileIntegrityMismatch(f *os.File, row db.ArtifactRow) (mismatch bool, detail string) {
	st, err := f.Stat()
	if err != nil {
		return false, ""
	}
	if st.Size() != row.Size {
		return true, fmt.Sprintf("size on disk %d, row says %d", st.Size(), row.Size)
	}
	if st.Size() > liveIntegrityShaCheckCap {
		return false, ""
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return false, "" // a disk read error here will fail the actual serve too; not this check's job
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, ""
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != row.SHA256 {
		return true, fmt.Sprintf("sha256 on disk %s, row says %s", got, row.SHA256)
	}
	return false, ""
}

// latestVersions maps each (origin, name) of rows to its highest version.
// latestVersions maps each (origin, name) of rows to the version a reader should treat as latest:
// the live row's, when the name has one, else the highest version among its rows. The live row
// wins even when some other row outranks it (BLG-470 #5): a server that does not know the live
// column can still insert an ordinary version over a live name during a rolling update, and that
// version's number can outrank the live row's.
func latestVersions(rows []db.ArtifactRow) map[string]int {
	out := map[string]int{}
	live := map[string]int{}
	for _, a := range rows {
		k := latestKey(a)
		if a.Live {
			live[k] = a.Version
		}
		if a.Version > out[k] {
			out[k] = a.Version
		}
	}
	for k, v := range live {
		out[k] = v
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
	info := artifactInfo{ID: a.ID, Name: a.Name, Size: a.Size, ContentType: a.ContentType, View: viewForContentType(a.ContentType), Origin: originOf(a.Origin), Version: a.Version, SHA256: a.SHA256}
	if a.Live {
		info.Live = true
		info.UpdatedAt = a.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return info
}

// ─── upload ───────────────────────────────────────────────────────────────────

func (h *artifactsHandler) upload(w http.ResponseWriter, r *http.Request) {
	a := h.api
	sid := r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
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
	live := r.Header.Get("X-Artifact-Live") == "true"
	info := h.store(w, r, sid, artifactIntake{origin: db.OriginAgent, maxFiles: maxArtifactsPerSession, maxTotal: maxArtifactBytesPerSession, noun: "files", live: live})
	if info == nil {
		return
	}
	if info.liveOverwrite {
		// An in-place republish of a live file is not history (BLG-470 #4): pushed to open
		// viewers without becoming a transcript event, so a chart refreshed often does not grow
		// the session's replay or storage, and a viewer updates just this one file instead of
		// re-reading the whole list.
		a.fanOutLiveArtifactUpdate(sid, info.artifactInfo)
	} else {
		a.recordArtifactEvent(r.Context(), sid, info.artifactInfo)
	}
	superseded := a.supersedeReviews(context.WithoutCancel(r.Context()), sid, info.artifactInfo)
	writeJSON(w, http.StatusCreated, struct {
		storedArtifact
		URL string `json:"url"`
		// SupersededReviews: pending review requests of older versions this one superseded.
		SupersededReviews []reviewRequestView `json:"superseded_reviews,omitempty"`
	}{*info, "/sessions/" + sid + "?artifact=" + info.ID, superseded})
}

// storedArtifact is what store() returns: the new file, the version it follows (null for the
// first of its name) and the latest version of the name (always the new file's own). Only the
// upload replies carry `previous` and `latest_version`; the event payload does not.
type storedArtifact struct {
	artifactInfo
	LatestVersion int  `json:"latest_version"`
	Previous      *int `json:"previous"`
	// liveOverwrite (BLG-470, unexported: never serialized): set only by overwriteLive's success
	// path, so upload() knows this republish updated an existing live row in place rather than
	// adding a row (a first live publish or the explicit switch from versioned) — the former is
	// pushed live without becoming a transcript event, the latter is recorded as one, same as any
	// other new file.
	liveOverwrite bool
}

// artifactIntake says how store() treats one upload: who it is from and which caps apply.
type artifactIntake struct {
	origin     string
	uploadedBy string
	maxFiles   int    // per session, among files of the same origin
	maxTotal   int64  // total bytes per session among files of the same origin; 0 = no cap
	noun       string // "files" / "uploaded files", for the 409 message
	// live (BLG-470): `publish --live`. A name with no live row yet gets one (the explicit switch,
	// or the first publish of a new name); a name that already has one is overwritten in place.
	live bool
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
		writeDatabaseFailure(w, "check the session", "artifacts upload GetSession", err)
		return nil
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return nil
	}
	if terminalSessionStatus(sess.Status) {
		writeError(w, http.StatusConflict, "This session has ended; files can be added only while it runs.")
		return nil
	}
	name := artifactNameFromRequest(r)
	// The size check comes before a byte is read: answered without reading the body, a large
	// upload is cut off, but the client that checked the advertised limits never sends one.
	if r.ContentLength > maxArtifactBytes {
		writeError(w, http.StatusRequestEntityTooLarge, tooLargeMsg(r.ContentLength))
		return nil
	}
	existingLive, err := db.GetLiveArtifact(ctx, a.dbPool, sid, in.origin, name)
	if err != nil {
		writeDatabaseFailure(w, "find the live file", "artifacts upload GetLiveArtifact", err)
		return nil
	}
	switch {
	case in.live && existingLive != nil:
		return h.overwriteLive(w, r, sid, *existingLive, in)
	case in.live:
		// No live row yet for this name: the explicit switch (a name with versioned history) or a
		// brand new live file. Either way this publish becomes that name's one live row, the same
		// way a first version does, just marked live (below, via InsertLiveArtifactCapped).
	case existingLive != nil:
		// A plain publish never silently takes over a live name's slot (switching is explicit,
		// in either direction): without this, a later ordinary version would outrank the live row
		// by version number and bump it out of "latest" while it stayed marked live.
		writeError(w, http.StatusConflict, "this file is live; publish --live to update it in place, or unpublish it first to start a new versioned file under this name")
		return nil
	default:
		if n, err := db.CountArtifactVersions(ctx, a.dbPool, sid, in.origin, name); err != nil {
			writeDatabaseFailure(w, "count the file's versions", "artifacts upload CountArtifactVersions", err)
			return nil
		} else if refuseArtifactVersion(w, n) {
			return nil
		}
	}
	if n, total, err := db.CountArtifactsOrigin(ctx, a.dbPool, sid, in.origin); err != nil {
		writeDatabaseFailure(w, "count the session's files", "artifacts upload CountArtifactsOrigin", err)
		return nil
	} else if in.maxFiles > 0 && n >= in.maxFiles {
		writeError(w, http.StatusConflict, limitMsg)
		return nil
	} else if in.maxTotal > 0 && r.ContentLength > 0 && total+r.ContentLength > in.maxTotal {
		writeJSON(w, http.StatusConflict, map[string]any{"error": totalLimitMsg(in), "code": "file_space_required"})
		return nil
	}

	id := newPublishID()
	dir := artifactDir(sid, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeStorageFailure(w, "save the file", accessWrite, "artifacts upload mkdir", err)
		return nil
	}
	fail := func(status int, msg string) *storedArtifact {
		_ = os.RemoveAll(dir)
		writeError(w, status, msg)
		return nil
	}
	failStorage := func(logContext string, err error) *storedArtifact {
		_ = os.RemoveAll(dir)
		writeStorageFailure(w, "save the file", accessWrite, logContext, err)
		return nil
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is built from a validated uuid and a generated id; name is a sanitised base name
	if err != nil {
		return failStorage("artifacts upload create", err)
	}
	sum := sha256.New()
	disk := &errWriter{w: f}
	size, copyErr := io.Copy(io.MultiWriter(disk, sum), http.MaxBytesReader(w, r.Body, maxArtifactBytes))
	closeErr := f.Close()
	var tooBig *http.MaxBytesError
	switch {
	case disk.err != nil:
		// The disk failed, not the client: a full disk shows up here, at the first write it refuses.
		return failStorage("artifacts upload write", disk.err)
	case errors.As(copyErr, &tooBig):
		return fail(http.StatusRequestEntityTooLarge, tooLargeMsg(r.ContentLength))
	case copyErr != nil:
		return fail(http.StatusBadRequest, "The upload stopped before the whole file arrived; try again.")
	case closeErr != nil:
		return failStorage("artifacts upload close", closeErr)
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

	newRow := db.ArtifactRow{
		ID: id, SessionID: sid, Name: name, Size: size, ContentType: contentType,
		SHA256: hex.EncodeToString(sum.Sum(nil)), Origin: in.origin, UploadedBy: in.uploadedBy,
	}
	insert := db.InsertArtifactCapped
	if in.live {
		insert = db.InsertLiveArtifactCapped
	}
	row, err := insert(ctx, a.dbPool, newRow, in.maxFiles, in.maxTotal)
	if errors.Is(err, db.ErrArtifactVersionLimit) {
		return fail(http.StatusConflict, versionLimitMsg)
	}
	if errors.Is(err, db.ErrArtifactLimit) {
		return fail(http.StatusConflict, limitMsg)
	}
	if errors.Is(err, db.ErrArtifactBytesLimit) {
		_ = os.RemoveAll(dir)
		writeJSON(w, http.StatusConflict, map[string]any{"error": totalLimitMsg(in), "code": "file_space_required"})
		return nil
	}
	if errors.Is(err, db.ErrArtifactAlreadyLive) || isUniqueViolation(err) {
		// Another request made this name's live row between our check above and the insert's own
		// lock-protected re-check (ErrArtifactAlreadyLive, the deterministic path) or, failing
		// that, the unique index itself (isUniqueViolation, defense in depth): that request won.
		// Ask this one to retry, which will then find the row and overwrite it in place.
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusConflict, "another publish just made this file live; publish --live again")
		return nil
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		writeDatabaseFailure(w, "record the file", "artifacts upload insert", err)
		return nil
	}
	out := storedArtifact{artifactInfo: infoOf(row)}
	out.View = view
	out.LatestVersion = row.Version
	if row.Previous > 0 {
		out.Previous = &row.Previous
	}
	return &out
}

// isUniqueViolation reports whether err is a Postgres unique-constraint failure (23505): here,
// the live partial index, caught when two `--live` publishes race to become a name's first live row.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// overwriteLive republishes an existing live file in place: the new bytes replace the old ones at
// the row's own path (same id, same version), and the index moves to match. Written to a staging
// file first and renamed over the real one only once both the bytes and the index update have
// succeeded, so a failed or partial upload never corrupts the file an open tab is still showing.
func (h *artifactsHandler) overwriteLive(w http.ResponseWriter, r *http.Request, sid string, live db.ArtifactRow, in artifactIntake) *storedArtifact {
	a := h.api
	ctx := r.Context()
	dir := artifactDir(sid, live.ID)
	staging := filepath.Join(dir, liveStagingPrefix+newPublishID())
	path := artifactPath(sid, live)
	cleanup := func() *storedArtifact { _ = os.Remove(staging); return nil }
	f, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is the existing row's own directory; staging has a fresh random suffix
	if err != nil {
		writeStorageFailure(w, "save the file", accessWrite, "artifacts upload live create", err)
		return nil
	}
	sum := sha256.New()
	disk := &errWriter{w: f}
	size, copyErr := io.Copy(io.MultiWriter(disk, sum), http.MaxBytesReader(w, r.Body, maxArtifactBytes))
	closeErr := f.Close()
	var tooBig *http.MaxBytesError
	switch {
	case disk.err != nil:
		writeStorageFailure(w, "save the file", accessWrite, "artifacts upload live write", disk.err)
		return cleanup()
	case errors.As(copyErr, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, tooLargeMsg(r.ContentLength))
		return cleanup()
	case copyErr != nil:
		writeError(w, http.StatusBadRequest, "The upload stopped before the whole file arrived; try again.")
		return cleanup()
	case closeErr != nil:
		writeStorageFailure(w, "save the file", accessWrite, "artifacts upload live close", closeErr)
		return cleanup()
	}
	head := make([]byte, 512)
	if hf, err := os.Open(staging); err == nil { //nolint:gosec // same path as above
		n, _ := io.ReadFull(hf, head)
		head = head[:n]
		_ = hf.Close()
	}
	contentType, view := classifyArtifact(live.Name, head, size)
	// commitFile runs after UpdateLiveArtifactContent's transaction commits, but while it still
	// holds the per-session advisory lock across the rename (and, if that keeps failing, the
	// restore): no other update for this session can even begin until this one's rename or
	// restore is fully resolved, so two concurrent republishes of this file can never have their
	// rename and their row commit interleaved.
	commitFile := func() error { return os.Rename(staging, path) }
	row, err := db.UpdateLiveArtifactContent(ctx, a.dbPool, sid, live.ID, size, contentType, hex.EncodeToString(sum.Sum(nil)), in.maxTotal, commitFile)
	if errors.Is(err, db.ErrArtifactBytesLimit) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": totalLimitMsg(in), "code": "file_space_required"})
		return cleanup()
	}
	if err != nil {
		writeDatabaseFailure(w, "record the file", "artifacts upload update live", err)
		return cleanup()
	}
	out := storedArtifact{artifactInfo: infoOf(row)}
	out.View = view
	out.LatestVersion = row.Version
	out.liveOverwrite = true
	return &out
}

// totalLimitMsg is the 409 for a session whose files of one origin would pass their total.
func totalLimitMsg(in artifactIntake) string {
	return fmt.Sprintf("this session's %s would go over their %s total; delete one first", in.noun, sizeText(in.maxTotal))
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
		a.hub.FanOutAgentEvent(sessionID, seq, msg)
	}
}

// fanOutLiveArtifactUpdate pushes a live file's current state (BLG-470) to every open viewer of
// the session without writing to the transcript: unlike recordArtifactEvent, it never calls
// AppendAgentEvent, so an in-place republish never grows the session's replay or storage however
// often it happens. Seq 0 and Transient mark it as never persisted, the same way a streaming
// delta is (hubgap.go); the hub fans it out to live subscribers and drops it for one whose buffer
// is full, same as any other transient event — a dropped one costs a client a stale card until
// its next republish or an explicit list read, never a gap to fill in.
func (a *API) fanOutLiveArtifactUpdate(sessionID string, info artifactInfo) {
	raw, err := json.Marshal(info)
	if err != nil {
		return
	}
	ev := protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(), Transient: true,
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: artifactEventKind, Payload: raw,
	}
	if msg, err := json.Marshal(ev); err == nil {
		a.hub.FanOutAgentEvent(sessionID, 0, msg)
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
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return "", "", false
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return "", "", false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "artifacts GetSession", err)
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
		writeDatabaseFailure(w, "find the file", "artifacts get", err)
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
		p, valid := h.api.authBrowser(w, r)
		if !valid {
			return
		}
		h.listSession(w, r, sid, browserFileActor(p))
	}
}

func (h *artifactsHandler) listSession(w http.ResponseWriter, r *http.Request, sid string, actor fileActor) {
	sess, err := db.GetSession(r.Context(), h.api.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "artifacts list GetSession", err)
		return
	}
	rows, err := db.ListArtifacts(r.Context(), h.api.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "list the files", "artifacts list", err)
		return
	}
	limits, err := h.api.sessionFileLimits(r.Context(), sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's storage", "artifacts list storage", err)
		return
	}
	latest := latestVersions(rows)
	out := make([]artifactListEntry, 0, len(rows))
	for _, a := range rows {
		out = append(out, artifactListEntry{infoOf(a), latest[latestKey(a)], a.CreatedAt.UTC().Format(time.RFC3339), artifactFileMissing(sid, a), actor.canDelete(sess, a)})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"artifacts": out, "limits": limits, "can_delete": actor.managesFiles(sess),
		"can_increase": actor.admin && actor.account != "" && limits.MaxSessionUploadBytes < limits.MaxSessionUploadCapBytes,
	})
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
			writeDatabaseFailure(w, "find the file", "artifacts latest version", err)
			return
		}
	}
	f, err := os.Open(artifactPath(sid, *row))
	if errors.Is(err, os.ErrNotExist) {
		// The row survived but the bytes did not (a data directory lost on a restart): say so,
		// distinctly from a file that was never there or was deleted (404).
		writeError(w, http.StatusGone, fileGoneMsg)
		return
	}
	if err != nil {
		writeStorageFailure(w, "read the file", accessRead, "artifacts open "+sid+"/"+row.ID, err)
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
	if row.Live {
		if mismatch, detail := liveFileIntegrityMismatch(f, *row); mismatch {
			log.Printf("artifacts: live file %s/%s (%s) does not match its row on disk (%s); the row may describe bytes a crashed update never wrote",
				sid, row.ID, row.Name, detail)
			hd.Set("X-Artifact-Live-Updating", "true")
		}
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}

// ─── snapshot (a live file's current content, saved as a normal versioned file) ──────────────

// snapshotName is a live file's name with the time it was snapshotted, so a person can tell two
// snapshots of the same chart apart: "latency-v3.png" -> "latency-v3-20261103-153045.png".
func snapshotName(name string, at time.Time) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // a dotfile such as .env has no extension
		stem, ext = name, ""
	}
	return fmt.Sprintf("%s-%s%s", stem, at.UTC().Format("20060102-150405"), ext)
}

func (h *artifactsHandler) snapshot(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.person(w, r)
	if !ok {
		return
	}
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	if !row.Live {
		writeError(w, http.StatusConflict, "this file already keeps its own versions; Snapshot is only for a live file")
		return
	}
	h.snapshotSession(w, r, sid, *row)
}

func (h *artifactsHandler) snapshotSession(w http.ResponseWriter, r *http.Request, sid string, live db.ArtifactRow) {
	a := h.api
	ctx := r.Context()
	src, err := os.Open(artifactPath(sid, live))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusGone, fileGoneMsg)
		return
	}
	if err != nil {
		writeStorageFailure(w, "read the file", accessRead, "artifacts snapshot open", err)
		return
	}
	defer func() { _ = src.Close() }()

	name := snapshotName(live.Name, time.Now())
	id := newPublishID()
	dir := artifactDir(sid, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeStorageFailure(w, "save the file", accessWrite, "artifacts snapshot mkdir", err)
		return
	}
	path := filepath.Join(dir, name)
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // dir is a freshly generated id; name derives from the existing sanitised name plus a fixed timestamp format
	if err != nil {
		_ = os.RemoveAll(dir)
		writeStorageFailure(w, "save the file", accessWrite, "artifacts snapshot create", err)
		return
	}
	sum := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(dst, sum), src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		logErr := copyErr
		if logErr == nil {
			logErr = closeErr
		}
		writeStorageFailure(w, "save the file", accessWrite, "artifacts snapshot write", logErr)
		return
	}
	head := make([]byte, 512)
	if hf, err := os.Open(path); err == nil { //nolint:gosec // same path as above
		n, _ := io.ReadFull(hf, head)
		head = head[:n]
		_ = hf.Close()
	}
	_, view := classifyArtifact(name, head, size)

	newRow, err := db.InsertArtifactCapped(ctx, a.dbPool, db.ArtifactRow{
		ID: id, SessionID: sid, Name: name, Size: size, ContentType: live.ContentType,
		SHA256: hex.EncodeToString(sum.Sum(nil)), Origin: live.Origin, UploadedBy: live.UploadedBy,
	}, maxArtifactsPerSession, maxArtifactBytesPerSession)
	if errors.Is(err, db.ErrArtifactVersionLimit) {
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusConflict, versionLimitMsg)
		return
	}
	if errors.Is(err, db.ErrArtifactLimit) || errors.Is(err, db.ErrArtifactBytesLimit) {
		_ = os.RemoveAll(dir)
		writeError(w, http.StatusConflict, "this session is at its file limit; delete one first")
		return
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		writeDatabaseFailure(w, "record the file", "artifacts snapshot insert", err)
		return
	}
	out := storedArtifact{artifactInfo: infoOf(newRow)}
	out.View = view
	out.LatestVersion = newRow.Version
	a.recordArtifactEvent(ctx, sid, out.artifactInfo)
	writeJSON(w, http.StatusCreated, struct {
		storedArtifact
		URL string `json:"url"`
	}{out, "/sessions/" + sid + "?artifact=" + out.ID})
}

func (h *artifactsHandler) remove(w http.ResponseWriter, r *http.Request) {
	if sid, ok := h.person(w, r); ok {
		p, valid := h.api.authBrowser(w, r)
		if !valid {
			return
		}
		h.removeSession(w, r, sid, browserFileActor(p))
	}
}

// fileDeleteRefusedMsg is the 403 for a person who may not delete this file.
const fileDeleteRefusedMsg = "You can delete only the files you attached. The session's owner or an administrator can delete the others."

func fileAdmin(p runnerPrincipal) bool { return slices.Contains(p.Caps, "account.manage") }

func (h *artifactsHandler) removeSession(w http.ResponseWriter, r *http.Request, sid string, actor fileActor) {
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	sess, err := db.GetSession(r.Context(), h.api.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "artifacts delete GetSession", err)
		return
	}
	if !actor.canDelete(sess, *row) {
		writeError(w, http.StatusForbidden, fileDeleteRefusedMsg)
		return
	}
	if _, err := db.DeleteArtifactAudited(r.Context(), h.api.dbPool, *row, db.FileDeletion{Actor: actor.account, ActorAdmin: actor.admin, Via: actor.via}); err != nil {
		writeDatabaseFailure(w, "delete the file", "artifacts delete", err)
		return
	}
	h.api.withdrawDeletedReviews(context.WithoutCancel(r.Context()), sid, row.ID)
	h.api.closeDeletedAnswer(context.WithoutCancel(r.Context()), sid, row.ID)
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
