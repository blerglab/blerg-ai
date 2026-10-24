package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestClusterPodControllerIsolationAndBootstrap(t *testing.T) {
	pod := map[string]any{}
	container := map[string]any{"image": "fixture-image", "volumeMounts": []map[string]any{{"name": "projects", "mountPath": "/workspace/projects"}}}
	isolatedClusterPod(pod, container)
	raw, _ := json.Marshal(map[string]any{"pod": pod, "container": container})
	text := string(raw)
	for _, required := range []string{`"mountPath":"/run/blerg-workspace-private"`, `"mountPath":"/runtime-private"`, `chmod 0700 /runtime-private`, `--reuid=1001`, `--regid=1000`, `--ambient-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace`, `"readOnlyRootFilesystem":true`, `"KILL"`, `"SYS_PTRACE"`, `"name":"workspace-storage"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("production pod missing %s", required)
		}
	}
	for _, forbidden := range []string{`"privileged":true`, `"hostPID":true`, `"hostPath"`, `serviceAccountName`, `secretKeyRef`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("isolation acquired unrelated authority: %s", forbidden)
		}
	}
}

func TestClusterEntrypointsInstallProductionAdapters(t *testing.T) {
	for path, call := range map[string]string{"../../cmd/server/main.go": "api.ConfigureClusterWorkspaces()", "../../cmd/runner/main.go": "runner.ConfigureClusterRuntime(&cfg)"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), call) {
			t.Fatalf("production entry point %s has no adapter", path)
		}
	}
}
