package server

// Session start progress (startstages.go): stage derivation from a Job's pod,
// the tracker's merge rules, the watcher end to end, and persistence/replay.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// podJSON builds a Pod object the way the k8s API returns one.
func podJSON(phase string, conditions []map[string]any, container map[string]any) map[string]any {
	status := map[string]any{"phase": phase}
	if conditions != nil {
		status["conditions"] = conditions
	}
	if container != nil {
		status["containerStatuses"] = []map[string]any{container}
	}
	return map[string]any{"metadata": map[string]any{"creationTimestamp": "2026-09-26T10:00:00Z"}, "status": status}
}

func scheduled() []map[string]any {
	return []map[string]any{{"type": "PodScheduled", "status": "True"}}
}

func waitingOn(reason string) map[string]any {
	return map[string]any{"name": "runner", "state": map[string]any{"waiting": map[string]any{"reason": reason}}}
}

func decodePod(t *testing.T, raw map[string]any) *k8sPod {
	t.Helper()
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var p k8sPod
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func stateOf(updates []protocol.StartStage, id string) (protocol.StartStage, bool) {
	for _, u := range updates {
		if u.ID == id {
			return u, true
		}
	}
	return protocol.StartStage{}, false
}

func TestDeriveClusterStages(t *testing.T) {
	sc := stageContext{Namespace: "ns", Image: "reg/agent:1", SecretName: "agent-secret", JobName: "blerg-runner-agent-x"}
	cases := []struct {
		name      string
		pod       map[string]any
		stage     string // the stage the update is about
		state     string
		detailHas string
		hintHas   string
		wantFatal bool
		fatalHas  string
	}{
		{name: "no pod yet", pod: nil, stage: protocol.StageSchedule, state: protocol.StageStateActive, detailHas: "pod to be created"},
		{name: "pending, not scheduled yet", pod: podJSON("Pending", nil, nil),
			stage: protocol.StageSchedule, state: protocol.StageStateActive, detailHas: "Waiting for a node"},
		{name: "unschedulable: insufficient memory",
			pod: podJSON("Pending", []map[string]any{{"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
				"message": "0/3 nodes are available: 3 Insufficient memory. preemption: node-a.internal 192.0.2.7"}}, nil),
			stage: protocol.StageSchedule, state: protocol.StageStateFailed, detailHas: "not enough memory", hintHas: "BLERG_RUNNER_POD_MEM_REQUEST"},
		{name: "unschedulable: unknown cause",
			pod:   podJSON("Pending", []map[string]any{{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "weird"}}, nil),
			stage: protocol.StageSchedule, state: protocol.StageStateFailed, detailHas: "Unschedulable"},
		{name: "scheduled, creating container", pod: podJSON("Pending", scheduled(), waitingOn("ContainerCreating")),
			stage: protocol.StageImage, state: protocol.StageStateActive, detailHas: "Pulling"},
		{name: "image pull failing", pod: podJSON("Pending", scheduled(), waitingOn("ImagePullBackOff")),
			stage: protocol.StageImage, state: protocol.StageStateFailed, detailHas: "ImagePullBackOff", hintHas: "BLERG_RUNNER_AGENT_IMAGE"},
		{name: "image pull error", pod: podJSON("Pending", scheduled(), waitingOn("ErrImagePull")),
			stage: protocol.StageImage, state: protocol.StageStateFailed, detailHas: "reg/agent:1"},
		{name: "invalid image name", pod: podJSON("Pending", scheduled(), waitingOn("InvalidImageName")),
			stage: protocol.StageImage, state: protocol.StageStateFailed, detailHas: "invalid"},
		{name: "secret missing", pod: podJSON("Pending", scheduled(), waitingOn("CreateContainerConfigError")),
			stage: protocol.StageImage, state: protocol.StageStateFailed, detailHas: "missing Secret", hintHas: "agent-secret"},
		{name: "running: waiting for the pod to connect",
			pod:   podJSON("Running", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"running": map[string]any{"startedAt": "x"}}}),
			stage: protocol.StageConnect, state: protocol.StageStateActive},
		{name: "clone failure: pod exited",
			pod:       podJSON("Failed", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"terminated": map[string]any{"exitCode": 1, "reason": "Error"}}}),
			wantFatal: true, fatalHas: "exit code 1"},
		{name: "OOM killed",
			pod:       podJSON("Failed", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"terminated": map[string]any{"exitCode": 137, "reason": "OOMKilled"}}}),
			wantFatal: true, fatalHas: "out of memory"},
		{name: "evicted", pod: map[string]any{"status": map[string]any{"phase": "Failed", "reason": "Evicted"}},
			wantFatal: true, fatalHas: "evicted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates, fatal := deriveClusterStages(decodePod(t, tc.pod), sc)
			if tc.wantFatal {
				if fatal == nil || !strings.Contains(fatal.Detail, tc.fatalHas) || fatal.State != protocol.StageStateFailed {
					t.Fatalf("fatal = %+v, want failed containing %q", fatal, tc.fatalHas)
				}
				return
			}
			if fatal != nil {
				t.Fatalf("unexpected fatal %+v", fatal)
			}
			u, ok := stateOf(updates, tc.stage)
			if !ok {
				t.Fatalf("no update for %s in %+v", tc.stage, updates)
			}
			if u.State != tc.state || !strings.Contains(u.Detail, tc.detailHas) || !strings.Contains(u.Hint, tc.hintHas) {
				t.Fatalf("%s = %+v, want state %s detail~%q hint~%q", tc.stage, u, tc.state, tc.detailHas, tc.hintHas)
			}
			if q, ok := stateOf(updates, protocol.StageQueued); !ok || q.State != protocol.StageStateDone {
				t.Errorf("queued not reported done: %+v", updates)
			}
			// Never pass the scheduler's raw message on: it names nodes and addresses.
			for _, s := range updates {
				if strings.Contains(s.Detail, "192.0.2.7") || strings.Contains(s.Detail, "node-a") {
					t.Errorf("raw k8s message leaked into %q", s.Detail)
				}
			}
		})
	}
}

func TestNewestLivePodSkipsTerminatingAndPicksNewest(t *testing.T) {
	del := "2026-09-26T10:05:00Z"
	var old, newer, dying k8sPod
	old.Metadata.CreationTimestamp = "2026-09-26T10:00:00Z"
	old.Status.Phase = "Failed"
	newer.Metadata.CreationTimestamp = "2026-09-26T10:04:00Z"
	newer.Status.Phase = "Pending"
	dying.Metadata.CreationTimestamp = "2026-09-26T10:06:00Z"
	dying.Metadata.DeletionTimestamp = &del
	got := newestLivePod([]k8sPod{old, dying, newer})
	if got == nil || got.Status.Phase != "Pending" {
		t.Fatalf("newestLivePod = %+v, want the newest non-terminating pod", got)
	}
	if newestLivePod(nil) != nil {
		t.Fatal("no pods must be nil")
	}
}

func TestStartTrackerMergeRules(t *testing.T) {
	var tr startTracker
	tr.begin("s", clusterStartPlan("org/repo", nil))

	// The pod says it has connected and is cloning.
	got := tr.merge("s", []protocol.StartStage{done(protocol.StageConnect), activeStage(protocol.StageClone)}, true)
	if len(got) != 2 {
		t.Fatalf("authoritative update changed %d stages, want 2", len(got))
	}
	// Every stage before the clone is now done.
	if cur, _ := tr.current("s"); cur.ID != protocol.StageClone {
		t.Fatalf("current = %s, want clone", cur.ID)
	}
	// A stale derived reading (image still pulling, connect active) must not
	// undo it.
	if got := tr.merge("s", []protocol.StartStage{activeStage(protocol.StageImage), activeStage(protocol.StageConnect)}, false); len(got) != 0 {
		t.Fatalf("derived update moved stages backwards: %+v", got)
	}
	// Repeating what is already known changes nothing.
	if got := tr.merge("s", []protocol.StartStage{activeStage(protocol.StageClone)}, true); len(got) != 0 {
		t.Fatalf("duplicate update was not deduplicated: %+v", got)
	}
	// Labels are filled from the plan so every emitted update is readable.
	got = tr.merge("s", []protocol.StartStage{{ID: protocol.StageClone, State: protocol.StageStateFailed, Detail: "boom"}}, true)
	if len(got) != 1 || got[0].Label != "Cloning repo" {
		t.Fatalf("failed update = %+v, want the plan's label", got)
	}
	tr.close("s")
	if tr.isOpen("s") {
		t.Fatal("closed attempt still open")
	}
	// A derived update for a closed attempt is dropped; an authoritative one
	// is still said.
	if got := tr.merge("s", []protocol.StartStage{activeStage(protocol.StageImage)}, false); got != nil {
		t.Fatalf("derived update after close = %+v", got)
	}
}

func done(id string) protocol.StartStage {
	return protocol.StartStage{ID: id, State: protocol.StageStateDone}
}

func activeStage(id string) protocol.StartStage {
	return protocol.StartStage{ID: id, State: protocol.StageStateActive}
}

// collect subscribes to a session's fan-out and returns a reader for the
// start_stage payloads that arrive.
func collectStages(t *testing.T, h *Hub, sessionID string) func(timeout time.Duration, until func([]protocol.StartStagePayload) bool) []protocol.StartStagePayload {
	t.Helper()
	ch := make(chan []byte, 256)
	h.Subscribe(sessionID, "browser-test", ch)
	var got []protocol.StartStagePayload
	return func(timeout time.Duration, until func([]protocol.StartStagePayload) bool) []protocol.StartStagePayload {
		deadline := time.After(timeout)
		for !until(got) {
			select {
			case raw := <-ch:
				var ev protocol.AgentEvent
				if json.Unmarshal(raw, &ev) != nil || ev.Kind != protocol.StartStageKind {
					continue
				}
				var p protocol.StartStagePayload
				if json.Unmarshal(ev.Payload, &p) == nil {
					got = append(got, p)
				}
			case <-deadline:
				t.Fatalf("timed out; stage payloads so far: %+v", got)
			}
		}
		return got
	}
}

func hasStage(ps []protocol.StartStagePayload, id, state string) bool {
	for _, p := range ps {
		for _, s := range p.Stages {
			if s.ID == id && s.State == state {
				return true
			}
		}
	}
	return false
}

// The watcher, end to end against a fake API server: the pod goes from
// unscheduled to pulling to failing its image pull, and each step reaches a
// subscribed browser as a stage update.
func TestClusterStartWatcherFollowsThePod(t *testing.T) {
	f := &fakeK8s{}
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, f))
	const sid = "11111111-2222-4333-8444-555555555555"
	read := collectStages(t, h, sid)

	f.setPod(sid, podJSON("Pending", nil, nil))
	ctx := context.Background()
	announceClusterStart(ctx, h, nil, sid, "org/repo", false, nil)
	clusterJobCreated(ctx, h, nil, sid, 10*time.Millisecond)

	got := read(2*time.Second, func(ps []protocol.StartStagePayload) bool { return len(ps) > 0 && ps[0].Plan })
	if len(got[0].Stages) != 7 || got[0].Runtime != "cluster" {
		t.Fatalf("plan = %+v", got[0])
	}
	read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageQueued, protocol.StageStateDone) && hasStage(ps, protocol.StageSchedule, protocol.StageStateActive)
	})

	f.setPod(sid, podJSON("Pending", scheduled(), waitingOn("ContainerCreating")))
	read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageImage, protocol.StageStateActive)
	})

	f.setPod(sid, podJSON("Pending", scheduled(), waitingOn("ImagePullBackOff")))
	got = read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageImage, protocol.StageStateFailed)
	})
	last := got[len(got)-1].Stages[0]
	if !strings.Contains(last.Detail, "ImagePullBackOff") || last.Hint == "" {
		t.Fatalf("image failure = %+v, want the reason and a next step", last)
	}

	// The pull recovers, the pod runs and reports its own progress; the ready
	// transition closes the attempt and the watcher stops.
	f.setPod(sid, podJSON("Running", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"running": map[string]any{}}}))
	read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageConnect, protocol.StageStateActive)
	})
	markStartReady(ctx, h, nil, sid, false)
	read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageReady, protocol.StageStateDone)
	})
	if h.starts.isOpen(sid) {
		t.Fatal("attempt still open after ready")
	}
}

// A pod that exits while starting fails the stage the start was on.
func TestClusterStartWatcherReportsPodExit(t *testing.T) {
	f := &fakeK8s{}
	h := NewHub()
	h.SetJobManager(newTestJobManager(t, f))
	const sid = "11111111-2222-4333-8444-666666666666"
	read := collectStages(t, h, sid)
	ctx := context.Background()
	announceClusterStart(ctx, h, nil, sid, "org/repo", false, nil)
	// The pod connected and started cloning, then died.
	advanceStart(ctx, h, nil, sid, true, done(protocol.StageConnect), activeStage(protocol.StageClone))
	f.setPod(sid, podJSON("Failed", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"terminated": map[string]any{"exitCode": 128}}}))
	clusterJobCreated(ctx, h, nil, sid, 10*time.Millisecond)
	got := read(2*time.Second, func(ps []protocol.StartStagePayload) bool {
		return hasStage(ps, protocol.StageClone, protocol.StageStateFailed)
	})
	last := got[len(got)-1].Stages[0]
	if !strings.Contains(last.Detail, "exit code 128") || !strings.Contains(last.Hint, "kubectl logs") {
		t.Fatalf("clone failure = %+v", last)
	}
}

// ─── Persistence / replay (Postgres) ─────────────────────────────────────────

// Stage events persist as agent_events and replay in order, so a browser that
// reloads mid-start rebuilds the same panel — and a server that restarted
// mid-start still closes the attempt from the persisted history.
func TestStartStagesPersistAndReplay(t *testing.T) {
	f := &fakeK8s{}
	api, h, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sid := newUUID()
	if err := db.InsertClusterSession(ctx, pool, sid, clusterDaemonID, "starting", "/workspace/org/repo", "org/repo", "T", ""); err != nil {
		t.Fatal(err)
	}
	_ = api
	announceClusterStart(ctx, h, pool, sid, "org/repo", false, nil)
	advanceStart(ctx, h, pool, sid, true, done(protocol.StageQueued), activeStage(protocol.StageSchedule))

	rows, err := db.ListAgentEvents(ctx, pool, sid, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Kind != protocol.StartStageKind || rows[1].Seq <= rows[0].Seq {
		t.Fatalf("persisted events = %+v", rows)
	}
	var plan protocol.StartStagePayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &plan); err != nil || !plan.Plan {
		t.Fatalf("first event is not the plan: %s", rows[0].Payload)
	}
	if !lastAttemptOpen(ctx, pool, sid) {
		t.Fatal("attempt should be open")
	}

	// Server restart: the in-memory attempt is gone. The session becoming
	// live must still close it, from the history.
	h.starts.close(sid)
	if err := HandleSessionStateChanged(ctx, h, pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sid, Status: "idle",
	}); err != nil {
		t.Fatal(err)
	}
	if lastAttemptOpen(ctx, pool, sid) {
		t.Fatal("ready was not recorded after the restart")
	}
	row, _ := db.GetSession(ctx, pool, sid)
	if row == nil || row.Status != "idle" || row.Unread {
		t.Fatalf("starting → idle: row = %+v, want idle and not unread (no turn has finished)", row)
	}
}

// A start that fails (the pod reports its clone failed and the session
// errors) records the failure with its reason; nothing is marked ready.
func TestStartFailureIsRecorded(t *testing.T) {
	f := &fakeK8s{}
	_, h, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sid := newUUID()
	if err := db.InsertClusterSession(ctx, pool, sid, clusterDaemonID, "starting", "/workspace/org/repo", "org/repo", "T", ""); err != nil {
		t.Fatal(err)
	}
	announceClusterStart(ctx, h, pool, sid, "org/repo", false, nil)
	advanceStart(ctx, h, pool, sid, true, done(protocol.StageConnect), activeStage(protocol.StageClone))
	msg := "Repository not found, or the git credential can't see it"
	if err := HandleSessionStateChanged(ctx, h, pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sid, Status: "error", Message: &msg,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListAgentEventsTail(ctx, pool, sid, protocol.StartStageKind, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("tail: %v %v", rows, err)
	}
	var p protocol.StartStagePayload
	_ = json.Unmarshal([]byte(rows[0].Payload), &p)
	if len(p.Stages) != 1 || p.Stages[0].ID != protocol.StageClone || p.Stages[0].State != protocol.StageStateFailed || p.Stages[0].Detail != msg {
		t.Fatalf("failure event = %+v", p)
	}
	if lastAttemptOpen(ctx, pool, sid) || h.starts.isOpen(sid) {
		t.Fatal("failed attempt still open")
	}
}

// POST /api/sessions on the cluster runtime makes the session visible at
// once and records its plan; a Job that cannot be created ends the session
// with the reason instead of leaving it "starting".
func TestClusterSpawnAnnouncesAndRecordsJobFailure(t *testing.T) {
	f := &fakeK8s{}
	_, h, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(h, pool, "daemon-tok-1234567890", nil, "")
	sid := newUUID()
	if err := db.InsertClusterSession(ctx, pool, sid, clusterDaemonID, "starting", "/workspace/org/repo", "org/repo", "T", ""); err != nil {
		t.Fatal(err)
	}
	announceClusterStart(ctx, h, pool, sid, "org/repo", false, nil)
	api.markClusterStartFailed(ctx, sid, "cluster session cap reached (2)")
	row, _ := db.GetSession(ctx, pool, sid)
	if row == nil || row.Status != "error" || row.ErrorReason == nil || *row.ErrorReason != "cluster session cap reached (2)" {
		t.Fatalf("row = %+v", row)
	}
	rows, _ := db.ListAgentEventsTail(ctx, pool, sid, protocol.StartStageKind, 1)
	var p protocol.StartStagePayload
	_ = json.Unmarshal([]byte(rows[0].Payload), &p)
	if p.Stages[0].ID != protocol.StageQueued || p.Stages[0].State != protocol.StageStateFailed || p.Stages[0].Hint == "" {
		t.Fatalf("failure = %+v, want queued failed with a next step", p)
	}
}
