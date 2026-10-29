package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const workspaceWorkerFlag = "--blerg-workspace-worker"
const workspaceWorkerCleanup = 5 * time.Second

type workspaceWorkerRequest struct {
	QuotaBudget        protocol.WorkspaceBudget
	QuotaUID, QuotaGID int
	Action             string
	Deadline           time.Time
	State              workspacePrivateManifest
	Record             string
	Source             WorkspaceSource
	Clone              string
}
type workspaceWorkerResponse struct {
	QuotaDevice string
	State       workspacePrivateManifest
	Member      workspacePrivateMember
	Error       string
}
type workspaceWorker interface {
	Cleanup(context.Context, string, string) error
	Run(context.Context, workspaceWorkerRequest) (workspaceWorkerResponse, error)
}
type workspaceWorkerProvider interface{ workspaceWorker() workspaceWorker }
type workspaceWorkerUncertain struct{ err error }

func (e *workspaceWorkerUncertain) Error() string {
	return "workspace worker cleanup unproved: " + e.err.Error()
}
func (e *workspaceWorkerUncertain) Unwrap() error { return e.err }

func (w *LocalWorkspace) worker() workspaceWorker {
	if provider, ok := w.storage.(workspaceWorkerProvider); ok {
		return provider.workspaceWorker()
	}
	return dockerWorkspaceWorker{}
}

// WorkspaceWorkerMain is a private subprocess entry, before daemon provisioning.
// There is no weaker in-process production fallback. Linux workers run in their
// own Docker cgroup on all supported Docker engines; unsupported binaries refuse.
func WorkspaceWorkerMain(args []string) bool {
	if len(args) == 1 && args[0] == "--blerg-workspace-volume" {
		if workspaceWorkerMemoryBound() != nil {
			os.Exit(125)
		}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		<-stop
		return true
	}
	if len(args) != 1 || args[0] != workspaceWorkerFlag {
		return false
	}
	var request workspaceWorkerRequest
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		os.Exit(125)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		os.Exit(125)
	}
	if request.Deadline.IsZero() {
		os.Exit(125)
	}
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline)
	if err := workspaceWorkerMemoryBound(); err != nil {
		cancel()
		_ = json.NewEncoder(os.Stdout).Encode(workspaceWorkerResponse{Error: err.Error()})
		return true
	}
	if err := workspaceWorkerEntryBound(); err != nil {
		cancel()
		_ = json.NewEncoder(os.Stdout).Encode(workspaceWorkerResponse{Error: err.Error()})
		return true
	}
	response := executeWorkspaceWorker(ctx, request)
	cancel()
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(125)
	}
	return true
}

func executeWorkspaceWorker(ctx context.Context, request workspaceWorkerRequest) (response workspaceWorkerResponse) {
	raw, _ := json.Marshal(request.State)
	_ = json.Unmarshal(raw, &response.State)
	fail := func(err error) workspaceWorkerResponse { response.Error = err.Error(); return response }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if strings.HasPrefix(request.Action, "quota-") {
		if err := workspaceQuotaKey(request.Record, request.QuotaUID); err != nil {
			return fail(err)
		}
		storage := &FilesystemWorkspaceStorage{Root: "/quota"}
		_, _, _, tree, err := storage.paths(request.State.Session)
		if err != nil {
			return fail(err)
		}
		switch request.Action {
		case "quota-allocate":
			tree, err = storage.Allocate(ctx, request.State.Session, request.QuotaBudget)
			if err == nil {
				err = os.Chown(tree, request.QuotaUID, request.QuotaGID)
			}
		case "quota-recover":
			directory, _, _, _, pathErr := storage.paths(request.State.Session)
			if pathErr != nil {
				return fail(pathErr)
			}
			saved, readErr := readWorkspaceQuota(directory)
			if readErr != nil {
				return fail(readErr)
			}
			if saved.Session != request.State.Session || saved.Budget != request.QuotaBudget || (saved.Phase != "ready" && saved.Phase != "mount") {
				return fail(errors.New("quota recovery is not an existing allocation"))
			}
			err = storage.Validate(ctx, request.State.Session, tree, request.QuotaBudget)
		case "quota-validate":
			err = storage.Validate(ctx, request.State.Session, tree, request.QuotaBudget)
		case "quota-release":
			err = storage.Release(ctx, request.State.Session, tree)
		default:
			err = errors.New("unknown quota request")
		}
		if err != nil {
			return fail(err)
		}
		if request.Action != "quota-release" {
			response.State.RootIdentity, err = workspaceQuotaRootIdentity(tree, request.QuotaUID)
			if err == nil {
				response.QuotaDevice, err = workspaceQuotaDevice(filepath.Dir(tree))
			}
			if err != nil {
				return fail(err)
			}
		}
		return response
	}
	switch request.Action {
	case "prepare":
		source := request.Source
		if err := os.MkdirAll(filepath.Dir(request.Clone), 0700); err != nil {
			return fail(err)
		}
		common, err := hostgit.Output(ctx, hostgit.Options{Dir: source.Path, Timeout: sandboxGitTimeout}, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return fail(err)
		}
		common, err = filepath.EvalSymlinks(common)
		if err != nil {
			return fail(err)
		}
		tmp, err := os.CreateTemp("", "blerg-source-*")
		if err != nil {
			return fail(err)
		}
		name := tmp.Name()
		_ = tmp.Close()
		defer func() { _ = os.Remove(name) }()
		if request.State.Preparing && request.State.RuntimeIdentity == "" {
			if source.Member.Path == "." {
				entries, err := os.ReadDir(request.Clone)
				if err != nil {
					return fail(err)
				}
				root, err := os.OpenRoot(request.Clone)
				if err != nil {
					return fail(err)
				}
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						_ = root.Close()
						return fail(err)
					}
					if err := root.RemoveAll(entry.Name()); err != nil {
						_ = root.Close()
						return fail(err)
					}
				}
				_ = root.Close()
			} else if err := os.RemoveAll(request.Clone); err != nil {
				return fail(err)
			}
		}
		if _, err := createSandboxCloneAt(ctx, source.Path, request.State.Session, "", request.Clone, name, source.Member.Path == "."); err != nil {
			return fail(err)
		}
		raw, err := os.ReadFile(name) //nolint:gosec // private tempfile created by this bounded worker
		if err != nil {
			return fail(err)
		}
		var c sandboxClone
		if err := json.Unmarshal(raw, &c); err != nil {
			return fail(err)
		}
		m := workspacePrivateMember{Member: source.Member, Clone: request.Clone, Source: c, SourceCommon: common, LastSynced: c.Base}
		if err := pinWorkspaceMember(&m); err != nil {
			return fail(err)
		}
		refs, err := workspaceCloneRefs(ctx, m.Clone)
		if err != nil {
			return fail(err)
		}
		m.InitialRefsDigest = workspaceRefsDigest(refs, "refs/heads/"+sandboxBranch(request.State.Session))
		response.Member = m
		response.State.WorkerRoot, err = workspaceDirectoryIdentity(request.State.Root)
		if err != nil {
			return fail(err)
		}
	case "copy", "release-member":
		// The private launch record/key are read only by this trusted service, never
		// mounted in the agent container. Authenticate again inside the frozen copy.
		if err := os.Setenv(DaemonStateDirEnv, filepath.Dir(filepath.Dir(request.Record))); err != nil {
			return fail(err)
		}
		state, err := readPrivateWorkspace(request.Record)
		if err != nil || !reflect.DeepEqual(state, request.State) {
			return fail(errors.New("private workspace manifest changed"))
		}
		w := &LocalWorkspace{}
		_ = json.Unmarshal(raw, &w.state)
		w.state.RootIdentity = w.state.WorkerRoot
		for i := range w.state.Members {
			applyWorkerPins(&w.state.Members[i])
		}
		root, err := workspaceDirectoryIdentity(w.state.Root)
		if err != nil || root != w.state.RootIdentity {
			return fail(errors.New("worker root replaced"))
		}
		if request.Action == "release-member" {
			for _, m := range w.state.Members {
				if m.Clone == request.Clone && m.ReleasePending && m.SettledClean && m.SavedTreeHead != "" && m.SavedTreeHead == m.Result.Head && !m.Result.Pending {
					if _, err := os.Lstat(m.Clone); os.IsNotExist(err) {
						return response
					}
					if err := w.validateMember(m); err != nil {
						return fail(err)
					}
					if err := os.RemoveAll(m.Clone); err != nil {
						return fail(err)
					}
					return response
				}
			}
			return fail(errors.New("remote member release not authorized"))
		}
		if err := w.checkAggregateGitBudget(ctx); err != nil {
			return fail(err)
		}
		start := w.state.Next
		response.State.Next = (start + 1) % len(w.state.Members)
		for n := range w.state.Members {
			index := (start + n) % len(w.state.Members)
			m := &w.state.Members[index]
			if m.Released {
				continue
			}
			result := &response.State.Members[index]
			if err := ctx.Err(); err != nil {
				result.Result.Pending = true
				continue
			}
			err := w.validateMember(*m)
			if err == nil {
				head, ok := readCloneFile(m.Clone, "HEAD", 512)
				if !ok || strings.TrimSpace(head) != "ref: refs/heads/"+sandboxBranch(w.state.Session) {
					err = errors.New("member HEAD is outside the session branch; work retained")
				}
			}
			if request.State.Stopped {
				result.SettledClean = false
				result.SavedTreeHead = ""
			}
			if err == nil && request.State.Stopped {
				clean, settleErr := settleWorkspaceMember(ctx, m, w.state.Session)
				if settleErr != nil {
					err = settleErr
				} else {
					result.SettledClean = clean
				}
			}
			if err == nil {
				_, err = fetchSandboxCloneContext(ctx, m.Clone, w.state.Session, &m.Source)
			}
			if err != nil {
				result.Result.SyncError = err.Error()
				result.Result.Pending = true
				continue
			}
			head, ok := readCloneRef(m.Clone, "refs/heads/"+sandboxBranch(w.state.Session))
			if !ok && m.Source.Base != "" {
				result.Result.SyncError = "member branch unavailable"
				result.Result.Pending = true
				continue
			}
			if request.State.Stopped && result.SettledClean {
				fetched, err := hostgit.Output(ctx, hostgit.Options{Dir: m.Source.Repo, Timeout: sandboxGitTimeout}, "rev-parse", "--verify", "refs/heads/"+sandboxBranch(w.state.Session))
				if err != nil || fetched != head {
					result.SettledClean = false
					result.Result.Pending = true
					result.Result.SyncError = "final source tree not proved saved"
					continue
				}
				if err := workspaceVerifyFetchedBytes(ctx, *m, head); err != nil {
					result.SettledClean = false
					result.Result.Pending = true
					result.Result.SyncError = err.Error()
					continue
				}
				result.SavedTreeHead = head
			}
			result.LastSynced = head
			result.Result.Head = head
			result.Result.Pending = false
			result.Result.SyncError = ""
		}
	default:
		return fail(errors.New("unknown workspace worker operation"))
	}
	return response
}

type dockerWorkspaceWorker struct{}

func (dockerWorkspaceWorker) Run(ctx context.Context, request workspaceWorkerRequest) (response workspaceWorkerResponse, err error) {
	if err := ctx.Err(); err != nil {
		return response, err
	}
	workerImage, binary, err := workspaceWorkerProgram()
	if err != nil {
		return response, err
	}
	dockerContext := request.State.WorkerContext
	if dockerContext == "" {
		return response, errors.New("workspace Docker context unavailable")
	}
	entryPolicy, err := workspaceEntrySecurityOption()
	if err != nil {
		return response, err
	}
	label := "blerg-workspace-worker=" + request.State.ActiveWorker
	if request.State.ActiveWorker == "" || request.State.WorkerContext != dockerContext {
		return response, errors.New("worker journal/context missing")
	}
	args := []string{"--context", dockerContext, "create", "-i", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt", entryPolicy, "--init", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--label", label, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,size=16777216,nr_inodes=1024,mode=1777", "--env", "GOMEMLIMIT=256MiB", "--entrypoint", "/usr/local/bin/blerg-workspace-worker"}
	if binary != "" {
		args = append(args, "--mount", sandboxBindMount(binary, "/usr/local/bin/blerg-workspace-worker")+",readonly")
		workerImage = sandboxImage()
	}
	mounts := map[string]bool{}
	rootSource := request.State.Root
	if request.State.RemoteMountSource != "" {
		rootSource = request.State.RemoteMountSource
	}
	rootMount := workspaceStorageMount(rootSource, request.State.Root)
	args = append(args, "--mount", rootMount)
	sources := request.State.Sources
	if request.Action == "prepare" {
		sources = []WorkspaceSource{request.Source}
	}
	for _, source := range sources {
		mounts[source.Path] = true
		common := source.CommonPath
		if common == "" {
			common = filepath.Join(source.Path, ".git")
		}
		mounts[common] = request.Action != "copy"
	}
	if request.Record != "" {
		mounts[filepath.Dir(request.Record)] = true
	}
	paths := make([]string, 0, len(mounts))
	for p := range mounts {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		spec := sandboxBindMount(p, p)
		if mounts[p] {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	args = append(args, workerImage, workspaceWorkerFlag)
	raw, err := workspaceDockerOutput(ctx, args...)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceWorkerCleanup)
		defer cancel()
		if cleanupErr := (dockerWorkspaceWorker{}).Cleanup(cleanup, request.State.ActiveWorker, dockerContext); cleanupErr != nil {
			return response, &workspaceWorkerUncertain{err: cleanupErr}
		}
		return response, err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != 64 {
		return response, errors.New("invalid workspace worker ID")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceWorkerCleanup)
		defer cancel()
		// Remove only the exact ID we created, after checking its unique label.
		raw, inspectErr := workspaceDockerOutput(cleanup, "--context", dockerContext, "inspect", "--format", `{{index .Config.Labels "blerg-workspace-worker"}}`, id)
		if inspectErr == nil && strings.TrimSpace(string(raw)) == strings.TrimPrefix(label, "blerg-workspace-worker=") {
			_, removeErr := workspaceDockerOutput(cleanup, "--context", dockerContext, "rm", "-f", id)
			gone, goneErr := workspaceDockerOutput(cleanup, "--context", dockerContext, "ps", "-aq", "--no-trunc", "--filter", "id="+id)
			if removeErr == nil && goneErr == nil && strings.TrimSpace(string(gone)) == "" {
				return
			}
		}
		err = &workspaceWorkerUncertain{err: errors.Join(err, inspectErr, errors.New("worker removal not proved"))}
	}()
	request.Deadline, _ = ctx.Deadline()
	if request.Deadline.IsZero() {
		return response, errors.New("workspace worker deadline required")
	}
	input, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	cmd := exec.CommandContext(ctx, "docker", "--context", dockerContext, "start", "-a", "-i", id) //nolint:gosec // exact created ID, fixed command, bounded context
	cmd.Stdin = bytes.NewReader(input)
	output := &workspaceBoundedBuffer{limit: 2 << 20}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return response, err
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		return response, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func workspaceDockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed binary, daemon-built argv
	cmd.WaitDelay = time.Second
	out := &workspaceBoundedBuffer{limit: 2 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return out.Bytes(), err
}

type workspaceBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *workspaceBoundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("workspace worker output limit exceeded")
	}
	return b.Buffer.Write(p)
}

type workspaceMemberPins struct{ Clone, Git, Objects, Source, SourceObjects, Common protocol.WorkspaceRoot }

func pinWorkspaceMember(m *workspacePrivateMember) error {
	for _, pin := range []struct {
		path     string
		identity *protocol.WorkspaceRoot
	}{
		{m.Clone, &m.CloneIdentity}, {filepath.Join(m.Clone, ".git"), &m.GitIdentity}, {filepath.Join(m.Clone, ".git", "objects"), &m.ObjectsIdentity}, {m.Source.Repo, &m.SourceIdentity}, {m.Source.Objects, &m.SourceObjectsIdentity}, {m.SourceCommon, &m.SourceCommonIdentity},
	} {
		id, err := workspaceDirectoryIdentity(pin.path)
		if err != nil {
			return err
		}
		*pin.identity = id
	}
	return nil
}
func workerPins(m workspacePrivateMember) workspaceMemberPins {
	return workspaceMemberPins{m.CloneIdentity, m.GitIdentity, m.ObjectsIdentity, m.SourceIdentity, m.SourceObjectsIdentity, m.SourceCommonIdentity}
}
func applyWorkerPins(m *workspacePrivateMember) {
	p := m.WorkerPins
	m.CloneIdentity = p.Clone
	m.GitIdentity = p.Git
	m.ObjectsIdentity = p.Objects
	m.SourceIdentity = p.Source
	m.SourceObjectsIdentity = p.SourceObjects
	m.SourceCommonIdentity = p.Common
}

func (dockerWorkspaceWorker) Cleanup(ctx context.Context, token, dockerContext string) error {
	if token == "" || dockerContext == "" {
		return errors.New("worker cleanup lacks binding")
	}
	raw, err := workspaceDockerOutput(ctx, "--context", dockerContext, "ps", "-aq", "--no-trunc", "--filter", "label=blerg-workspace-worker="+token)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(raw)) {
		if len(id) != 64 {
			return errors.New("invalid worker cleanup ID")
		}
		if _, err := workspaceDockerOutput(ctx, "--context", dockerContext, "rm", "-f", id); err != nil {
			return err
		}
	}
	raw, err = workspaceDockerOutput(ctx, "--context", dockerContext, "ps", "-aq", "--filter", "label=blerg-workspace-worker="+token)
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		return errors.New("worker cleanup absence unproved")
	}
	return nil
}

func (w *LocalWorkspace) beginWorker(ctx context.Context) error {
	if w.state.ActiveWorker != "" {
		return errors.New("previous worker needs recovery")
	}
	w.state.ActiveWorker = ccUUID()
	w.state.WorkerContext = w.state.RuntimeContext
	if w.state.WorkerContext == "" {
		if dockerContext, id, ok := strings.Cut(w.state.RuntimeIdentity, ":"); ok && len(id) == 64 {
			w.state.WorkerContext = dockerContext
		}
	}
	if remote, ok := w.storage.(workspaceRemoteStorage); ok {
		var err error
		w.state.WorkerContext, err = remote.DockerContext(ctx, w.state.Session, w.state.Root)
		if err != nil {
			return err
		}
	}
	if w.state.WorkerContext == "" {
		w.state.WorkerContext = currentDockerContext()
	}
	if _, testProvider := w.storage.(workspaceWorkerProvider); testProvider && w.state.WorkerContext == "" {
		w.state.WorkerContext = "test-peer"
	}
	if w.state.WorkerContext == "" {
		return errors.New("worker context unavailable")
	}
	return w.save()
}
func (w *LocalWorkspace) runWorker(ctx context.Context, request workspaceWorkerRequest) (workspaceWorkerResponse, error) {
	if storage, ok := w.storage.(workspaceRemoteStorage); ok {
		source, err := storage.MountSource(ctx, w.state.Session, w.state.Root)
		if err != nil {
			return workspaceWorkerResponse{}, err
		}
		w.state.RemoteMountSource = source
		if err := w.save(); err != nil {
			return workspaceWorkerResponse{}, err
		}
		request.State = w.state
	}

	request.State = w.state
	response, err := w.worker().Run(ctx, request)
	var uncertain *workspaceWorkerUncertain
	if !errors.As(err, &uncertain) {
		if response.State.Session == w.state.Session {
			response.State.ActiveWorker = ""
		}
		w.state.ActiveWorker = ""
	}
	return response, err
}

func workspaceWorkerMemoryBound() error {
	// Kernel cgroup accounting includes the worker, Git, upload-pack, pack-objects
	// and every descendant together, on Linux Docker and Docker Desktop alike.
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes", "/sys/fs/cgroup/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(path) //nolint:gosec // fixed list of kernel cgroup memory-limit files
		if err != nil {
			continue
		}
		limit, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err == nil && limit > 0 && limit <= 512<<20 {
			return nil
		}
	}
	return errors.New("aggregate workspace worker memory authority unavailable")
}

// A macOS host selects the Linux image; a Linux daemon may mount its own ELF
// binary. Both paths execute only in the same constrained Docker cgroup.
func workspaceWorkerProgram() (string, string, error) {
	return workspaceWorkerProgramForHost(runtime.GOOS)
}
func workspaceWorkerProgramForHost(hostOS string) (image, binary string, err error) {
	image = os.Getenv("BLERG_WORKSPACE_WORKER_IMAGE")
	if image == "" && hostOS == "linux" {
		binary, err = os.Executable()
		image = sandboxImage()
	}
	if image == "" {
		image = "blerg-workspace-worker:latest"
	}
	return
}
func workspaceDockerRequest(ctx context.Context, args []string, input []byte) (response workspaceWorkerResponse, err error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed Docker executable and trusted exact-ID argv
	cmd.Stdin = bytes.NewReader(input)
	output := &workspaceBoundedBuffer{limit: 2 << 20}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return response, err
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		return response, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
