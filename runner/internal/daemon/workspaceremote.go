package daemon

// Docker VM storage keeps quotas in a Linux filesystem on a private named
// volume. The host path is only a logical worker path; macOS never mounts or
// parses that filesystem. Consumers mount the keeper-attested device through
// a labelled local volume and its fixed public tree subdirectory.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type workspaceRemoteStorage interface {
	WorkspaceStorage
	Identity(context.Context, string, string) (protocol.WorkspaceRoot, error)
	MountSource(context.Context, string, string) (string, error)
	DockerContext(context.Context, string, string) (string, error)
}
type DockerWorkspaceStorage struct {
	Root string
}
type workspaceRemoteRecord struct {
	Session   string
	Token     string
	Context   string
	Budget    protocol.WorkspaceBudget
	Container string
	Releasing bool
	Released  bool
}

func (r workspaceRemoteRecord) volume() string     { return "blerg-workspace-" + r.Token }
func (r workspaceRemoteRecord) dataVolume() string { return r.volume() + "-data" }
func (r workspaceRemoteRecord) logical() string    { return "/blerg-workspaces/" + r.Token + "/tree" }
func (s *DockerWorkspaceStorage) recordPath(session string) (string, error) {
	if !sandboxCloneIDPattern.MatchString(session) || !filepath.IsAbs(s.Root) {
		return "", errors.New("invalid VM allocation")
	}
	return filepath.Join(s.Root, session, "allocation.json"), nil
}
func (s *DockerWorkspaceStorage) save(r workspaceRemoteRecord) error {
	path, err := s.recordPath(r.Session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := workspaceDirectoryIdentity(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return errors.New("vm allocation directory is not private")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	envelope, _ := json.Marshal(workspaceEnvelope{State: raw, MAC: hex.EncodeToString(mac.Sum(nil))})
	return writeWorkspacePrivate(path, envelope)
}
func (s *DockerWorkspaceStorage) read(session string) (workspaceRemoteRecord, error) {
	var r workspaceRemoteRecord
	path, err := s.recordPath(session)
	if err != nil {
		return r, err
	}
	if _, err := workspaceDirectoryIdentity(filepath.Dir(path)); err != nil {
		if _, statErr := os.Lstat(filepath.Dir(path)); os.IsNotExist(statErr) {
			return r, statErr
		}
		return r, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !safeSandboxGitInfo(info, false) || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return r, errors.New("unsafe VM journal")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // session-derived private journal, bounded mode/ownership checks above
	if err != nil {
		return r, err
	}
	var e workspaceEnvelope
	if err = json.Unmarshal(raw, &e); err != nil {
		return r, err
	}
	key, err := workspaceManifestKey()
	if err != nil {
		return r, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(e.State)
	sig, err := hex.DecodeString(e.MAC)
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return r, errors.New("vm journal authentication failed")
	}
	if err = json.Unmarshal(e.State, &r); err != nil {
		return r, err
	}
	if r.Session != session || !sandboxCloneIDPattern.MatchString(r.Token) || r.Context == "" {
		return r, errors.New("vm allocation binding failed")
	}
	return r, nil
}
func (s *DockerWorkspaceStorage) Allocate(ctx context.Context, session string, budget protocol.WorkspaceBudget) (string, error) {
	lock := workspaceLock("quota:" + s.Root + ":" + session)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.read(session)
	if os.IsNotExist(err) {
		r = workspaceRemoteRecord{Session: session, Token: ccUUID(), Context: currentDockerContext(), Budget: budget}
		if err = s.save(r); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if r.Budget != budget || r.Releasing || r.Released {
		return "", errors.New("vm quota budget/state mismatch")
	}
	if r.Container == "" {
		label := "blerg.workspace.storage=" + r.Token
		existing, err := workspaceDockerOutput(ctx, "--context", r.Context, "ps", "-aq", "--no-trunc", "--filter", "label="+label)
		if err != nil {
			return "", err
		}
		ids := strings.Fields(string(existing))
		if len(ids) > 1 {
			return "", errors.New("ambiguous VM quota keeper")
		}
		if len(ids) == 1 {
			r.Container = ids[0]
		} else {
			// The durable UUID is both the volume's ownership label and create token.
			if _, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "create", "--label", label, r.volume()); err != nil {
				return "", err
			}
			if err := s.volumeOwned(ctx, r, r.volume()); err != nil {
				return "", err
			}
			policy, err := workspaceEntrySecurityOption()
			if err != nil {
				return "", err
			}
			args := []string{"--context", r.Context, "create", "--pull=never", "--privileged", "--restart=unless-stopped", "--user", "0:0", "--read-only", "--network=none", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--security-opt", policy, "--label", label, "--tmpfs", "/tmp:rw,size=16777216,nr_inodes=1024,mode=1777", "--mount", "type=volume,src=" + r.volume() + ",dst=/quota", "--mount", sandboxBindMount(sandboxCloneRoot(), sandboxCloneRoot()) + ",readonly", "--entrypoint", "/usr/local/bin/blerg-workspace-worker"}
			image, binary, err := workspaceWorkerProgram()
			if err != nil {
				return "", err
			}
			if binary != "" {
				args = append(args, "--mount", sandboxBindMount(binary, "/usr/local/bin/blerg-workspace-worker")+",readonly")
			}
			args = append(args, image, "--blerg-workspace-volume")
			out, err := workspaceDockerOutput(ctx, args...)
			if err != nil {
				return "", err
			}
			r.Container = strings.TrimSpace(string(out))
		}
		if len(r.Container) != 64 {
			return "", errors.New("invalid VM keeper identity")
		}
		if err := s.save(r); err != nil {
			return "", err
		}
	}
	if err := s.owned(ctx, r); err != nil {
		return "", err
	}
	if _, err := workspaceDockerOutput(ctx, "--context", r.Context, "start", r.Container); err != nil {
		return "", err
	}
	allocation, err := s.call(ctx, r, "quota-allocate")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(allocation.QuotaDevice, "/dev/loop") {
		return "", errors.New("invalid VM quota device")
	}
	label := "blerg.workspace.storage=" + r.Token
	if _, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "create", "--label", label, "--driver", "local", "--opt", "type=ext4", "--opt", "device="+allocation.QuotaDevice, "--opt", "o=nodev,nosuid", r.dataVolume()); err != nil {
		return "", err
	}
	if err := s.validateDataVolume(ctx, r, allocation.QuotaDevice); err != nil {
		return "", err
	}
	return r.logical(), nil
}
func (s *DockerWorkspaceStorage) owned(ctx context.Context, r workspaceRemoteRecord) error {
	raw, err := workspaceDockerOutput(ctx, "--context", r.Context, "inspect", "--format", `{{index .Config.Labels "blerg.workspace.storage"}}`, r.Container)
	if err != nil || strings.TrimSpace(string(raw)) != r.Token {
		return errors.New("vm keeper ownership unavailable")
	}
	return nil
}
func (s *DockerWorkspaceStorage) call(ctx context.Context, r workspaceRemoteRecord, action string) (workspaceWorkerResponse, error) {
	var response workspaceWorkerResponse
	if err := s.ensureKeeper(ctx, r); err != nil {
		return response, err
	}
	if action == "quota-validate" {
		action = "quota-recover"
	}
	record, err := workspaceRecord(r.Session)
	if err != nil {
		return response, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	request := workspaceWorkerRequest{Action: action, Record: record, Deadline: deadline, QuotaBudget: r.Budget, QuotaUID: os.Getuid(), QuotaGID: os.Getgid(), State: workspacePrivateManifest{Session: r.Session}}
	raw, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	return workspaceDockerRequest(ctx, []string{"--context", r.Context, "exec", "-i", r.Container, "/usr/local/bin/blerg-workspace-worker", workspaceWorkerFlag}, raw)
}
func (s *DockerWorkspaceStorage) Validate(ctx context.Context, session, root string, budget protocol.WorkspaceBudget) error {
	lock := workspaceLock("quota:" + s.Root + ":" + session)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.read(session)
	if err != nil {
		return err
	}
	if r.logical() != root || r.Budget != budget || r.Releasing || r.Released {
		return errors.New("vm allocation mismatch")
	}
	out, err := s.call(ctx, r, "quota-validate")
	if err != nil {
		return err
	}
	return s.validateDataVolume(ctx, r, out.QuotaDevice)
}
func (s *DockerWorkspaceStorage) Identity(ctx context.Context, session, root string) (protocol.WorkspaceRoot, error) {
	lock := workspaceLock("quota:" + s.Root + ":" + session)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.read(session)
	if err != nil || r.logical() != root || r.Releasing || r.Released {
		return protocol.WorkspaceRoot{}, errors.New("vm allocation mismatch")
	}
	out, err := s.call(ctx, r, "quota-validate")
	if err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	if err := s.validateDataVolume(ctx, r, out.QuotaDevice); err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	return out.State.RootIdentity, nil
}
func (s *DockerWorkspaceStorage) MountSource(ctx context.Context, session, root string) (string, error) {
	lock := workspaceLock("quota:" + s.Root + ":" + session)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.read(session)
	if err != nil || r.logical() != root || r.Releasing || r.Released {
		return "", errors.New("vm mount binding mismatch")
	}
	if err := s.owned(ctx, r); err != nil {
		return "", err
	}
	allocation, err := s.call(ctx, r, "quota-validate")
	if err != nil {
		return "", err
	}
	if err := s.validateDataVolume(ctx, r, allocation.QuotaDevice); err != nil {
		return "", err
	}
	return "volume:" + r.dataVolume(), nil
}
func (s *DockerWorkspaceStorage) volumeOwned(ctx context.Context, r workspaceRemoteRecord, name string) error {
	raw, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "inspect", "--format", `{{index .Labels "blerg.workspace.storage"}}`, name)
	if err != nil || strings.TrimSpace(string(raw)) != r.Token {
		return errors.New("vm volume ownership mismatch")
	}
	return nil
}
func (s *DockerWorkspaceStorage) removeVolume(ctx context.Context, r workspaceRemoteRecord, name string) error {
	if err := s.volumeOwned(ctx, r, name); err != nil {
		out, checkErr := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "ls", "--format", "{{.Name}}", "--filter", "name=^"+name+"$")
		if checkErr != nil || strings.TrimSpace(string(out)) != "" {
			return err
		}
		return nil
	}
	_, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "rm", name)
	return err
}

func (s *DockerWorkspaceStorage) Release(ctx context.Context, session, root string) error {
	lock := workspaceLock("quota:" + s.Root + ":" + session)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.read(session)
	if err != nil {
		return err
	}
	if r.logical() != root {
		return errors.New("vm release root mismatch")
	}
	if r.Released {
		return nil
	}
	r.Releasing = true
	if err = s.save(r); err != nil {
		return err
	}
	workspaceCrashBoundary("before-quota-data-release")
	if err := s.removeVolume(ctx, r, r.dataVolume()); err != nil {
		return err
	}
	workspaceCrashBoundary("after-quota-data-release")
	if err := s.owned(ctx, r); err == nil {
		if _, err = s.call(ctx, r, "quota-release"); err != nil {
			return err
		}
		workspaceCrashBoundary("after-quota-image-release")
		if _, err = workspaceDockerOutput(ctx, "--context", r.Context, "rm", "-f", r.Container); err != nil {
			return err
		}
	} else {
		out, checkErr := workspaceDockerOutput(ctx, "--context", r.Context, "ps", "-aq", "--no-trunc", "--filter", "id="+r.Container)
		if checkErr != nil || strings.TrimSpace(string(out)) != "" {
			return errors.New("vm keeper absence unproved")
		}
	}
	if err := s.removeVolume(ctx, r, r.volume()); err != nil {
		return err
	}
	r.Released = true
	return s.save(r)
}

// The mount service needs UID 0 while the daemon key belongs to the host user.
// Authenticate that owner before copying the same secret into a root-private
// tmpfs keyring. Never weaken generic private-manifest ownership checks.
func workspaceQuotaKey(record string, uid int) error {
	if uid < 0 {
		return errors.New("invalid quota owner")
	}
	source := filepath.Join(filepath.Dir(record), "workspace-integrity.key")
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(uid) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 32 {
		return errors.New("quota authority key ownership mismatch")
	}
	key, err := os.ReadFile(source) //nolint:gosec // fixed key below authenticated private-record directory, ownership/size checked above
	if err != nil || len(key) != 32 {
		return errors.New("quota authority key unavailable")
	}
	local := "/tmp/blerg-quota-authority"
	if err := os.MkdirAll(filepath.Join(local, sandboxCloneDirName), 0700); err != nil {
		return err
	}
	target := filepath.Join(local, sandboxCloneDirName, "workspace-integrity.key")
	if old, err := os.ReadFile(target); err == nil { //nolint:gosec // root-private fixed tmpfs keyring, no agent access
		if !bytes.Equal(old, key) {
			return errors.New("quota key changed")
		}
	} else if os.IsNotExist(err) {
		if err := os.WriteFile(target, key, 0600); err != nil { //nolint:gosec // fixed root-private keyring path, bytes are never a path
			return err
		}
	} else {
		return err
	}
	return os.Setenv(DaemonStateDirEnv, local)
}

func workspaceQuotaRootIdentity(path string, uid int) (protocol.WorkspaceRoot, error) {
	if uid < 0 {
		return protocol.WorkspaceRoot{}, errors.New("invalid quota owner")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return protocol.WorkspaceRoot{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0002 != 0 || uint64(stat.Uid) != uint64(uid) {
		return protocol.WorkspaceRoot{}, errors.New("quota public root ownership mismatch")
	}
	return protocol.WorkspaceRoot{Device: uint64(stat.Dev), Inode: stat.Ino}, nil //nolint:unconvert // Darwin and Linux device types differ
}

func workspaceStorageMount(source, target string) string {
	if strings.HasPrefix(source, "volume:") {
		return "type=volume,src=" + strings.TrimPrefix(source, "volume:") + ",dst=" + target + ",volume-subpath=tree"
	}
	return sandboxBindMount(source, target)
}
func workspaceQuotaDevice(mount string) (string, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] != mount {
			continue
		}
		_, after, ok := strings.Cut(line, " - ext4 ")
		if !ok {
			return "", errors.New("quota device filesystem changed")
		}
		device := strings.Fields(after)
		if len(device) < 1 || !strings.HasPrefix(device[0], "/dev/loop") {
			return "", errors.New("quota device invalid")
		}
		return device[0], nil
	}
	return "", errors.New("quota device unavailable")
}

func (s *DockerWorkspaceStorage) validateDataVolume(ctx context.Context, r workspaceRemoteRecord, device string) error {
	if err := s.volumeOwned(ctx, r, r.dataVolume()); err != nil {
		return err
	}
	raw, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "inspect", "--format", "{{json .Options}}", r.dataVolume())
	var options map[string]string
	if err != nil || json.Unmarshal(raw, &options) != nil || options["type"] != "ext4" || options["device"] != device || options["o"] != "nodev,nosuid" {
		return errors.New("vm data volume device/options changed")
	}
	return nil
}

func (s *DockerWorkspaceStorage) DockerContext(_ context.Context, session, root string) (string, error) {
	r, err := s.read(session)
	if err != nil || r.logical() != root {
		return "", errors.New("vm allocation context mismatch")
	}
	return r.Context, nil
}

// Restart only the immutable, authenticated keeper. An unavailable engine or
// unknown ownership retains the allocation; never create a substitute keeper.
// The worker then reconstructs only an existing authenticated image, verifies
// its inode/size/quota and remounts its pinned loop device before returning.
func (s *DockerWorkspaceStorage) ensureKeeper(ctx context.Context, r workspaceRemoteRecord) error {
	if err := s.owned(ctx, r); err != nil {
		return err
	}
	if err := s.volumeOwned(ctx, r, r.volume()); err != nil {
		return err
	}
	raw, err := workspaceDockerOutput(ctx, "--context", r.Context, "inspect", "--format", "{{.State.Running}}", r.Container)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return nil
	case "false":
		workspaceCrashBoundary("before-quota-keeper-restart")
		if _, err := workspaceDockerOutput(ctx, "--context", r.Context, "start", r.Container); err != nil {
			return err
		}
		workspaceCrashBoundary("after-quota-keeper-restart")
		return s.owned(ctx, r)
	default:
		return errors.New("quota keeper state uncertain")
	}
}
