package daemon

import (
	"bytes"
	"log"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSandboxReattachContextLabel(t *testing.T) {
	for _, label := range []string{"started-here", ""} {
		t.Run("label="+label, func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			name := "blerg-sandbox-context-reattach-" + ccUUID()[:8]
			forgetSandboxDeath(name) // simulate the memory lost on restart
			bin, calls := fakeDocker(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_DOCKER_LOG", calls)
			t.Setenv("FAKE_DOCKER_PS_OUTPUT", name+"\t"+sandboxOwner()+"\t"+label+"\n")
			t.Setenv("FAKE_DOCKER_INSPECT_OUTPUT", runningState)
			oldOut, oldCtx := dockerOutput, currentDockerContext
			t.Cleanup(func() {
				dockerOutput, currentDockerContext = oldOut, oldCtx
				forgetSandboxDeath(name)
			})
			currentDockerContext = func() string { return "current-context" }
			want := label
			if want == "" {
				want = "current-context"
			}
			var buf bytes.Buffer
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			dockerOutput = func(args ...string) ([]byte, error) {
				if args[0] == "ps" {
					if !strings.Contains(strings.Join(args, " "), "blerg.runner.docker-context") {
						t.Error("rediscovery did not request the context label")
					}
					return oldOut(args...)
				}
				if !slices.Equal(args[:2], []string{"--context", want}) {
					t.Errorf("inspection context: %v", args)
				}
				return oldOut(args...)
			}
			for range 2 {
				if got := listOwnSandboxContainers(nil); !slices.Equal(got, []string{name}) {
					t.Fatalf("rediscovered %v", got)
				}
			}
			if got := pinnedDockerArgs(name, "inspect"); !slices.Equal(got, []string{"--context", want, "inspect"}) {
				t.Fatalf("restored pin %v", got)
			}
			if _, dead := sandboxContainerExit(name); dead {
				t.Fatal("healthy reattached container marked dead")
			}
			logs := strings.Count(buf.String(), "falling back to current context")
			if label == "" && logs != 1 || label != "" && logs != 0 {
				t.Fatalf("fallback logs=%d: %s", logs, buf.String())
			}
		})
	}
}

func TestSandboxHungListingDoesNotBlockWatchers(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	fastSandboxWatch(t, time.Hour)
	sandboxSnapMu.Lock()
	sandboxSnaps = map[string]*sandboxSnapshot{}
	sandboxSnapMu.Unlock()
	old := dockerOutput
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	const slow = "blerg-sandbox-slow"
	const fast = "blerg-sandbox-fast"
	pinSandboxContext(slow, "slow")
	pinSandboxContext(fast, "fast")
	dockerOutput = func(args ...string) ([]byte, error) {
		if args[1] == "slow" {
			close(started)
			<-release
			return []byte(slow + "\trunning"), nil
		}
		return []byte(fast + "\trunning"), nil
	}
	t.Cleanup(func() { dockerOutput = old; forgetSandboxDeath(slow); forgetSandboxDeath(fast) })
	go func() { sandboxLooksAlive(slow); close(finished) }()
	<-started
	// Both a peer on the same context and one on another context must proceed.
	done := make(chan bool, 1)
	go func() { done <- sandboxLooksAlive(slow) && sandboxLooksAlive(fast) }()
	select {
	case alive := <-done:
		close(release)
		<-finished
		if !alive {
			t.Fatal("watchers did not see live containers")
		}
	case <-time.After(time.Second):
		close(release)
		<-finished
		<-done
		t.Fatal("hung docker ps blocked other watchers")
	}
}
