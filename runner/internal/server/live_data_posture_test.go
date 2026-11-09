package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

func posturePodFixture(isolated bool) map[string]any {
	container := map[string]any{"name": "runner", "image": "fixture-agent:current", "securityContext": sessionContainerSecurityContext()}
	spec := map[string]any{"automountServiceAccountToken": false, "containers": []map[string]any{container}}
	if isolated {
		isolatedClusterPod(spec, container)
	}
	return map[string]any{"metadata": map[string]any{"labels": map[string]string{"session": "00000000-0000-4000-8000-000000000514"}}, "spec": spec, "status": map[string]any{"phase": "Running"}}
}
func decodePosturePod(t *testing.T, raw map[string]any) posturePod {
	t.Helper()
	body, e := json.Marshal(raw)
	if e != nil {
		t.Fatal(e)
	}
	var pod posturePod
	if e = json.Unmarshal(body, &pod); e != nil {
		t.Fatal(e)
	}
	return pod
}
func TestLivePostureManagedPodBoundary(t *testing.T) {
	manager := &JobManager{Image: "fixture-agent:current"}
	for _, isolated := range []bool{false, true} {
		baseline := posturePodFixture(isolated)
		if !manager.isolatedPosturePod(decodePosturePod(t, baseline)) {
			t.Fatal("managed cluster runtime was not accepted", isolated)
		}
		changes := map[string]func(map[string]any){
			"host PID":          func(p map[string]any) { p["hostPID"] = true },
			"host IPC":          func(p map[string]any) { p["hostIPC"] = true },
			"host network":      func(p map[string]any) { p["hostNetwork"] = true },
			"API token":         func(p map[string]any) { p["automountServiceAccountToken"] = true },
			"unknown API token": func(p map[string]any) { delete(p, "automountServiceAccountToken") },
			"host path": func(p map[string]any) {
				p["volumes"] = []any{map[string]any{"name": "host", "hostPath": map[string]any{"path": "/var/run"}}}
			},
			"secret mount": func(p map[string]any) {
				p["volumes"] = []any{map[string]any{"name": "credential", "secret": map[string]any{"secretName": "fixture-secret"}}}
			},
			"projected API token": func(p map[string]any) {
				p["volumes"] = []any{map[string]any{"name": "token", "projected": map[string]any{"sources": []any{map[string]any{"serviceAccountToken": map[string]any{"path": "token"}}}}}}
			},
			"unknown CSI volume": func(p map[string]any) {
				p["volumes"] = []any{map[string]any{"name": "unknown", "csi": map[string]any{"driver": "fixture.example"}}}
			},
			"sysctl": func(p map[string]any) {
				p["securityContext"] = map[string]any{"sysctls": []any{map[string]string{"name": "kernel.fixture", "value": "1"}}}
			},
			"no container": func(p map[string]any) { p["containers"] = []any{} },
		}
		containerChanges := map[string]func(map[string]any){
			"privileged":         func(c map[string]any) { c["securityContext"].(map[string]any)["privileged"] = true },
			"escalation":         func(c map[string]any) { c["securityContext"].(map[string]any)["allowPrivilegeEscalation"] = true },
			"unknown escalation": func(c map[string]any) { delete(c["securityContext"].(map[string]any), "allowPrivilegeEscalation") },
			"system admin capability": func(c map[string]any) {
				c["securityContext"].(map[string]any)["capabilities"] = map[string]any{"drop": []string{"ALL"}, "add": []string{"SYS_ADMIN"}}
			},
			"no capability drop": func(c map[string]any) { c["securityContext"].(map[string]any)["capabilities"] = map[string]any{} },
			"unconfined seccomp": func(c map[string]any) {
				c["securityContext"].(map[string]any)["seccompProfile"] = map[string]string{"type": "Unconfined"}
			},
			"unmasked proc": func(c map[string]any) { c["securityContext"].(map[string]any)["procMount"] = "Unmasked" },
			"Windows host process": func(c map[string]any) {
				c["securityContext"].(map[string]any)["windowsOptions"] = map[string]bool{"hostProcess": true}
			},
			"device mount": func(c map[string]any) {
				c["volumeDevices"] = []any{map[string]string{"name": "disk", "devicePath": "/dev/fixture"}}
			},
			"mount propagation": func(c map[string]any) {
				c["volumeMounts"] = []any{map[string]string{"name": "runtime", "mountPath": "/run/runtime", "mountPropagation": "Bidirectional"}}
			},
			"runtime socket": func(c map[string]any) {
				c["volumeMounts"] = []any{map[string]string{"name": "runtime", "mountPath": "/run/docker.sock"}}
			},
			"Docker endpoint": func(c map[string]any) {
				c["env"] = []any{map[string]string{"name": "DOCKER_HOST", "value": "tcp://runtime.example:2375"}}
			},
			"Kubernetes credential": func(c map[string]any) {
				c["env"] = []any{map[string]string{"name": "KUBECONFIG", "value": "/fixture/config"}}
			},
			"unknown env source": func(c map[string]any) {
				c["envFrom"] = []any{map[string]any{"secretRef": map[string]string{"name": "fixture"}}}
			},
			"live credential env": func(c map[string]any) {
				c["env"] = []any{map[string]string{"name": "BLERG_LIVE_DATA_KEY", "value": "fixture"}}
			},
			"live credential reference": func(c map[string]any) {
				c["env"] = []any{map[string]any{"name": "OTHER", "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "blerg-live-data-issuer", "key": "authority.json"}}}}
			},
			"API token reference": func(c map[string]any) {
				c["env"] = []any{map[string]any{"name": "OTHER", "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": "fixture-token", "key": "token"}}}}
			},
			"unknown root entrypoint": func(c map[string]any) {
				c["securityContext"].(map[string]any)["runAsUser"] = 0
				c["command"] = []string{"/bin/sh"}
			},
		}
		for name, change := range changes {
			t.Run(name+map[bool]string{false: " legacy", true: " isolated"}[isolated], func(t *testing.T) {
				raw := posturePodFixture(isolated)
				change(raw["spec"].(map[string]any))
				if manager.isolatedPosturePod(decodePosturePod(t, raw)) {
					t.Fatal("unsafe pod attested safe")
				}
			})
		}
		for name, change := range containerChanges {
			t.Run(name+map[bool]string{false: " legacy", true: " isolated"}[isolated], func(t *testing.T) {
				raw := posturePodFixture(isolated)
				c := raw["spec"].(map[string]any)["containers"].([]map[string]any)[0]
				change(c)
				if manager.isolatedPosturePod(decodePosturePod(t, raw)) {
					t.Fatal("unsafe container attested safe")
				}
			})
		}
	}
	for _, mutation := range []string{"image", "arguments", "capability", "init command"} {
		t.Run("controller "+mutation, func(t *testing.T) {
			raw := posturePodFixture(true)
			p := raw["spec"].(map[string]any)
			c := p["containers"].([]map[string]any)[0]
			switch mutation {
			case "image":
				c["image"] = "fixture-agent:other"
			case "arguments":
				c["args"] = []string{"untrusted"}
			case "capability":
				c["securityContext"].(map[string]any)["capabilities"].(map[string]any)["add"] = []string{"CHOWN"}
			case "init command":
				p["initContainers"].([]map[string]any)[0]["command"] = []string{"/bin/sh"}
			}
			if manager.isolatedPosturePod(decodePosturePod(t, raw)) {
				t.Fatal("changed controller contract attested safe")
			}
		})
	}
}

func TestLivePostureKubernetesFreshAndFailClosed(t *testing.T) {
	var mode atomic.Int64
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-kubernetes-token" || r.URL.Path != "/api/v1/namespaces/fixture-sessions/pods" {
			t.Error("unexpected Kubernetes request")
			w.WriteHeader(403)
			return
		}
		switch mode.Load() {
		case 2:
			w.WriteHeader(403)
			return
		case 3:
			_, _ = w.Write([]byte(`{"kind":"SecretList","apiVersion":"v1","items":[]}`))
			return
		case 4:
			_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1"}`))
			return
		case 5:
			_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","metadata":{"continue":"repeated"},"items":[]}`))
			return
		}
		pod := posturePodFixture(true)
		if mode.Load() == 1 {
			pod["spec"].(map[string]any)["hostPID"] = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "PodList", "apiVersion": "v1", "items": []any{pod}})
	}))
	defer server.Close()
	manager := &JobManager{BaseURL: server.URL, Namespace: "fixture-sessions", Image: "fixture-agent:current", Token: "fixture-kubernetes-token", Client: server.Client()}
	if safe, _, e := manager.livePodPosture(t.Context()); e != nil || !safe {
		t.Fatal("isolated pod not attested", safe, e)
	}
	mode.Store(1)
	if safe, _, e := manager.livePodPosture(t.Context()); e != nil || safe || calls.Load() != 2 {
		t.Fatal("privileged runtime hidden by a cached safe result", safe, e)
	}
	for _, bad := range []int64{2, 3, 4, 5} {
		mode.Store(bad)
		if safe, _, e := manager.livePodPosture(t.Context()); e == nil || safe {
			t.Fatal("uncertain Kubernetes inventory attested safe", bad)
		}
	}
}
func TestLivePostureDesktopAndUnknownHost(t *testing.T) {
	desktop := &API{liveDataDesktop: true}
	if safe, cause, e := desktop.livePosture(t.Context()); e != nil || safe || cause != "desktop_container_runtime" {
		t.Fatal("desktop Docker posture was safe", cause, e)
	}
	api := &API{hub: NewHub()}
	if safe, _, e := api.livePosture(t.Context()); e == nil || safe {
		t.Fatal("unknown runtime attested safe")
	}
}
func TestLivePostureDedicatedIdentityAndNoClientAttestation(t *testing.T) {
	identity := "spiffe://blerg/installation/00000000-0000-4000-8000-000000000001/core"
	uri, _ := url.Parse(identity)
	leaf := &x509.Certificate{URIs: []*url.URL{uri}}
	for _, tc := range []struct {
		name, body                string
		tls, forwarded, caRefused bool
		want                      int
	}{
		{name: "key only", want: 401}, {name: "forwarded", tls: true, forwarded: true, want: 401}, {name: "other CA", tls: true, caRefused: true, want: 401}, {name: "claimed safe", body: `{"safe":true}`, tls: true, want: 400}, {name: "desktop", tls: true, want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &API{liveDataDesktop: true, liveDataCoreIdentity: identity, coreInternalKey: "fixture"}
			if tc.caRefused {
				api.SetLiveDataPeerVerifier(func(*http.Request) error { return errors.New("fixture CA refusal") })
			}
			request := httptest.NewRequest("POST", "/internal/live-data/posture", strings.NewReader(tc.body))
			request.Header.Set("X-Internal-Key", "fixture")
			if tc.tls {
				request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}}
			}
			if tc.forwarded {
				request.Header["Forwarded"] = []string{""}
			}
			response := httptest.NewRecorder()
			api.handleLivePosture(response, request)
			if response.Code != tc.want {
				t.Fatal(response.Code, tc.want)
			}
			if response.Code == 200 {
				var report ld.PostureReport
				if json.Unmarshal(response.Body.Bytes(), &report) != nil || !report.Valid() || report.Safe {
					t.Fatal("desktop endpoint bypassed consent")
				}
			}
		})
	}
}

func TestLivePostureTrustedInventoryAndRecordedHosts(t *testing.T) {
	fx := newTopicFx(t)
	// This fixture begins with a host. Its attestations are intentionally removed
	// before testing a managed cluster, rather than trusting a claimed mode.
	if _, e := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster';UPDATE daemons SET desktop_owner_id=NULL`); e != nil {
		t.Fatal(e)
	}
	fx.api.hub = NewHub()
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList", "items": []any{posturePodFixture(true)}})
	}))
	defer kube.Close()
	fx.api.hub.SetJobManager(&JobManager{BaseURL: kube.URL, Client: kube.Client(), Token: "fixture", Namespace: "fixture-sessions", Image: "fixture-agent:current"})
	if safe, cause, e := fx.api.livePosture(t.Context()); e != nil || !safe || cause != "isolated_cluster" {
		t.Fatal("verified cluster could not poll without a host warning", cause, e)
	}
	runtime := &DaemonConn{ID: newUUID(), Mode: "runner"}
	fx.api.hub.Register(runtime)
	if safe, cause, e := fx.api.livePosture(t.Context()); e != nil || safe || cause != "host_runtime" {
		t.Fatal("self-reported runner mode attested isolation", cause, e)
	}
	fx.api.hub.UnregisterIfCurrent(runtime)
	runtime = &DaemonConn{ID: newUUID(), Mode: "runner", BoundSessionID: "00000000-0000-4000-8000-000000000514", RuntimeTokenID: "fixture-verified-runtime"}
	fx.api.hub.Register(runtime)
	if safe, _, e := fx.api.livePosture(t.Context()); e != nil || !safe {
		t.Fatal("verified launch token and isolated pod did not match", e)
	}
	fx.api.hub.UnregisterIfCurrent(runtime)
	runtime = &DaemonConn{ID: newUUID(), Mode: "runner", BoundSessionID: newUUID(), RuntimeTokenID: "fixture-verified-runtime"}
	fx.api.hub.Register(runtime)
	if safe, _, e := fx.api.livePosture(t.Context()); e == nil || safe {
		t.Fatal("runtime without a matching actual pod attested safe")
	}
	fx.api.hub.UnregisterIfCurrent(runtime)
	if _, e := fx.pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='fixture-owner' WHERE id=$1`, topicDaemonID); e != nil {
		t.Fatal(e)
	}
	if safe, cause, e := fx.api.livePosture(t.Context()); e != nil || safe || cause != "host_runtime" {
		t.Fatal("disconnected enrolled host omitted", cause, e)
	}
	if _, e := fx.pool.Exec(t.Context(), `WITH cleared AS (UPDATE daemons SET desktop_owner_id=NULL) UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); e != nil {
		t.Fatal(e)
	}
	if safe, cause, e := fx.api.livePosture(t.Context()); e != nil || safe || cause != "host_runtime" {
		t.Fatal("active host session omitted", cause, e)
	}
}
