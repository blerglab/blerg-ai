package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const podEndedLabel = "blerg.dev/session-ended"

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
	if pod.Status.Phase == "Failed" && pod.Status.Reason != "" {
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
	hint += "If this session has a retained workspace volume, its work, including commits from an interrupted turn, is restored when you resume this session. It may not yet be on wip/<session>. Files outside the retained workspace may be lost."
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
		return false
	}
	fatal := podTermination(pod)
	if fatal == nil {
		return false
	}
	changed, err := db.RecordClusterPodFailure(ctx, a.dbPool, row, fatal.Detail+". "+fatal.Hint)
	if err != nil {
		return true
	}
	if changed {
		persistStartStages(ctx, a.hub, a.dbPool, row.ID, protocol.StartStagePayload{Stages: []protocol.StartStage{*fatal}})
		broadcastSessionCreated(ctx, a.hub, a.dbPool, row.ID)
	}
	if err := jm.labelEndedPod(ctx, pod, row.ID); err != nil {
		// No raw API responses; RBAC/API failures are retried on the next pass.
		log.Printf("cluster reconcile: failed pod end label unavailable; retrying")
	}
	return true
}
