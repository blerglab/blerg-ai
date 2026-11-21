//go:build linux

package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/server"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if handled, code := ServeClusterWriter(os.Args[1:]); handled {
		os.Exit(code)
	}
	if len(os.Args) >= 2 && os.Args[1] == "cluster-shell-supervisor" {
		if len(os.Args) == 3 && os.Args[2] == "--probe" {
			if shellruntime.Probe(os.Stdout) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
		if shellruntime.ServeClusterHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPodIsolatedImageEntry(t *testing.T) {
	if os.Getenv("P6_POD_IMAGE_ENTRY") != "1" {
		t.Skip("owned container entry only")
	}
	if os.Getpid() != 1 || os.Geteuid() != 1001 {
		t.Fatal("not the production supervisor identity/PID namespace")
	}
	m := protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "repo", Path: "repo", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/repo"}}}
	cfg := Config{Home: "/workspace", SessionID: "session", Workspace: &clusterworkspace.Plan{SessionID: "session", Manifest: m, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, Members: []clusterworkspace.Member{{ID: "member", Provider: "github", CloneURL: "https://github.com/org/repo.git", PushURL: "https://github.com/org/repo.git", DestinationIdentity: "org/repo", CredentialHost: "github.com"}}}}
	if err := ConfigureClusterRuntime(&cfg); err != nil {
		var header = unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		data := [2]unix.CapUserData{}
		_ = unix.Capget(&header, &data[0])
		a, _ := os.Stat(clusterPrivateRoot)
		h, _ := os.Stat(cfg.Home)
		t.Fatalf("production isolation config: %v uid=%d pid=%d caps=%x private=%v home=%v", err, os.Geteuid(), os.Getpid(), data[0].Effective, a, h)
	}
	if cfg.ClusterPrivateRoot != clusterPrivateRoot || cfg.ClusterWriters == nil || cfg.ClusterServices == nil || cfg.StartCommand == nil {
		t.Fatal("incomplete production config")
	}
	testPodProductionCapabilityNegotiation(t, cfg)
	if err := os.WriteFile(filepath.Join(clusterPrivateRoot, "snapshot"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/workspace/public", nil, 0660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/workspace/public", 1000, 1000); err != nil {
		t.Fatal(err)
	}
	// The untrusted engine can write its workspace, but cannot read/mutate trusted
	// snapshots, acquire capabilities, or read the supervisor's procfs descriptors.
	cmd := exec.Command("/bin/sh", "-ec", `test "$(id -u)" = 1000
 test "$(awk '/^CapEff:/{print $2}' /proc/self/status)" = 0000000000000000
 test ! -r /run/blerg-workspace-private/snapshot
 test ! -w /run/blerg-workspace-private/snapshot
 test ! -r /proc/1/environ
 echo public > /workspace/public
 echo boundary-ok`)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cfg.StartCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("agent UID/storage boundary: %v %s", err, output.String())
	}
	if !strings.Contains(output.String(), "boundary-ok") {
		t.Fatal("agent code did not run")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := cfg.ClusterWriters.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ClusterWriters.Thaw(ctx); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll("/tmp/blerg-mcp-fixture", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/blerg-mcp-fixture/mcp.json", []byte("engine-context"), 0600); err != nil {
		t.Fatal(err)
	}
	support := exec.Command("/bin/sh", "-ec", `test "$(cat "$1")" = engine-context`, "sh", "/tmp/blerg-mcp-fixture/mcp.json")
	if err := cfg.StartCommand(support); err != nil {
		t.Fatal(err)
	}
	if err := support.Wait(); err != nil {
		t.Fatal("engine cannot read projected launch file", err)
	}
	if err := os.MkdirAll("/workspace/.blerg", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(clusterPrivateRoot, "/workspace/.blerg/forged"); err != nil {
		t.Fatal(err)
	}
	forged := exec.Command("true")
	forged.Env = []string{"CLAUDE_CONFIG_DIR=/workspace/.blerg/forged"}
	if cfg.StartCommand(forged) == nil {
		t.Fatal("support-file projection followed private-root symlink")
	}
	if err := os.Remove("/workspace/.blerg/forged"); err != nil {
		t.Fatal(err)
	}
	testClusterWorkspaceFailureRecovery(t)
	testPodShellService(ctx, t, &cfg)

	// Kernel state, not an environment path, is the admission boundary.
	if err := os.Chmod(clusterPrivateRoot, 0770); err != nil {
		t.Fatal(err)
	}
	if ConfigureClusterRuntime(&cfg) == nil {
		t.Fatal("unprotected private storage admitted")
	}
	var st unix.Stat_t
	if unix.Stat("/workspace/public", &st) != nil || st.Uid != 1000 {
		t.Fatal("agent file ownership")
	}
}

func testPodShellService(ctx context.Context, t *testing.T, cfg *Config) {
	t.Helper()
	if err := os.MkdirAll("/workspace/projects", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/workspace/projects/readme.txt", []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	rt, err := clusterworkspace.New(*cfg.Workspace, requestGit{})
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.SetWriters(cfg.ClusterWriters); err != nil {
		t.Fatal(err)
	}
	if err = rt.SetSupervisor(cfg.Supervisor, cfg.SupervisorLaunch); err != nil {
		t.Fatal(err)
	}
	if err = rt.Prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err = rt.ArmWriters(); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if unix.Stat("/workspace/projects", &st) != nil {
		t.Fatal("root")
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: cfg.Workspace.Runtime, ManifestDigest: cfg.Workspace.Manifest.Digest, Root: protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}}
	p := cfg.Supervisor.(*podRuntime)
	p.service = shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	services, err := cfg.ClusterServices("/workspace/projects", clusterPrivateRoot, cfg.Workspace.Manifest, binding, rt)
	if err != nil {
		t.Fatal("actual pod services:", err)
	}
	defer func() { _ = services.Close(ctx); _ = p.service.Shutdown(ctx) }()
	b, err := p.service.Binding(cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	call := func(q shellruntime.RPC) shellruntime.RPC {
		t.Helper()
		q.SessionID = cfg.SessionID
		r, e := p.service.Call(ctx, q)
		if e != nil || !r.OK {
			t.Fatalf("pod shell %s failed: %v", q.Op, e)
		}
		return r
	}
	if !protocol.ClusterWorkspaceEnabled || !b.ServerCapabilities.Supports("cluster", true) || !b.RuntimeCapabilities.Supports("cluster", true) {
		t.Fatal("production pod must offer cluster shell", b)
	}
	call(shellruntime.RPC{Op: "configure", Binding: b, Key: pub, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}})
	now := time.Now().Unix()
	g := ownershell.SignGrant(key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: "shell", Runtime: "cluster", PolicyRevision: 1, IssuedAt: now, ExpiresAt: now + 30})
	call(shellruntime.RPC{Op: "open", Grant: g})
	call(shellruntime.RPC{Op: "input", Grant: g, Seq: 1, Data: []byte(`test "$(id -u)" = 1000 && test "$(awk '/^CapEff:/{print $2}' /proc/self/status)" = 0000000000000000 && test ! -r /run/blerg-workspace-private/snapshot && echo safe > /workspace/shell-safe
`)})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if raw, _ := os.ReadFile("/workspace/shell-safe"); string(raw) == "safe\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PTY violated UID/capability/private-storage boundary")
		}
		time.Sleep(time.Millisecond)
	}
	if err := cfg.ClusterWriters.Freeze(ctx); err != nil {
		t.Fatal("combined engine+PTY freeze", err)
	}
	if err := cfg.ClusterWriters.Thaw(ctx); err != nil {
		t.Fatal("combined thaw", err)
	}
	if err := services.Close(ctx); err != nil {
		t.Fatal("reader close", err)
	}
	rt.Stop(ctx)
	if err := p.service.ProveRecordedShellsEmpty(cfg.SessionID); err != nil {
		t.Fatal("no exact PTY cleanup evidence", err)
	}
}

func TestPodUIDStorageBoundaryDocker(t *testing.T) {
	if os.Getenv("P6_TEST_DOCKER") != "1" {
		t.Skip("requires opt-in owned Docker fixture")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	image := fmt.Sprintf("blerg-p6cluster-fixture:%d", time.Now().UnixNano())
	name := fmt.Sprintf("blerg-p6cluster-%d", time.Now().UnixNano())
	dockerfile := "FROM postgres:16\nRUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*\nRUN groupadd -g 1000 agent && useradd -u 1000 -g 1000 -d /workspace agent && useradd -u 1001 -d /run/blerg-workspace-private supervisor\nENTRYPOINT []\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("owned Docker fixture failed: %v\n%s", err, out)
		}
		return out
	}
	run("build", "--label", "blerg-test-run="+name, "-t", image, dir)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "image", "rm", image).Run()
	})
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := run("run", "--rm", "--name", name, "--label", "blerg-test-run="+name, "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "KILL", "--cap-add", "SYS_PTRACE", "--security-opt", "no-new-privileges", "--read-only", "--network", "none", "--tmpfs", "/workspace:uid=1000,gid=1000,mode=0770,size=64m,nr_inodes=10000", "--tmpfs", "/run/blerg-workspace-private:uid=1001,gid=1001,mode=0700,size=64m,nr_inodes=10000", "--tmpfs", "/tmp:mode=1777,size=64m,nr_inodes=10000", "--mount", "type=bind,src="+executable+",dst=/fixture/runner.test,readonly", "--env", "P6_POD_IMAGE_ENTRY=1", image, "/usr/bin/setpriv", "--reuid=1001", "--regid=1000", "--clear-groups", "--inh-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace", "--ambient-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace", "/fixture/runner.test", "-test.run=^TestPodIsolatedImageEntry$", "-test.v")
	t.Log(strings.TrimSpace(string(out)))
	t.Run("OOMKilled without restarts", func(t *testing.T) { testClusterPodOOM(ctx, t, image, executable, name+"-oom") })
}

// This runs as the real supervisor UID/PID 1, with the protected mount and all
// six capabilities checked by ConfigureClusterRuntime. No capable peer is
// stamped by hand. The fixture token authenticates this isolated local server;
// session-scoped runtime-token isolation is covered by the server auth suite.
func testPodProductionCapabilityNegotiation(t *testing.T, cfg Config) {
	t.Helper()
	if got := podWorkspaceCapabilities(t.Context(), cfg); got != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("pod advertised before services attached", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hub := server.NewHub()
	api := server.NewAPI(hub, nil, "pod-fixture-credential", nil, "")
	if err := api.StartRuntimeShellChannel(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the nil-DB fixture accepts this compatibility credential. Real
	// deployments bind pod identities to their persisted launch tokens.
	t.Setenv("BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS", "true")
	endpoint := httptest.NewServer(hub.ServeDaemon("pod-fixture-credential", nil))
	defer endpoint.Close()
	cfg.ServerWS = "ws" + strings.TrimPrefix(endpoint.URL, "http")
	cfg.DaemonToken = "pod-fixture-credential"
	unsupported := make(chan struct{}, 1)
	client := newPodWorkspaceClient(cfg, "/workspace/projects", func() {
		select {
		case unsupported <- struct{}{}:
		default:
		}
	})
	if err := cfg.ClusterAttach(ctx, client); err != nil {
		t.Fatal(err)
	}
	caps := podWorkspaceCapabilities(t.Context(), cfg)
	if !protocol.ClusterWorkspaceEnabled || !caps.Supports("cluster", true) || caps.LinuxHost || caps.MacOSHost || caps.Sandbox {
		t.Fatal("POD_CAPABILITIES: complete image/runtime not advertised", caps)
	}
	incomplete := cfg
	incomplete.ClusterServices = nil
	if got := podWorkspaceCapabilities(t.Context(), incomplete); got != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("incomplete pod advertised", got)
	}
	incomplete = cfg
	incomplete.ClusterWriters = nil
	if got := podWorkspaceCapabilities(t.Context(), incomplete); got != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("pod without writers advertised", got)
	}
	incomplete = cfg
	incomplete.ClusterPrivateRoot = ""
	if got := podWorkspaceCapabilities(t.Context(), incomplete); got != (protocol.WorkspaceCapabilities{}) {
		t.Fatal("pod without protected storage advertised", got)
	}
	connected := make(chan struct{}, 1)
	client.SetOnConnect(func() {
		select {
		case connected <- struct{}{}:
		default:
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx, func() []string { return nil }) }()
	defer func() { cancel(); <-done }()

	select {
	case <-connected:
	case <-unsupported:
		t.Fatal("POD_HELLO: real pod configuration was not negotiated")
	case <-ctx.Done():
		t.Fatal("pod hello/welcome did not complete")
	}
	if err := client.WaitWorkspace(ctx); err != nil {
		t.Fatal("pod lost negotiated workspace", err)
	}
	for {
		peers := hub.GetAllDaemons()
		if len(peers) > 0 {
			if peers[0].Mode != "runner" || !peers[0].WorkspaceCapabilities.Supports("cluster", true) || peers[0].WorkspaceCapabilities.MacOSHost {
				t.Fatal("POD_WELCOME: server retained unnegotiated peer", peers[0].WorkspaceCapabilities)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("pod did not register")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Launched by server.TestClusterWorkerToolsFromJobPodSpec using the actual Job
// environment, init script and writable mounts. Only Git and the engine are fakes.
func TestClusterWorkerToolsPodEntry(t *testing.T) {
	if os.Getenv("BLG555_POD_ENTRY") != "1" {
		t.Skip("owned Job pod fixture only")
	}
	cfg := ConfigFromEnv()
	cfg.Home = "/workspace"
	p := clusterworkspace.Plan{SessionID: cfg.SessionID, Manifest: protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "org/one"}}}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, Members: []clusterworkspace.Member{{ID: "one", Provider: "github", CloneURL: "https://github.com/org/one.git", PushURL: "https://github.com/org/one.git", CredentialHost: "github.com", DestinationIdentity: "org/one"}}}
	cfg.Workspace = &p
	if err := ConfigureClusterRuntime(&cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := clusterworkspace.New(p, requestGit{})
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Prepare(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	cwd := "/workspace/projects/one"
	if err = os.MkdirAll(cwd, 0750); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(cwd, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	// A private credential sibling must stay private when skills become readable.
	if err = os.MkdirAll("/workspace/.blerg-runner-daemon", 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("/workspace/.blerg-runner-daemon/fixture-token", []byte("private-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(clusterPrivateRoot+"/snapshot", []byte("private-fixture"), 0600); err != nil {
		t.Fatal(err)
	}

	// The same generated Job launches this real clone. Main starts with only its base.
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "safe.directory=" + dir}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(cwd, "init", "-q", "-b", "main")
	git(cwd, "commit", "--allow-empty", "-qm", "default base")
	git(cwd, "checkout", "-qb", "alpha/0.6.0")
	git(cwd, "commit", "--allow-empty", "-qm", "current remote base")
	base := git(cwd, "rev-parse", "HEAD")
	mainDir := "/tmp/main-repo"
	origin := "/tmp/origin.git"
	git("/tmp", "clone", "-q", "--bare", cwd, origin)
	git(origin, "symbolic-ref", "HEAD", "refs/heads/main")
	git("/tmp", "clone", "-q", origin, mainDir)
	git(cwd, "update-ref", "refs/blerg/result-base", base)
	git(cwd, "checkout", "-qb", cfg.WorkBranch)
	if err := filepath.Walk(cwd, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, 1000, 1000)
	}); err != nil {
		t.Fatal(err)
	}
	var artifactMu sync.Mutex
	var artifact map[string]any
	var bundle []byte
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		artifactMu.Lock()
		defer artifactMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/artifacts"):
			var err error
			bundle, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			artifact = map[string]any{"id": "result-1", "name": "result.bundle", "version": 1, "size": len(bundle), "sha256": fmt.Sprintf("%x", sha256.Sum256(bundle)), "content_type": "application/x-git-bundle"}
			_ = json.NewEncoder(w).Encode(artifact)
		case strings.HasSuffix(r.URL.Path, "/download"):
			_, _ = w.Write(bundle)
		case strings.HasSuffix(r.URL.Path, "/imports"):
			_ = json.NewEncoder(w).Encode(map[string]any{"next_n": 1, "outstanding": []any{artifact}})
		case strings.HasSuffix(r.URL.Path, "/artifacts"):
			_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": []any{artifact}})
		case strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`{"files":[]}`))
		default:
			t.Errorf("unexpected fixture request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer endpoint.Close()
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", endpoint.URL)
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-message-token")
	t.Setenv("PATH", "/fixture:"+os.Getenv("PATH"))
	result := make(chan error, 1)
	capture := func(engine *exec.Cmd) error {
		// Keep the driver's pipes/process: a real result event must reach turn_done.
		engine.Path = "/usr/bin/python3"
		engine.Args = append([]string{"python3", "/fixture/probe.py"}, engine.Args[1:]...)
		engine.Args = append(engine.Args, daemon.PodSkillStateDir(cfg.Home))
		return cfg.StartCommand(engine)
	}
	onTurnDone := func(string) {
		artifactMu.Lock()
		published := len(bundle) > 0
		artifactMu.Unlock()
		if !published {
			result <- errors.New("turn_done did not publish a bundle")
			return
		}
		cmd := exec.Command("python3", "/fixture/blerg-runner", "session", "import", cfg.SessionID, "result.bundle", "--into", "alpha/0.6.0")
		cmd.Dir = mainDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			result <- fmt.Errorf("session import: %w: %s", err, out)
			return
		}
		head := git(cwd, "rev-parse", "HEAD")
		landed := git(mainDir, "rev-parse", "refs/heads/wip/"+cfg.SessionID+"/import-1")
		if landed != head || head == base {
			result <- fmt.Errorf("imported %s, want new worker commit %s", landed, head)
			return
		}
		if content := git(mainDir, "show", landed+":worker.txt"); content != "cluster worker" {
			result <- errors.New("imported worker content is missing")
			return
		}
		t.Log("launched, committed with pod identity, published bundle, and session import landed the worker commit")
		result <- nil
	}
	sender := launchSender{started: make(chan protocol.SessionStarted, 1)}
	host := newPodAgentHost(sender, cfg, rt, func() ClusterServices { return &launchExplorer{} }, daemon.AgentHostConfig{HomeDir: cfg.Home, ReposRoot: "/workspace/projects", ServerHTTP: cfg.ServerHTTP, DaemonToken: cfg.DaemonToken, ClaudeCode: true, StartCommand: capture, OnTurnDone: onTurnDone})
	defer host.Kill(cfg.SessionID)
	msg := podSpawn(cfg, cwd)
	host.Spawn(msg)
	select {
	case <-sender.started:
	default:
		t.Fatal("pod host did not start")
	}
	host.UserMessage(cfg.SessionID, "run the engine probe", "session")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("engine probe did not run")
	}
}

// Allocate and touch real cgroup memory; a synthetic exit 137 is not OOM proof.
func TestClusterOOMPodEntry(t *testing.T) {
	if os.Getenv("BLERG_TEST_OOM_ENTRY") != "1" {
		t.Skip("owned OOM container entry only")
	}
	var held [][]byte
	for {
		block := make([]byte, 1<<20)
		for i := 0; i < len(block); i += 4096 {
			block[i] = 1
		}
		held = append(held, block)
		runtime.KeepAlive(held)
	}
}

func testClusterPodOOM(parent context.Context, t *testing.T, image, binary, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run(); err != nil {
			t.Error("OOM fixture cleanup failed")
		}
	})
	cmd := exec.CommandContext(ctx, "docker", "run", "--pull=never", "--name", name, "--label", "blerg-test-run="+name, "--network=none", "--read-only", "--user", "1001:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=48m", "--memory-swap=48m", "--restart=no", "--mount", "type=bind,src="+binary+",dst=/fixture/runner.test,readonly", "--env", "BLERG_TEST_OOM_ENTRY=1", image, "/fixture/runner.test", "-test.run=^TestClusterOOMPodEntry$")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 137 {
		t.Fatalf("expected kernel OOM exit 137: %v", err)
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", name).Output()
	if err != nil {
		t.Fatal("OOM fixture inspect failed")
	}
	var state []struct {
		State struct {
			OOMKilled bool
			ExitCode  int
		}
		RestartCount int
	}
	if json.Unmarshal(out, &state) != nil || len(state) != 1 || !state[0].State.OOMKilled || state[0].State.ExitCode != 137 || state[0].RestartCount != 0 {
		t.Fatal("OOM cause or zero-restart proof missing")
	}
}
