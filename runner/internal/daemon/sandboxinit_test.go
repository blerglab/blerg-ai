package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Real Docker, no engine, login, network or daemon: an exec'd command leaves
// orphan descendants behind while the container's sleep process stays alive.
func TestSandboxInitReapsOrphansInDocker(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	label := labelTestContainers(t)
	name := "blerg-reaper-test-" + ccUUID()
	args := sandboxRunArgs(name, t.TempDir(), t.TempDir(), nil, sandboxRunExtras{network: "none"})
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	if !strings.Contains(strings.Join(args, " "), "--label "+label) {
		t.Fatal("the test container would carry no test label")
	}
	if out, err := run(args...); err != nil {
		t.Fatalf("start test container: %v: %s", err, out)
	}
	// The intermediate child exits first, so its child is adopted by PID 1.
	// Wait for every adopted process to disappear, rather than mistaking a
	// still-running orphan for a successfully reaped one.
	script := `import os, time
orphans = []
for _ in range(8):
    read, write = os.pipe()
    child = os.fork()
    if child == 0:
        os.close(read)
        orphan = os.fork()
        if orphan == 0:
            os.close(write)
            time.sleep(0.1)
            os._exit(0)
        os.write(write, str(orphan).encode())
        os._exit(0)
    os.close(write)
    orphans.append(int(os.read(read, 32)))
    os.close(read)
    os.waitpid(child, 0)
deadline = time.monotonic() + 3
while time.monotonic() < deadline:
    remaining = [pid for pid in orphans if os.path.exists('/proc/' + str(pid))]
    if not remaining:
        print('all 8 orphan descendants reaped')
        break
    time.sleep(0.02)
else:
    zombies = 0
    for pid in remaining:
        try:
            with open('/proc/' + str(pid) + '/stat') as stat:
                zombies += stat.read().split(') ', 1)[1].split()[0] == 'Z'
        except FileNotFoundError:
            pass
    raise SystemExit(str(len(remaining)) + ' orphan descendants remain; zombies=' + str(zombies))
`
	if out, err := run("exec", name, "python3", "-c", script); err != nil {
		t.Fatalf("sandbox did not reap orphan descendants: %v: %s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
