package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"reflect"
	"time"
)

const launchCreateAnnotation = "blerg.ai/create-attempt"
const launchCreateTimeout = 10 * time.Second

type launchCreateResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *launchCreateResponseBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

type launchCreateMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid,omitempty"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	OwnerReferences   []map[string]any  `json:"ownerReferences"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
}

// createLaunchObject retries rejected optimistic-concurrency creates, never
// blindly accepting AlreadyExists. Freeze the body and scheduling guard once:
// retries retain the same name, generation, credentials and ownership marker.
func (j *JobManager) createLaunchObject(parent context.Context, path string, body map[string]any) (response *http.Response, failure error) {
	ctx, cancel := context.WithTimeout(parent, launchCreateTimeout)
	cancelOnReturn := true
	defer func() {
		if cancelOnReturn {
			cancel()
		}
	}()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, jobSubmissionTransportError(err)
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil {
		return nil, &jobSubmissionError{reason: "TransportError"}
	}
	meta, ok := object["metadata"].(map[string]any)
	if !ok || meta["name"] == nil {
		return nil, &jobSubmissionError{reason: "TransportError"}
	}
	if ns, exists := meta["namespace"]; exists && ns != j.Namespace {
		return nil, &jobSubmissionError{reason: "TransportError"}
	}
	meta["namespace"] = j.Namespace
	annotations, _ := meta["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations[launchCreateAnnotation] = newUUID()
	meta["annotations"] = annotations
	if err := j.guardJobScheduling(http.MethodPost, path, object); err != nil {
		return nil, jobSubmissionTransportError(err)
	}
	raw, err = json.Marshal(object)
	if err != nil {
		return nil, jobSubmissionTransportError(err)
	}
	var desired struct {
		Kind     string
		Metadata launchCreateMetadata
	}
	if json.Unmarshal(raw, &desired) != nil || desired.Metadata.Name == "" {
		return nil, &jobSubmissionError{reason: "TransportError"}
	}
	switch desired.Kind {
	case "Job", "Secret", "PersistentVolumeClaim":
	default:
		return nil, &jobSubmissionError{reason: "TransportError"}
	}

	// Session Secrets have no Job owner yet. Retain invocation metadata even
	// when a lost POST response consumes the entire create/parent deadline.
	tracked := desired.Kind == "Secret" && isSessionLaunchSecret(desired.Metadata)
	ambiguous := false
	if tracked {
		if ctx.Err() != nil {
			return nil, jobSubmissionTransportError(ctx.Err())
		}
		if err := j.trackLaunchSecret(ctx, desired.Metadata); err != nil {
			return nil, err
		}
		defer func() {
			cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
			defer done()
			success := failure == nil && response != nil && (response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusOK)
			if success && parent.Err() != nil {
				_ = response.Body.Close()
				response = nil
				failure = jobSubmissionTransportError(parent.Err())
				ambiguous = true
				success = false
			}
			if success || !ambiguous {
				if err := j.forgetLaunchSecret(cleanup, desired.Metadata); err == nil {
					return
				} else if success {
					_ = response.Body.Close()
					response = nil
					failure = err
				}
			}
			if err := j.cleanupLaunchSecret(cleanup, desired.Metadata, false); err != nil {
				logLaunchFailure("secret_cleanup", err)
			}
		}()
	}
	objectPath := path + "/" + url.PathEscape(desired.Metadata.Name)
	last := &jobSubmissionError{reason: "Timeout"}
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return nil, last
		}
		resp, err := j.sendJSON(ctx, http.MethodPost, path, raw, "application/json")
		switch {
		case err != nil:
			ambiguous = true
			if ctx.Err() != nil && last.reason == "Conflict" {
				return nil, last
			}
			last = jobSubmissionTransportError(err)
			if tracked && ctx.Err() != nil {
				return nil, last
			}
			// A lost response is not proof of rejection. Read before any retry;
			// unavailable/foreign metadata cannot authorize adoption or cleanup.
			recovered, err := j.recoverLaunchCreate(ctx, objectPath, &desired.Metadata)
			if err != nil {
				return nil, err
			}
			if recovered != nil {
				return recovered, nil
			}
		case resp.StatusCode != http.StatusConflict:
			// The caller still needs the created UID (and may receive a streamed
			// body). Transfer the deadline's lifetime to its mandatory Close.
			resp.Body = &launchCreateResponseBody{ReadCloser: resp.Body, cancel: cancel}
			cancelOnReturn = false
			return resp, nil
		default:
			// Preserve the bounded body for the caller's safe reason projection.
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(payload))
			var status struct{ Reason string }
			_ = json.Unmarshal(payload, &status)
			if status.Reason != "Conflict" {
				if attempt > 0 && status.Reason == "AlreadyExists" {
					recovered, err := j.recoverLaunchCreate(ctx, objectPath, &desired.Metadata)
					if err != nil || recovered != nil {
						_ = resp.Body.Close()
						return recovered, err
					}
				}
				return resp, nil
			}
			_ = resp.Body.Close()
			last = &jobSubmissionError{reason: "Conflict"}
		}
		// Enum/constant fields only: never log response messages or object data.
		stage := "queued"
		switch desired.Kind {
		case "Secret":
			stage = "secrets"
		case "PersistentVolumeClaim":
			stage = "storage"
		}
		logLaunchFailure(stage, last)
		timer := time.NewTimer(launchCreateBackoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, last
		case <-timer.C:
		}
	}
}

func launchCreateBackoff(attempt int) time.Duration {
	delay := 50 * time.Millisecond
	for i := 0; i < attempt && delay < time.Second; i++ {
		delay *= 2
	}
	if delay > time.Second {
		delay = time.Second
	}
	return delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1)) //nolint:gosec // jitter is scheduling, not a credential
}

// Two reads pin a nonempty UID and reject deletion/replacement while recovering
// an ambiguous create. Labels alone never identify this invocation's object.
func (j *JobManager) recoverLaunchCreate(ctx context.Context, path string, want *launchCreateMetadata) (*http.Response, error) {
	var uid string
	for read := 0; read < 2; read++ {
		resp, err := j.doWithContentType(ctx, http.MethodGet, path, nil, "application/json")
		if err != nil {
			return nil, jobSubmissionTransportError(err)
		}
		if resp.StatusCode == http.StatusNotFound && read == 0 {
			_ = resp.Body.Close()
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			err := jobSubmissionRejection(resp)
			_ = resp.Body.Close()
			return nil, err
		}
		var found struct{ Metadata launchCreateMetadata }
		err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&found)
		_ = resp.Body.Close()
		m := found.Metadata
		if err != nil || m.Name != want.Name || m.Namespace != want.Namespace || m.UID == "" || m.DeletionTimestamp != nil || m.Annotations[launchCreateAnnotation] != want.Annotations[launchCreateAnnotation] || len(m.OwnerReferences) != len(want.OwnerReferences) || (len(want.OwnerReferences) > 0 && !reflect.DeepEqual(m.OwnerReferences, want.OwnerReferences)) {
			return nil, &jobSubmissionError{reason: "AlreadyExists"}
		}
		for k, v := range want.Labels {
			if m.Labels[k] != v {
				return nil, &jobSubmissionError{reason: "AlreadyExists"}
			}
		}
		if want.UID != "" && want.UID != m.UID {
			return nil, &jobSubmissionError{reason: "AlreadyExists"}
		}
		if read == 0 {
			want.UID = m.UID
			if isSessionLaunchSecret(*want) {
				if err := j.pinLaunchSecretUID(ctx, *want); err != nil {
					return nil, err
				}
			}
		}
		if read > 0 && uid != m.UID {
			return nil, &jobSubmissionError{reason: "AlreadyExists"}
		}
		uid = m.UID
	}
	// Only the pinned UID is needed by Job/Secret ownership adoption. Do not
	// forward a recovered Secret's data through the synthetic create response.
	payload, _ := json.Marshal(map[string]any{"metadata": map[string]string{"uid": uid}})
	return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header)}, nil
}
