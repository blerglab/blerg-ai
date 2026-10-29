package server

// A session's highlight reel (docs/design/session-rail-history.md).
//
//   POST /api/sessions/{id}/topics — `blerg-runner topic` marks the start or the end of a piece of
//     work. The session's own credential (its messaging token, bound to the path's session) or the
//     daemon token, as for publish. The read of the open topic and the append run in one transaction
//     under the session's topic lock (a Postgres advisory lock, so it holds across runner processes);
//     the event is fanned out after the commit, as recordServerAgentEvent does. A done may name the
//     commits the work landed (`--commit <sha>`), checked by topics.CleanCommits.
//   GET /api/sessions/{id}/topics — the reel (topics.Build) for a person who may see the session: the
//     marked topics, and the work outside them as one unmarked summary. A done's commits link to the
//     session's repository when it is known (sessionRepo).

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// RegisterTopicRoutes registers the reel's routes on mux.
func (a *API) RegisterTopicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/topics", a.HandlePostTopic)
	mux.HandleFunc("GET /api/sessions/{id}/topics", a.HandleGetTopics)
	a.RegisterMarkRoutes(mux)
}

type topicRequest struct {
	Action  string              `json:"action"`
	Title   string              `json:"title"`
	Outcome string              `json:"outcome"`
	Commits []agent.TopicCommit `json:"commits"`
}

type topicRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// topicStarted answers a start: the payload, and the topic it closed (null when none was open).
type topicStarted struct {
	agent.TopicPayload
	Closed *topicRef `json:"closed"`
}

// HandlePostTopic is POST /api/sessions/{id}/topics.
func (a *API) HandlePostTopic(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
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
	var req topicRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	var title, outcome string
	var commits []agent.TopicCommit
	switch req.Action {
	case "start":
		if title = topics.CleanText(req.Title, topics.MaxTitle); title == "" {
			writeError(w, http.StatusBadRequest, "a topic needs a title")
			return
		}
		if len(req.Commits) > 0 {
			writeError(w, http.StatusBadRequest, "commits go with a done, not a start")
			return
		}
	case "done":
		outcome = topics.CleanText(req.Outcome, topics.MaxOutcome)
		var err error
		if commits, err = topics.CleanCommits(req.Commits); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, `action must be "start" or "done"`)
		return
	}
	ctx := r.Context()
	sess, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "topics GetSession", err)
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if terminalSessionStatus(sess.Status) {
		writeError(w, http.StatusConflict, "This session has ended; topics can be marked only while it runs.")
		return
	}

	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		writeDatabaseFailure(w, "record the topic", "topics begin "+sid, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockTopics(ctx, tx, sid); err != nil {
		writeDatabaseFailure(w, "record the topic", "topics lock "+sid, err)
		return
	}
	latest, n, err := db.TopicState(ctx, tx, sid)
	if err != nil {
		writeDatabaseFailure(w, "record the topic", "topics TopicState "+sid, err)
		return
	}
	// The cap refuses only a start: a done on an open topic is always allowed, so no topic stays open for good.
	if req.Action == "start" && n >= topics.MaxEvents {
		writeError(w, http.StatusConflict, fmt.Sprintf("this session already has %d topic events", topics.MaxEvents))
		return
	}
	var prev []byte
	if latest != nil {
		prev = []byte(latest.Payload)
	}
	open, isOpen := topics.OpenTopic(prev)
	var p agent.TopicPayload
	var closed *topicRef
	if req.Action == "start" {
		p = agent.TopicPayload{ID: topics.NewID(), Title: title, Action: "start"}
		if isOpen {
			closed = &topicRef{ID: open.ID, Title: open.Title}
		}
	} else {
		if !isOpen {
			writeError(w, http.StatusConflict, "no topic is open")
			return
		}
		p = agent.TopicPayload{ID: open.ID, Title: open.Title, Action: "done", Outcome: outcome, Commits: commits}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode failed")
		return
	}
	raw = protocol.StripNUL(raw)
	at := time.Now()
	clientEventID := newUUID()
	seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, clientEventID, "topic", string(raw), at)
	if err != nil {
		writeDatabaseFailure(w, "record the topic", "topics append "+sid, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeDatabaseFailure(w, "record the topic", "topics commit "+sid, err)
		return
	}
	fanOutServerEvent(a.hub, sid, clientEventID, "topic", raw, seq, at)
	if req.Action == "start" {
		writeJSON(w, http.StatusCreated, topicStarted{TopicPayload: p, Closed: closed})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// topicsReply is GET /api/sessions/{id}/topics' body.
type topicsReply struct {
	SessionID    string           `json:"session_id"`
	ThroughSeq   int64            `json:"through_seq"`
	GeneratedAt  time.Time        `json:"generated_at"`
	TokensHidden bool             `json:"tokens_hidden"`
	Unmarked     *topics.Unmarked `json:"unmarked"`
	Topics       []topics.Topic   `json:"topics"`
	// Strokes is the History feed (topics/strokes.go), newest first.
	Strokes []topics.Stroke `json:"strokes"`
	// StrokesTruncated is how many older strokes the feed left out (every waiting one is kept).
	StrokesTruncated int `json:"strokes_truncated"`
}

// topicsBootTag names this process in the reel's ETag (see topicsETag).
var topicsBootTag = strconv.FormatInt(time.Now().UnixNano(), 36)

// topicsETag names a reel without building it: the newest event, the files (count and newest; a
// deletion moves the count), whether the session is live and its end (subagent statuses depend on them),
// the repository its commit links point at (repo: sessionRepo's key, "" when none) and the
// viewer class (tokens shown or hidden).
func topicsETag(through int64, files int, newest time.Time, status string, ended *time.Time, repo string, showTokens bool, asks string) string {
	var end int64
	if ended != nil {
		end = ended.UnixNano()
	}
	class := "h"
	if showTokens {
		class = "t"
	}
	// The reel reads only whether the session is live, so a turn's running/idle/waiting keeps the tag.
	state := "live"
	if terminalSessionStatus(status) {
		state = "end"
	}
	r := fnv.New32a()
	_, _ = r.Write([]byte(repo))
	// The asks' states: closing one without an answer writes no event.
	_, _ = r.Write([]byte{0})
	_, _ = r.Write([]byte(asks))
	// The process's start is in the tag too: a version's missing flag reads the disk, which the
	// rest of the tag does not see, and the way bytes go missing is a restart onto a data
	// directory that did not survive it.
	return `"` + topicsBootTag + "." + strconv.FormatInt(through, 10) + "." + strconv.Itoa(files) + "." + strconv.FormatInt(newest.UnixNano(), 10) +
		"." + state + "." + strconv.FormatInt(end, 10) + "." + strconv.FormatUint(uint64(r.Sum32()), 36) + "." + class + `"`
}

// sessionRepo is the hosted repository a session works in, for its commit links: the one its clone
// URL names (a session the runner cloned for), else the origin its daemon reports for its folder
// while that daemon is connected. ok=false when neither names a registered provider's repository;
// nothing is guessed from the name alone.
func (a *API) sessionRepo(ctx context.Context, viewer string, row *db.SessionRow) (gitprovider.Ref, bool) {
	if !a.hub.desktopSessionVisible(ctx, viewer, row) || (derefOrEmpty(row.Runtime) != "cluster" && !ownsSession(viewer, row)) {
		return gitprovider.Ref{}, false
	}
	if row.GitURL != nil && *row.GitURL != "" {
		if ref, ok := gitprovider.Default.ParseRemoteURL(*row.GitURL); ok {
			return ref, true
		}
	}
	if row.Repo == "" || a.hub == nil {
		return gitprovider.Ref{}, false
	}
	dc := a.hub.ownedHostDaemon(ctx, viewer, row.DaemonID)
	if dc == nil {
		return gitprovider.Ref{}, false
	}
	folder := row.Repo
	if i := strings.LastIndex(folder, "/"); i >= 0 {
		folder = folder[i+1:]
	}
	o, ok := dc.RepoOrigin(folder)
	if !ok || (strings.Contains(row.Repo, "/") && !strings.EqualFold(o.FullName, row.Repo)) {
		return gitprovider.Ref{}, false
	}
	return gitprovider.Default.ParseFullName(o.Provider, o.FullName)
}

// HandleGetTopics is GET /api/sessions/{id}/topics. Access is /insights': a signed-in person who may
// see the session; an agent token is refused and a private session of someone else's answers the
// uniform 404. The ETag is three cheap reads, so an unchanged reel answers 304 without building it.
func (a *API) HandleGetTopics(w http.ResponseWriter, r *http.Request) {
	err := a.sessionReadFence(r.Context(), r.PathValue("id"), func(ctx context.Context) error {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
		a.snapshotHandleGetTopics(w, r.WithContext(ctx))
		return nil
	})
	if err != nil {
		writeError(w, 409, "session source changed; refresh")
	}
}

func (a *API) snapshotHandleGetTopics(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		writeError(w, http.StatusForbidden, "a session's history is for a signed-in person, not an agent token")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "the history needs the database")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "topics GetSession", err)
		return
	}
	if row == nil || !canSeeAccount(p.Sub, row) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	showTokens := ownsSession(p.Sub, row) || p.Has(insightsAdminCap)
	repo, haveRepo := a.sessionRepo(ctx, p.Sub, row)
	repoKey := p.Sub + ":"
	if haveRepo {
		repoKey += repo.Key()
	}
	through, err := db.MaxAgentEventSeq(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics MaxAgentEventSeq "+sid, err)
		return
	}
	files, newest, err := db.ArtifactStamp(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ArtifactStamp "+sid, err)
		return
	}
	askClosed, askDismissed, askOpen, err := db.AskStates(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics AskStates "+sid, err)
		return
	}
	etag := topicsETag(through, files, newest, row.Status, row.EndedAt, repoKey, showTokens, fmt.Sprintf("%d.%d", askOpen, len(askClosed)))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-store")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rows, err := db.ListReelEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ListReelEvents "+sid, err)
		return
	}
	// The cards approved proposals created: their chips sit in the topic that was open (else unmarked).
	cards, err := db.ListProposalDecidedEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ListProposalDecidedEvents "+sid, err)
		return
	}
	rows = append(rows, cards...)
	// The session's renames: rows in the topic that was open (else unmarked).
	renames, err := db.ListSessionRenamedEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ListSessionRenamedEvents "+sid, err)
		return
	}
	rows = append(rows, renames...)
	// The History feed's marks, asks, approvals, proposals and reviews.
	strokeRows, err := db.ListStrokeEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ListStrokeEvents "+sid, err)
		return
	}
	rows = append(rows, strokeRows...)
	arts, err := db.ListArtifacts(ctx, a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's history", "topics ListArtifacts "+sid, err)
		return
	}
	events := make([]topics.Event, 0, len(rows))
	for _, e := range rows {
		events = append(events, topics.Event{Seq: e.Seq, ID: e.ClientEventID, Kind: e.Kind, Payload: json.RawMessage(e.Payload), Ts: e.Ts})
	}
	var published []topics.Artifact
	for _, f := range arts {
		if originOf(f.Origin) == db.OriginAgent {
			published = append(published, topics.Artifact{ID: f.ID, Name: f.Name, Version: f.Version, CreatedAt: f.CreatedAt, Missing: artifactFileMissing(sid, f)})
		}
	}
	var commitURL func(string) string
	if haveRepo {
		commitURL = func(sha string) string {
			u, _ := gitprovider.Default.CommitURL(repo, sha)
			return u
		}
	}
	reel := topics.Build(events, published, topics.Options{
		ThroughSeq: through, Live: !terminalSessionStatus(row.Status), EndedAt: row.EndedAt, ShowTokens: showTokens,
		CommitURL: commitURL, AskClosed: askClosed, AskDismissed: askDismissed,
	})
	writeJSON(w, http.StatusOK, topicsReply{
		SessionID: sid, ThroughSeq: through, GeneratedAt: time.Now().UTC(), TokensHidden: reel.TokensHidden,
		Unmarked: reel.Unmarked, Topics: reel.Topics, Strokes: reel.Strokes, StrokesTruncated: reel.StrokesTruncated,
	})
}
