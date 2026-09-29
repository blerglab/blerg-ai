package server

// Cluster job reconciliation: a Job can end without the pod ever reporting
// back (activeDeadlineSeconds, OOM kill, image pull failure, node loss). The
// reconciler is what turns that into a terminal session row and a completion
// webhook, instead of a session that reads "running" forever.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// reconcileFixture is a cluster session in the given status with a callback
// pointing at a receiver whose hits the test can count.
type reconcileFixture struct {
	api       *API
	pool      *pgxpool.Pool
	fake      *fakeK8s
	sessionID string
	jobName   string
	hits      func() int
}

func newReconcileFixture(t *testing.T, status string) *reconcileFixture {
	t.Helper()
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	api.webhookBackoff = []time.Duration{}
	ctx := context.Background()

	var mu sync.Mutex
	hits := 0
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recv.Close)

	const daemonID = "00000000-0000-4000-8000-0000000000da"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, status, "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionPosture(ctx, pool, sessionID, "cluster", false); err != nil {
		t.Fatal(err)
	}
	allowLoopbackDelivery(t, recv.URL+"/hook")
	if err := db.SetSessionCallback(ctx, pool, sessionID, recv.URL+"/hook", ""); err != nil {
		t.Fatal(err)
	}
	return &reconcileFixture{
		api: api, pool: pool, fake: f, sessionID: sessionID,
		jobName: api.hub.JobManager().jobName(sessionID),
		hits: func() int {
			mu.Lock()
			defer mu.Unlock()
			return hits
		},
	}
}

// status reads the session's current status and error reason.
func (f *reconcileFixture) status(t *testing.T) (string, string) {
	t.Helper()
	row, err := db.GetSession(context.Background(), f.pool, f.sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	return row.Status, derefOrEmpty(row.ErrorReason)
}

// waitForWebhook waits briefly for the (asynchronous) delivery to land.
func (f *reconcileFixture) waitForWebhook(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.hits() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("webhook deliveries = %d, want %d", f.hits(), want)
}

// A Job that exited cleanly without the pod reporting back still ends the
// session — and the broker is told.
func TestClusterReconcilerEndsSucceededJob(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.fake.setJobStatus(f.jobName, map[string]any{"succeeded": 1})

	f.api.reconcileClusterJobsOnce(context.Background())

	status, reason := f.status(t)
	if status != "ended" {
		t.Errorf("status = %q, want ended", status)
	}
	if reason != "" {
		t.Errorf("error_reason = %q, want empty for a clean exit", reason)
	}
	row, _ := db.GetSession(context.Background(), f.pool, f.sessionID)
	if row.EndedAt == nil {
		t.Error("ended_at not set")
	}
	f.waitForWebhook(t, 1)
}

// A failed Job becomes an error the caller can read, carrying the k8s
// condition reason and nothing else from the API response.
func TestClusterReconcilerMarksFailedJobError(t *testing.T) {
	f := newReconcileFixture(t, "starting")
	f.fake.setJobStatus(f.jobName, map[string]any{
		"failed": 1,
		"conditions": []map[string]any{{
			"type": "Failed", "status": "True",
			"reason":  "DeadlineExceeded",
			"message": "Job was active longer than specified deadline",
		}},
	})

	f.api.reconcileClusterJobsOnce(context.Background())

	status, reason := f.status(t)
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if !strings.Contains(reason, "cluster job failed") || !strings.Contains(reason, "DeadlineExceeded") {
		t.Errorf("error_reason = %q, want the fixed reason plus the k8s condition reason", reason)
	}
	f.waitForWebhook(t, 1)
}

// A live Job is not the reconciler's business.
func TestClusterReconcilerLeavesRunningJobAlone(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.fake.setJobStatus(f.jobName, map[string]any{"active": 1})

	f.api.reconcileClusterJobsOnce(context.Background())

	if status, _ := f.status(t); status != "running" {
		t.Errorf("status = %q, want running (untouched)", status)
	}
	if f.hits() != 0 {
		t.Errorf("webhook deliveries = %d, want 0", f.hits())
	}
}

// A session the pod already closed out is never revisited — the reconciler
// must not rewrite a status the session itself reported.
func TestClusterReconcilerSkipsTerminalSession(t *testing.T) {
	f := newReconcileFixture(t, "ended")
	f.fake.setJobStatus(f.jobName, map[string]any{"failed": 1})

	f.api.reconcileClusterJobsOnce(context.Background())

	if status, reason := f.status(t); status != "ended" || reason != "" {
		t.Errorf("status/reason = %q/%q, want ended with no reason", status, reason)
	}
	if f.hits() != 0 {
		t.Errorf("webhook deliveries = %d, want 0", f.hits())
	}
}

// A Job that is not there yet is not a Job that is gone: a young session is
// left alone, and only one older than the grace period is called disappeared.
func TestClusterReconcilerMissingJobHonoursGracePeriod(t *testing.T) {
	f := newReconcileFixture(t, "starting")
	f.fake.setJobMissing(f.jobName)

	f.api.reconcileClusterJobsOnce(context.Background())
	if status, _ := f.status(t); status != "starting" {
		t.Fatalf("status = %q, want starting (within grace period)", status)
	}
	if f.hits() != 0 {
		t.Fatalf("webhook deliveries = %d, want 0 within the grace period", f.hits())
	}

	// Age the row past the grace period.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE sessions SET started_at = now() - interval '10 minutes' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(context.Background())

	status, reason := f.status(t)
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if !strings.Contains(reason, "cluster job disappeared") {
		t.Errorf("error_reason = %q, want the disappeared reason", reason)
	}
	f.waitForWebhook(t, 1)
}

// A disconnected cluster session is RESUMABLE: its pod is gone by definition,
// so a finished Job says nothing about whether the work is over. It is left
// alone until the resume window passes, and only then declared expired.
func TestClusterReconcilerKeepsDisconnectedSessionResumable(t *testing.T) {
	f := newReconcileFixture(t, "disconnected")
	f.api.clusterResumeWindow = time.Hour
	f.fake.setJobStatus(f.jobName, map[string]any{"failed": 1})

	f.api.reconcileClusterJobsOnce(context.Background())
	if status, _ := f.status(t); status != "disconnected" {
		t.Fatalf("status = %q, want disconnected (still resumable)", status)
	}
	if f.hits() != 0 {
		t.Fatalf("webhook deliveries = %d, want 0 while resumable", f.hits())
	}

	// Nobody resumed it for longer than the window.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE sessions
		    SET status_changed_at = now() - interval '2 hours',
		        started_at        = now() - interval '3 hours'
		  WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(context.Background())

	status, reason := f.status(t)
	if status != "error" {
		t.Errorf("status = %q, want error", status)
	}
	if reason != "session expired without resume" {
		t.Errorf("error_reason = %q, want the expiry reason", reason)
	}
	f.waitForWebhook(t, 1)
}

// A desktop session whose daemon dropped is revivable — until it is not. The
// sweep waits out the window, then finalises the session exactly once.
func TestDaemonLostSweepFinalisesAbandonedSession(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.api.daemonLostWindow = time.Hour
	ctx := context.Background()
	// A desktop session, marked lost the way a disconnect marks it.
	if err := db.SetSessionPosture(ctx, f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(ctx, f.pool, f.sessionID, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionDaemonLost(ctx, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}

	f.api.reconcileClusterJobsOnce(ctx)
	if f.hits() != 0 {
		t.Fatalf("webhook deliveries = %d, want 0 while the daemon may still return", f.hits())
	}
	if row, _ := db.GetSession(ctx, f.pool, f.sessionID); row.EndedAt != nil {
		t.Fatalf("session finalised inside the window: ended_at = %v", row.EndedAt)
	}

	// The daemon never came back.
	if _, err := f.pool.Exec(ctx,
		`UPDATE sessions SET daemon_lost_at = now() - interval '2 hours' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	f.api.reconcileClusterJobsOnce(ctx)
	f.waitForWebhook(t, 1)

	row, _ := db.GetSession(ctx, f.pool, f.sessionID)
	if row.Status != "error" || row.EndedAt == nil {
		t.Errorf("session = %q / ended_at %v, want error with an ended_at", row.Status, row.EndedAt)
	}
	if derefOrEmpty(row.ErrorReason) != "daemon did not return" {
		t.Errorf("error_reason = %q, want the lost-daemon reason", derefOrEmpty(row.ErrorReason))
	}
	// A second sweep must not find it again.
	f.api.reconcileClusterJobsOnce(ctx)
	time.Sleep(200 * time.Millisecond)
	if f.hits() != 1 {
		t.Errorf("webhook deliveries = %d, want exactly 1", f.hits())
	}
}

// A daemon that DOES come back clears the marker, so the sweep never sees the
// session however long it runs afterwards.
func TestDaemonLostSweepSkipsRevivedSession(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.api.daemonLostWindow = time.Millisecond
	ctx := context.Background()
	if err := db.SetSessionPosture(ctx, f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionDaemonLost(ctx, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	// The daemon reconnects and reconcile revives the session.
	setSessionStatus(ctx, f.api.hub, f.pool, f.sessionID, "running", nil, nil, false)

	f.api.reconcileClusterJobsOnce(ctx)
	time.Sleep(200 * time.Millisecond)

	if f.hits() != 0 {
		t.Errorf("webhook deliveries = %d, want 0 for a revived session", f.hits())
	}
	if status, _ := f.status(t); status != "running" {
		t.Errorf("status = %q, want running", status)
	}
}

// The sweep's decision is one guarded statement, so a daemon that reconnects
// between the listing and the write wins the race: the session is not ended
// and no completion is reported for a session that is alive.
func TestDaemonLostSweepSkipsSessionRevivedMidSweep(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.api.daemonLostWindow = time.Millisecond
	ctx := context.Background()
	if err := db.SetSessionPosture(ctx, f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(ctx, f.pool, f.sessionID, "error", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionDaemonLost(ctx, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	// The listing saw a lost session…
	// The cutoff comes from the database's own clock, the one that stamped the
	// lost marker: a cutoff from this machine's clock would miss the row whenever
	// the database's clock runs ahead of it.
	var cutoff time.Time
	if err := f.pool.QueryRow(ctx, `SELECT now() + interval '1 second'`).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListLostDaemonSessions(ctx, f.pool, cutoff)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListLostDaemonSessions = %v, %v; want one row", rows, err)
	}
	// …and the daemon reconnected before the write: the revive clears the marker.
	if err := db.ClearSessionDaemonLost(ctx, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}

	finalised, err := db.FinishLostDaemonSession(ctx, f.pool, f.sessionID, "daemon did not return", cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if finalised {
		t.Error("finalised a session whose daemon had come back")
	}
	f.api.reconcileClusterJobsOnce(ctx)
	time.Sleep(200 * time.Millisecond)
	if f.hits() != 0 {
		t.Errorf("webhook deliveries = %d, want 0 for a revived session", f.hits())
	}
	if row, _ := db.GetSession(ctx, f.pool, f.sessionID); row.EndedAt != nil {
		t.Errorf("ended_at = %v, want unset", row.EndedAt)
	}
}

// Resuming a disconnected cluster session restarts its clock: a session
// resumed a moment before the expiry boundary must survive the next pass.
func TestResumeResetsTheExpiryClock(t *testing.T) {
	f := newReconcileFixture(t, "disconnected")
	f.api.clusterResumeWindow = time.Hour
	ctx := context.Background()
	// It has been disconnected for longer than the whole window: without a
	// resume, the very next pass would expire it.
	if _, err := f.pool.Exec(ctx,
		`UPDATE sessions
		    SET status_changed_at = now() - interval '2 hours',
		        started_at        = now() - interval '3 hours'
		  WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	// Somebody resumes it — the row stays "disconnected" while the pod boots.
	resumeClusterSession(ctx, f.api.hub, f.pool, f.sessionID, "carry on", "", testSID)

	f.api.reconcileClusterJobsOnce(ctx)
	time.Sleep(200 * time.Millisecond)

	if status, reason := f.status(t); status != "disconnected" || reason != "" {
		t.Errorf("status/reason = %q/%q, want an untouched disconnected session", status, reason)
	}
	if f.hits() != 0 {
		t.Errorf("webhook deliveries = %d, want 0 for a just-resumed session", f.hits())
	}
}

// A session_started for an existing row is a revive: it must clear the
// lost-daemon marker and restart the status clock, or the sweep could still
// end a session that has just come back.
func TestInsertSessionReviveClearsLostMarkerAndClock(t *testing.T) {
	f := newReconcileFixture(t, "running")
	ctx := context.Background()
	if err := db.SetSessionDaemonLost(ctx, f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE sessions SET status_changed_at = now() - interval '2 hours' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetSession(ctx, f.pool, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, f.pool, f.sessionID, row.DaemonID, "running",
		"/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}

	var lost *time.Time
	var changed time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT daemon_lost_at, status_changed_at FROM sessions WHERE id = $1`,
		f.sessionID).Scan(&lost, &changed); err != nil {
		t.Fatal(err)
	}
	if lost != nil {
		t.Errorf("daemon_lost_at = %v, want cleared by the revive", lost)
	}
	if time.Since(changed) > time.Minute {
		t.Errorf("status_changed_at = %v, want restarted by the revive", changed)
	}
}

// Every reconciler finalisation is an ending, so the session's messaging
// token must not outlive it.
func TestClusterReconcilerRevokesSessionTokens(t *testing.T) {
	f := newReconcileFixture(t, "running")
	ctx := context.Background()
	token, err := db.MintSessionToken(ctx, f.pool, f.sessionID, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatalf("MintSessionToken: %v", err)
	}
	f.fake.setJobStatus(f.jobName, map[string]any{"succeeded": 1})

	f.api.reconcileClusterJobsOnce(ctx)

	if _, err := db.ValidateBoardToken(ctx, f.pool, token); err == nil {
		t.Error("session token still valid after the session was finalised")
	}
}

// The loop honours its context: cancelling it stops the reconciler.
func TestClusterReconcilerLoopStopsOnContextCancel(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.fake.setJobStatus(f.jobName, map[string]any{"succeeded": 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.api.StartClusterJobReconciler(ctx, 10*time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status, _ := f.status(t); status == "ended" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, _ := f.status(t); status != "ended" {
		t.Fatalf("status = %q, want the loop to have reconciled it to ended", status)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not stop on context cancel")
	}
}
