//go:build linux || darwin

package hostgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const gitDiscoveryTimeout = 10 * time.Second

// Verify the well-known privileged Darwin group in both directions; a renamed
// group or an unexpected directory-service mapping must not widen trust.
func trustedDarwinAdminGroup(byName, byID func(string) (*user.Group, error)) bool {
	named, err := byName("admin")
	if err != nil || named == nil || named.Name != "admin" || named.Gid != "80" {
		return false
	}
	numbered, err := byID("80")
	return err == nil && numbered != nil && numbered.Name == "admin" && numbered.Gid == "80"
}

var darwinAdminGroup = sync.OnceValue(func() bool {
	return runtime.GOOS == "darwin" && trustedDarwinAdminGroup(user.LookupGroup, user.LookupGroupId)
})

// The canonical path must stay inside the developer installation. Include the
// entire Xcode app bundle (not just Contents/Developer) in the allowed location.
func validateDarwinGitPath(path string) error {
	root := ""
	if strings.HasPrefix(path, "/Library/Developer/") {
		root = "/Library/Developer"
	}
	parts := strings.Split(path, "/")
	if len(parts) > 3 && parts[1] == "Applications" && strings.HasPrefix(parts[2], "Xcode") && strings.HasSuffix(parts[2], ".app") {
		root = filepath.Join("/Applications", parts[2])
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") || filepath.Base(path) != "git" || root == "" {
		return fmt.Errorf("developer Git %q: path check: must be an absolute executable under /Library/Developer or /Applications/Xcode*.app; Apple /usr/bin/git shim is refused", path)
	}
	return nil
}

func validateDarwinGitOwner(info os.FileInfo, euid int) error {
	return validateGitOwner(info, euid, false)
}

// Only directory ancestors may use this exception. Darwin admin members already
// have root authority via sudo. Root:admin group-write adds no less-privileged
// writer; world-write, other owners/groups and writable executables stay refused.
func validateGitOwner(info os.FileInfo, euid int, allowAdmin bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return errors.New("owner UID check: filesystem ownership unavailable")
	}
	if euid < 0 || stat.Uid != 0 && int64(stat.Uid) != int64(euid) {
		return fmt.Errorf("owner UID check: got %d, require root (0) or daemon effective UID %d", stat.Uid, euid)
	}
	trustedAdmin := allowAdmin && stat.Uid == 0 && stat.Gid == 80
	if info.Mode().Perm()&0002 != 0 || info.Mode().Perm()&0020 != 0 && !trustedAdmin {
		return fmt.Errorf("group/world write check: mode %04o", info.Mode().Perm())
	}
	return nil
}

func validateDarwinGit(path string, info os.FileInfo, euid int) error {
	if err := validateDarwinGitPath(path); err != nil {
		return err
	}
	if info == nil || !info.Mode().IsRegular() {
		return fmt.Errorf("developer Git %q: executable type check: require a regular file, no symlink", path)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("developer Git %q: executable mode check: require execute permission without set-ID bits", path)
	}
	if err := validateDarwinGitOwner(info, euid); err != nil {
		return fmt.Errorf("developer Git %q: %w", path, err)
	}
	return nil
}

func validateDarwinGitTree(path string, stat func(string) (os.FileInfo, error), euid int) error {
	return validateDarwinGitTreeWithAdmin(path, stat, euid, darwinAdminGroup())
}

// Linux-runnable seam for the Darwin ancestor policy. Production enables the
// exception only on Darwin after resolving admin by both name and numeric ID.
func validateDarwinGitTreeWithAdmin(path string, stat func(string) (os.FileInfo, error), euid int, allowAdmin bool) error {
	if err := validateDarwinGitPath(path); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := stat(current)
		switch {
		case err != nil:
			err = fmt.Errorf("lstat check: %w", err)
		case current == path:
			err = validateDarwinGit(path, info, euid)
		case info == nil || info.Mode().Type() != os.ModeDir:
			err = errors.New("directory type check: require a directory, no symlink")
		default:
			err = validateGitOwner(info, euid, allowAdmin)
		}
		if err != nil {
			return fmt.Errorf("resolved developer Git %q: component %q: %w", path, current, err)
		}
		if current == "/" {
			return nil
		}
	}
}

// Discovery (including failure) is cached. Recheck the cached executable and
// installation directories on use, without rediscovery or adopting a symlink.
func cachedDarwinGit(find func() (string, error), canonical func(string) (string, error), stat func(string) (os.FileInfo, error), euid int) func() (string, error) {
	once := sync.OnceValues(func() (string, error) { return resolveDarwinGit(find, canonical, stat, euid) })
	return func() (string, error) {
		path, err := once()
		if err != nil {
			return "", err
		}
		current, err := resolveDarwinGit(func() (string, error) { return path, nil }, canonical, stat, euid)
		if err != nil {
			return "", err
		}
		if current != path {
			return "", fmt.Errorf("%w: cached macOS developer Git %q: canonical path check: changed to %q", ErrNoGit, path, current)
		}
		return path, nil
	}
}

func resolveDarwinGit(find func() (string, error), canonical func(string) (string, error), stat func(string) (os.FileInfo, error), euid int) (string, error) {
	path, err := find()
	stage := "discovery check"
	if err == nil {
		stage = "canonical path check"
		var resolved string
		resolved, err = canonical(path)
		if err == nil {
			path = resolved
		}
	}
	if err == nil {
		stage = "installation metadata check"
		err = validateDarwinGitTree(path, stat, euid)
	}
	if err != nil {
		return "", fmt.Errorf("%w: real macOS developer Git %q resolution failed (Apple shim disabled): %s: %w", ErrNoGit, path, stage, err)
	}
	return path, nil
}

// Both commands ignore caller DEVELOPER_DIR, SDKROOT, PATH and HOME. The
// selected directory is validated before use; xcrun is a once-only fallback.
func discoverDarwinGit(run func(*exec.Cmd) (string, error), canonical func(string) (string, error), stat func(string) (os.FileInfo, error), euid int) (string, error) {
	dir, err := run(darwinDeveloperDirectoryCommand())
	if err == nil {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\x00\r\n") {
			err = fmt.Errorf("invalid active developer directory %q", dir)
		} else {
			var path string
			path, err = resolveDarwinGit(func() (string, error) { return filepath.Join(dir, "usr/bin/git"), nil }, canonical, stat, euid)
			if err == nil {
				return path, nil
			}
		}
	}
	selectionErr := err
	path, err := run(darwinGitDiscoveryCommand())
	if err != nil {
		return "", fmt.Errorf("xcode-select -p: %w; fallback: %w", selectionErr, err)
	}
	return path, nil
}

func darwinDeveloperDirectoryCommand() *exec.Cmd {
	cmd := exec.Command("/usr/bin/xcode-select", "-p") //nolint:noctx // runCommand owns the discovery deadline, process-group kill and joins.
	return prepareDarwinDiscoveryCommand(cmd)
}

func darwinGitDiscoveryCommand() *exec.Cmd {
	cmd := exec.Command("/usr/bin/xcrun", "--find", "git") //nolint:noctx // runCommand owns the discovery deadline, process-group kill and joins.
	return prepareDarwinDiscoveryCommand(cmd)
}

func prepareDarwinDiscoveryCommand(cmd *exec.Cmd) *exec.Cmd {
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd
}

// Reuse the bounded process-group runner without recursively looking up Git.
func runDarwinGitDiscovery(cmd *exec.Cmd) (string, error) {
	return runDarwinGitDiscoveryTimeout(cmd, gitDiscoveryTimeout)
}

func runDarwinGitDiscoveryTimeout(cmd *exec.Cmd, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, stderr := &capped{max: 4096}, &capped{max: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := runCommand(ctx.Done(), nil, Options{MemoryLimit: true, LimitKB: 256 << 10}, cmd, platformMemoryWatch)
	if errors.Is(err, errStopped) {
		err = ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w; stderr=%q", strings.Join(cmd.Args, " "), err, stderr.buf.String())
	}
	if out.overflow || stderr.overflow || stderr.buf.Len() != 0 {
		return "", fmt.Errorf("%s discovery produced excessive output or diagnostics: stderr=%q", cmd.Path, stderr.buf.String())
	}
	path := strings.TrimSpace(out.buf.String())
	if path == "" {
		return "", fmt.Errorf("%s returned no developer path", cmd.Path)
	}
	return path, nil
}
