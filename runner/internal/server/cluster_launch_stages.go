package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These events are the session's persisted launch status. Each transition has
// the event timestamp, outcome and a fixed reason; upstream bodies, credentials,
// repository URLs and manifests must never be passed as reasons.
var workspaceLaunchStages = []protocol.StartStage{
	stage("selection", "Authorizing selection", protocol.StageStateActive, "Authorizing hosted selection"),
	stage("manifest", "Saving workspace manifest", protocol.StageStatePending, ""),
	stage("plugins_resolve", "Resolving launch plugins", protocol.StageStatePending, ""),
	stage("authorization", "Checking launch authority", protocol.StageStatePending, ""),
	stage("credentials", "Checking repository access", protocol.StageStatePending, ""),
	stage("storage", "Allocating protected storage", protocol.StageStatePending, ""),
	stage("writer_fence", "Fencing previous writers", protocol.StageStatePending, ""),
	stage("runtime_binding", "Binding workspace generation", protocol.StageStatePending, ""),
	stage("launch_admission", "Checking launch capacity", protocol.StageStatePending, ""),
	stage("runtime_token", "Issuing runtime authority", protocol.StageStatePending, ""),
	stage("launch_credentials", "Preparing engine credentials", protocol.StageStatePending, ""),
	stage("pod_manifest", "Validating pod bootstrap", protocol.StageStatePending, ""),
	stage("secrets", "Creating launch Secret", protocol.StageStatePending, ""),
}

func clusterWorkspaceStartPlan() []protocol.StartStage {
	plan := append([]protocol.StartStage(nil), workspaceLaunchStages...)
	for _, s := range clusterStartPlan("", nil) {
		if s.ID == protocol.StageQueued {
			s.State = protocol.StageStatePending
		}
		if s.ID == protocol.StageConnect {
			plan = append(plan, stage("bootstrap", "Initializing protected storage", protocol.StageStatePending, ""))
		}
		plan = append(plan, s)
		if s.ID == protocol.StageConnect {
			plan = append(plan, stage("negotiation", "Negotiating workspace and shell", protocol.StageStatePending, ""))
		}
		if s.ID == protocol.StageClone {
			plan = append(plan, stage("workspace_prepare", "Preparing runtime readers", protocol.StageStatePending, ""))
		}
	}
	return plan
}

func (j *JobManager) launchStage(parent context.Context, id, name, state, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	update := stage(name, "", state, reason)
	if j.launchHub != nil {
		advanceStart(ctx, j.launchHub, j.historyPool, id, true, update)
	} else {
		persistStartStages(ctx, nil, j.historyPool, id, protocol.StartStagePayload{Stages: []protocol.StartStage{update}})
	}
}

type clusterLaunchProgress struct {
	j                *JobManager
	session, current string
}

func (p *clusterLaunchProgress) next(ctx context.Context, name, reason string) {
	p.done(ctx)
	p.current = name
	p.j.launchStage(ctx, p.session, name, protocol.StageStateActive, reason)
}
func (p *clusterLaunchProgress) done(ctx context.Context) {
	if p.current != "" {
		p.j.launchStage(ctx, p.session, p.current, protocol.StageStateDone, "Stage completed")
		p.current = ""
	}
}
func (p *clusterLaunchProgress) finish(ctx context.Context, err *error) {
	if *err != nil && p.current != "" {
		detail := "Stage refused or its deadline expired; inspect server and cluster configuration"
		var submission *jobSubmissionError
		if errors.As(*err, &submission) && safeJobSubmissionReason(submission.reason) {
			detail = "Launch object creation refused: " + submission.reason
		}
		p.j.launchStage(ctx, p.session, p.current, protocol.StageStateFailed, detail)
	} else {
		p.done(ctx)
	}
}

// Log only known stage/state identifiers. Pod event text is untrusted and may
// contain private data; the full event remains in its authorized session.
func logLaunchStages(_ string, stages []protocol.StartStage) {
	for _, s := range stages {
		if !knownLaunchStage(s.ID) {
			continue
		}
		switch s.State {
		case protocol.StageStateActive, protocol.StageStateDone, protocol.StageStateFailed, protocol.StageStateWarning:
			log.Printf("cluster launch stage=%s outcome=%s reason=%s", s.ID, s.State, launchDiagnosticReason(s))
		}
	}
}
func knownLaunchStage(id string) bool {
	for _, s := range workspaceLaunchStages {
		if s.ID == id {
			return true
		}
	}
	switch id {
	case protocol.StageQueued, protocol.StageSchedule, protocol.StageImage, protocol.StageConnect, protocol.StageClone, protocol.StagePlugins, protocol.StageEngine, protocol.StageReady, "bootstrap", "negotiation", "workspace_prepare", "shell_open", "pod_terminated":
		return true
	}
	return false
}

// Safe receipt for offline acceptance results. Deliberately exclude stage text:
// normal events may describe private repository/configuration details.
type clusterLaunchDiagnostic struct {
	Stage      string    `json:"stage"`
	Outcome    string    `json:"outcome"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Reason     string    `json:"reason"`
}

func clusterLaunchSnapshot(ctx context.Context, pool *pgxpool.Pool, id string) ([]clusterLaunchDiagnostic, error) {
	events, err := db.ListAgentEventsTail(ctx, pool, id, protocol.StartStageKind, 500)
	if err != nil {
		return nil, err
	}
	var out []clusterLaunchDiagnostic
	indexes := map[string]int{}
	for i := len(events) - 1; i >= 0; i-- {
		var payload protocol.StartStagePayload
		if json.Unmarshal([]byte(events[i].Payload), &payload) != nil {
			continue
		}
		if payload.Plan {
			out = nil
			indexes = map[string]int{}
		}
		for _, s := range payload.Stages {
			if !knownLaunchStage(s.ID) {
				continue
			}
			switch s.State {
			case protocol.StageStatePending, protocol.StageStateActive, protocol.StageStateDone, protocol.StageStateFailed, protocol.StageStateWarning:
			default:
				continue
			}
			n, ok := indexes[s.ID]
			if !ok {
				n = len(out)
				indexes[s.ID] = n
				out = append(out, clusterLaunchDiagnostic{Stage: s.ID})
			}
			at := events[i].Ts
			if recorded, err := time.Parse(time.RFC3339Nano, s.At); err == nil {
				at = recorded.UTC()
			}
			d := &out[n]
			d.Outcome = s.State
			switch s.State {
			case protocol.StageStatePending:
				d.Reason = "not_started"
			case protocol.StageStateActive:
				if d.StartedAt.IsZero() {
					d.StartedAt = at
				}
				d.Reason = "in_progress"
			default:
				if d.StartedAt.IsZero() {
					d.StartedAt = at
				}
				d.FinishedAt = at
				d.Reason = "completed"
				if s.State == protocol.StageStateFailed {
					d.Reason = launchDiagnosticReason(s)
				}
				if s.State == protocol.StageStateWarning {
					d.Reason = "completed_with_warning"
				}
			}
			// Match the persisted progress contract: later active/done stages prove
			// earlier pending/active stages have completed, even if a poll missed them.
			if s.State == protocol.StageStateActive || s.State == protocol.StageStateDone {
				for previous := 0; previous < n; previous++ {
					before := &out[previous]
					if before.Outcome == protocol.StageStatePending || before.Outcome == protocol.StageStateActive {
						before.Outcome, before.Reason = protocol.StageStateDone, "completed"
						if before.StartedAt.IsZero() {
							before.StartedAt = at
						}
						before.FinishedAt = at
					}
				}
			}
		}
	}
	return out, nil
}

func clusterLaunchProblem(stages []clusterLaunchDiagnostic, now time.Time, limit time.Duration) string {
	for _, s := range stages {
		if s.Outcome == protocol.StageStateFailed {
			return "cluster launch failed at stage " + s.Stage
		}
	}
	for _, s := range stages {
		if s.Outcome == protocol.StageStateActive && !s.StartedAt.IsZero() && now.Sub(s.StartedAt) >= limit {
			return "cluster launch stalled at stage " + s.Stage
		}
	}
	return ""
}

func launchDiagnosticReason(s protocol.StartStage) string {
	if s.State != protocol.StageStateFailed {
		return "stage_transition"
	}
	if reason, ok := strings.CutPrefix(s.Detail, "Launch object creation refused: "); ok && safeJobSubmissionReason(reason) {
		return reason
	}
	if s.ID == "pod_terminated" {
		if strings.HasPrefix(s.Detail, "stopped: ran out of memory (") {
			return "pod_oom_killed"
		}
		return "pod_terminated"
	}
	if s.ID == "shell_open" {
		if reason, ok := strings.CutPrefix(s.Detail, "Owner shell refused: "); ok && ownershell.SafeRefusalReason(reason) {
			return reason
		}
	}
	if s.ID == protocol.StageQueued {
		if reason, ok := strings.CutPrefix(s.Detail, "Job submission refused: "); ok && safeJobSubmissionReason(reason) {
			return reason
		}
	}
	switch s.Detail {
	case "Pod inventory refused; launcher requires get/list on pods (no exec permission)":
		return "pod_inventory_refused"
	case "Server and pod workspace capabilities do not match":
		return "capabilities_mismatch"
	case "Protected storage bootstrap init container failed":
		return "init_failed"
	case "Bootstrap init container image or configuration unavailable":
		return "init_configuration_refused"
	case "Pod runtime startup refused; inspect its protected storage, capabilities and configuration":
		return "runtime_startup_refused"
	case "Launch stage exceeded the smoke progress deadline":
		return "stage_deadline_exceeded"
	}
	return "stage_refused"
}
