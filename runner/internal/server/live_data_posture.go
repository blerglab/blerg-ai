package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

var errLivePosture = errors.New("live_data_posture_unavailable")

// SetLiveDataDesktop is trusted startup configuration, never hello/request data.
func (a *API) SetLiveDataDesktop(desktop bool) { a.liveDataDesktop = desktop }

func (a *API) handleLivePosture(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, cause string) {
		failurelog.Default.Event("live_data_posture", slog.LevelWarn, "cause", cause)
		writeError(w, status, "Unavailable.")
	}
	if !a.liveDefinitionAuthenticated(r) {
		fail(401, "posture_unauthenticated")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 1))
	if e != nil || len(raw) != 0 || r.URL.RawQuery != "" {
		fail(400, "posture_body_refused")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	safe, cause, e := a.livePosture(ctx)
	if e != nil {
		fail(503, "posture_unavailable")
		return
	}
	if !safe {
		failurelog.Default.Event("live_data_posture", slog.LevelWarn, "cause", cause)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ld.PostureReport{Safe: safe, Cause: cause, CheckedAt: time.Now().UTC()})
}
func (a *API) livePosture(ctx context.Context) (bool, string, error) {
	if a.liveDataDesktop {
		return false, "desktop_container_runtime", nil
	}
	if a.hub == nil || a.dbPool == nil {
		return false, "", errLivePosture
	}
	connected := a.hub.GetAllDaemons()
	for _, dc := range connected {
		// A mode/OS/group claim from an agent-controlled hello cannot attest
		// isolation. Only the server-verified, per-launch cluster token can.
		if dc.BoundSessionID == "" || dc.RuntimeTokenID == "" {
			return false, "host_runtime", nil
		}
	}
	var host bool
	e := a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daemons WHERE id<>$1 AND desktop_owner_id IS NOT NULL) OR EXISTS(SELECT 1 FROM sessions WHERE daemon_id<>$1 AND runtime IS DISTINCT FROM 'cluster' AND ended_at IS NULL AND status NOT IN ('ended','stopped'))`, clusterDaemonID).Scan(&host)
	if e != nil {
		return false, "", errLivePosture
	}
	if host {
		return false, "host_runtime", nil
	}
	manager := a.hub.JobManager()
	if manager == nil {
		return false, "", errLivePosture
	}
	safe, sessions, e := manager.livePodPosture(ctx)
	if e != nil {
		return false, "", e
	}
	if !safe {
		return false, "privileged_runtime", nil
	}
	for _, dc := range connected {
		if !sessions[dc.BoundSessionID] {
			return false, "", errLivePosture
		}
	}
	return true, "isolated_cluster", nil
}

type postureSecurity struct {
	Privileged               bool
	AllowPrivilegeEscalation *bool
	RunAsUser                *int64
	RunAsNonRoot             *bool
	ReadOnlyRootFilesystem   bool
	Capabilities             struct{ Drop, Add []string }
	SeccompProfile           struct{ Type string }
	ProcMount                string
	WindowsOptions           json.RawMessage
	AppArmorProfile          struct{ Type string }
}
type postureContainer struct {
	Name, Image     string
	Command, Args   []string
	SecurityContext *postureSecurity
	VolumeMounts    []struct{ MountPath, MountPropagation string }
	VolumeDevices   []json.RawMessage
	EnvFrom         []json.RawMessage
	Env             []struct {
		Name      string
		ValueFrom struct{ SecretKeyRef *struct{ Name, Key string } }
	}
}
type posturePod struct {
	Metadata struct{ Labels map[string]string }
	Spec     struct {
		HostPID, HostIPC, HostNetwork                   bool
		AutomountServiceAccountToken                    *bool
		SecurityContext                                 struct{ Sysctls []json.RawMessage }
		Volumes                                         []map[string]json.RawMessage
		Containers, InitContainers, EphemeralContainers []postureContainer
	}
	Status struct{ Phase string }
}

func (j *JobManager) livePodPosture(ctx context.Context) (bool, map[string]bool, error) {
	if j.Client == nil || j.BaseURL == "" || j.Token == "" || j.Namespace == "" || j.Image == "" {
		return false, nil, errLivePosture
	}
	client := *j.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	continuation := ""
	sessions := make(map[string]bool)
	for page := 0; page < 10; page++ {
		path := "/api/v1/namespaces/" + url.PathEscape(j.Namespace) + "/pods?limit=100"
		if continuation != "" {
			path += "&continue=" + url.QueryEscape(continuation)
		}
		request, e := http.NewRequestWithContext(ctx, "GET", j.BaseURL+path, nil) // #nosec G704 -- configured in-cluster Kubernetes API, never an agent URL.
		if e != nil {
			return false, nil, errLivePosture
		}
		request.Header.Set("Authorization", "Bearer "+j.Token)
		response, e := client.Do(request) // #nosec G704 -- fixed Kubernetes endpoint and redirects disabled.
		if e != nil {
			return false, nil, errLivePosture
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
		_ = response.Body.Close()
		if readErr != nil || len(raw) > 4*1024*1024 || response.StatusCode != 200 {
			return false, nil, errLivePosture
		}
		var list struct {
			APIVersion, Kind string
			Metadata         struct{ Continue string }
			Items            *[]posturePod
		}
		if json.Unmarshal(raw, &list) != nil || list.Kind != "PodList" || list.APIVersion != "v1" || list.Items == nil || len(*list.Items) > 100 || len(list.Metadata.Continue) > 4096 {
			return false, nil, errLivePosture
		}
		for _, pod := range *list.Items {
			if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
				continue
			}
			if !j.isolatedPosturePod(pod) {
				return false, nil, nil
			}
			if id := pod.Metadata.Labels["session"]; id != "" {
				sessions[id] = true
			}
		}
		continuation = list.Metadata.Continue
		if continuation == "" {
			return true, sessions, nil
		}
	}
	return false, nil, errLivePosture
}
func (j *JobManager) isolatedPosturePod(p posturePod) bool {
	if p.Spec.HostPID || p.Spec.HostIPC || p.Spec.HostNetwork || p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken || len(p.Spec.SecurityContext.Sysctls) > 0 || len(p.Spec.Containers) == 0 {
		return false
	}
	for _, volume := range p.Spec.Volumes {
		// Managed runtimes use only private emptyDir and controller-owned PVCs.
		// hostPath, device/CSI and projected/Secret token or credential mounts are
		// not an attested safe source, even if a mount claims to be read-only.
		if len(volume) != 2 || len(volume["name"]) == 0 || len(volume["emptyDir"]) == 0 && len(volume["persistentVolumeClaim"]) == 0 {
			return false
		}
	}
	for _, c := range p.Spec.Containers {
		if !j.isolatedPostureContainer(c, false) {
			return false
		}
	}
	for _, c := range p.Spec.InitContainers {
		if !j.isolatedPostureContainer(c, true) {
			return false
		}
	}
	for _, c := range p.Spec.EphemeralContainers {
		if !j.isolatedPostureContainer(c, false) {
			return false
		}
	}
	return true
}
func (j *JobManager) isolatedPostureContainer(c postureContainer, init bool) bool {
	s := c.SecurityContext
	if s == nil || s.Privileged || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation || !slices.Contains(s.Capabilities.Drop, "ALL") || s.SeccompProfile.Type != "RuntimeDefault" || s.ProcMount != "" && s.ProcMount != "Default" || len(s.WindowsOptions) > 0 || s.AppArmorProfile.Type != "" && s.AppArmorProfile.Type != "RuntimeDefault" || len(c.VolumeDevices) > 0 || len(c.EnvFrom) > 0 {
		return false
	}
	for _, mount := range c.VolumeMounts {
		if mount.MountPropagation != "" && mount.MountPropagation != "None" {
			return false
		}
		for _, socket := range []string{"docker.sock", "containerd.sock", "podman.sock", "crio.sock"} {
			if strings.Contains(mount.MountPath, socket) {
				return false
			}
		}
	}
	for _, env := range c.Env {
		switch strings.ToUpper(env.Name) {
		case "DOCKER_HOST", "CONTAINER_HOST", "CONTAINERD_ADDRESS", "KUBECONFIG":
			return false
		}
		if strings.Contains(strings.ToUpper(env.Name), "LIVE_DATA") {
			return false
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil {
			if strings.HasPrefix(ref.Name, "blerg-live-data-") || ref.Key == "token" || ref.Key == "key.pem" || ref.Key == "ca-key.pem" || ref.Key == "tls.key" || ref.Key == "authority.json" {
				return false
			}
		}
	}
	if len(s.Capabilities.Add) == 0 && s.RunAsNonRoot != nil && *s.RunAsNonRoot && (s.RunAsUser == nil || *s.RunAsUser > 0) {
		return true
	}
	// The existing UID-separated controller starts root only to install a
	// fixed capability set and drop to UID 1001; the agent runs as UID 1000.
	// It is not host-root: no host namespace, API token or host/secret mount.
	// Accept only its trusted image and exact entry point/capability projection.
	if c.Image != j.Image || !s.ReadOnlyRootFilesystem || s.RunAsUser == nil || *s.RunAsUser != 0 || len(c.Args) > 0 {
		return false
	}
	expected := map[string]any{"image": j.Image}
	pod := map[string]any{}
	isolatedClusterPod(pod, expected)
	var target map[string]any
	if init {
		if c.Name != "workspace-storage" {
			return false
		}
		inits, ok := pod["initContainers"].([]map[string]any)
		if !ok || len(inits) == 0 {
			return false
		}
		target = inits[0]
	} else {
		if c.Name != "runner" {
			return false
		}
		target = expected
	}
	command, ok := target["command"].([]string)
	if !ok || !reflect.DeepEqual(c.Command, command) {
		return false
	}
	security, ok := target["securityContext"].(map[string]any)
	if !ok {
		return false
	}
	capabilities, ok := security["capabilities"].(map[string]any)
	if !ok {
		return false
	}
	expectedCaps, ok := capabilities["add"].([]string)
	if !ok {
		return false
	}
	actual := slices.Clone(s.Capabilities.Add)
	expectedCaps = slices.Clone(expectedCaps)
	slices.Sort(actual)
	slices.Sort(expectedCaps)
	return slices.Equal(actual, expectedCaps)
}
