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
)

// Exercise the shipped CLI as agent UID 1000 with the production pod's writable
// mounts, read-only image and HOME. No running session container is touched.
func TestClusterAttachmentWritesOnReadOnlyPod(t *testing.T) {
	image := os.Getenv("BLERG_TEST_ATTACHMENT_POD_IMAGE")
	if image == "" {
		t.Skip("set BLERG_TEST_ATTACHMENT_POD_IMAGE to a local sandbox image with Python and setpriv")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	if err := j.CreateSessionJob(SessionJobSpec{SessionID: "attachments-session", NoRepo: true}); err != nil {
		t.Fatal(err)
	}
	pod := f.created[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	isolatedClusterPod(pod, container)
	var target string
	for _, value := range container["env"].([]any) {
		env := value.(map[string]any)
		if env["name"] == "BLERG_RUNNER_ATTACHMENTS_DIR" {
			target, _ = env["value"].(string)
		}
	}
	cli, err := filepath.Abs("../../.claude/skills/session-messaging/blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("blerg-attachments-test-%d", time.Now().UnixNano())
	args := []string{"run", "--pull=never", "--rm", "-i", "--name", name, "--label", "blerg-test-run=" + name, "--network=none", "--read-only", "--user", "0:0", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--cap-add=FOWNER", "--cap-add=SETUID", "--cap-add=SETGID", "--security-opt=no-new-privileges", "--mount", "type=bind,src=" + cli + ",dst=/fixture/blerg-runner,readonly", "--env", "HOME=/workspace", "--env", "PYTHONDONTWRITEBYTECODE=1", "--env", "BLERG_RUNNER_SESSION_ID=attachments-session", "--env", "BLERG_RUNNER_SERVER_HTTP=http://example.invalid", "--env", "BLERG_RUNNER_SESSION_TOKEN=fixture", "--env", "BLERG_RUNNER_ATTACHMENTS_DIR=" + target}
	mounts := container["volumeMounts"].([]map[string]any)
	for _, mount := range mounts {
		args = append(args, "--tmpfs", mount["mountPath"].(string)+":rw,size=67108864,mode=0755")
	}
	init := pod["initContainers"].([]map[string]any)[0]
	script := init["command"].([]string)[2]
	// The home image copy does not affect attachment storage; the empty fixture
	// home replaces it. Apply the actual ownership/mode script to matching mounts.
	script = strings.ReplaceAll(script, "cp -a /workspace/. /runtime-home/", ":")
	for _, initMount := range init["volumeMounts"].([]map[string]any) {
		for _, mount := range mounts {
			if mount["name"] == initMount["name"] {
				script = strings.ReplaceAll(script, initMount["mountPath"].(string), mount["mountPath"].(string))
			}
		}
	}
	script += "\nexec setpriv --reuid=1000 --regid=1000 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all python3 -"
	args = append(args, "--entrypoint", "/bin/sh", image, "-ec", script)
	// Remove only the uniquely named container started by this test, even on timeout.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	})
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader(`import io, json, os, runpy, stat
m = runpy.run_path('/fixture/blerg-runner')
g = m['cmd_fetch'].__globals__
payload = b'private attachment'
def fetch(path, **kwargs):
    if path.endswith('/file'):
        return io.BytesIO(payload)
    return io.BytesIO(json.dumps({'attachments': [{'id': 'a'*32, 'name': 'input.txt', 'size': len(payload)}]}).encode())
g['_fetch_request'] = fetch
os.chdir('/workspace')
m['cmd_fetch']([], True, None, False)
target = os.environ['BLERG_RUNNER_ATTACHMENTS_DIR']
assert not target.startswith('/workspace/')
assert open(target + '/input.txt', 'rb').read() == payload
assert stat.S_IMODE(os.stat(target).st_mode) == 0o700
assert stat.S_IMODE(os.stat('/home/agent/.blerg-runner-daemon').st_mode) == 0o700
assert os.stat('/home/agent/.blerg-runner-daemon').st_uid == 1000
review = {'file': {'name': 'report.md'}, 'requests': [{'id': 'r1', 'status': 'open', 'text': 'Fix it'}]}
g['_load_reviews'] = lambda: {'report.md': review}
g['cmd_publish'] = lambda path, **kwargs: None
m['cmd_review_reply']('r1', 'done', 'Fixed')
state = target + '/.reviews/report.md.review.json'
assert json.load(open(state))['requests'][0]['status'] == 'done'
assert stat.S_IMODE(os.stat(target + '/.reviews').st_mode) == 0o700
assert not os.path.exists('/workspace/attachments')
print('private fetch and review writes passed as UID', os.getuid())
`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pod filesystem writes: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
