package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/nativehistory"
)

var errNativeHistory = errors.New("native conversation history is unavailable or does not match this session; no replacement was started")
var historyID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type NativeHistoryConfig struct {
	Enabled            bool
	StorageClass, Size string
	MaxClaims          int
	Retention          time.Duration
}

type historyClaim struct {
	Metadata struct {
		Name    string            `json:"name"`
		UID     string            `json:"uid"`
		Labels  map[string]string `json:"labels"`
		Owners  []json.RawMessage `json:"ownerReferences"`
		Deleted *time.Time        `json:"deletionTimestamp"`
	} `json:"metadata"`
}

func historyClaimName(id string) string { return "blerg-native-" + id }
func historyLabels(spec SessionJobSpec) map[string]string {
	return map[string]string{"app": "blerg-native-history", "session": spec.SessionID, "account": spec.SpawningAccountID, "engine": models.NormalizeEngine(spec.Engine)}
}

// withNativeHistory owns the entire storage/write fence, including old Pod
// termination and Job creation. RWO alone permits two writers on one node.
func (j *JobManager) withNativeHistory(spec SessionJobSpec) error {
	if j.historyPool == nil {
		if j.NativeHistory.Enabled && spec.SpawningAccountID != "" {
			return errNativeHistory
		}
		return j.createSessionJob(spec)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, supported := nativehistory.ForEngine(spec.Engine)
	// PostgreSQL accepts other UUID spellings. Requiring the canonical server
	// spelling prevents aliases from skipping expected-history lookup or taking
	// a different distributed writer lock, including ownerless legacy starts.
	if !historyID.MatchString(spec.SessionID) {
		return errNativeHistory
	}
	return db.WithSessionNativeHistoryLock(ctx, j.historyPool, spec.SessionID, func() error {
		expected, err := db.GetSessionNativeHistory(ctx, j.historyPool, spec.SessionID)
		if err != nil {
			return errNativeHistory
		}
		if expected == nil && (!j.NativeHistory.Enabled || !supported || spec.SpawningAccountID == "" || spec.Resume) {
			return j.createSessionJobContext(ctx, spec)
		}
		if expected != nil && (expected.DetachedAt != nil || expected.ReclaimedAt != nil) {
			return errNativeHistory
		}
		if !j.NativeHistory.Enabled || !supported || !historyID.MatchString(spec.SpawningAccountID) {
			return errNativeHistory
		}
		if !j.nativeHistorySessionCurrent(ctx, spec) {
			return errNativeHistory
		}
		if err := verifyCorePrincipal(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof()); err != nil {
			return err
		}
		bound, err := j.prepareNativeHistory(ctx, spec)
		if err != nil {
			return err
		}
		if err = j.fenceNativeHistoryWriter(ctx, spec); err != nil {
			return err
		}
		if err = verifyCorePrincipal(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, spec.SpawningAccountID, spec.proof()); err != nil {
			return err
		}
		if !j.nativeHistorySessionCurrent(ctx, spec) {
			return errNativeHistory
		}
		spec.nativeHistory = bound
		return j.createSessionJobContext(ctx, spec)
	})
}

func (j *JobManager) nativeHistorySessionCurrent(ctx context.Context, spec SessionJobSpec) bool {
	row, err := db.GetSession(ctx, j.historyPool, spec.SessionID)
	return err == nil && row != nil && row.EndedAt == nil && row.Status != "ended" && row.Status != "stopped" && row.Status != "error" && row.SpawningAccountID != nil && *row.SpawningAccountID == spec.SpawningAccountID && row.Runtime != nil && *row.Runtime == "cluster" && models.NormalizeEngine(derefOrEmpty(row.Engine)) == models.NormalizeEngine(spec.Engine)
}

func (j *JobManager) historyClaim(ctx context.Context, name string) (*historyClaim, error) {
	resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/persistentvolumeclaims/"+url.PathEscape(name), nil, "application/json")
	if err != nil {
		return nil, errNativeHistory
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	var claim historyClaim
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&claim) != nil {
		return nil, errNativeHistory
	}
	return &claim, nil
}

func (j *JobManager) prepareNativeHistory(ctx context.Context, spec SessionJobSpec) (*db.SessionNativeHistory, error) {
	bound, err := db.GetSessionNativeHistory(ctx, j.historyPool, spec.SessionID)
	if err != nil {
		return nil, errNativeHistory
	}
	engine := models.NormalizeEngine(spec.Engine)
	if bound != nil && (bound.DetachedAt != nil || bound.ReclaimedAt != nil || bound.AccountID != spec.SpawningAccountID || bound.Engine != engine || bound.ClaimName != historyClaimName(spec.SessionID)) {
		return nil, errNativeHistory
	}
	claim, err := j.historyClaim(ctx, historyClaimName(spec.SessionID))
	if err != nil {
		return nil, err
	}
	if bound != nil && (claim == nil || claim.Metadata.UID != bound.ClaimUID) {
		return nil, errNativeHistory
	}
	if claim == nil {
		if spec.Resume {
			return nil, errNativeHistory
		}
		err = db.WithSessionNativeHistoryLock(ctx, j.historyPool, "claim-capacity", func() error {
			if err := j.checkNativeHistoryCapacity(ctx); err != nil {
				return err
			}
			capacity := j.NativeHistory.Size
			if capacity == "" {
				capacity = "1Gi"
			}
			storage := map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": capacity}}}
			if j.NativeHistory.StorageClass != "" {
				storage["storageClassName"] = j.NativeHistory.StorageClass
			}
			body := map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": historyClaimName(spec.SessionID), "namespace": j.Namespace, "labels": historyLabels(spec)}, "spec": storage}
			resp, e := j.createLaunchObject(ctx, "/api/v1/namespaces/"+j.Namespace+"/persistentvolumeclaims", body)
			if e != nil {
				return launchCreateFailure(e, errNativeHistory)
			}
			if resp.StatusCode != http.StatusCreated {
				failure := launchCreateFailure(jobSubmissionRejection(resp), errNativeHistory)
				_ = resp.Body.Close()
				return failure
			}
			_ = resp.Body.Close()
			return nil
		})
		if err != nil {
			return nil, err
		}
		claim, err = j.historyClaim(ctx, historyClaimName(spec.SessionID))
		if err != nil || claim == nil {
			return nil, errNativeHistory
		}
	}
	if claim.Metadata.Name != historyClaimName(spec.SessionID) || claim.Metadata.UID == "" || claim.Metadata.Deleted != nil || len(claim.Metadata.Owners) > 0 {
		return nil, errNativeHistory
	}
	for key, value := range historyLabels(spec) {
		if claim.Metadata.Labels[key] != value {
			return nil, errNativeHistory
		}
	}
	if bound == nil {
		bound = &db.SessionNativeHistory{SessionID: spec.SessionID, AccountID: spec.SpawningAccountID, Engine: engine, ClaimName: claim.Metadata.Name, ClaimUID: claim.Metadata.UID}
		if err = db.BindSessionNativeHistory(ctx, j.historyPool, *bound); err != nil {
			return nil, errNativeHistory
		}
	}
	if err = db.TouchSessionNativeHistory(ctx, j.historyPool, spec.SessionID); err != nil {
		return nil, errNativeHistory
	}
	return bound, nil
}

func (j *JobManager) checkNativeHistoryCapacity(ctx context.Context) error {
	limit := j.NativeHistory.MaxClaims
	if limit <= 0 {
		limit = 128
	}
	path := "/api/v1/namespaces/" + j.Namespace + "/persistentvolumeclaims?limit=" + fmt.Sprint(limit) + "&labelSelector=" + url.QueryEscape("app=blerg-native-history")
	resp, err := j.doWithContentType(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return errNativeHistory
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Items    []json.RawMessage `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list) != nil || list.Items == nil || list.Metadata.Continue != "" || len(list.Items) >= limit {
		return errors.New("native history capacity is unavailable; end old sessions or increase the history storage limit")
	}
	return nil
}

// fenceNativeHistoryWriter never starts a new writer until both the owned Job
// and all of its session Pods have disappeared. Timeout means retry, not force.
func (j *JobManager) fenceNativeHistoryWriter(parent context.Context, spec SessionJobSpec) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	path := "/apis/batch/v1/namespaces/" + j.Namespace + "/jobs/" + j.jobName(spec.SessionID)
	resp, err := j.doWithContentType(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return errNativeHistory
	}
	var old historyClaim
	status := resp.StatusCode
	if status == http.StatusOK {
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&old)
	}
	_ = resp.Body.Close()
	if err != nil || (status != http.StatusOK && status != http.StatusNotFound) {
		return errNativeHistory
	}
	if status == http.StatusOK {
		if !spec.Resume {
			return errNativeHistory
		} // A duplicate fresh spawn must not replace a live writer.
		if old.Metadata.UID == "" || old.Metadata.Labels["session"] != spec.SessionID || old.Metadata.Labels["app"] != "blerg-runner-agent" {
			return errNativeHistory
		}
		body := map[string]any{"propagationPolicy": "Foreground", "preconditions": map[string]string{"uid": old.Metadata.UID}}
		resp, err = j.doWithContentType(ctx, http.MethodDelete, path, body, "application/json")
		if err != nil {
			return errNativeHistory
		}
		status = resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNotFound {
			return errNativeHistory
		}
	}
	for {
		resp, err = j.doWithContentType(ctx, http.MethodGet, path, nil, "application/json")
		if err != nil {
			return errNativeHistory
		}
		status = resp.StatusCode
		_ = resp.Body.Close()
		if status != http.StatusOK && status != http.StatusNotFound {
			return errNativeHistory
		}
		pods, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/pods?labelSelector="+url.QueryEscape("app=blerg-runner-agent,session="+spec.SessionID), nil, "application/json")
		if err != nil {
			return errNativeHistory
		}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(pods.Body, 4<<20)).Decode(&list)
		podStatus := pods.StatusCode
		_ = pods.Body.Close()
		if podStatus != http.StatusOK || decodeErr != nil || list.Items == nil {
			return errNativeHistory
		}
		if status == http.StatusNotFound && len(list.Items) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errNativeHistory
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// sessionHomeDir is the agent's HOME in the session image, where it is
// agent-owned (runner/Dockerfile.devcontainer).
const sessionHomeDir = "/workspace"

const engineHomeVolume = "engine-home"

func nativeHistoryPod(spec SessionJobSpec, pod map[string]any) error {
	if spec.nativeHistory == nil {
		return nil
	}
	layout, _ := nativehistory.ForEngine(spec.Engine)
	return nativeHistoryPodLayout(spec, pod, layout)
}

func nativeHistoryPodLayout(spec SessionJobSpec, pod map[string]any, layout nativehistory.Layout) error {
	mounts := []map[string]any{{"name": "native-history", "mountPath": nativehistory.IdentityPath, "subPath": "identity"}}
	subdirs := []string{"identity"}
	// The engine's home (~/.codex, ~/.claude) would otherwise be created as
	// root by the container runtime, as the parent of the nested history
	// mountpoints, and the engine could not write its login there. It is an
	// emptyDir subdirectory the init container creates as the agent (0700):
	// pod-ephemeral, so auth.json and config never reach the history volume.
	var homeDir string
	if layout.Home != "" {
		homeDir = strings.TrimPrefix(path.Base(layout.Home), ".")
		if path.Dir(layout.Home) != sessionHomeDir || homeDir == "" || layout.Home != path.Clean(layout.Home) {
			return errNativeHistory
		}
		mounts = append(mounts, map[string]any{"name": engineHomeVolume, "mountPath": layout.Home, "subPath": homeDir})
	}
	for _, mount := range layout.Mounts {
		if strings.HasPrefix(mount.Path, sessionHomeDir+"/") && (layout.Home == "" || !strings.HasPrefix(mount.Path, layout.Home+"/")) {
			return errNativeHistory
		}
		mounts = append(mounts, map[string]any{"name": "native-history", "mountPath": mount.Path, "subPath": mount.SubPath})
		subdirs = append(subdirs, mount.SubPath)
	}
	containers, ok := pod["containers"].([]map[string]any)
	if !ok || len(containers) == 0 {
		return errNativeHistory
	}
	if existing, ok := containers[0]["volumeMounts"].([]map[string]any); ok {
		mounts = append(existing, mounts...)
	}
	containers[0]["volumeMounts"] = mounts
	volumes, _ := pod["volumes"].([]map[string]any)
	volumes = append(volumes, map[string]any{"name": "native-history", "persistentVolumeClaim": map[string]string{"claimName": spec.nativeHistory.ClaimName}})
	prepare := "umask 077; mkdir -p -- " + strings.Join(subdirs, " ")
	initMounts := []map[string]any{{"name": "native-history", "mountPath": "/native-history"}}
	if homeDir != "" {
		volumes = append(volumes, map[string]any{"name": engineHomeVolume, "emptyDir": map[string]any{}})
		prepare += " && mkdir -p -- " + path.Join("/engine-home", homeDir)
		initMounts = append(initMounts, map[string]any{"name": engineHomeVolume, "mountPath": "/engine-home"})
	}
	pod["volumes"] = volumes
	security, _ := pod["securityContext"].(map[string]any)
	if security == nil {
		security = map[string]any{}
	}
	security["fsGroup"], security["fsGroupChangePolicy"] = sessionAgentUID, "OnRootMismatch"
	pod["securityContext"] = security
	inits, _ := pod["initContainers"].([]map[string]any)
	pod["initContainers"] = append(inits, map[string]any{"name": "prepare-native-history", "image": containers[0]["image"], "securityContext": sessionContainerSecurityContext(), "command": []string{"/bin/sh", "-c", prepare}, "workingDir": "/native-history", "volumeMounts": initMounts, "resources": map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "16Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "64Mi"}}})
	return nil
}

// reclaimNativeHistory runs only after a true ending and the configured
// retention period. The immutable binding remains after deletion, so an old
// resume request cannot mistake an empty replacement claim for retained data.
func (j *JobManager) reclaimNativeHistory(parent context.Context) error {
	if !j.NativeHistory.Enabled || j.historyPool == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	retention := j.NativeHistory.Retention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	cutoff := time.Now().Add(-retention)
	ids, err := db.ListExpiredNativeHistory(ctx, j.historyPool, cutoff)
	if err != nil {
		return errNativeHistory
	}
	failed := false
	for _, id := range ids {
		err = db.WithSessionNativeHistoryLock(ctx, j.historyPool, id, func() error {
			return db.ReclaimExpiredNativeHistory(ctx, j.historyPool, id, cutoff, func(h db.SessionNativeHistory) error {
				spec := SessionJobSpec{SessionID: h.SessionID, SpawningAccountID: h.AccountID, Engine: h.Engine, Resume: true}
				if !historyID.MatchString(h.SessionID) || h.ClaimName != historyClaimName(h.SessionID) {
					return errNativeHistory
				}
				claim, e := j.historyClaim(ctx, h.ClaimName)
				if e != nil {
					return e
				}
				if claim == nil {
					return nil
				}
				if claim.Metadata.UID != h.ClaimUID || len(claim.Metadata.Owners) > 0 {
					return errNativeHistory
				}
				for key, value := range historyLabels(spec) {
					if claim.Metadata.Labels[key] != value {
						return errNativeHistory
					}
				}
				if e = j.fenceNativeHistoryWriter(ctx, spec); e != nil {
					return e
				}
				body := map[string]any{"preconditions": map[string]string{"uid": h.ClaimUID}}
				resp, e := j.doWithContentType(ctx, http.MethodDelete, "/api/v1/namespaces/"+j.Namespace+"/persistentvolumeclaims/"+url.PathEscape(h.ClaimName), body, "application/json")
				if e != nil {
					return errNativeHistory
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNotFound {
					return errNativeHistory
				}
				return nil
			})
		})
		// One claim that cannot be reclaimed must not block the others: log,
		// skip it and report the failure after the pass.
		if err != nil {
			log.Printf("native history %s: reclaim failed; skipped", id)
			failed = true
		}
	}
	if failed {
		return errNativeHistory
	}
	return nil
}
