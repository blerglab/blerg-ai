package server

// The "the person is needed" notice: an in-app toast (a session_notice broadcast, which every open
// browser shows as given) and, when nobody has been active for activityTTL, an OS push.
//
// It goes out when a session stops and waits for a person, and only then:
//
//   - waiting (a turn paused for input): at once.
//   - idle (a turn finished): only once nothing the session started still runs. A turn can end
//     while its background subagents and background tasks carry on, and the work is not done
//     until they are; so when any still runs (by the live state's rules, livestate.go), the notice
//     is deferred and sent when the last of them ends while the session is still idle — one
//     notice, however many there were. A new turn, a question (its own notice covers it) or the
//     session's end cancels a deferred notice.
//
// A session nobody watches (headlessSession) gets none of it, nor an ask's toast or push.

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// headlessSession reports whether nobody watches the session, so nothing about it should call a
// person: an unattended session (effectiveInteraction: chosen at start, and the reading of an
// older row), a cron's, one the operator key started, a child another session started
// (delegation.go), and one an agent token started that did not ask for a person in the chat. A board's Discuss and board sessions (agent-token starts recorded
// interactive) and its Assist sessions are driven by a person and keep their notices.
func headlessSession(row *db.SessionRow) bool {
	if row == nil {
		return false
	}
	if effectiveInteraction(row) == protocol.InteractionUnattended {
		return true
	}
	if row.CronID != nil && *row.CronID != "" {
		return true
	}
	switch row.StartedByKind {
	case "cron", "runner_key", "session":
		return true
	case agentPrincipalKind:
		return row.Interaction != protocol.InteractionInteractive
	}
	return false
}

// How a held idle notice is looked at again besides a recorded end: every idleRecheckEvery (a run
// that never reports its end, or one crossing livestate.StaleRunning, is caught there), and once a
// hold is idleTaskCap old with only background tasks left running (a dev server, a Monitor: work
// that may never end by itself), the notice goes out anyway and says a task still runs. A running
// subagent is work the person waits on; it holds the notice until it ends or goes stale.
const (
	idleRecheckEvery = time.Minute
	idleTaskCap      = 30 * time.Minute
)

// idleNoticeSet holds the sessions whose idle notice waits for their running work to end, with
// when each was held.
type idleNoticeSet struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (s *idleNoticeSet) hold(sessionID string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]time.Time{}
	}
	s.m[sessionID] = at
}

// heldAt reports when the session's notice was held, and whether it is.
func (s *idleNoticeSet) heldAt(sessionID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.m[sessionID]
	return at, ok
}

// held lists the sessions with a held notice.
func (s *idleNoticeSet) held() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for id := range s.m {
		out = append(out, id)
	}
	return out
}

// take removes the session's held notice and reports whether there was one: of two callers
// racing, one gets it.
func (s *idleNoticeSet) take(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[sessionID]; !ok {
		return false
	}
	delete(s.m, sessionID)
	return true
}

// CancelIdleNotice drops the session's held idle notice, if it has one.
func (h *Hub) CancelIdleNotice(sessionID string) {
	h.idleNotices.take(sessionID)
}

// sendSessionNotice tells browsers to toast the session's notice for status (waiting or idle), and
// pushes it when nobody is active. taskRunning: an idle notice sent at the cap, while a background
// task still runs, says so. A private session's broadcast reaches only its owner (privacy.go) and
// it is never pushed: a subscription belongs to no account.
func sendSessionNotice(hub *Hub, pool *pgxpool.Pool, row *db.SessionRow, status string, taskRunning bool) {
	var title string
	if row.Title != nil {
		title = *row.Title
	}
	label := sessionLabel(title, row.Repo, row.ID)
	heading, body, ok := sessionNotification(status, label)
	if !ok {
		return
	}
	if status == "idle" && taskRunning {
		body = fmt.Sprintf("%s is waiting for you (a background task is still running).", label)
	}
	url := "/sessions/" + row.ID
	hub.BroadcastJSON(protocol.SessionNotice{
		Type: "session_notice", SessionID: row.ID, Status: status, Title: heading, Body: body, URL: url,
	})
	if !hub.ActiveWithin(activityTTL) && !row.Private {
		SendPush(pool, heading, body, url)
	}
}

// noticeOnTransition applies the notice rule to a session that has just entered status (the row
// as it was before). readyIdle: it became ready rather than finishing a turn, which is no notice.
func noticeOnTransition(ctx context.Context, hub *Hub, pool *pgxpool.Pool, row *db.SessionRow, status string, readyIdle bool) {
	if status != "idle" {
		// A new turn, a question, an end: whatever the session waited to say is moot.
		hub.CancelIdleNotice(row.ID)
	}
	if readyIdle || headlessSession(row) {
		return
	}
	switch status {
	case "waiting":
		sendSessionNotice(hub, pool, row, status, false) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request
	case "idle":
		now := time.Now()
		w, err := runningWork(ctx, pool, row.ID, status, now)
		if err != nil {
			// Unknown is told as before: a notice too many beats a session that never says so.
			log.Printf("idle notice %s: running work: %v", row.ID, err)
		}
		if w.total() > 0 {
			hub.idleNotices.hold(row.ID, now)
			return
		}
		sendSessionNotice(hub, pool, row, status, false) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request
	}
}

// noticeOnWorkEnded is called when something a session ran has ended (a subagent_done or a
// background_task_done is recorded): a held notice is looked at again.
func noticeOnWorkEnded(ctx context.Context, hub *Hub, pool *pgxpool.Pool, sessionID string) {
	recheckIdleNotice(ctx, hub, pool, sessionID, time.Now())
}

// recheckIdleNotice looks at a session's held idle notice at now: dropped when the session is no
// longer idle (or gone); sent when nothing runs any more (by the live state's rules, its stale rule
// included), or when the hold is idleTaskCap old and only background tasks still run.
func recheckIdleNotice(ctx context.Context, hub *Hub, pool *pgxpool.Pool, sessionID string, now time.Time) {
	since, ok := hub.idleNotices.heldAt(sessionID)
	if pool == nil || !ok {
		return
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		log.Printf("idle notice %s: %v", sessionID, err)
		return
	}
	if row == nil || row.Status != "idle" {
		hub.CancelIdleNotice(sessionID)
		return
	}
	w, err := runningWork(ctx, pool, sessionID, row.Status, now)
	if err != nil {
		log.Printf("idle notice %s: running work: %v", sessionID, err)
		return
	}
	capped := w.total() > 0 && w.subagents == 0 && now.Sub(since) >= idleTaskCap
	if (w.total() > 0 && !capped) || !hub.idleNotices.take(sessionID) {
		return
	}
	sendSessionNotice(hub, pool, row, "idle", capped) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request
}

// recheckIdleNotices looks at every held idle notice at now (recheckIdleNotice).
func recheckIdleNotices(ctx context.Context, hub *Hub, pool *pgxpool.Pool, now time.Time) {
	for _, id := range hub.idleNotices.held() {
		if ctx.Err() != nil {
			return
		}
		recheckIdleNotice(ctx, hub, pool, id, now)
	}
}

// RunIdleNoticeChecks looks at the held idle notices every `every` (idleRecheckEvery when 0)
// until ctx is done. One goroutine for all of them; inert without a database.
func RunIdleNoticeChecks(ctx context.Context, hub *Hub, pool *pgxpool.Pool, every time.Duration) {
	if pool == nil {
		return
	}
	if every <= 0 {
		every = idleRecheckEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			recheckIdleNotices(ctx, hub, pool, time.Now())
		}
	}
}
