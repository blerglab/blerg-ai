package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Translate the generated Kubernetes Job into an owned local pod fixture. The
// production env, Secret references, UID boundary and init permissions all run;
// no cluster, provider credential or existing worker is accessed.
// Run with -count=1: the helper sources, Python probe and Docker image are
// external inputs that Go's test cache cannot observe.
func TestClusterWorkerToolsFromJobPodSpec(t *testing.T) {
	image := os.Getenv("BLERG_TEST_WORKER_POD_IMAGE")
	if image == "" {
		t.Skip("set BLERG_TEST_WORKER_POD_IMAGE to a local image with Python and setpriv")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "runner.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "../runner")
	build.Env = append(os.Environ(), "GOWORK=off", "GOOS=linux", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pod fixture: %v\n%s", err, out)
	}
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	_, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	j.historyPool = pool
	sessionID := "00000000-0000-4000-8000-000000000570"
	if err := db.UpsertDaemon(ctx, pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, clusterDaemonID, "starting", "/workspace", "", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	markClusterWorkerRow(t, pool, sessionID)
	j.ServerHTTPURL = "http://localhost:19191"
	spec := SessionJobSpec{SessionID: sessionID, WorkBranch: "wip/worker-tools", NoRepo: true, ExtraEnv: map[string]string{"BLERG_RUNNER_BOARD_ID": "fixture-board"}, MCPGateway: &protocol.MCPGatewayConfig{BaseURL: j.ServerHTTPURL, Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-board-grant"}}}}
	if err := j.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	pod := f.created[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	isolatedClusterPod(pod, container)
	cli, err := filepath.Abs("../../.claude/skills/session-messaging/blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := filepath.Abs("testdata/cluster_worker_probe.py")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("blerg-worker-tools-%d", time.Now().UnixNano())
	args := []string{"run", "--pull=never", "--rm", "--name", name, "--label", "blerg-test-run=" + name, "--network=none", "--read-only", "--user", "0:0", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", "type=bind,src=" + binary + ",dst=/fixture/runner.test,readonly", "--mount", "type=bind,src=" + cli + ",dst=/fixture/blerg-runner,readonly", "--mount", "type=bind,src=" + probe + ",dst=/fixture/probe.py,readonly", "--env", "BLG555_POD_ENTRY=1", "--env", "PYTHONDONTWRITEBYTECODE=1"}
	// The init container also has FOWNER for chmod after chown. The
	// production setpriv command carries only the supervisor ambient set.
	args = append(args, "--cap-add=FOWNER")
	security := container["securityContext"].(map[string]any)
	for _, capability := range security["capabilities"].(map[string]any)["add"].([]string) {
		args = append(args, "--cap-add="+capability)
	}
	// Secret values are fixture constants. Resolve only references actually present
	// in the generated Job; missing required values fail rather than being guessed.
	secrets := map[string]map[string]any{}
	for _, secret := range f.createdSecrets {
		secrets[secret["metadata"].(map[string]any)["name"].(string)] = secret["stringData"].(map[string]any)
	}
	for _, item := range container["env"].([]any) {
		env := item.(map[string]any)
		value, ok := env["value"].(string)
		if !ok {
			from := env["valueFrom"].(map[string]any)
			if from["resourceFieldRef"] != nil {
				continue
			}
			ref := from["secretKeyRef"].(map[string]any)
			value, ok = secrets[ref["name"].(string)][ref["key"].(string)].(string)
			if !ok {
				if ref["optional"] == true {
					continue
				}
				t.Fatalf("unresolved required fixture env %s", env["name"])
			}
		}
		args = append(args, "--env", env["name"].(string)+"="+value)
	}
	mounts := container["volumeMounts"].([]map[string]any)
	for _, mount := range mounts {
		args = append(args, "--tmpfs", mount["mountPath"].(string)+":rw,size=134217728,mode=0755")
	}
	init := pod["initContainers"].([]map[string]any)[0]
	script := init["command"].([]string)[2]
	script = strings.ReplaceAll(script, "cp -a /workspace/. /runtime-home/", ":")
	for _, initMount := range init["volumeMounts"].([]map[string]any) {
		for _, mount := range mounts {
			if mount["name"] == initMount["name"] {
				script = strings.ReplaceAll(script, initMount["mountPath"].(string), mount["mountPath"].(string))
			}
		}
	}
	// setpriv is the production command, with only its final executable replaced
	// by the compiled test entry (the same ConfigureClusterRuntime/AgentHost path).
	command := append([]string(nil), container["command"].([]string)...)
	command[len(command)-1] = "/fixture/runner.test"
	script += "\nexec " + strings.Join(command, " ") + " -test.run=^TestClusterWorkerToolsPodEntry$ -test.v"
	args = append(args, "--entrypoint", "/bin/sh", image, "-ec", script)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	})
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("Job pod worker tools: %v\n%s", err, out)
	} else {
		if !strings.Contains(string(out), "--- PASS: TestClusterWorkerToolsPodEntry") {
			t.Fatalf("pod fixture did not execute its engine boundary test: %s", out)
		}
		t.Log(string(out))
	}
}

// All runtime failures use the same Job retry policy. Repeated failures must
// require an explicit resume rather than a Kubernetes retry pile-up.
func TestClusterFailureJobNeverRetries(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	if err := j.CreateSessionJob(SessionJobSpec{SessionID: "failure-policy", NoRepo: true}); err != nil {
		t.Fatal(err)
	}
	spec := f.created[0]["spec"].(map[string]any)
	if spec["backoffLimit"] != float64(0) && spec["backoffLimit"] != 0 {
		t.Fatal("Job allows failure retries")
	}
	container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	identity := map[string]string{}
	for _, item := range container["env"].([]any) {
		env := item.(map[string]any)
		if value, ok := env["value"].(string); ok {
			identity[env["name"].(string)] = value
		}
	}
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if identity[key] == "" {
			t.Errorf("pod has no default %s", key)
		}
	}
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["restartPolicy"] != "Never" {
		t.Fatal("pod allows failure restarts")
	}
}
