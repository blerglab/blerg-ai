package server

import (
	"encoding/json"
	"testing"
)

// TestCreateSessionJobPodHardening pins the pod-level hardening every session
// Job carries, and that hardening did not disturb how credentials reach the
// pod (env from a Secret via secretKeyRef, no volumes or mounts).
func TestCreateSessionJobPodHardening(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "hard-1", Repo: "proj", Title: "T", Model: "claude-sonnet-5"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.created[0])
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	pod := generic["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	containers := pod["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("containers = %d", len(containers))
	}
	c := containers[0].(map[string]any)
	sc, _ := c["securityContext"].(map[string]any)
	if sc == nil {
		t.Fatal("container has no securityContext")
	}
	caps, _ := sc["capabilities"].(map[string]any)
	seccomp, _ := sc["seccompProfile"].(map[string]any)

	if v, ok := pod["automountServiceAccountToken"]; !ok || v != false {
		t.Errorf("automountServiceAccountToken = %v (present %v), want false", v, ok)
	}
	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"runAsNonRoot", sc["runAsNonRoot"], true},
		{"runAsUser", sc["runAsUser"], float64(1000)},
		{"allowPrivilegeEscalation", sc["allowPrivilegeEscalation"], false},
		{"seccompProfile.type", seccomp["type"], "RuntimeDefault"},
	} {
		if tc.got != tc.want {
			t.Errorf("securityContext.%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	drop, _ := caps["drop"].([]any)
	if len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities.drop = %v, want [ALL]", drop)
	}
	if _, ok := caps["add"]; ok {
		t.Error("no capability may be added back")
	}
	if sc["readOnlyRootFilesystem"] == true {
		t.Error("readOnlyRootFilesystem must stay off: the agent writes /workspace, /tmp and caches")
	}
	if sc["privileged"] == true {
		t.Error("container must not be privileged")
	}

	// Credentials still arrive as secretKeyRef env, never as volumes.
	if _, ok := pod["volumes"]; ok {
		t.Error("pod must not declare volumes")
	}
	if _, ok := c["volumeMounts"]; ok {
		t.Error("container must not mount volumes")
	}
	refs := 0
	for _, e := range c["env"].([]any) {
		vf, _ := e.(map[string]any)["valueFrom"].(map[string]any)
		if _, ok := vf["secretKeyRef"]; ok {
			refs++
		}
	}
	if refs == 0 {
		t.Error("expected credential env entries via secretKeyRef")
	}
}
