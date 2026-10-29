package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Register before POST, while failure can still prevent allocation. Ambiguous
// failure handling must not depend on a new database write. The lease covers
// the create deadline and immediate cleanup; a successful create must retire
// the intent before its caller may submit a consuming Job.
func (j *JobManager) trackLaunchSecret(ctx context.Context, want launchCreateMetadata) error {
	if j.historyPool == nil { // Standalone HTTP fixtures have no persistence.
		return nil
	}
	raw, _ := json.Marshal(want)
	err := db.PutLaunchSecretCleanup(ctx, j.historyPool, db.LaunchSecretCleanup{Attempt: want.Annotations[launchCreateAnnotation], Namespace: j.Namespace, Metadata: raw, ReadyAfter: time.Now().Add(launchCreateTimeout + time.Minute)})
	if err != nil {
		return &jobSubmissionError{reason: "InternalError"}
	}
	return nil
}

func (j *JobManager) forgetLaunchSecret(ctx context.Context, want launchCreateMetadata) error {
	if j.historyPool == nil {
		return nil
	}
	if err := db.DeleteLaunchSecretCleanup(ctx, j.historyPool, want.Annotations[launchCreateAnnotation]); err != nil {
		return &jobSubmissionError{reason: "InternalError"}
	}
	return nil
}

// No name-only deletion: two authoritative reads must pin the invocation's
// metadata and stable UID, and DELETE carries that UID as a precondition.
func (j *JobManager) cleanupLaunchSecret(ctx context.Context, want launchCreateMetadata, absenceFinal bool) error {
	path := "/api/v1/namespaces/" + url.PathEscape(want.Namespace) + "/secrets/" + url.PathEscape(want.Name)
	recovered, err := j.recoverLaunchCreate(ctx, path, &want)
	if err != nil {
		return err
	}
	if recovered == nil {
		// A timed-out POST may still finish at the API server. Keep the durable
		// intent through the grace period before accepting absence as final.
		if !absenceFinal {
			return &jobSubmissionError{reason: "Timeout"}
		}
		return j.forgetLaunchSecret(ctx, want)
	}
	var found struct{ Metadata launchCreateMetadata }
	err = json.NewDecoder(recovered.Body).Decode(&found)
	_ = recovered.Body.Close()
	if err != nil || found.Metadata.UID == "" {
		return &jobSubmissionError{reason: "UnknownAPIError"}
	}
	resp, err := j.doWithContentType(ctx, http.MethodDelete, path, map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": found.Metadata.UID}}, "application/json")
	if err != nil {
		return jobSubmissionTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusAccepted {
		return jobSubmissionRejection(resp)
	}
	// A 202 is only acceptance. Retain the intent until a later read proves
	// absence; this also handles a lost DELETE response without widening authority.
	if resp.StatusCode == http.StatusAccepted {
		return &jobSubmissionError{reason: "Timeout"}
	}
	// A successful DELETE may only set deletionTimestamp when finalizers exist.
	// Keep the obligation until an authoritative read proves physical absence.
	if resp.StatusCode != http.StatusNotFound {
		check, err := j.doWithContentType(ctx, http.MethodGet, path, nil, "application/json")
		if err != nil {
			return jobSubmissionTransportError(err)
		}
		_ = check.Body.Close()
		if check.StatusCode != http.StatusNotFound {
			return &jobSubmissionError{reason: "Timeout"}
		}
	}
	return j.forgetLaunchSecret(ctx, want)
}

func (j *JobManager) reconcileLaunchSecrets(ctx context.Context) {
	if j.historyPool == nil {
		return
	}
	intents, err := db.ListLaunchSecretCleanup(ctx, j.historyPool, j.Namespace)
	if err != nil {
		logLaunchFailure("secret_cleanup", &jobSubmissionError{reason: "InternalError"})
		return
	}
	for _, intent := range intents {
		if ctx.Err() != nil {
			return
		}
		var want launchCreateMetadata
		if json.Unmarshal(intent.Metadata, &want) != nil || want.Namespace != j.Namespace || want.Annotations[launchCreateAnnotation] != intent.Attempt || want.Name == "" {
			logLaunchFailure("secret_cleanup", &jobSubmissionError{reason: "Invalid"})
			continue
		}
		cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := j.cleanupLaunchSecret(cleanup, want, true)
		cancel()
		if err != nil {
			if retryErr := db.RetryLaunchSecretCleanup(ctx, j.historyPool, intent.Attempt); retryErr != nil {
				logLaunchFailure("secret_cleanup", &jobSubmissionError{reason: "InternalError"})
			}
			logLaunchFailure("secret_cleanup", err)
		}
	}
}

func isSessionLaunchSecret(want launchCreateMetadata) bool {
	return want.Labels["app"] == "blerg-runner-agent" && want.Labels["session"] != "" && want.Name == sessionSecretName(want.Labels["session"]) && len(want.OwnerReferences) == 0
}

func (j *JobManager) pinLaunchSecretUID(ctx context.Context, want launchCreateMetadata) error {
	if j.historyPool == nil {
		return nil
	}
	if err := db.PinLaunchSecretCleanupUID(ctx, j.historyPool, want.Annotations[launchCreateAnnotation], want.UID); err != nil {
		return &jobSubmissionError{reason: "InternalError"}
	}
	return nil
}
