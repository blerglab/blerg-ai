package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/scratch"
)

// CleanupScratchFolders is offline maintenance, never a sweep based on age or names
// alone. Stop the daemon and end its sessions first. Unknown state blocks cleanup.
// apply=false inspects and reports only; it never changes a candidate.
func CleanupScratchFolders(reposRoot, stateDir string, apply bool, out io.Writer) error {
	if reposRoot == "" || stateDir == "" {
		return fmt.Errorf("repos root and daemon state directory are required")
	}
	dir := AgentRecordsDir(stateDir)
	if _, err := secureAgentRecordsDir(dir, true); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0600) //nolint:gosec // fixed name inside the ownership-checked, daemon-private records directory
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("stop the daemon before scratch cleanup")
	}
	// Do not use the recovery readers here: they may skip or remove corrupt records.
	// Every unresolved allocation or record is a reason to leave the folders alone.
	for _, path := range []string{dir, recordsDir(reposRoot), filepath.Join(stateDir, sandboxCloneDirName), filepath.Join(stateDir, "host-children")} {
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if path == dir && (entry.Name() == ".lock" || entry.Name() == "inputs" && entry.IsDir()) {
				continue
			}
			return fmt.Errorf("scratch cleanup refused: recoverable session or unresolved allocation at %s", path)
		}
	}
	// Containers survive daemon shutdown. Probe them even if records are
	// missing, and fail closed when Docker cannot be queried.
	if err := scratchRuntimeIdle(); err != nil {
		return err
	}
	entries, err := os.ReadDir(reposRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !scratch.Valid(entry.Name()) {
			continue
		}
		path := filepath.Join(reposRoot, entry.Name())
		folder := bindScratchFolder(reposRoot, path, entry.Name())
		if folder == nil {
			_, _ = fmt.Fprintf(out, "Scratch folder kept at %s: its identity could not be verified\n", path)
			continue
		}
		if err := scratchNoOpenFiles(path); err != nil {
			_, _ = fmt.Fprintf(out, "Scratch folder kept at %s: %v\n", path, err)
			continue
		}
		report := folder.inspect(apply)
		switch {
		case report != "":
			_, _ = fmt.Fprintln(out, report)
		case apply:
			_, _ = fmt.Fprintf(out, "Removed scratch folder %s\n", path)
		default:
			_, _ = fmt.Fprintf(out, "Would remove scratch folder %s\n", path)
		}
	}
	return nil
}

func scratchProbe(name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed maintenance tools and daemon-built arguments
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func scratchRuntimeIdle() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("scratch cleanup needs Docker available to rule out live sandbox sessions")
	}
	output, _, err := scratchProbe("docker", "ps", "-a", "--filter", "name=blerg-", "--format", "{{.Names}}")
	if err != nil || strings.TrimSpace(output) != "" {
		return fmt.Errorf("scratch cleanup could not rule out sandbox sessions; end them first")
	}

	return nil
}

func scratchNoOpenFiles(path string) error {
	output, stderr, err := scratchProbe("lsof", "-nP", "+D", path)
	if strings.TrimSpace(output) != "" {
		return fmt.Errorf("a process still uses it")
	}
	// lsof's documented exit 1 means no matches. Diagnostics or any other exit
	// are uncertainty (including missing lsof and permission/inspection failures).
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr == "" {
		return nil
	}
	return fmt.Errorf("open processes could not be ruled out (lsof is required)")
}
