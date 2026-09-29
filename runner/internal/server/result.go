package server

// The structured outcome of a runner session (agent contract v1, spec §3).
//
// GET /api/runner/sessions/{id}/result answers "what happened?" in one object,
// so a broker does not have to replay the whole transcript to find out. The
// builder is a plain function rather than handler-only logic because the SSE
// stream's terminal `end` event, the completion webhook and the MCP
// get_result tool all owe the caller the SAME body.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// assistantTextKind is the agent-loop event kind carrying assistant prose
// (internal/agent/events.go: AssistantTextPayload). Only the consolidated
// done=true event is persisted as the final text of a turn — streaming deltas
// are transient — so the result reports the newest done=true one.
const assistantTextKind = "assistant_text"

// resultAssistantScanLimit bounds how many of a session's newest events the
// builder reads looking for the last assistant message. A long session can
// have thousands of events; the last assistant turn is essentially always
// within a short tail, and an unbounded read here would be a cheap way to
// make the runner do expensive work.
const resultAssistantScanLimit = 400

// sessionResult is the documented Result schema (openapi.json). Every field is
// emitted, including the empty ones: a broker's schema check should not have to
// treat "absent" and "empty" as the same thing. There is deliberately no
// callback_secret field — the secret signs webhooks and never leaves the server.
type sessionResult struct {
	SessionID            string `json:"session_id"`
	Lifecycle            string `json:"lifecycle"`
	Terminal             bool   `json:"terminal"`
	Runtime              string `json:"runtime"`
	Repo                 string `json:"repo"`
	GitURL               string `json:"git_url"`
	Branch               string `json:"branch"`
	LastAssistantMessage string `json:"last_assistant_message"`
	ErrorReason          string `json:"error_reason"`
	// EndReason is why the session ended (a fixed code, "" while it has not
	// or when nothing was recorded); EndedBy the kind of actor that ended it,
	// null when none did. The actor's account is never included here.
	EndReason string            `json:"end_reason"`
	EndedBy   *protocol.EndedBy `json:"ended_by"`
	AutoStop  bool              `json:"auto_stop"`
	StartedAt string            `json:"started_at"`
	EndedAt   *string           `json:"ended_at"`
	Artifacts []map[string]any  `json:"artifacts"`
}

// runnerLifecycle projects a sessions.status value onto the five documented
// lifecycle values (spec §3). The session state machine (sessions.go) is finer
// grained than the contract — waiting/idle are both "running" as far as a
// broker is concerned, and stopped is just how a session ends — so the raw
// column must never be handed out directly.
//
// jobFinished is the cluster fact that the session's Job has exited: combined
// with a session that never got past "starting", that is a startup failure
// (bad repo, image pull, crash before the runner connected), not a session
// still booting, so it reports terminal rather than "starting" forever.
//
// An unrecognised status maps to "running": a value this build does not know
// is not evidence the work is over, and reporting a false terminal state would
// have a broker close out a session that is still going.
func runnerLifecycle(status string, jobFinished bool) string {
	if jobFinished && status == "starting" {
		return "error"
	}
	switch status {
	case "starting":
		return "starting"
	case "running", "waiting", "idle":
		return "running"
	case "disconnected":
		return "disconnected"
	case "ended", "stopped":
		return "ended"
	case "error":
		return "error"
	default:
		return "running"
	}
}

// runnerLifecycleTerminal reports whether a lifecycle value means the work is
// over — the `terminal` field, and the trigger for the SSE end event and the
// completion webhook.
func runnerLifecycleTerminal(lifecycle string) bool {
	return lifecycle == "ended" || lifecycle == "error"
}

// runnerRuntimeAxis reports the hosting layer for a session: where it lives
// right now, which is not the same question as what state it is in.
func (a *API) runnerRuntimeAxis(sessionID string) string {
	jm := a.hub.JobManager()
	switch {
	case a.hub.FindDaemonForSession(sessionID) != nil:
		return "daemon"
	case jm != nil && jm.jobFinished(sessionID):
		return "job_finished"
	case jm != nil:
		return "cluster"
	default:
		return "none"
	}
}

// buildSessionResult assembles the result body for one session. It returns an
// error when the session does not exist (the handler turns that into a 404) or
// when the lookup fails — never a zero-valued result, which a caller could
// mistake for a real one.
func (a *API) buildSessionResult(ctx context.Context, sessionID string) (sessionResult, error) {
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil {
		return sessionResult{}, fmt.Errorf("session lookup: %w", err)
	}
	if row == nil {
		return sessionResult{}, fmt.Errorf("session %s not found", sessionID)
	}
	runtime := a.runnerRuntimeAxis(sessionID) //nolint:contextcheck // cluster calls are bounded by the JobManager client timeout and deliberately not tied to the caller: a Job or Secret half-made because the caller went away would be orphaned
	lifecycle := runnerLifecycle(row.Status, runtime == "job_finished")
	res := sessionResult{
		SessionID: row.ID,
		Lifecycle: lifecycle,
		Terminal:  runnerLifecycleTerminal(lifecycle),
		Runtime:   runtime,
		Repo:      row.Repo,
		GitURL:    derefOrEmpty(row.GitURL),
		// The branch a session's work lands on is named from its id by the
		// session runtime; it is a convention, not a column, so it is derived
		// the same way here.
		Branch:               "wip/" + row.ID,
		LastAssistantMessage: a.lastAssistantMessage(ctx, sessionID),
		ErrorReason:          derefOrEmpty(row.ErrorReason),
		AutoStop:             row.AutoStop,
		StartedAt:            row.StartedAt.UTC().Format(time.RFC3339),
		Artifacts:            []map[string]any{},
	}
	if row.EndedAt != nil {
		ended := row.EndedAt.UTC().Format(time.RFC3339)
		res.EndedAt = &ended
	}
	res.EndReason, res.EndedBy = endAttribution(row, "")
	return res, nil
}

// lastAssistantMessage returns the text of the newest completed assistant
// event, or "" when the session has not produced one yet. A failure to read
// events is logged and reported as "": the rest of the result (including the
// error reason a caller most needs when something went wrong) is still worth
// returning.
func (a *API) lastAssistantMessage(ctx context.Context, sessionID string) string {
	rows, err := db.ListAgentEventsTail(ctx, a.dbPool, sessionID, assistantTextKind, resultAssistantScanLimit)
	if err != nil {
		log.Printf("runner result %s: assistant event query: %v", sessionID, err)
		return ""
	}
	for _, ev := range rows {
		var payload struct {
			Text string `json:"text"`
			Done bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			continue
		}
		// Streaming deltas (done=false) are partial text; only a consolidated
		// event is the assistant's actual message.
		if payload.Done && payload.Text != "" {
			return payload.Text
		}
	}
	return ""
}

// SessionResult is sessionResult under a name other packages can write down
// (the MCP get_result tool returns exactly this body).
type SessionResult = sessionResult

// Result is the body of GET /api/runner/sessions/{id}/result, shared with the
// MCP get_result tool. A session that does not exist — or that this principal
// may not see — is the same 404 either way.
func (a *API) Result(ctx context.Context, principal RunnerPrincipal, sessionID string) (SessionResult, *APIError) {
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		return SessionResult{}, apiErr
	}
	res, err := a.buildSessionResult(ctx, sessionID)
	if err != nil {
		return SessionResult{}, apiErrorf(http.StatusNotFound, "session not found")
	}
	return res, nil
}

// HandleRunnerResult serves GET /api/runner/sessions/{id}/result.
func (a *API) HandleRunnerResult(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	res, apiErr := a.Result(r.Context(), principal, r.PathValue("id"))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
