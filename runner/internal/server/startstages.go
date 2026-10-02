package server

// Session start progress.
//
// Between "Start" and a live agent a session can spend minutes — a cluster
// session waits for a node, pulls an image, clones the repo and connects —
// and all of that used to read as a bare "Starting…". Start stages make it
// visible: an ordered plan of named steps, each pending → active → done (or
// failed, with the reason and the next step to take).
//
// Stages travel as agent_events of kind start_stage (protocol.StartStagePayload),
// so they persist and replay with the transcript: a browser that reloads mid
// start sees exactly where it had got to. Three parties write them:
//
//   - the server, at spawn: the runtime's plan (plan=true opens an attempt);
//   - the server's start watcher (cluster only): what the Job's pod is doing,
//     derived from the pod's status (deriveClusterStages);
//   - the runner pod itself: connect / clone / engine, which only it can see.
//
// Readiness and failure close an attempt: a transition to a live status emits
// "ready", and an error while starting marks the stage the session was on as
// failed. The in-memory tracker below mirrors the current attempt so server
// derived updates can be deduplicated and never move a stage backwards.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Plans ───────────────────────────────────────────────────────────────────

func stage(id, label, state, detail string) protocol.StartStage {
	return protocol.StartStage{ID: id, Label: label, State: state, Detail: detail}
}

// clusterStartPlan is the plan of a cluster-runtime start (or resume): the
// server creates the Job, k8s schedules the pod and pulls the image, then the
// pod dials the server, clones and starts the engine.
//
// plugins is the always-on plugin stage (see pluginStage), included only when the
// session has plugins — or a note about why it does not — and placed after the
// clone and before the agent starts, the order the pod does them in.
func clusterStartPlan(repo string, plugins *protocol.StartStage) []protocol.StartStage {
	cloneLabel, cloneDetail := "Cloning repo", repo
	if repo == "" {
		// A "No repository" session: the pod only makes an empty directory.
		cloneLabel, cloneDetail = "Preparing workspace", "empty — no repository"
	}
	plan := []protocol.StartStage{
		stage(protocol.StageQueued, "Queued", protocol.StageStateActive, "Creating the session's Job"),
		stage(protocol.StageSchedule, "Scheduling pod", protocol.StageStatePending, ""),
		stage(protocol.StageImage, "Pulling image", protocol.StageStatePending, ""),
		stage(protocol.StageConnect, "Connecting to server", protocol.StageStatePending, ""),
		stage(protocol.StageClone, cloneLabel, protocol.StageStatePending, cloneDetail),
	}
	if plugins != nil {
		plan = append(plan, *plugins)
	}
	return append(plan,
		stage(protocol.StageEngine, "Starting agent", protocol.StageStatePending, ""),
		stage(protocol.StageReady, "Ready", protocol.StageStatePending, ""),
	)
}

// daemonStartPlan is the plan of a daemon-hosted agent start: the spawn goes
// to the daemon, which (for the Docker runtime) starts the sandbox container
// and then the engine.
func daemonStartPlan(daemonName string, sandbox bool) []protocol.StartStage {
	plan := []protocol.StartStage{
		stage(protocol.StageQueued, "Queued", protocol.StageStateDone, ""),
		stage(protocol.StageDaemon, "Waiting for daemon", protocol.StageStateActive, daemonName),
	}
	if sandbox {
		plan = append(plan, stage(protocol.StageSandbox, "Starting sandbox container", protocol.StageStatePending, ""))
	}
	return append(plan,
		stage(protocol.StageEngine, "Starting agent", protocol.StageStatePending, ""),
		stage(protocol.StageReady, "Ready", protocol.StageStatePending, ""),
	)
}

// ─── Tracker ─────────────────────────────────────────────────────────────────

// startAttempt is the server's view of one session's current start attempt.
type startAttempt struct {
	stages   []protocol.StartStage // merged, in plan order
	watching bool                  // a cluster start watcher owns this attempt
}

// startTracker holds the open attempts, keyed by session id. Its zero value
// is ready to use.
type startTracker struct {
	mu       sync.Mutex
	attempts map[string]*startAttempt
}

func (t *startTracker) get(sessionID string) *startAttempt {
	if t.attempts == nil {
		t.attempts = make(map[string]*startAttempt)
	}
	return t.attempts[sessionID]
}

// begin opens a new attempt with plan. The caller persists the plan event.
func (t *startTracker) begin(sessionID string, plan []protocol.StartStage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.get(sessionID)
	t.attempts[sessionID] = &startAttempt{stages: append([]protocol.StartStage(nil), plan...)}
}

// adopt opens an in-memory attempt for a session whose plan event was
// persisted by an earlier server process — nothing is emitted, updates simply
// merge into what the browser already has. No-op when an attempt exists.
func (t *startTracker) adopt(sessionID string, plan []protocol.StartStage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.get(sessionID) != nil {
		return
	}
	stages := append([]protocol.StartStage(nil), plan...)
	for i := range stages {
		stages[i].State = protocol.StageStatePending
	}
	t.attempts[sessionID] = &startAttempt{stages: stages}
}

// isOpen reports whether sessionID has an attempt still in progress.
func (t *startTracker) isOpen(sessionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.get(sessionID)
	return a != nil
}

// claimWatch marks the attempt as watched; false when there is no open
// attempt or a watcher already owns it.
func (t *startTracker) claimWatch(sessionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.get(sessionID)
	if a == nil || a.watching {
		return false
	}
	a.watching = true
	return true
}

func (t *startTracker) releaseWatch(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a := t.get(sessionID); a != nil {
		a.watching = false
	}
}

// current returns the stage the attempt is on: the first one not done.
func (t *startTracker) current(sessionID string) (protocol.StartStage, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.get(sessionID)
	if a == nil {
		return protocol.StartStage{}, false
	}
	for _, s := range a.stages {
		if s.State != protocol.StageStateDone && s.State != protocol.StageStateWarning {
			return s, true
		}
	}
	return protocol.StartStage{}, false
}

// close ends the attempt (ready or failed) and forgets it: derived updates
// for a session with no open attempt are dropped, so nothing more is said.
func (t *startTracker) close(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, sessionID)
}

// merge applies updates to the attempt and returns the ones that changed
// something. authoritative updates (the pod's own reports, readiness and
// failures) are always applied; derived ones (the server's reading of the
// pod) never move a stage backwards — a done stage stays done, and a stage
// behind the furthest-progressed one is left alone — so a stale pod status
// can't undo what the pod itself reported.
func (t *startTracker) merge(sessionID string, updates []protocol.StartStage, authoritative bool) []protocol.StartStage {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.get(sessionID)
	if a == nil {
		if authoritative {
			return updates // nothing to dedupe against; say it anyway
		}
		return nil
	}
	furthest := -1
	for i, s := range a.stages {
		if s.State == protocol.StageStateActive || s.State == protocol.StageStateDone {
			furthest = i
		}
	}
	var changed []protocol.StartStage
	for _, u := range updates {
		idx := -1
		for i, s := range a.stages {
			if s.ID == u.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			// A stage the plan did not have (the pod reports plugins to a server that restarted
			// mid-start and adopted a plan without it): keep it before the agent start, not after.
			r := stageIndex(a.stages, protocol.StageEngine)
			if r < 0 {
				r = stageIndex(a.stages, protocol.StageReady)
			}
			if r >= 0 && u.ID != protocol.StageReady && u.ID != protocol.StageEngine {
				a.stages = append(a.stages[:r], append([]protocol.StartStage{u}, a.stages[r:]...)...)
			} else {
				a.stages = append(a.stages, u)
			}
			changed = append(changed, u)
			continue
		}
		cur := a.stages[idx]
		if !authoritative {
			if (cur.State == protocol.StageStateDone || cur.State == protocol.StageStateWarning) && u.State != protocol.StageStateDone {
				continue
			}
			if idx < furthest && u.State != protocol.StageStateDone {
				continue
			}
		}
		if cur.State == u.State && cur.Detail == u.Detail && cur.Hint == u.Hint {
			continue
		}
		if u.Label == "" {
			u.Label = cur.Label
		}
		a.stages[idx] = u
		changed = append(changed, u)
	}
	// Everything before the furthest active/done stage is done — the same
	// rule the browser applies — so current() stays truthful.
	furthest = -1
	for i, s := range a.stages {
		if s.State == protocol.StageStateActive || s.State == protocol.StageStateDone {
			furthest = i
		}
	}
	for i := 0; i < furthest; i++ {
		if a.stages[i].State != protocol.StageStateWarning { // a warning is already over; folding it to done would hide it
			a.stages[i].State = protocol.StageStateDone
		}
	}
	return changed
}

func stageIndex(stages []protocol.StartStage, id string) int {
	for i, s := range stages {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// ─── Emission ────────────────────────────────────────────────────────────────

// persistStartStages appends one start_stage event and fans it out to the
// session's subscribers. The event reaches only browsers watching the
// session; the sessions list learns about status from session_* messages.
func persistStartStages(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string, payload protocol.StartStagePayload) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(),
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: protocol.StartStageKind, Payload: raw,
	}
	if pool != nil {
		seq, _, err := db.AppendAgentEvent(ctx, pool, sessionID, ev.ClientEventID, ev.Kind, string(raw))
		if err != nil {
			log.Printf("start_stage %s: %v", sessionID, err)
			return
		}
		ev.Seq = seq
	}
	if msg, err := json.Marshal(ev); err == nil {
		h.FanOutSessionOutput(sessionID, msg)
	}
}

// beginStart opens a start attempt for sessionID and emits its plan.
func beginStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, runtime string, plan []protocol.StartStage) {
	h.starts.begin(sessionID, plan)
	persistStartStages(ctx, h, pool, sessionID, protocol.StartStagePayload{Plan: true, Runtime: runtime, Stages: plan})
}

// advanceStart emits stage updates for the current attempt, skipping what
// changes nothing. authoritative: see startTracker.merge.
func advanceStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string, authoritative bool, updates ...protocol.StartStage) {
	changed := h.starts.merge(sessionID, updates, authoritative)
	if len(changed) == 0 {
		return
	}
	persistStartStages(ctx, h, pool, sessionID, protocol.StartStagePayload{Stages: changed})
}

// markStartReady closes sessionID's attempt as ready. When this process has
// no record of an attempt (it restarted mid-start), the persisted history
// decides: the latest start_stage event says whether an attempt is still
// open. lookup=false skips that query for hot paths.
func markStartReady(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string, lookup bool) {
	ready := stage(protocol.StageReady, "Ready", protocol.StageStateDone, "")
	if h.starts.isOpen(sessionID) {
		advanceStart(ctx, h, pool, sessionID, true, ready)
		h.starts.close(sessionID)
		return
	}
	if !lookup || pool == nil || !lastAttemptOpen(ctx, pool, sessionID) {
		return
	}
	persistStartStages(ctx, h, pool, sessionID, protocol.StartStagePayload{Stages: []protocol.StartStage{ready}})
}

// failStart marks the stage sessionID's attempt is on as failed (unless the
// pod already said why) and closes the attempt.
func failStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, detail, hint string) {
	if !h.starts.isOpen(sessionID) {
		return
	}
	if cur, ok := h.starts.current(sessionID); ok && cur.State != protocol.StageStateFailed {
		cur.State = protocol.StageStateFailed
		cur.Detail = detail
		cur.Hint = hint
		advanceStart(ctx, h, pool, sessionID, true, cur)
	}
	h.starts.close(sessionID)
}

// lastAttemptOpen reports whether the latest persisted start_stage event
// belongs to an attempt that has neither become ready nor failed.
func lastAttemptOpen(ctx context.Context, pool *pgxpool.Pool, sessionID string) bool {
	rows, err := db.ListAgentEventsTail(ctx, pool, sessionID, protocol.StartStageKind, 1)
	if err != nil || len(rows) == 0 {
		return false
	}
	var p protocol.StartStagePayload
	if json.Unmarshal([]byte(rows[0].Payload), &p) != nil {
		return false
	}
	for _, s := range p.Stages {
		if s.ID == protocol.StageReady && s.State == protocol.StageStateDone {
			return false
		}
		if s.State == protocol.StageStateFailed && !p.Plan {
			return false
		}
	}
	return true
}

// isLiveStatus is a status a started session settles into.
func isLiveStatus(status string) bool {
	return status == "running" || status == "idle" || status == "waiting"
}

// ─── Cluster watcher ─────────────────────────────────────────────────────────

const (
	// startWatchInterval paces the pod polls of a starting cluster session.
	// Starts are rare and short; a 2 s poll of one label-selected pod list
	// is cheap and makes the panel feel live.
	startWatchInterval = 2 * time.Second
	// startWatchLimit bounds a watcher that never sees the session settle.
	// The browser offers "stop" long before this; the watcher only stops
	// polling the API.
	startWatchLimit = 20 * time.Minute
)

func (a *API) startWatchEvery() time.Duration {
	if a.startWatchInterval > 0 {
		return a.startWatchInterval
	}
	return startWatchInterval
}

// watchClusterStart polls the session's pod until the attempt closes (ready
// or failed), the session leaves its starting status, or startWatchLimit
// passes, turning what the pod is doing into stage updates. One watcher per
// attempt; a second call while one runs is a no-op.
func watchClusterStart(h *Hub, pool *pgxpool.Pool, sessionID string, every time.Duration) {
	jm := h.JobManager()
	if jm == nil || !h.starts.claimWatch(sessionID) {
		return
	}
	if every <= 0 {
		every = startWatchInterval
	}
	go func() {
		defer h.starts.releaseWatch(sessionID)
		ctx, cancel := context.WithTimeout(context.Background(), startWatchLimit)
		defer cancel()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			if !h.starts.isOpen(sessionID) {
				return
			}
			pod, err := jm.sessionPod(sessionID)
			if errors.Is(err, errPodsForbidden) {
				// No RBAC for pods: nothing to watch, and asking again every
				// two seconds for twenty minutes would change nothing. The
				// pod's own reports and the status transitions still arrive.
				log.Printf("start watcher %s: pods not readable (grant get/list on pods to show start progress)", sessionID)
				return
			}
			if err == nil {
				if pool != nil {
					row, err := db.GetSession(ctx, pool, sessionID)
					if err == nil && (row == nil || (row.Status != "starting" && row.Status != "disconnected")) {
						return // settled (ready/failed are recorded by those paths) or gone
					}
				}
				updates, fatal := deriveClusterStages(pod, jm.stageContext(sessionID))
				advanceStart(ctx, h, pool, sessionID, false, updates...)
				if fatal != nil {
					failStart(ctx, h, pool, sessionID, fatal.Detail, fatal.Hint)
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// ─── Spawn-path hooks ────────────────────────────────────────────────────────

// broadcastSessionCreated tells every browser about a session row the moment
// it exists, as the same session_started message a daemon's report produces
// (browsers upsert it). Without it a cluster session is invisible — a bare
// "Starting…" in the detail view, absent from the list — until its pod
// connects, which is minutes on a cold node.
func broadcastSessionCreated(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string) {
	if pool == nil {
		return
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil {
		return
	}
	h.BroadcastJSON(protocol.BrowserSessionStarted{Type: "session_started", Session: sessionRowToInfo(*row, "")})
}

// announceClusterStart runs once a cluster session's row exists, before its
// Job is created: the session appears everywhere as "starting" and its start
// plan is recorded. resume marks the plan as a resume attempt.
func announceClusterStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, repo string, resume bool, plugins *protocol.StartStage) {
	plan := clusterStartPlan(repo, plugins)
	if resume {
		plan[0].Detail = "Resuming: creating a new Job"
	} else {
		broadcastSessionCreated(ctx, h, pool, sessionID)
	}
	beginStart(ctx, h, pool, sessionID, "cluster", plan)
}

// clusterJobCreated moves a cluster start past "Queued" and starts watching
// its pod.
func clusterJobCreated(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string, every time.Duration) {
	advanceStart(ctx, h, pool, sessionID, true,
		stage(protocol.StageQueued, "", protocol.StageStateDone, ""),
		stage(protocol.StageSchedule, "", protocol.StageStateActive, "Waiting for a node"))
	watchClusterStart(h, pool, sessionID, every) //nolint:contextcheck // the watcher runs for the whole start attempt, long after this call returns, under its own deadline
}

// clusterJobCreateFailed records why a cluster session never got a Job.
// reason is the same text the caller of the start is given (CreateSessionJob
// already keeps anything credential-bearing or cluster-internal out of it).
func clusterJobCreateFailed(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, reason string) {
	failStart(ctx, h, pool, sessionID, reason,
		"Nothing was started. If the cluster is at its session cap, stop another session; otherwise check the server log, then start again.")
}

// markClusterStartFailed ends a cluster session whose Job could not be
// created: terminal status and reason on the row, the start attempt closed as
// failed, browsers told. reason must already be caller-safe.
func (a *API) markClusterStartFailed(ctx context.Context, sessionID, reason string) {
	if a.dbPool != nil {
		now := time.Now()
		if err := db.EndSessionStatus(ctx, a.dbPool, sessionID, "error", &now, systemEnd(db.EndReasonStartFailed)); err != nil {
			log.Printf("cluster start: mark %s failed: %v", sessionID, err)
		}
		if err := db.SetSessionError(ctx, a.dbPool, sessionID, reason); err != nil {
			log.Printf("cluster start: SetSessionError %s: %v", sessionID, err)
		}
		// A start that failed after its grants were made: no Job will ever use the tokens.
		revokeSessionGrants(ctx, a.dbPool, sessionID, "cluster start failed")
	}
	clusterJobCreateFailed(ctx, a.hub, a.dbPool, sessionID, reason)
	broadcastWithEnd(a.hub, sessionEndFor(ctx, a.dbPool, sessionID, "error"), func(endReason string, endedBy *protocol.EndedBy) any {
		return protocol.SessionStateChanged{
			Type: "session_state_changed", SessionID: sessionID, Status: "error", Message: &reason,
			EndReason: endReason, EndedBy: endedBy,
		}
	})
}

// announceDaemonAgentStart records the start plan of an agent session sent
// to a daemon and shows the session everywhere. Called only once the spawn
// is in the daemon's send queue: before that the row may still be deleted
// (abortSpawnSessionToken), and agent_events rows would block the delete.
func announceDaemonAgentStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, daemonName string, sandbox bool) {
	runtime := daemonRuntimeName
	if sandbox {
		runtime = "docker"
	}
	beginStart(ctx, h, pool, sessionID, runtime, daemonStartPlan(daemonName, sandbox))
	broadcastSessionCreated(ctx, h, pool, sessionID)
}

// adoptClusterStart picks a starting cluster session back up after a server
// restart: when its persisted attempt is still open, the tracker adopts it
// and a watcher resumes. Called from the reconciler.
func adoptClusterStart(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, repo string, every time.Duration) {
	if h.starts.isOpen(sessionID) || pool == nil || !lastAttemptOpen(ctx, pool, sessionID) {
		return
	}
	h.starts.adopt(sessionID, clusterStartPlan(repo, nil))
	watchClusterStart(h, pool, sessionID, every) //nolint:contextcheck // the watcher runs for the whole start attempt, long after this call returns, under its own deadline
}

// ─── Pod observation ─────────────────────────────────────────────────────────

// k8sPod is the slice of a Pod's status the start stages are derived from.
type k8sPod struct {
	Metadata struct {
		CreationTimestamp string  `json:"creationTimestamp"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
		ContainerStatuses []k8sContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type k8sContainerStatus struct {
	Name  string `json:"name"`
	State struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Running *struct {
			StartedAt string `json:"startedAt"`
		} `json:"running"`
		Terminated *struct {
			ExitCode int    `json:"exitCode"`
			Reason   string `json:"reason"`
		} `json:"terminated"`
	} `json:"state"`
}

// errPodsForbidden: the server's service account may not read pods.
var errPodsForbidden = errors.New("pods not readable")

// sessionPod returns the newest live pod of the session's Job, or nil when
// none exists yet. An error means the API could not be asked (no RBAC for
// pods, API down) — the caller says nothing rather than guessing.
func (j *JobManager) sessionPod(sessionID string) (*k8sPod, error) {
	resp, err := j.do(http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/pods?labelSelector="+
		url.QueryEscape("app=blerg-runner-agent,session="+sessionID), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, errPodsForbidden
		}
		return nil, fmt.Errorf("list pods: %d", resp.StatusCode)
	}
	var list struct {
		Items []k8sPod `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return newestLivePod(list.Items), nil
}

// newestLivePod picks the pod that matters: not being deleted (a resume
// replaces a finished Job, whose old pod can linger while it terminates),
// and the most recently created.
func newestLivePod(items []k8sPod) *k8sPod {
	live := make([]k8sPod, 0, len(items))
	for _, p := range items {
		if p.Metadata.DeletionTimestamp == nil {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return nil
	}
	sort.SliceStable(live, func(a, b int) bool {
		return live[a].Metadata.CreationTimestamp > live[b].Metadata.CreationTimestamp
	})
	return &live[0]
}

// stageContext is the operator configuration the hints point at. None of it
// is secret — the cluster status endpoint already shows all of it.
type stageContext struct {
	Namespace  string
	Image      string
	SecretName string
	JobName    string
}

func (j *JobManager) stageContext(sessionID string) stageContext {
	return stageContext{Namespace: j.Namespace, Image: j.Image, SecretName: j.SecretName, JobName: j.jobName(sessionID)}
}

// deriveClusterStages turns a pod's status into stage updates. fatal is set
// when the pod can no longer bring the session up (it exited, or was
// evicted); the stage it failed on is the tracker's current one. Only fixed
// texts and k8s reason tokens go into details — never a raw condition or
// event message, which can carry environment detail (node names, addresses).
func deriveClusterStages(pod *k8sPod, sc stageContext) (updates []protocol.StartStage, fatal *protocol.StartStage) {
	queuedDone := stage(protocol.StageQueued, "", protocol.StageStateDone, "")
	if pod == nil {
		return []protocol.StartStage{queuedDone,
			stage(protocol.StageSchedule, "", protocol.StageStateActive, "Waiting for the pod to be created")}, nil
	}
	if pod.Status.Reason == "Evicted" {
		return nil, &protocol.StartStage{State: protocol.StageStateFailed,
			Detail: "The pod was evicted (its node ran short of resources)",
			Hint:   "Stop this session and start it again; if it keeps happening, the cluster needs more headroom."}
	}
	var runner *k8sContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == "runner" || runner == nil {
			runner = &pod.Status.ContainerStatuses[i]
		}
	}
	if runner != nil && runner.State.Terminated != nil {
		t := runner.State.Terminated
		detail := fmt.Sprintf("The pod exited (exit code %d)", t.ExitCode)
		hint := "See the pod log: kubectl logs -n " + sc.Namespace + " job/" + sc.JobName
		if t.Reason == "OOMKilled" {
			detail = "The pod ran out of memory (OOMKilled)"
			hint = "Raise BLERG_RUNNER_POD_MEM_LIMIT for cluster sessions, then start the session again."
		}
		return nil, &protocol.StartStage{State: protocol.StageStateFailed, Detail: detail, Hint: hint}
	}
	if runner != nil && runner.State.Running != nil {
		return []protocol.StartStage{queuedDone,
			stage(protocol.StageImage, "", protocol.StageStateDone, ""),
			stage(protocol.StageConnect, "", protocol.StageStateActive, "Pod running; waiting for it to dial the server")}, nil
	}

	scheduled := false
	for _, c := range pod.Status.Conditions {
		if c.Type != "PodScheduled" {
			continue
		}
		if c.Status == "True" {
			scheduled = true
		} else if c.Reason == "Unschedulable" {
			return []protocol.StartStage{queuedDone, {
				ID: protocol.StageSchedule, State: protocol.StageStateFailed,
				Detail: unschedulableDetail(c.Message),
				Hint:   "The scheduler keeps trying and the session will continue once a node fits. To give up, stop the session; to fix it, free capacity or lower BLERG_RUNNER_POD_CPU_REQUEST / BLERG_RUNNER_POD_MEM_REQUEST.",
			}}, nil
		}
	}
	if !scheduled {
		return []protocol.StartStage{queuedDone,
			stage(protocol.StageSchedule, "", protocol.StageStateActive, "Waiting for a node")}, nil
	}
	scheduledDone := stage(protocol.StageSchedule, "", protocol.StageStateDone, "")
	waiting := ""
	if runner != nil && runner.State.Waiting != nil {
		waiting = runner.State.Waiting.Reason
	}
	switch waiting {
	case "ErrImagePull", "ImagePullBackOff":
		return []protocol.StartStage{queuedDone, scheduledDone, {
			ID: protocol.StageImage, State: protocol.StageStateFailed,
			Detail: "Can't pull the agent image " + sc.Image + " (" + waiting + "); the cluster keeps retrying",
			Hint:   "Check BLERG_RUNNER_AGENT_IMAGE names an image that exists and that the cluster can pull from its registry (image pull secret). Stop the session to give up.",
		}}, nil
	case "InvalidImageName":
		return []protocol.StartStage{queuedDone, scheduledDone, {
			ID: protocol.StageImage, State: protocol.StageStateFailed,
			Detail: "The agent image name " + sc.Image + " is invalid",
			Hint:   "Fix BLERG_RUNNER_AGENT_IMAGE on the server, then start a new session. Stop this one.",
		}}, nil
	case "CreateContainerConfigError", "CreateContainerError":
		return []protocol.StartStage{queuedDone, scheduledDone, {
			ID: protocol.StageImage, State: protocol.StageStateFailed,
			Detail: "The pod can't be set up (" + waiting + ") — usually a missing Secret or Secret key",
			Hint:   "Check the Secret " + sc.SecretName + " exists in namespace " + sc.Namespace + " with BLERG_RUNNER_DAEMON_TOKEN set. Stop the session, fix it, and start again.",
		}}, nil
	}
	return []protocol.StartStage{queuedDone, scheduledDone,
		stage(protocol.StageImage, "", protocol.StageStateActive, "Pulling the image and creating the container")}, nil
}

// unschedulableDetail names why no node fits, from the scheduler's message,
// using only fixed phrases for the causes it recognises.
func unschedulableDetail(msg string) string {
	m := strings.ToLower(msg)
	var causes []string
	if strings.Contains(m, "insufficient cpu") {
		causes = append(causes, "not enough CPU")
	}
	if strings.Contains(m, "insufficient memory") {
		causes = append(causes, "not enough memory")
	}
	if strings.Contains(m, "too many pods") {
		causes = append(causes, "too many pods")
	}
	if strings.Contains(m, "taint") {
		causes = append(causes, "node taints")
	}
	if strings.Contains(m, "affinity") || strings.Contains(m, "selector") {
		causes = append(causes, "node selector/affinity")
	}
	if len(causes) == 0 {
		return "No node can run the pod right now (Unschedulable)"
	}
	return "No node can run the pod: " + strings.Join(causes, ", ")
}
