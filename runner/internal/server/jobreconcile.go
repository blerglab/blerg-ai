package server

// Cluster job reconciliation.
//
// A cluster session normally ends by telling the server so: the agent finishes
// and the pod reports it. But a Job can also end without anyone reporting —
// activeDeadlineSeconds fires, the container is OOM-killed, the image never
// pulls, the node goes away. The session row then sits at "starting" or
// "running" forever: status and result answer with a session that is still
// going, and the completion webhook never fires, so a broker waits for a
// callback that is never coming.
//
// This reconciler closes that gap from the other side: it asks the cluster
// about the Jobs of sessions the database still believes are live, and writes
// the terminal status (plus the webhook) the pod never got to send. It is a
// fallback, never an authority — every write is conditional on the session
// still being non-terminal, so a status the pod itself reported always wins.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	// clusterReconcileInterval is how often the loop polls. Job endings are
	// not urgent (the pod path handles every normal one) and each pass costs
	// one API GET per live session, so this is deliberately lazy.
	clusterReconcileInterval = 30 * time.Second
	// clusterJobMissingGrace is how long a session may have no Job at all
	// before that is read as "gone" rather than "not created yet". A Job
	// appears within seconds of the start request; two minutes is slack for a
	// slow API server, not a real wait. Measured from the session's last
	// status change, not its start: a long-lived session that has just
	// transitioned deserves the same slack as a new one.
	clusterJobMissingGrace = 2 * time.Minute
	// clusterResumeWindow is how long a disconnected cluster session stays
	// resumable. "disconnected" is not a failure — the pod is gone but the
	// work can be picked up again — so the reconciler must not read anything
	// into its Job having finished. Only after a day with nobody resuming it
	// is the session declared over.
	clusterResumeWindow = 24 * time.Hour
	// daemonLostWindow is how long a desktop session waits for its daemon to
	// come back before it is finalised. A laptop lid, a network blip or a
	// daemon restart all reconnect within seconds; half an hour without one
	// means the session is not coming back.
	daemonLostWindow = 30 * time.Minute
)

// Fixed reasons written to error_reason. Short, caller-facing, and free of any
// environment detail.
const (
	reasonJobFailed      = "cluster job failed"
	reasonJobDisappeared = "cluster job disappeared"
	reasonNotResumed     = "session expired without resume"
	reasonDaemonLost     = "daemon did not return"
)

// resumeWindow / lostDaemonWindow / jobMissingGrace read the tunable windows,
// falling back to the constants above. The fields exist so tests can drive
// day-long and half-hour timeouts without waiting for them.
func (a *API) resumeWindow() time.Duration {
	if a.clusterResumeWindow > 0 {
		return a.clusterResumeWindow
	}
	return clusterResumeWindow
}

func (a *API) lostDaemonWindow() time.Duration {
	if a.daemonLostWindow > 0 {
		return a.daemonLostWindow
	}
	return daemonLostWindow
}

func (a *API) jobMissingGrace() time.Duration {
	if a.clusterJobMissingGrace > 0 {
		return a.clusterJobMissingGrace
	}
	return clusterJobMissingGrace
}

// Job states the reconciler distinguishes. "unknown" covers an unreachable or
// unparseable API answer — the one case where doing nothing is right, since a
// cluster the runner cannot talk to is not evidence about any session.
const (
	jobStateRunning   = "running"
	jobStateSucceeded = "succeeded"
	jobStateFailed    = "failed"
	jobStateMissing   = "missing"
	jobStateUnknown   = "unknown"
)

// StartClusterJobReconciler runs the reconcile loop until ctx is cancelled.
// interval <= 0 selects the default. A database is all it needs: the cluster
// half is skipped when there is no JobManager, but the daemon-lost sweep
// matters on every deployment shape, desktop included.
func (a *API) StartClusterJobReconciler(ctx context.Context, interval time.Duration) {
	if a.dbPool == nil || a.hub == nil {
		return
	}
	if interval <= 0 {
		interval = clusterReconcileInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reconcileClusterJobsOnce(ctx)
		}
	}
}

// reconcileClusterJobsOnce runs a single pass (called directly by tests): the
// cluster half (what the Jobs say) and the desktop half (daemons that never
// came back).
func (a *API) reconcileClusterJobsOnce(ctx context.Context) {
	if a.dbPool == nil {
		return
	}
	if jm := a.hub.JobManager(); jm != nil {
		rows, err := db.ListClusterSessionsToReconcile(ctx, a.dbPool)
		if err != nil {
			log.Printf("cluster reconcile: list sessions: %v", err)
		} else {
			for _, row := range rows {
				a.reconcileClusterSession(ctx, jm, row)
			}
		}
	}
	a.finaliseLostDaemonSessions(ctx)
}

// reconcileClusterSession decides one session's fate from its Job's state.
func (a *API) reconcileClusterSession(ctx context.Context, jm *JobManager, row db.ReconcileSessionRow) {
	// A disconnected session is resumable: its pod is gone by definition, so
	// the Job's state says nothing about whether the work is over. The only
	// question is whether anyone still intends to resume it.
	if row.Status == "disconnected" {
		cutoff := time.Now().Add(-a.resumeWindow())
		if row.LastChange.After(cutoff) {
			// A resume may be booting (its start attempt still open): pick
			// its progress back up after a server restart, like a fresh start.
			adoptClusterStart(ctx, a.hub, a.dbPool, row.ID, "", a.startWatchEvery())
			return
		}
		// The write carries the same cutoff the decision was made on: a
		// message that resumes the session between this check and the UPDATE
		// restarts its status clock, and must not be ended by a verdict passed
		// before it arrived.
		a.finishSession(ctx, row, "error", reasonNotResumed, db.EndReasonNotResumed, "not resumed", &cutoff)
		return
	}

	// A start this process is not watching (the server restarted mid-start)
	// is picked back up, so its progress keeps moving in the browser.
	if row.Status == "starting" {
		adoptClusterStart(ctx, a.hub, a.dbPool, row.ID, "", a.startWatchEvery())
	}

	state, condition := jm.jobState(row.ID) //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
	var status, reason, endReason string
	switch state {
	case jobStateSucceeded:
		// The Job exited cleanly; the session simply ended. No error text —
		// there is no error to explain.
		status, endReason = "ended", db.EndReasonJobFinished
	case jobStateFailed:
		status, reason, endReason = "error", reasonJobFailed, db.EndReasonJobFailed
		if condition != "" {
			reason += ": " + condition
		}
	case jobStateMissing:
		// Only after the grace period: a Job that has not been created yet
		// looks exactly like one that is gone.
		if time.Since(row.LastChange) < a.jobMissingGrace() {
			return
		}
		status, reason, endReason = "error", reasonJobDisappeared, db.EndReasonJobDisappeared
	default:
		// running / unknown: nothing to say about this session yet.
		return
	}
	a.finishSession(ctx, row, status, reason, endReason, "job "+state, nil)
}

// finishSession writes the terminal status, and — only if that write actually
// landed — does everything that follows an ending exactly once: revoke the
// session's credentials, tell browsers, fire the completion webhook. endReason
// is the fixed code recorded alongside (migration 018); reason stays the
// caller-facing error text it always was.
func (a *API) finishSession(ctx context.Context, row db.ReconcileSessionRow, status, reason, endReason, why string, staleBefore *time.Time) {
	updated, err := db.FinishClusterSession(ctx, a.dbPool, row.ID, status, reason, endReason, staleBefore)
	if err != nil {
		log.Printf("cluster reconcile %s: mark %s: %v", row.ID, status, err)
		return
	}
	if !updated {
		// The pod reported its own outcome between the list and this write
		// (or, for the expiry case, somebody resumed the session). Either way
		// the session has moved on since the decision, and whatever moved it
		// knows better than this pass does.
		return
	}
	log.Printf("cluster reconcile: session %s %s → %s", row.ID, why, status)
	if reason != "" {
		failStart(ctx, a.hub, a.dbPool, row.ID, reason,
			"The pod is gone. Its log may still be readable with kubectl logs in the sessions namespace; start the session again once the cause is fixed.")
	} else {
		a.hub.starts.close(row.ID) // ended cleanly: nothing left to watch
	}
	// The session is over: its messaging token must not outlive it.
	revokeSessionTokens(ctx, a.dbPool, row.ID, "reconcile: "+why)
	a.broadcastReconciledStatus(ctx, row.ID, status)
	a.notifyCompletion(row.ID) //nolint:contextcheck // webhook delivery outlives its caller by design: retries run for minutes (see notifyCompletion)
}

// finaliseLostDaemonSessions ends desktop sessions whose daemon dropped and
// never came back. The disconnect itself is not an ending (reconcileSessions
// revives the session when the daemon reattaches, and clears daemon_lost_at
// doing so), so this is the only path that closes them out — without it a
// broker's callback for a laptop that went to sleep for good never fires.
func (a *API) finaliseLostDaemonSessions(ctx context.Context) {
	cutoff := time.Now().Add(-a.lostDaemonWindow())
	rows, err := db.ListLostDaemonSessions(ctx, a.dbPool, cutoff)
	if err != nil {
		log.Printf("daemon-lost sweep: list sessions: %v", err)
		return
	}
	for _, row := range rows {
		// One guarded statement decides it: the session is finalised only if
		// it is STILL lost when the write runs. A daemon that reconnected
		// between the SELECT above and this line has already cleared
		// daemon_lost_at, and the update then matches nothing — so a live
		// session is never ended, and nothing downstream (token revocation,
		// the broadcast, the webhook) happens for it either. The reason and
		// ended_at land in the same statement, so the row the webhook reads
		// is already final.
		finalised, err := db.FinishLostDaemonSession(ctx, a.dbPool, row.ID, reasonDaemonLost, cutoff)
		if err != nil {
			log.Printf("daemon-lost sweep %s: %v", row.ID, err)
			continue
		}
		if !finalised {
			continue
		}
		log.Printf("cluster reconcile: session %s daemon lost → error", row.ID)
		revokeSessionTokens(ctx, a.dbPool, row.ID, "reconcile: daemon lost")
		a.broadcastReconciledStatus(ctx, row.ID, "error")
		a.notifyCompletion(row.ID) //nolint:contextcheck // webhook delivery outlives its caller by design: retries run for minutes (see notifyCompletion)
	}
}

// broadcastReconciledStatus tells browsers about a reconciled session, reading
// the unread flag back from the database first — the same care setSessionStatus
// takes, so a reconcile never silently clears a session's unread badge.
func (a *API) broadcastReconciledStatus(ctx context.Context, sessionID, status string) {
	var unread bool
	var end sessionEnd
	if row, err := db.GetSession(ctx, a.dbPool, sessionID); err != nil {
		log.Printf("cluster reconcile %s: read unread: %v", sessionID, err)
	} else if row != nil {
		unread = row.Unread
		end = rowEnd(row)
	}
	broadcastWithEnd(a.hub, end, func(endReason string, endedBy *protocol.EndedBy) any {
		return protocol.SessionStateChanged{
			Type:      "session_state_changed",
			SessionID: sessionID,
			Status:    status,
			Unread:    unread,
			EndReason: endReason,
			EndedBy:   endedBy,
		}
	})
}

// jobState reports what the cluster says about a session's Job: its state and,
// for a failure, the k8s condition reason (a short enum-like token such as
// "DeadlineExceeded" or "BackoffLimitExceeded"). Only that token is taken —
// never the condition message or a raw response body, which can carry
// environment detail that has no business in a session's error text.
func (j *JobManager) jobState(sessionID string) (state string, conditionReason string) {
	resp, err := j.do(http.MethodGet,
		"/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+j.jobName(sessionID), nil)
	if err != nil {
		return jobStateUnknown, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return jobStateMissing, ""
	}
	if resp.StatusCode != http.StatusOK {
		return jobStateUnknown, ""
	}
	var job struct {
		Status struct {
			Succeeded  int `json:"succeeded"`
			Failed     int `json:"failed"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&job) != nil {
		return jobStateUnknown, ""
	}
	switch {
	case job.Status.Failed > 0:
		for _, c := range job.Status.Conditions {
			if c.Type == "Failed" && c.Status == "True" {
				return jobStateFailed, sanitiseConditionReason(c.Reason)
			}
		}
		return jobStateFailed, ""
	case job.Status.Succeeded > 0:
		return jobStateSucceeded, ""
	default:
		return jobStateRunning, ""
	}
}

// sanitiseConditionReason keeps a k8s condition reason to the short token it
// is meant to be: letters, digits and a few separators, length-capped. The
// value comes from an API response, and a session's error_reason is shown to
// callers, so it is bounded here rather than trusted.
func sanitiseConditionReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 64 {
		reason = reason[:64]
	}
	var b strings.Builder
	for _, r := range reason {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}
