package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
)

const workspacePackServerFlag = "--blerg-workspace-pack-server"
const workspacePackWriterFlag = "--blerg-workspace-pack-writer"
const workspacePackControlLimit = 3 << 20
const workspacePackByteLimit int64 = 1 << 30

const workspaceBorrowedRecovery = "this older workspace could not verify its local Git objects: objects may be missing or historical commits may fail strict validation; work is retained. Start a new session for future work. Stop this session and recover its retained allocation into an independent repository before importing these commits; do not delete the allocation"

// Migration never mounts or reads the source. It succeeds only if all objects,
// including reflog and dangling objects, are already in the frozen allocation.
func migrateWorkspaceObjects(ctx context.Context, path string, c *sandboxClone) error {
	if c.OwnObjects {
		return nil
	}
	independent := *c
	independent.OwnObjects = true
	if err := resetCloneAlternates(path, &independent); err != nil {
		return err
	}
	_, err := hostgit.Output(ctx, hostgit.Options{Dir: path, Timeout: sandboxGitTimeout, Untrusted: true}, "fsck", "--full", "--strict")
	if err == nil {
		err = dissociateClone(ctx, path, &independent)
	}
	if err != nil {
		// resetCloneAlternates installed this plain file while writers are frozen.
		// Restore only the daemon-recorded dependency; never agent-supplied bytes.
		root, openErr := os.OpenRoot(filepath.Join(path, ".git", "objects", "info"))
		if openErr != nil {
			return errors.Join(fmt.Errorf("%s; retained path: %s", workspaceBorrowedRecovery, path), openErr)
		}
		defer func() { _ = root.Close() }()
		restoreErr := root.WriteFile("alternates", []byte(c.Objects+"\n"), 0644)
		return errors.Join(fmt.Errorf("%s; retained path: %s", workspaceBorrowedRecovery, path), restoreErr)
	}
	*c = independent
	return nil
}

func copyWorkspacePack(dst io.Writer, src io.Reader, limit int64) error {
	if _, err := io.Copy(dst, io.LimitReader(src, limit)); err != nil {
		return err
	}
	var extra [1]byte
	n, err := src.Read(extra[:])
	if n != 0 {
		return errors.New("workspace pack exceeds the size limit; work retained")
	}
	if err != io.EOF {
		return errors.Join(errors.New("workspace pack stream did not finish; work retained"), err)
	}
	return nil
}

// Only bounded control messages use this stream. Packs flow through OS pipes.
type workspacePackMessage struct {
	Clone    string `json:",omitempty"`
	Kind     string
	Repo     string                   `json:",omitempty"`
	Branch   string                   `json:",omitempty"`
	Target   string                   `json:",omitempty"`
	Value    string                   `json:",omitempty"`
	Error    string                   `json:",omitempty"`
	Response *workspaceWorkerResponse `json:",omitempty"`
}

type workspacePackStream struct {
	reader  *bufio.Reader
	encoder *json.Encoder
}

func (s *workspacePackStream) send(m workspacePackMessage) error {
	return s.encoder.Encode(m)
}
func (s *workspacePackStream) receive() (m workspacePackMessage, err error) {
	for {
		var line []byte
		line, err = s.reader.ReadSlice('\n')
		if err != nil {
			return m, err
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		err = json.Unmarshal(line, &m)
		return m, err
	}
}

func (s *workspacePackStream) fetch(_ context.Context, _ time.Duration, path, _ string, c *sandboxClone, branch, target string) (string, error) {
	if !c.OwnObjects {
		return "", errors.New(workspaceBorrowedRecovery)
	}
	if err := s.send(workspacePackMessage{Kind: "pack", Repo: c.Repo, Clone: path, Branch: branch, Target: target}); err != nil {
		return "", err
	}
	done, err := s.receive()
	if err != nil {
		return "", err
	}
	if done.Kind != "done" {
		return "", errors.New("pack writer failed; work retained")
	}
	if done.Error != "" {
		return "", errors.New(done.Error)
	}
	return done.Value, nil
}

// Hash the tree and every blob as well as verifying retained bytes locally. The
// source and allocation never need to be visible to the same Git process.
type workspaceDigestWriter struct{ hash.Hash }

func (w workspaceDigestWriter) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return w.Write(p)
}

func workspaceTreeDigest(ctx context.Context, repo, head string) (string, error) {
	h := workspaceDigestWriter{sha256.New()}
	_, _ = h.Write([]byte(head + "\x00"))
	options := hostgit.Options{Dir: repo, Timeout: sandboxGitTimeout, Untrusted: true, MemoryLimit: true, LimitKB: sandboxFetchMemoryKB}
	tree, err := hostgit.Output(ctx, options, "ls-tree", "-rz", "--full-tree", head)
	if err != nil {
		return "", err
	}
	_, _ = h.Write([]byte(tree))
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		meta, _, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" {
			// A gitlink records an external commit, not an object in this store.
			// Its identity is already included in the tree digest above.
			continue
		}
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return "", errors.New("saved tree entry unsupported")
		}
		blob := workspaceDigestWriter{sha256.New()}
		options.Stdout = blob
		if err := hostgit.Run(ctx, options, "cat-file", "blob", fields[2]); err != nil {
			return "", err
		}
		_, _ = h.Write(blob.Sum(nil))
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func workspacePackServer(ctx context.Context, path string) error {
	serve, err := hostgit.ServeCommand("upload-pack")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+serve+" "+packShellQuote(filepath.Join(path, ".git"))) //nolint:gosec // hardened ServeCommand and quoted validated clone path, server container only
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.Discard
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

func workspacePackWriter(ctx context.Context, repo, branch, target string) error {
	relay := "/bin/sh -c " + packShellQuote(sandboxPackRelay)
	_, err := hostgit.Output(ctx, hostgit.Options{Dir: repo, Protocols: []string{"file"}, ExtraFiles: []*os.File{os.Stdin, os.Stdout}, Config: []string{"fetch.fsckObjects=true", "transfer.fsckObjects=true", "fetch.unpackLimit=0"}, MemoryLimit: true, LimitKB: sandboxFetchMemoryKB, Untrusted: true, Timeout: sandboxFetchTimeout},
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--no-write-commit-graph", "--upload-pack="+relay, "file:///blerg-pack.git", "+"+branch+":"+target)
	return err
}

func workspacePackWriterArgs(request workspaceWorkerRequest, source WorkspaceSource, repo, branch, target, name, image, binary, policy string) []string {
	args := []string{"--context", request.State.WorkerContext, "run", "--rm", "-i", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt", policy, "--init", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--label", "blerg-workspace-worker=" + request.State.ActiveWorker, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,size=16777216,nr_inodes=1024,mode=1777", "--env", "GOMEMLIMIT=256MiB", "--mount", sandboxBindMount(source.Path, source.Path) + ",readonly"}
	common := source.CommonPath
	if common == "" {
		common = filepath.Join(source.Path, ".git")
	}
	args = append(args, "--mount", sandboxBindMount(common, common))
	if binary != "" {
		args = append(args, "--mount", sandboxBindMount(binary, "/usr/local/bin/blerg-workspace-worker")+",readonly")
		image = sandboxImage()
	}
	// An independent in-container timeout bounds a disconnected Docker client.
	args = append(args, "--entrypoint", "/usr/bin/timeout", image, "--signal=KILL", workspacePackPeerTimeout(request), "/usr/local/bin/blerg-workspace-worker", workspacePackWriterFlag, repo, branch, target)
	return args
}

func runWorkspaceCopyStream(ctx context.Context, request workspaceWorkerRequest, id string) (response workspaceWorkerResponse, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "--context", request.State.WorkerContext, "start", "-a", "-i", id) //nolint:gosec // exact created container ID
	input, err := cmd.StdinPipe()
	if err != nil {
		return response, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return response, err
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return response, err
	}
	defer func() {
		_ = input.Close()
		if err != nil {
			cancel()
		}
		err = errors.Join(err, cmd.Wait())
		_ = output.Close()
	}()
	stream := &workspacePackStream{reader: bufio.NewReaderSize(output, workspacePackControlLimit), encoder: json.NewEncoder(input)}
	if err := json.NewEncoder(input).Encode(request); err != nil {
		return response, err
	}
	for {
		message, err := stream.receive()
		if err != nil {
			return response, err
		}
		if message.Kind == "response" && message.Response != nil {
			response = *message.Response
			if response.Error != "" {
				return response, errors.New(response.Error)
			}
			return response, nil
		}
		if message.Kind != "pack" {
			return response, errors.New("invalid workspace pack request")
		}
		// A compromised server cannot select arbitrary host paths or refs for the writer.
		var source *WorkspaceSource
		var clone string
		for _, member := range request.State.Members {
			if member.Source.Repo == message.Repo && member.Clone == message.Clone {
				clone = member.Clone
				for i := range request.State.Sources {
					if request.State.Sources[i].Path == member.Source.Repo {
						source = &request.State.Sources[i]
						break
					}
				}
			}
		}
		branch := "refs/heads/" + workbranch.Effective(request.State.Session, request.State.WorkBranch)
		if source == nil || message.Branch != branch || (message.Target != branch && message.Target != branch+".incoming") {
			return response, errors.New("unauthorized pack destination")
		}
		// All person-repository queries run here under hostgit's hardened policy,
		// never in the allocation-mounted control worker.
		checked, err := checkedOutAnywhereContext(ctx, source.Path, branch)
		if err != nil {
			if err := stream.send(workspacePackMessage{Kind: "done", Error: "repository branch could not be checked; work retained"}); err != nil {
				return response, err
			}
			continue
		}
		target := branch
		if checked {
			target += ".incoming"
		}
		image, binary, err := workspaceWorkerProgram()
		if err != nil {
			return response, err
		}
		policy, err := workspaceEntrySecurityOption()
		if err != nil {
			return response, err
		}
		name := "blerg-workspace-pack-" + ccUUID()
		args := workspacePackWriterArgs(request, *source, message.Repo, branch, target, name, image, binary, policy)
		serverName := "blerg-workspace-pack-" + ccUUID()
		serverArgs := workspacePackServerArgs(request, clone, serverName, image, binary, policy)
		done := workspacePackMessage{Kind: "done"}
		if err := runWorkspacePackPair(ctx, request, serverArgs, args, serverName, name); err != nil {
			var uncertain *workspaceWorkerUncertain
			if errors.As(err, &uncertain) {
				return response, err
			}
			done.Error = err.Error()
		} else if checked {
			done.Error = (&sandboxCheckedOutError{sessionID: request.State.Session}).Error()
		} else {
			head, readErr := hostgit.Output(ctx, hostgit.Options{Dir: source.Path, Timeout: sandboxGitTimeout}, "rev-parse", "--verify", branch)
			if readErr == nil {
				done.Value, readErr = workspaceTreeDigest(ctx, source.Path, head)
			}
			if readErr != nil {
				done.Error = "saved source tree could not be verified; work retained"
			}
		}
		if err := stream.send(done); err != nil {
			return response, err
		}
	}
}

func workspacePackServerArgs(request workspaceWorkerRequest, clone, name, image, binary, policy string) []string {
	args := []string{"--context", request.State.WorkerContext, "run", "--rm", "-i", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--security-opt", policy, "--init", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--label", "blerg-workspace-worker=" + request.State.ActiveWorker, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,size=16777216,nr_inodes=1024,mode=1777", "--env", "GOMEMLIMIT=256MiB"}
	rootSource := request.State.Root
	if request.State.RemoteMountSource != "" {
		rootSource = request.State.RemoteMountSource
	}
	args = append(args, "--mount", workspaceStorageMount(rootSource, request.State.Root)+",readonly")
	if binary != "" {
		args = append(args, "--mount", sandboxBindMount(binary, "/usr/local/bin/blerg-workspace-worker")+",readonly")
		image = sandboxImage()
	}
	return append(args, "--entrypoint", "/usr/bin/timeout", image, "--signal=KILL", workspacePackPeerTimeout(request), "/usr/local/bin/blerg-workspace-worker", workspacePackServerFlag, clone)
}

// OS pipes provide duplex backpressure without buffering packs in daemon memory.
// Both Git parsers run in disposable containers with disjoint data mounts.
func runWorkspacePackPair(parent context.Context, request workspaceWorkerRequest, serverArgs, writerArgs []string, names ...string) (err error) {
	return runWorkspacePackPairLimit(parent, request, serverArgs, writerArgs, workspacePackByteLimit, names...)
}

func runWorkspacePackPairLimit(parent context.Context, request workspaceWorkerRequest, serverArgs, writerArgs []string, limit int64, names ...string) (err error) {
	for _, args := range [][]string{serverArgs, writerArgs} {
		if err := validateWorkspaceWorkerMounts(request, args); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(parent), workspaceWorkerCleanup)
		defer stop()
		for _, name := range names {
			_, removeErr := workspaceDockerOutput(cleanup, "--context", request.State.WorkerContext, "rm", "-f", name)
			gone, goneErr := workspaceDockerOutput(cleanup, "--context", request.State.WorkerContext, "ps", "-aq", "--filter", "name=^/"+name+"$")
			if goneErr != nil || strings.TrimSpace(string(gone)) != "" {
				err = &workspaceWorkerUncertain{err: errors.Join(err, removeErr, goneErr, errors.New("pack peer removal unproved"))}
			}
		}
	}()
	serverIn, writerOut, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = serverIn.Close(); _ = writerOut.Close() }()
	writerIn, serverOut, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = writerIn.Close(); _ = serverOut.Close() }()
	server := exec.CommandContext(ctx, "docker", serverArgs...) //nolint:gosec // trusted containment arguments
	writer := exec.CommandContext(ctx, "docker", writerArgs...) //nolint:gosec // trusted containment arguments
	server.Env, writer.Env = sandboxPackDockerEnv(), sandboxPackDockerEnv()
	server.Stdin, server.Stderr = serverIn, io.Discard
	pack, err := server.StdoutPipe()
	if err != nil {
		return err
	}
	writer.Stdin, writer.Stdout, writer.Stderr = writerIn, writerOut, io.Discard
	server.WaitDelay, writer.WaitDelay = time.Second, time.Second
	if err := server.Start(); err != nil {
		return err
	}
	if err := writer.Start(); err != nil {
		cancel()
		_ = server.Wait()
		return err
	}
	_ = serverIn.Close()
	_ = writerIn.Close()
	_ = writerOut.Close()
	finished := make(chan error, 3)
	go func() {
		err := copyWorkspacePack(serverOut, pack, limit)
		_ = serverOut.Close()
		if err != nil {
			cancel()
		}
		finished <- err
		finished <- server.Wait()
	}()
	go func() { finished <- writer.Wait() }()
	first := <-finished
	if first != nil {
		cancel()
	}
	second := <-finished
	if second != nil {
		cancel()
	}
	third := <-finished
	if first != nil || second != nil || third != nil {
		return errors.Join(errors.New("isolated workspace pack transfer failed; work retained"), first, second, third)
	}
	return nil
}

func workspacePackPeerTimeout(request workspaceWorkerRequest) string {
	duration := min(sandboxFetchTimeout, time.Until(request.Deadline))
	return strconv.Itoa(max(1, int(duration.Round(time.Second)/time.Second))) + "s"
}
