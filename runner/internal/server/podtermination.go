package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const podEndedLabel = "blerg.dev/session-ended"

func clusterPodInterrupted(row *db.SessionRow) bool {
	return row != nil && derefOrEmpty(row.Runtime) == runnerRuntimeCluster && row.Status == "disconnected" && strings.HasPrefix(derefOrEmpty(row.ErrorReason), "stopped:")
}

const sessionStoppedNotice = "This session stopped."

// Pod termination reasons have a stable prefix, including already persisted
// rows. Public conversation access does not grant access to pod diagnostics.
func podErrorReasonFor(row *db.SessionRow, viewer, reason string) string {
	if row != nil && derefOrEmpty(row.Runtime) == runnerRuntimeCluster && strings.HasPrefix(reason, "stopped:") && !ownsSession(viewer, row) {
		return sessionStoppedNotice
	}
	return reason
}

var podMemoryQuantity = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[KMGTPE]i?|[kMGTPE]|[eE][+-]?[0-9]+)?$`)

// Only Kubernetes reason classes, never free-form messages or arbitrary tokens.
func podEndReason(reason string) string {
	switch reason {
	case "OOMKilled", "Error", "ContainerCannotRun", "DeadlineExceeded", "Evicted", "NodeLost", "Shutdown", "Preempted", "Completed":
		return reason
	default:
		return "PodFailed"
	}
}

func podTermination(pod *k8sPod) *protocol.StartStage {
	if pod == nil {
		return nil
	}
	var runner *k8sContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == "runner" {
			runner = &pod.Status.ContainerStatuses[i]
			break
		}
	}
	reason := ""
	detail := ""
	if runner != nil && runner.State.Terminated != nil {
		term := runner.State.Terminated
		if term.ExitCode == 0 && pod.Status.Phase != "Failed" {
			return nil
		}
		reason = podEndReason(term.Reason)
		detail = fmt.Sprintf("stopped: pod exited with code %d (%s)", term.ExitCode, reason)
	}
	if pod.Status.Phase == "Failed" && (detail == "" || (pod.Status.Reason != "" && pod.Status.Reason != "Error" && reason != "OOMKilled")) {
		reason = podEndReason(pod.Status.Reason)
		detail = "stopped: pod terminated (" + reason + ")"
	}
	if detail == "" {
		return nil
	}
	hint := ""
	if reason == "OOMKilled" {
		detail = "stopped: ran out of memory (OOMKilled)"
		for _, c := range pod.Spec.Containers {
			limit := c.Resources.Limits["memory"]
			if c.Name == "runner" && len(limit) <= 32 && podMemoryQuantity.MatchString(limit) {
				if strings.HasSuffix(limit, "Gi") {
					limit = strings.TrimSuffix(limit, "Gi") + " GiB"
				}
				if strings.HasSuffix(limit, "Mi") {
					limit = strings.TrimSuffix(limit, "Mi") + " MiB"
				}
				detail = "stopped: ran out of memory (limit " + limit + "; OOMKilled)"
			}
		}
		hint = "Ask an administrator to raise BLERG_RUNNER_POD_MEM_LIMIT in install/k8s/.env and redeploy; the new limit applies on resume. "
	}
	hint += "If this session has a retained workspace volume, its work, including commits from an interrupted turn, is restored when you resume this session. It may not yet be on its recorded session branch. Files outside the retained workspace may be lost."
	return &protocol.StartStage{ID: "pod_terminated", Label: "Agent pod stopped", State: protocol.StageStateFailed, Detail: detail, Hint: hint}
}

// Label only the observed failed pod, with both Kubernetes identity preconditions.
// The Job TTL still handles deletion; this never removes a retained volume.
func (j *JobManager) labelEndedPod(ctx context.Context, pod *k8sPod, sessionID string) error {
	m := pod.Metadata
	if pod.Status.Phase != "Failed" || m.Name == "" || m.UID == "" || m.ResourceVersion == "" || m.Labels["app"] != "blerg-runner-agent" || m.Labels["session"] != sessionID || m.Labels[podEndedLabel] == "true" {
		return nil
	}
	patch := map[string]any{"metadata": map[string]any{"uid": m.UID, "resourceVersion": m.ResourceVersion, "labels": map[string]string{podEndedLabel: "true"}}}
	resp, err := j.doWithContentType(ctx, http.MethodPatch, "/api/v1/namespaces/"+j.Namespace+"/pods/"+url.PathEscape(m.Name), patch, "application/merge-patch+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pod end label: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *API) explainStoppedPod(ctx context.Context, jm *JobManager, row db.ReconcileSessionRow) bool {
	// A resume already being watched must not acquire the previous pod's failure.
	if a.hub.starts.isOpen(row.ID) || lastAttemptOpen(ctx, a.dbPool, row.ID) {
		// The old Job must not fall through to terminal reconciliation while
		// a replacement is booting. Its watcher owns the new attempt.
		return true
	}
	pod, err := jm.sessionPod(ctx, row.ID)
	if err != nil {
		failurelog.Default.Event("runner:pod:observe", slog.LevelError, "component", "runner", "operation", "pod_termination", "stage", "observe", "session_id", row.ID, "cause", failurelog.Cause(err))
		return false
	}
	fatal := podTermination(pod)
	if fatal == nil {
		return false
	}
	changed, err := db.RecordClusterPodFailure(ctx, a.dbPool, row, fatal.Detail+". "+fatal.Hint)
	if err != nil {
		failurelog.Default.Event("runner:pod:record", slog.LevelError, "component", "runner", "operation", "pod_termination", "stage", "record", "session_id", row.ID, "cause", failurelog.Cause(err))
		return true
	}
	if changed {
		logPodTermination(row.ID, pod)
		persistStartStages(ctx, a.hub, a.dbPool, row.ID, protocol.StartStagePayload{Stages: []protocol.StartStage{*fatal}})
		broadcastSessionCreated(ctx, a.hub, a.dbPool, row.ID)
	}
	if err := jm.labelEndedPod(ctx, pod, row.ID); err != nil {
		// No raw API responses; RBAC/API failures are retried on the next pass.
		failurelog.Default.Event("runner:pod:label", slog.LevelError, "component", "runner", "operation", "pod_termination", "stage", "label", "session_id", row.ID, "cause", failurelog.Cause(err))
	}
	return true
}

// Kubernetes messages can contain node names and workload details. Classify only
// known resource causes; never copy the message into diagnostics or worker input.
func logPodTermination(sessionID string, pod *k8sPod) {
	reason := podEndReason(pod.Status.Reason)
	for _, c := range pod.Status.ContainerStatuses {
		if c.Name == "runner" && c.State.Terminated != nil && (pod.Status.Reason == "" || pod.Status.Reason == "Error" || c.State.Terminated.Reason == "OOMKilled") {
			reason = podEndReason(c.State.Terminated.Reason)
		}
	}
	pressure := "unknown"
	message := strings.ToLower(pod.Status.Message)
	switch reason {
	case "OOMKilled":
		pressure = "memory"
	case "Evicted":
		switch {
		case strings.Contains(message, "memory"):
			pressure = "memory"
		case strings.Contains(message, "ephemeral-storage"), strings.Contains(message, "diskpressure"):
			pressure = "ephemeral_storage"
		case strings.Contains(message, "pidpressure"):
			pressure = "pids"
		}
	}
	limits := map[string]string{"memory": "unknown", "cpu": "unknown", "ephemeral-storage": "unknown"}
	for _, c := range pod.Spec.Containers {
		if c.Name != "runner" {
			continue
		}
		for resource := range limits {
			value := c.Resources.Limits[resource]
			if len(value) <= 32 && podResourceQuantity.MatchString(value) {
				limits[resource] = value
			}
		}
	}
	failurelog.Default.Event("runner:pod:termination:"+sessionID, slog.LevelError,
		"component", "runner", "operation", "pod_termination", "session_id", sessionID,
		"cause", reason, "resource_pressure", pressure, "memory_limit", limits["memory"],
		"cpu_limit", limits["cpu"], "ephemeral_storage_limit", limits["ephemeral-storage"])
}

var podResourceQuantity = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[KMGTPE]i?|[mkMGTPE]|[eE][+-]?[0-9]+)?$`)
