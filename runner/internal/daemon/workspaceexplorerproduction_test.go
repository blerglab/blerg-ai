package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// The fake Docker runs the real helper implementation in a distinct process.
// No host-reader shortcut is installed in the daemon under test.
func TestWorkspaceExplorerChildProcess(t *testing.T) {
	if os.Getenv("BLERG_EXPLORER_TEST_PROCESS") != "1" {
		return
	}
	root := os.Getenv("BLERG_EXPLORER_TEST_ROOT")
	if fault := os.Getenv("BLERG_EXPLORER_TEST_FAULT"); fault != "" {
		var start WorkspaceExplorerStart
		decoder := json.NewDecoder(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		if decoder.Decode(&start) != nil {
			os.Exit(2)
		}
		info, err := os.Stat(root)
		if err != nil {
			os.Exit(2)
		}
		_ = encoder.Encode(helperPrepared{WorkspaceRuntimePrepared: protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: start.SessionID, Binding: protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: start.Runtime, ManifestDigest: start.Manifest.Digest, Root: workspaceRootIdentity(info)}}})
		var req protocol.ReadWorkspaceFile
		if decoder.Decode(&req) != nil {
			os.Exit(2)
		}
		if fault == "stall" {
			if os.WriteFile(filepath.Join(root, "helper-started"), []byte("started"), 0600) != nil {
				os.Exit(2)
			}
			_, _ = io.Copy(io.Discard, os.Stdin)
			if os.WriteFile(filepath.Join(root, "helper-joined"), []byte("joined"), 0600) != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
		reply := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, OK: true, ContentB64: "bGVhaw=="}
		switch fault {
		case "session":
			reply.SessionID = "other"
		case "request":
			reply.RequestID = "other"
		case "generation":
			reply.Generation++
		case "type":
			reply.Type = "other"
		}
		_ = encoder.Encode(reply)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	ctx := context.WithValue(context.Background(), workspaceStatusDiagnosticKey{}, workspaceStatusDiagnostic(func(stage string, err error, stderr string) {
		fmt.Fprintf(os.Stderr, "workspace status unavailable: stage=%q err=%v stderr=%q\n", stage, err, stderr)
	}))
	ctx = context.WithValue(ctx, workspaceStatusTimingKey{}, workspaceStatusTiming(func(stage string, elapsed, remaining time.Duration, err error) {
		fmt.Fprintf(os.Stderr, "workspace status timing: stage=%q elapsed=%v remaining=%v err=%v\n", stage, elapsed, remaining, err)
	}))
	if fixture := os.Getenv("BLERG_EXPLORER_TEST_GIT_BINARY"); fixture != "" {
		// This test-only child exits after serving this one helper lifetime.
		_ = hostgit.SetBinaryForTest(fixture)
	}
	initializing := time.Now()
	git, err := hostgit.Binary()
	fmt.Fprintf(os.Stderr, "workspace helper Git: binary=%q platform=%s initialization=%v err=%v\n", git, runtime.GOOS, time.Since(initializing), err)
	if err := ServeWorkspaceExplorer(ctx, root, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "workspace helper preparation: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
func processExplorer(t *testing.T, root string, start WorkspaceExplorerStart) *remoteWorkspaceExplorer {
	t.Helper()
	remote := &remoteWorkspaceExplorer{start: start, command: func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspaceExplorerChildProcess$")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "BLERG_EXPLORER_TEST_PROCESS=1", "BLERG_EXPLORER_TEST_ROOT=" + root}
		cmd.Stderr = os.Stderr
		return cmd
	}}
	t.Cleanup(func() {
		if err := remote.Close(); err != nil {
			t.Errorf("helper cleanup: %v", err)
		}
	})
	return remote
}
func TestWorkspaceExplorerProductionProcess(t *testing.T) {
	started := time.Now()
	defer func() { t.Logf("production process total elapsed=%v", time.Since(started)) }()
	root := t.TempDir()
	initChildRepo(t, root)
	workspaceFixture(t, root, "README.md", "runtime bytes")
	workspaceFixture(t, root, ".env", "do not return")
	media := make([]byte, 3<<20)
	copy(media, []byte{137, 80, 78, 71, 13, 10, 26, 10})
	if err := os.WriteFile(filepath.Join(root, "large.png"), media, 0600); err != nil {
		t.Fatal(err)
	}
	start := legacyExplorerStart("session")
	remote := processExplorer(t, root, start)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_ = remote.Explore(canceled, protocol.WorkspaceExplorerRequest{SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime})
	for _, op := range []string{"list", "search", "status"} {
		req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: op, SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Operation: op, Path: "", Filter: ""}
		if op == "search" {
			req.Filter = "README"
		}
		operation := time.Now()
		res := remote.Explore(t.Context(), req)
		t.Logf("production process operation=%s elapsed=%v ok=%v code=%q total=%v", op, time.Since(operation), res.OK, res.ErrorCode, time.Since(started))
		if !res.OK {
			t.Fatalf("real helper %s: %+v", op, res)
		}
		if op == "search" && (len(res.Entries) != 1 || res.Entries[0].Path != "README.md") {
			t.Fatalf("quick-open search: %+v", res)
		}
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), ".env") {
			t.Fatalf("protected metadata: %s", raw)
		}
	}
	req := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "viewer", SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Path: "README.md"}
	if res := remote.ReadFile(t.Context(), req); !res.OK || res.ContentB64 != base64.StdEncoding.EncodeToString([]byte("runtime bytes")) {
		t.Fatalf("viewer: %+v", res)
	}
	mediaRequest := req
	mediaRequest.Path = "large.png"
	mediaRequest.RequestID = "large-media"
	if got := remote.ReadFile(t.Context(), mediaRequest); !got.OK || got.Size != int64(len(media)) || len(got.ContentB64) != base64.StdEncoding.EncodedLen(len(media)) {
		t.Fatalf("viewer-sized helper frame: ok=%v size=%d code=%s", got.OK, got.Size, got.ErrorCode)
	}
	req.Generation++
	if res := remote.ReadFile(t.Context(), req); res.OK {
		t.Fatal("stale runtime read")
	}
	req.Generation--
	if err := remote.stopHelper(); err != nil {
		t.Fatal(err)
	}
	// The next helper cannot adopt a replacement root, even at the same pathname.
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root + "-old")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	workspaceFixture(t, root, "README.md", "replacement")
	if res := remote.ReadFile(t.Context(), req); res.OK {
		t.Fatal("restarted helper adopted replacement root")
	}
}

func TestWorkspaceExplorerProductionStatusDeadlineReplies(t *testing.T) {
	root := t.TempDir()
	initChildRepo(t, root)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	start := legacyExplorerStart("deadline-session")
	remote := processExplorer(t, root, start)
	command := remote.command
	remote.command = func() *exec.Cmd {
		cmd := command()
		cmd.Env = append(cmd.Env, "PATH="+bin+":/usr/bin:/bin", "BLERG_EXPLORER_TEST_GIT_BINARY="+filepath.Join(bin, "git"))
		return cmd
	}
	started := time.Now()
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "slow-status", SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Operation: "status"}
	res := remote.Explore(t.Context(), req)
	elapsed := time.Since(started)
	if res.OK || res.ErrorCode != "cancelled" || elapsed > 5*time.Second {
		t.Fatalf("worker deadline lost over real helper pipes: code=%q ok=%v elapsed=%v", res.ErrorCode, res.OK, elapsed)
	}
	t.Logf("blocked Git joined and explicit refusal delivered in %v (worker budget remains two seconds)", elapsed)
}
func TestWorkspaceExplorerProductionCancellationAndMemberPins(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "member")
	initChildRepo(t, member)
	start := legacyExplorerStart("session")
	start.Manifest.Kind = "folder"
	start.Manifest.Members[0].Path = "member"
	remote := processExplorer(t, root, start)
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "before", SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Operation: "list", Path: "member"}
	if res := remote.Explore(t.Context(), req); !res.OK {
		t.Fatal(res)
	}
	// Cancellation tears down and joins the process. A later operation restarts
	// with the original member/root pins rather than adopting a mutable namespace.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = remote.Explore(ctx, req)
	req.RequestID = "after-cancel"
	if res := remote.Explore(t.Context(), req); !res.OK {
		t.Fatalf("navigation poisoned reader: %+v", res)
	}
	if err := remote.stopHelper(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(member, member+"-old"); err != nil {
		t.Fatal(err)
	}
	initChildRepo(t, member)
	if res := remote.Explore(t.Context(), req); res.OK {
		t.Fatal("helper restart adopted replaced member")
	}
}
func TestWorkspaceExplorerProductionLifecycleAdmission(t *testing.T) {
	remote := processExplorer(t, t.TempDir(), legacyExplorerStart("session"))
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: remote.start.Runtime, ManifestDigest: remote.start.Manifest.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	w := &LocalWorkspace{mu: &sync.Mutex{}, state: workspacePrivateManifest{Binding: &binding}, authorize: func(context.Context) error { return nil }}
	reader := &boundWorkspaceExplorer{workspace: w, remote: remote}
	var router WorkspaceExplorerRouter
	router.Bind("session", reader)
	sender := &agentTestSender{}
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "during-freeze", SessionID: "session", WorkspaceRuntimeRef: remote.start.Runtime, Operation: "list"}
	raw, _ := json.Marshal(req)
	w.mu.Lock()
	entered := false
	if reader.run(t.Context(), binding.WorkspaceRuntimeRef, func() { entered = true }) != "busy" || entered {
		t.Fatal("reader entered during lifecycle freeze")
	}
	busy := reader.ReadFile(t.Context(), protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "viewer-during-status", SessionID: "session", WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "README.md"})
	if busy.ErrorCode != "busy" || busy.OK {
		t.Fatalf("viewer cannot retry lifecycle contention: %+v", busy)
	}
	router.Handle(t.Context(), sender, raw)
	done := make(chan struct{})
	go func() { router.CancelSession("session"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader join deadlocked against lifecycle mutex")
	}
	w.mu.Unlock()
	if remote.cmd != nil {
		t.Fatal("reader executed helper while runtime frozen")
	}
	router.Unbind("session")
}
func TestWorkspaceExplorerDockerCommandAndLegacyReferences(t *testing.T) {
	r := newDockerWorkspaceExplorer("pinned-context", strings.Repeat("a", 64), legacyExplorerStart("session"))
	args := strings.Join(r.command().Args, " ")
	for _, want := range []string{"--context pinned-context exec -i -u 0 -w /", strings.Repeat("a", 64), "/usr/bin/env -i PATH=/usr/bin:/bin TMPDIR=/run/blerg-explorer", workspaceExplorerBinary + " --public-root /workspace"} {
		if !strings.Contains(args, want) {
			t.Fatalf("fixed helper argv missing %q: %s", want, args)
		}
	}
	for _, path := range []string{"README.md", "/workspace/README.md", "/private/clone/README.md"} {
		if got, ok := runtimeFileReference(path, "/private/clone", "/workspace"); !ok || got != "README.md" {
			t.Fatalf("alias %q: %q %v", path, got, ok)
		}
	}
	for _, path := range []string{"/private/original/README.md", "/workspace/../else", "//workspace/README.md", "/workspace-other/README.md"} {
		if _, ok := runtimeFileReference(path, "/private/clone", "/workspace"); ok {
			t.Fatalf("escaped runtime alias: %q", path)
		}
	}
}

type hostExplorerAuthority struct {
	testWorkspaceAuthority
	beforeLaunch func(string, protocol.WorkspaceManifest, string)
}

func (a *hostExplorerAuthority) Launch(ctx context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	if a.beforeLaunch != nil {
		a.beforeLaunch(id, m, token)
	}
	launch, err := a.testWorkspaceAuthority.Launch(ctx, id, m, token)
	launch.Runtime = "host"
	return launch, err
}
func TestWorkspaceExplorerProductionHostDispatch(t *testing.T) {
	testWorkspaceExplorerProductionHostDispatch(t, runtime.GOOS)
}

func TestWorkspaceExplorerProductionDarwinHostRefusal(t *testing.T) {
	testWorkspaceExplorerProductionHostDispatch(t, "darwin")
}

func testWorkspaceExplorerProductionHostDispatch(t *testing.T, platform string) {
	t.Helper()
	for _, scenario := range []string{"repo", "folder", "synthetic", "empty", "failed-bind", "concurrent"} {
		kind := scenario
		if scenario == "failed-bind" || scenario == "concurrent" {
			kind = "repo"
		}
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			initChildRepo(t, filepath.Join(root, "one"))
			initChildRepo(t, filepath.Join(root, "two"))
			workspaceFixture(t, root, "notes.txt", "host folder bytes")
			workspaceFixture(t, filepath.Join(root, "one"), "README.md", "one bytes")
			authority := &hostExplorerAuthority{}
			stateDir := t.TempDir()
			t.Setenv(DaemonStateDirEnv, stateDir)
			service := hostLifecycleTestService(t, authority)
			launched := false
			authority.beforeLaunch = func(id string, m protocol.WorkspaceManifest, token string) {
				launched = true
				record, _ := workspaceRecord(id)
				state, err := readPrivateWorkspace(record)
				if err != nil || !state.Host || !state.RuntimePreparing || state.RuntimeToken != token || len(state.Sources) != len(m.Members) {
					t.Error("host handoff was not durable before allocation")
				}
				if scenario == "concurrent" {
					if err := service.RecoverAll(t.Context(), nil); err != nil {
						t.Error("HOST_ADMISSION_RECOVERY: recovery interpreted an active factory journal", err)
					}
				}
			}
			manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: root, Workspaces: service})
			selection := discovery.New(func() string { return root })
			manager.discovery = selection
			page := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "list", Operation: "list", Runtime: "host", Path: "."})
			if !page.OK {
				t.Fatal(page)
			}
			request := protocol.DiscoverWorkspace{RequestID: "resolve", Operation: "resolve", Runtime: "host", RootIdentity: page.Page.RootIdentity}
			for _, entry := range page.Page.Entries {
				if (kind == "repo" && entry.Path == "one") || (kind == "synthetic" && entry.Kind == "repository") {
					request.Members = append(request.Members, protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity})
				}
			}
			if kind == "folder" || kind == "empty" {
				path := "."
				if kind == "empty" {
					path = "empty"
					if err := os.Mkdir(filepath.Join(root, path), 0700); err != nil {
						t.Fatal(err)
					}
				}
				folderPage := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "folder-list", Operation: "list", Runtime: "host", Path: path})
				request.RootIdentity = folderPage.Page.RootIdentity
				request.Operation = "expand"
				request.Folder = &protocol.SelectionRef{Path: path, SourceIdentity: folderPage.Page.Root.SourceIdentity}
			}
			resolved := selection.Handle(t.Context(), request)
			if !resolved.OK {
				t.Fatalf("resolve: %+v", resolved)
			}
			chosen := resolved.Resolved
			manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: "host-" + kind, Root: chosen.Root}
			if kind != "repo" {
				manifest.Kind = "folder"
			}
			paths, _ := workspaceselection.MemberPaths(*chosen)
			for i, source := range chosen.Members {
				manifest.Members = append(manifest.Members, protocol.WorkspaceMember{ID: source.ID, Name: source.Name, Path: paths[i], SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity})
			}
			manifest.Digest = workspaceselection.Digest(manifest)
			bind := protocol.DiscoverWorkspace{RequestID: "bind", Operation: "bind", Runtime: "host", SelectionIdentity: chosen.SelectionIdentity, Manifest: &manifest}
			if reply := selection.Handle(t.Context(), bind); !reply.OK {
				t.Fatalf("selection bind: %+v", reply)
			}
			host, sender, _ := newTestHost(t, &scriptedAgentProvider{}, "")
			host.cfg.PrepareHostWorkspace = manager.prepareHostWorkspace
			if platform != runtime.GOOS {
				host.cfg.PrepareHostWorkspace = func(ctx context.Context, msg protocol.SpawnSession) (string, WorkspaceExplorer, error) {
					return manager.prepareHostWorkspaceForPlatform(ctx, msg, platform)
				}
			}
			host.cfg.Workspaces = service
			manager.agents = host
			id := "host-" + scenario
			var fault *hostPreparationFaultAuthority
			if scenario == "failed-bind" {
				fault = &hostPreparationFaultAuthority{hostExplorerAuthority: authority}
				service.Authority = fault
				authority.bindErr = errors.New("interrupted bind")
			}
			spawn := protocol.SpawnSession{SessionID: id, Kind: "agent", Repo: "one", Workspace: &manifest, WorkspaceCapabilities: protocol.WorkspaceCapabilitiesFor("host", "linux")}
			if platform == "darwin" {
				host.Spawn(spawn)
				errs := sender.sessionErrors()
				if len(errs) != 1 || errs[0] != "workspace sessions are not supported on macOS hosts yet" {
					t.Fatalf("macOS host admission must explicitly refuse: errors=%q", errs)
				}
				if launched || authority.bound != nil || host.get(id) != nil || len(service.sessions) != 0 {
					t.Fatalf("macOS refusal left a partial host launch: allocated=%v bound=%v agent=%v workspaces=%d", launched, authority.bound != nil, host.get(id) != nil, len(service.sessions))
				}
				files, err := os.ReadDir(stateDir)
				if err != nil || len(files) != 0 {
					t.Fatalf("macOS refusal wrote private state: entries=%v err=%v", files, err)
				}
				registry := service.Supervisor.(*workspaceSupervisors)
				if _, err := registry.service.Binding(id); err == nil || registry.service.Lifecycle(id) != nil {
					t.Fatal("macOS refusal registered a host shell")
				}
				if got := service.Capabilities(t.Context()); got != (protocol.WorkspaceCapabilities{}) {
					t.Fatalf("macOS advertised unsupported host workspace/shell capabilities: %+v", got)
				}
				retained, err := selection.PinBound(t.Context(), manifest)
				if err != nil {
					t.Fatal("macOS refusal consumed the selection handoff:", err)
				}
				retained.Close()
				t.Logf("macOS host admission refused before side effects: %s", errs[0])
				return
			}
			if scenario == "concurrent" {
				firstEntered := make(chan struct{})
				release := make(chan struct{})
				secondEntered := make(chan struct{}, 1)
				var trips atomic.Int32
				service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return manifest, nil }
				oldHook := workspaceCrashHook
				workspaceCrashHook = func(stage string) {
					if stage == "before-host-handoff-save" {
						if trips.Add(1) == 1 {
							close(firstEntered)
							<-release
						} else {
							secondEntered <- struct{}{}
						}
					}
				}
				firstDone := make(chan struct{})
				go func() { host.Spawn(spawn); close(firstDone) }()
				<-firstEntered
				secondDone := make(chan error, 1)
				go func() { _, _, err := manager.prepareHostWorkspace(t.Context(), spawn); secondDone <- err }()
				violated := false
				select {
				case <-secondEntered:
					violated = true
				case <-time.After(100 * time.Millisecond):
				}
				close(release)
				<-firstDone
				secondErr := <-secondDone
				workspaceCrashHook = oldHook
				if violated || secondErr == nil {
					t.Fatal("HOST_ADMISSION: concurrent factory replaced an unfinished durable handoff")
				}
			} else {
				host.Spawn(spawn)
			}
			if fault != nil {
				if len(sender.sessionErrors()) == 0 {
					t.Fatal("failed host bind was admitted")
				}
				if fault.aborted {
					t.Fatal("HOST_PREPARE_TOKEN: failed bind revoked durable cleanup")
				}
				authority.bindErr = nil
				service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return manifest, nil }
				if err := service.RecoverAll(t.Context(), nil); err != nil {
					t.Fatal("HOST_PREPARE_RECOVERY:", err)
				}
				record, _ := workspaceRecord(id)
				state, err := readPrivateWorkspace(record)
				if err != nil || !state.TerminalAcknowledged {
					t.Fatal("HOST_PREPARE_RECOVERY: receipt missing", err)
				}
				return
			}
			defer host.Kill(id)
			if errs := sender.sessionErrors(); len(errs) > 0 {
				t.Fatalf("production host dispatch on %s (%s): %q", runtime.GOOS, scenario, errs)
			}
			if authority.bound == nil {
				t.Fatal("host reader was never bound through authority")
			}
			local := host.get(id).localWorkspace
			if local == nil || !local.state.Host {
				t.Fatal("production host did not install its lifecycle")
			}
			registry := service.Supervisor.(*workspaceSupervisors)
			if _, err := registry.service.Binding(id); err != nil {
				t.Fatal("production host did not register P5", err)
			}
			guide := workspaceGuideForRuntime(&manifest, local)
			if strings.Contains(guide, "/workspace/") {
				t.Fatal("host guide used container aliases")
			}
			for _, source := range local.state.Sources {
				if !strings.Contains(guide, source.Path) {
					t.Fatal("host guide omitted a real member path")
				}
			}

			req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "real-search", SessionID: id, WorkspaceRuntimeRef: authority.bound.WorkspaceRuntimeRef, Operation: "search", Filter: "README"}
			raw, _ := json.Marshal(req)
			manager.handleWorkspaceExplorer(raw)
			result := waitExplorerResult(t, sender, req.RequestID)
			if !result.OK {
				t.Fatalf("manager-to-agent search: %+v", result)
			}
			if kind == "empty" {
				if len(result.Entries) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Entries) != 1 {
				t.Fatalf("real host search: %+v", result)
			}
			if kind == "repo" {
				read := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "absolute-host", SessionID: id, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Path: filepath.Join(root, "one", "README.md")}
				raw, _ = json.Marshal(read)
				manager.handleReadWorkspaceFile(raw)
				if got := waitWorkspaceResult(t, sender, read.RequestID); !got.OK || got.Path != "README.md" || got.RequestedPath != read.Path {
					t.Fatalf("#165 host reference: %+v", got)
				}
			}
			if kind == "folder" {
				read := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "loose", SessionID: id, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Path: "notes.txt"}
				raw, _ = json.Marshal(read)
				manager.handleReadWorkspaceFile(raw)
				if got := waitWorkspaceResult(t, sender, "loose"); !got.OK {
					t.Fatalf("host loose file: %+v", got)
				}
			}
			if kind == "synthetic" {
				for _, entry := range result.Entries {
					if !strings.HasPrefix(entry.Path, "one/") && !strings.HasPrefix(entry.Path, "two/") {
						t.Fatal("synthetic search escaped selected mapping")
					}
				}
			}
			zero := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "zero-version", SessionID: id, Path: "README.md"}
			rawZero, _ := json.Marshal(zero)
			manager.handleReadWorkspaceFile(rawZero)
			if got := waitWorkspaceResult(t, sender, zero.RequestID); got.OK {
				t.Fatal("HOST_ZERO_GENERATION: v2 dispatch downgraded to legacy")
			}
			if err := host.explorer.Suspend(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			suspended := zero
			suspended.RequestID = "suspended-version"
			suspended.WorkspaceRuntimeRef = req.WorkspaceRuntimeRef
			host.ReadWorkspaceFile(suspended)
			if got := waitWorkspaceResult(t, sender, suspended.RequestID); got.OK {
				t.Fatal("suspended host reader delivered")
			}
			if err := host.explorer.Resume(id); err != nil {
				t.Fatal(err)
			}

			authority.stale = true
			read := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "stale", SessionID: id, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, Path: "README.md"}
			raw, _ = json.Marshal(read)
			manager.handleReadWorkspaceFile(raw)
			if got := waitWorkspaceResult(t, sender, "stale"); got.OK {
				t.Fatal("stale authority delivered bytes")
			}

			authority.stale = false
			var delivered []protocol.WorkspaceMemberResult
			service.Results = func(_ context.Context, _ string, _ protocol.WorkspaceRuntimeBinding, r []protocol.WorkspaceMemberResult) error {
				delivered = r
				return nil
			}
			host.Kill(id)
			if !local.state.TerminalAcknowledged || len(delivered) != len(manifest.Members) {
				t.Fatal("HOST_RESULTS: host cleanup skipped durable member finalization")
			}
			for _, result := range delivered {
				if result.Pending || result.Branch == "" || result.Head == "" {
					t.Fatalf("HOST_MEMBER_RESULT: incomplete host outcome: %+v", result)
				}
			}
			if registry.service.Lifecycle(id) != nil {
				t.Fatal("host cleanup retained shell registration")
			}
			for _, source := range local.state.Sources {
				if got := childRepoGit(t, source.Path, "branch", "--show-current"); got != "main" && got != "master" {
					t.Fatal("host lifecycle changed branch", got)
				}
			}
			if retained, err := selection.PinBound(t.Context(), manifest); err == nil {
				retained.Close()
				t.Fatal("ended host retained its selection handoff")
			}
		})
	}
}
func waitExplorerResult(t *testing.T, sender *agentTestSender, id string) protocol.WorkspaceExplorerResult {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		sender.mu.Lock()
		for _, message := range sender.msgs {
			if result, ok := message.(protocol.WorkspaceExplorerResult); ok && result.RequestID == id {
				sender.mu.Unlock()
				return result
			}
		}
		sender.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("explorer result not delivered")
	return protocol.WorkspaceExplorerResult{}
}

func TestWorkspaceExplorerProductionDockerImage(t *testing.T) {
	if os.Getenv("BLERG_EXPLORER_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_EXPLORER_DOCKER_TEST=1 with a locally built production sandbox image")
	}
	root := t.TempDir()
	initChildRepo(t, root)
	workspaceFixture(t, root, "README.md", "packaged runtime bytes")
	workspaceFixture(t, root, ".env", "protected")
	// Mode 0700 exercises the root helper's narrow read capability; the agent
	// runs non-root with no effective capabilities and cannot reach private tmp.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	label := testContainerLabel()
	dockerContext := strings.TrimSpace(dockerTest(t, "context", "show"))
	args := sandboxRunArgs("explorer-"+ccUUID(), root, "", nil, sandboxRunExtras{owner: sandboxOwner()})
	launchArgs := append([]string{}, args[:len(args)-3]...)
	launchArgs = append(launchArgs, "--label", label, "--network", "none", "--read-only", "--tmpfs", "/tmp:rw,size=268435456,nr_inodes=16384,mode=1777")
	launchArgs = append(launchArgs, args[len(args)-3:]...)
	id := strings.TrimSpace(dockerTest(t, append([]string{"--context", dockerContext}, launchArgs...)...))
	t.Cleanup(func() {
		if got := strings.TrimSpace(dockerTest(t, "--context", dockerContext, "inspect", "--format", `{{index .Config.Labels "blerg-test-run"}}`, id)); got != strings.TrimPrefix(label, "blerg-test-run=") {
			t.Fatal("test runtime ownership changed")
		}
		dockerTest(t, "--context", dockerContext, "rm", "-f", id)
	})
	start := legacyExplorerStart("image-session")
	remote := newDockerWorkspaceExplorer(dockerContext, id, start)
	t.Cleanup(func() {
		if err := remote.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, op := range []string{"list", "search", "status"} {
		req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: op, SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Operation: op}
		if op == "search" {
			req.Filter = "README"
		}
		got := remote.Explore(t.Context(), req)
		if !got.OK {
			t.Fatalf("packaged %s: %+v", op, got)
		}
		if op == "search" && (len(got.Entries) != 1 || got.Entries[0].Path != "README.md") {
			t.Fatal(got)
		}
	}
	read := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "viewer", SessionID: start.SessionID, WorkspaceRuntimeRef: start.Runtime, Path: "README.md"}
	if got := remote.ReadFile(t.Context(), read); !got.OK || got.ContentB64 != base64.StdEncoding.EncodeToString([]byte("packaged runtime bytes")) {
		t.Fatalf("packaged viewer: %+v", got)
	}
	// The immutable image helper is root-owned/executable, while agent execs have
	// no effective capabilities and cannot see its private root-owned snapshots.
	mode := dockerTest(t, "--context", dockerContext, "exec", id, "stat", "-c", "%u %g %a", workspaceExplorerBinary)
	if strings.TrimSpace(mode) != "0 0 755" {
		t.Fatalf("helper ownership: %s", mode)
	}
	caps := dockerTest(t, "--context", dockerContext, "exec", id, "sh", "-c", "grep '^CapEff:' /proc/self/status")
	if !strings.Contains(caps, "0000000000000000") {
		t.Fatalf("agent gained capabilities: %s", caps)
	}
	parent := dockerTest(t, "--context", dockerContext, "exec", "-u", "0", id, "stat", "-c", "%u %g %a", workspaceExplorerTempRoot)
	if strings.TrimSpace(parent) != "0 0 700" {
		t.Fatalf("agent can replace private snapshot parent: %s", parent)
	}
	private := dockerTest(t, "--context", dockerContext, "exec", id, "sh", "-c", "test ! -r /run/blerg-explorer && test ! -w /run/blerg-explorer && test ! -w /run && echo private")
	if strings.TrimSpace(private) != "private" {
		t.Fatal(private)
	}
}

func TestWorkspaceExplorerProductionUnbindJoinsHelper(t *testing.T) {
	root := t.TempDir()
	initChildRepo(t, root)
	remote := processExplorer(t, root, legacyExplorerStart("session"))
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "start", SessionID: "session", WorkspaceRuntimeRef: remote.start.Runtime, Operation: "list"}
	if res := remote.Explore(t.Context(), req); !res.OK {
		t.Fatal(res)
	}
	var router WorkspaceExplorerRouter
	router.Bind("session", remote)
	router.Unbind("session")
	if !remote.closed || remote.cmd != nil {
		t.Fatal("unbind left runtime helper running")
	}
}

func TestWorkspaceExplorerProductionDeliveryAuthority(t *testing.T) {
	root := t.TempDir()
	initChildRepo(t, root)
	workspaceFixture(t, root, "README.md", "do not deliver")
	remote := processExplorer(t, root, legacyExplorerStart("session"))
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: remote.start.Runtime, ManifestDigest: remote.start.Manifest.Digest, Root: workspaceRootIdentity(info)}
	calls := 0
	w := &LocalWorkspace{mu: &sync.Mutex{}, state: workspacePrivateManifest{Binding: &binding}, authorize: func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("replaced")
		}
		return nil
	}}
	reader := &boundWorkspaceExplorer{workspace: w, remote: remote}
	req := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "replaced", SessionID: "session", WorkspaceRuntimeRef: binding.WorkspaceRuntimeRef, Path: "README.md"}
	if got := reader.ReadFile(t.Context(), req); got.OK || got.ContentB64 != "" || calls < 2 {
		t.Fatalf("authority replaced during read: %+v calls=%d", got, calls)
	}
}
func TestWorkspaceExplorerProductionReplyCorrelation(t *testing.T) {
	for _, fault := range []string{"session", "request", "generation", "type"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			start := legacyExplorerStart("session")
			remote := processExplorer(t, root, start)
			original := remote.command
			remote.command = func() *exec.Cmd {
				cmd := original()
				cmd.Env = append(cmd.Env, "BLERG_EXPLORER_TEST_FAULT="+fault)
				return cmd
			}
			got := remote.ReadFile(t.Context(), protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "read", SessionID: "session", WorkspaceRuntimeRef: start.Runtime, Path: "README.md"})
			if got.OK || got.ContentB64 != "" {
				t.Fatalf("uncorrelated helper reply: %+v", got)
			}
		})
	}
}

func TestWorkspaceExplorerProductionCancelJoinsBeforeRestart(t *testing.T) {
	root := t.TempDir()
	workspaceFixture(t, root, "README.md", "after navigation")
	start := legacyExplorerStart("session")
	start.Manifest.Runtime = "host"
	start.Manifest.Kind = "folder"
	start.Manifest.Members = nil
	remote := processExplorer(t, root, start)
	original := remote.command
	first := true
	remote.command = func() *exec.Cmd {
		cmd := original()
		if first {
			first = false
			cmd.Env = append(cmd.Env, "BLERG_EXPLORER_TEST_FAULT=stall")
		}
		return cmd
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "navigation", SessionID: "session", WorkspaceRuntimeRef: start.Runtime, Path: "README.md"}
	finished := make(chan protocol.WorkspaceFileResult, 1)
	go func() { finished <- remote.ReadFile(ctx, req) }()
	until := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "helper-started")); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("helper never received read")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("navigation did not join helper")
	}
	if _, err := os.Stat(filepath.Join(root, "helper-joined")); err != nil {
		t.Fatal("request returned before helper cleanup acknowledgment")
	}
	if remote.cmd != nil || remote.failed {
		t.Fatal("canceled helper still active or runtime poisoned")
	}
	req.RequestID = "after-navigation"
	if got := remote.ReadFile(t.Context(), req); !got.OK {
		t.Fatalf("reader did not restart after joined cancellation: %+v", got)
	}
}

func TestWorkspaceExplorerProductionUnprovenLegacyCleanupBlocksSync(t *testing.T) {
	id := "fenced-reader-" + ccUUID()
	legacyExplorerFences.Store(id, true)
	defer legacyExplorerFences.Delete(id)
	// This refusal must precede even looking up a container or reading clone Git.
	if err := syncSandboxAtTurnEnd("/unavailable/agent-clone", id); err == nil || !strings.Contains(err.Error(), "explorer cleanup unproven") {
		t.Fatalf("unproven reader cleanup did not fence sync: %v", err)
	}
}

func TestWorkspaceExplorerProductionBindAfterShutdownClosesHelper(t *testing.T) {
	root := t.TempDir()
	initChildRepo(t, root)
	remote := processExplorer(t, root, legacyExplorerStart("session"))
	if res := remote.Explore(t.Context(), protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "start", SessionID: "session", WorkspaceRuntimeRef: remote.start.Runtime, Operation: "list"}); !res.OK {
		t.Fatal(res)
	}
	var router WorkspaceExplorerRouter
	router.Close()
	router.Bind("session", remote)
	if !remote.closed || remote.cmd != nil {
		t.Fatal("shutdown raced registration and leaked helper")
	}
}

type hostPreparationFaultAuthority struct {
	*hostExplorerAuthority
	aborted bool
}

func (a *hostPreparationFaultAuthority) Abort(context.Context, string, protocol.WorkspaceManifest, string, protocol.SupervisorLaunch) error {
	a.aborted = true
	return nil
}
func (a *hostPreparationFaultAuthority) CleanupLaunch(ctx context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	return a.Launch(ctx, id, m, token)
}
func (a *hostPreparationFaultAuthority) CleanupBind(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	return a.Bind(ctx, l, b)
}
