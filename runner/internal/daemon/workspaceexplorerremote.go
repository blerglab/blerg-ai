package daemon

// The Docker client carries private helper pipes only. Repository traversal and
// Git run in the container, including for legacy single-repository sessions.
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const workspaceExplorerBinary = "/usr/local/bin/blerg-workspace-explorer"
const workspaceExplorerTempRoot = "/run/blerg-explorer"

// The parent namespace must be root-owned as well as the snapshots themselves.
// An agent-owned /tmp could rename a root-owned child and replace its pathname.
const workspaceExplorerTempMount = workspaceExplorerTempRoot + ":rw,size=268435456,nr_inodes=200000,uid=0,gid=0,mode=0700,noexec,nosuid,nodev"

// helperPrepared is private launcher state. Retain every pinned member across
// helper restarts; navigation cancellation must never adopt a replaced member.
type helperPrepared struct {
	protocol.WorkspaceRuntimePrepared
	Members  map[string]protocol.WorkspaceRoot `json:"member_roots"`
	GitRoots map[string]protocol.WorkspaceRoot `json:"git_roots"`
	Pins     map[string]WorkspaceGitPin        `json:"git_pins,omitempty"`
}

type remoteWorkspaceExplorer struct {
	mu             sync.Mutex
	command        func() *exec.Cmd
	start          WorkspaceExplorerStart
	expected       *helperPrepared
	cmd            *exec.Cmd
	input          *os.File
	output         *os.File
	scanner        *bufio.Scanner
	closed, failed bool
}

func (r *remoteWorkspaceExplorer) startHelper(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed || r.failed {
		return errors.New("explorer helper fenced")
	}
	if r.cmd != nil {
		return nil
	}
	cmd := r.command()
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return err
	}
	r.cmd = cmd
	// Go-created pipes support deadlines; do not use inherited blocking fd wrappers.
	r.input, _ = input.(*os.File)
	r.output, _ = output.(*os.File)
	if r.input == nil || r.output == nil {
		_ = r.stopHelper()
		return errors.New("explorer requires private pipes")
	}
	r.scanner = bufio.NewScanner(r.output)
	r.scanner.Buffer(make([]byte, 4096), 40<<20) // viewer's 25 MiB binary cap plus base64/JSON
	start := r.start
	if r.expected != nil {
		start.Expected = r.expected
	}
	var prepared helperPrepared
	if err = r.exchange(ctx, start, &prepared); err != nil {
		_ = r.stopHelper()
		return err
	}
	if prepared.Type != "workspace_runtime_prepared" || prepared.SessionID != start.SessionID || prepared.Binding.Validate(start.Manifest) != nil || prepared.Binding.WorkspaceRuntimeRef != start.Runtime {
		_ = r.stopHelper()
		return errors.New("invalid explorer helper binding")
	}
	if r.expected != nil && (prepared.Binding != r.expected.Binding || !equalWorkspaceRoots(prepared.Members, r.expected.Members) || !equalWorkspaceRoots(prepared.GitRoots, r.expected.GitRoots)) {
		_ = r.stopHelper()
		return errors.New("explorer helper root changed")
	}
	r.expected = &prepared
	r.start.GitPins = prepared.Pins
	return nil
}
func equalWorkspaceRoots(a, b map[string]protocol.WorkspaceRoot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func (r *remoteWorkspaceExplorer) exchange(ctx context.Context, req, reply any) error {
	deadline := time.Now().Add(workspaceReplyTTL)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := r.input.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := r.output.SetReadDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = r.output.SetReadDeadline(time.Now())
		_ = r.input.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = r.input.SetWriteDeadline(time.Time{})
		_ = r.output.SetReadDeadline(time.Time{})
	}()
	if err := json.NewEncoder(r.input).Encode(req); err != nil {
		return err
	}
	if !r.scanner.Scan() {
		return errors.New("explorer helper reply unavailable")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return json.Unmarshal(r.scanner.Bytes(), reply)
}

// EOF cancels and joins the helper's router/Git workers. Wait must acknowledge
// helper exit before releasing the lifecycle lock. Killing only docker's host
// client is never accepted as a cleanup proof.
func (r *remoteWorkspaceExplorer) stopHelper() error {
	if r.cmd == nil {
		return nil
	}
	_ = r.input.Close()
	// Drain a reply already in flight while EOF cancels the router. Closing stdout
	// early could turn ordinary navigation cancellation into a broken-pipe exit.
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r.output); close(drained) }()
	cmd := r.cmd
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill() // only our Docker client; the runtime remains fenced
		<-done
		err = errors.New("explorer helper exit unproven")
	}
	_ = r.output.Close()
	<-drained
	r.cmd = nil
	r.input = nil
	r.output = nil
	r.scanner = nil
	if err != nil {
		r.failed = true
	}
	return err
}
func (r *remoteWorkspaceExplorer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.stopHelper()
}
func (r *remoteWorkspaceExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := explorerFailure(req, "unavailable")
	if req.SessionID != r.start.SessionID || req.WorkspaceRuntimeRef != r.start.Runtime {
		return result
	}
	if r.startHelper(ctx) != nil {
		return result
	}
	var reply protocol.WorkspaceExplorerResult
	if r.exchange(ctx, req, &reply) != nil {
		_ = r.stopHelper()
		return result
	}
	if reply.Type != result.Type || reply.RequestID != req.RequestID || reply.SessionID != req.SessionID || reply.WorkspaceRuntimeRef != req.WorkspaceRuntimeRef || reply.Operation != req.Operation {
		_ = r.stopHelper()
		return result
	}
	return reply
}
func (r *remoteWorkspaceExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	if req.SessionID != r.start.SessionID || req.WorkspaceRuntimeRef != r.start.Runtime {
		return result
	}
	if r.startHelper(ctx) != nil {
		return result
	}
	var reply protocol.WorkspaceFileResult
	if r.exchange(ctx, req, &reply) != nil {
		_ = r.stopHelper()
		return result
	}
	if reply.Type != result.Type || reply.RequestID != req.RequestID || reply.SessionID != req.SessionID || reply.WorkspaceRuntimeRef != req.WorkspaceRuntimeRef {
		_ = r.stopHelper()
		return result
	}
	return reply
}

// An immutable Docker ID and pinned context come from the trusted launcher.
// Run as root with the sandbox's dropped capabilities/no-new-privileges: private
// status snapshots and pipe fds are inaccessible to the unprivileged agent UID.
func newDockerWorkspaceExplorer(dockerContext, id string, start WorkspaceExplorerStart) *remoteWorkspaceExplorer {
	return &remoteWorkspaceExplorer{start: start, command: func() *exec.Cmd {
		return exec.Command("docker", "--context", dockerContext, "exec", "-i", "-u", "0", "-w", "/", id, "/usr/bin/env", "-i", "PATH=/usr/bin:/bin", "TMPDIR="+workspaceExplorerTempRoot, workspaceExplorerBinary, "--public-root", sandboxWorkdir) //nolint:gosec,noctx // fixed trusted argv; EOF cancels and joins the in-container helper, request cancellation must not kill only its Docker client
	}}
}

func legacyExplorerStart(id string) WorkspaceExplorerStart {
	return WorkspaceExplorerStart{SessionID: id, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "legacy-" + id, Generation: 1}, Manifest: protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "legacy-runtime", Runtime: "sandbox", LaunchGeneration: "legacy-" + id, Digest: "private-legacy-reader", Members: []protocol.WorkspaceMember{{ID: "repo", Name: "repository", Path: ".", SourceIdentity: "legacy-source", CommonDirectoryIdentity: "legacy-common", Destination: "legacy-destination"}}}}
}

// legacySandboxExplorer opens no host repository metadata. The session's Docker
// registry is trusted; the first read pins its immutable ID and actual runtime
// root. Public legacy requests keep their zero runtime reference.
var legacyExplorerFences sync.Map

func legacyExplorerReady(id string) error {
	if _, fenced := legacyExplorerFences.Load(id); fenced {
		return errors.New("sandbox explorer cleanup unproven; workspace retained")
	}
	return nil
}

type legacySandboxExplorer struct {
	mu        sync.Mutex
	id, alias string
	live      func() bool
	remote    *remoteWorkspaceExplorer
	closed    bool
}

func (l *legacySandboxExplorer) reader(ctx context.Context) *remoteWorkspaceExplorer {
	if l.closed || !l.live() {
		return nil
	}
	if l.remote != nil {
		return l.remote
	}
	container, ok := sandboxContainer(l.id)
	if !ok {
		return nil
	}
	args := pinnedDockerArgs(container, "inspect", "--type", "container", container)
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // trusted registered container; no shell
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	var values []struct {
		ID     string
		Config struct{ Labels map[string]string }
		State  struct{ Running, Paused bool }
	}
	if json.Unmarshal(raw, &values) != nil || len(values) != 1 {
		return nil
	}
	v := values[0]
	contextArgs := pinnedDockerArgs(container)
	if len(contextArgs) != 2 || len(v.ID) != 64 || !v.State.Running || v.State.Paused || v.Config.Labels[sandboxOwnerLabel] != sandboxOwner() || v.Config.Labels[sandboxContextLabel] != contextArgs[1] {
		return nil
	}
	l.remote = newDockerWorkspaceExplorer(contextArgs[1], v.ID, legacyExplorerStart(l.id))
	return l.remote
}
func (l *legacySandboxExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	return explorerFailure(req, "unavailable")
}
func (l *legacySandboxExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	result := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	lock := workspaceLock(l.id)
	if !lock.TryLock() {
		result.ErrorCode = "busy"
		return result
	}
	defer lock.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if req.SessionID != l.id || req.LaunchGeneration != "" || req.Generation != 0 {
		return result
	}
	name, ok := runtimeFileReference(req.Path, l.alias, sandboxWorkdir)
	if !ok {
		result.ErrorCode = "forbidden"
		return result
	}
	remote := l.reader(ctx)
	if remote == nil {
		return result
	}
	inner := req
	inner.Type = "read_workspace_file"
	inner.Path = name
	inner.WorkspaceRuntimeRef = remote.start.Runtime
	result = remote.ReadFile(ctx, inner)
	if remote.failed {
		legacyExplorerFences.Store(l.id, true)
	}
	result.WorkspaceRuntimeRef = req.WorkspaceRuntimeRef
	if strings.HasPrefix(req.Path, "/") {
		result.RequestedPath = filepath.Clean(req.Path)
	}
	if !l.live() {
		result.OK = false
		result.ContentB64 = ""
		result.ErrorCode = "unavailable"
	}
	return result
}
func (l *legacySandboxExplorer) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.remote != nil {
		err := l.remote.Close()
		if err != nil {
			legacyExplorerFences.Store(l.id, true)
		}
		return err
	}
	return nil
}
func runtimeFileReference(name string, aliases ...string) (string, bool) {
	if strings.HasPrefix(name, "/") {
		if strings.HasPrefix(name, "//") {
			return "", false
		}
		for _, alias := range aliases {
			if alias != "" && strings.HasPrefix(name, strings.TrimSuffix(alias, "/")+"/") {
				return workspaceCleanRequest(strings.TrimPrefix(name, strings.TrimSuffix(alias, "/")+"/"))
			}
		}
		return "", false
	}
	return workspaceCleanRequest(name)
}

var _ io.Closer = (*remoteWorkspaceExplorer)(nil)
