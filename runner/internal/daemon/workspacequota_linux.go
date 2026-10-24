//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
)

func (s *FilesystemWorkspaceStorage) allocate(ctx context.Context, session string, budget protocol.WorkspaceBudget) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if os.Geteuid() != 0 {
		return "", errors.New("persistent workspace quotas require privileged Linux mount authority")
	}
	if budget.CheckoutBytes < 8<<20 || budget.CheckoutEntries < 64 {
		return "", errors.New("quota budget too small for a dedicated filesystem")
	}
	directory, image, mount, tree, err := s.paths(session)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if _, err := workspaceDirectoryIdentity(directory); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("quota allocation directory is not private")
	}

	record, err := readWorkspaceQuota(directory)
	if os.IsNotExist(err) {
		return "", err
	} // read helper deliberately conceals unsafe/missing records
	if err != nil {
		if _, statErr := os.Lstat(filepath.Join(directory, "allocation.json")); !os.IsNotExist(statErr) {
			return "", err
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil || len(entries) != 0 {
			return "", errors.New("unrecorded quota allocation retained")
		}
		record = workspaceQuotaRecord{Version: 1, Session: session, Budget: budget, Phase: "create"}
		if err := saveWorkspaceQuota(directory, record); err != nil {
			return "", err
		}
	}
	if record.Session != session || record.Budget != budget || record.Phase == "releasing" || record.Phase == "released" {
		return "", errors.New("quota journal binding/state mismatch")
	}
	if record.Phase == "create" {
		f, err := os.OpenFile(image, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // exact session-private allocation image; exclusive creation
		if os.IsExist(err) {
			// The durable create intent proves no runtime has received this image.
			// Resume an interrupted create/truncate only inside this private allocation.
			info, statErr := os.Lstat(image)
			if statErr != nil || !safeSandboxGitInfo(info, false) || info.Mode().Perm() != 0o600 || (info.Size() != 0 && info.Size() != budget.CheckoutBytes/4096*4096) {
				return "", errors.New("unrecorded quota image retained")
			}
			f, err = os.OpenFile(image, os.O_RDWR|syscall.O_NOFOLLOW, 0) //nolint:gosec // private create-intent image, no-follow open
		}
		if err != nil {
			return "", err
		}
		truncateErr := f.Truncate(budget.CheckoutBytes / 4096 * 4096)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(truncateErr, syncErr, closeErr); err != nil {
			return "", err
		}
		record.ImageIdentity, err = quotaImageIdentity(image)
		if err != nil {
			return "", err
		}
		record.Phase = "format"
		if err := saveWorkspaceQuota(directory, record); err != nil {
			return "", err
		}
	}
	if err := validateQuotaImage(image, record); err != nil {
		return "", err
	}
	if err := os.MkdirAll(mount, 0o700); err != nil {
		return "", err
	}
	mounted, err := quotaMountSource(mount, image)
	if err != nil {
		return "", err
	}
	if record.Phase == "format" {
		if mounted {
			return "", errors.New("format journal unexpectedly mounted")
		}
		entries := quotaInodes(budget)
		if err := quotaCommand(ctx, "mkfs.ext4", "-q", "-F", "-b", "4096", "-m", "0", "-N", strconv.Itoa(entries), "-O", "^has_journal", "-E", "lazy_itable_init=0", image); err != nil {
			return "", err
		}
		record.Phase = "mount"
		if err := saveWorkspaceQuota(directory, record); err != nil {
			return "", err
		}
	}
	if !mounted {
		if !emptyDir(mount) {
			return "", errors.New("unmounted quota target is not empty")
		}
		options := "loop,nodev,nosuid"
		if record.LoopDevice != "" {
			if !validQuotaLoopDevice(record.LoopDevice) {
				return "", errors.New("invalid pinned loop device")
			}
			backing, readErr := os.ReadFile(filepath.Join("/sys/block", filepath.Base(record.LoopDevice), "loop/backing_file"))
			if readErr == nil && filepath.Clean(strings.TrimSpace(string(backing))) != image {
				return "", errors.New("pinned loop device belongs to another image")
			}
			if readErr != nil && !os.IsNotExist(readErr) {
				return "", readErr
			}
			options = "loop=" + record.LoopDevice + ",nodev,nosuid"
		}
		if err := quotaCommand(ctx, "mount", "-o", options, image, mount); err != nil {
			return "", err
		}
	}
	if ok, err := quotaMountSource(mount, image); err != nil || !ok {
		return "", errors.New("quota mount not proved")
	}
	device, err := workspaceQuotaDevice(mount)
	if err != nil {
		return "", err
	}
	if record.LoopDevice != "" && record.LoopDevice != device {
		return "", errors.New("quota loop device changed")
	}
	record.LoopDevice = device
	var stat unix.Statfs_t
	if err := unix.Statfs(mount, &stat); err != nil {
		return "", err
	}
	if stat.Type != unix.EXT4_SUPER_MAGIC || stat.Bsize <= 0 || stat.Blocks > uint64(budget.CheckoutBytes)/uint64(stat.Bsize) || stat.Files > uint64(budget.CheckoutEntries) {
		return "", errors.New("filesystem does not enforce the requested aggregate quota")
	}
	if err := os.MkdirAll(tree, 0o700); err != nil {
		return "", err
	}
	record.Phase = "ready"
	if err := saveWorkspaceQuota(directory, record); err != nil {
		return "", err
	}
	return tree, nil
}
func (s *FilesystemWorkspaceStorage) release(ctx context.Context, session, root string) error {
	directory, image, mount, tree, err := s.paths(session)
	if err != nil {
		return err
	}
	if root != tree {
		return errors.New("quota release root mismatch")
	}
	record, err := readWorkspaceQuota(directory)
	if err != nil {
		return err
	}
	if record.Session != session {
		return errors.New("quota release session mismatch")
	}
	if record.Phase == "released" {
		return nil
	}
	if record.Phase == "releasing" {
		if _, err := os.Lstat(image); os.IsNotExist(err) {
			if mounted, mountErr := quotaMountSource(mount, image); mountErr != nil || mounted {
				return errors.New("removed image still mounted or unavailable")
			}
			record.Phase = "released"
			return saveWorkspaceQuota(directory, record)
		}
	}
	if err := validateQuotaImage(image, record); err != nil {
		return err
	}
	record.Phase = "releasing"
	if err := saveWorkspaceQuota(directory, record); err != nil {
		return err
	}
	mounted, err := quotaMountSource(mount, image)
	if err != nil {
		return err
	}
	if mounted {
		if err := quotaCommand(ctx, "umount", mount); err != nil {
			return err
		}
	}
	if mounted, err := quotaMountSource(mount, image); err != nil || mounted {
		return errors.New("quota unmount unproved")
	}
	if err := os.Remove(image); err != nil && !os.IsNotExist(err) {
		return err
	}
	record.Phase = "released"
	return saveWorkspaceQuota(directory, record)
}
func quotaCommand(ctx context.Context, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tool names, daemon-private session paths, no shell
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	return cmd.Run()
}
func quotaImageIdentity(path string) (protocol.WorkspaceRoot, error) {
	info, err := os.Lstat(path)
	if err != nil || !safeSandboxGitInfo(info, false) || info.Mode().Perm() != 0o600 {
		return protocol.WorkspaceRoot{}, errors.New("quota image unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return protocol.WorkspaceRoot{}, errors.New("quota image is not private")
	}
	return protocol.WorkspaceRoot{Device: stat.Dev, Inode: stat.Ino}, nil
}
func validateQuotaImage(path string, record workspaceQuotaRecord) error {
	identity, err := quotaImageIdentity(path)
	if err != nil || identity != record.ImageIdentity {
		return errors.New("quota image replaced")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != record.Budget.CheckoutBytes/4096*4096 {
		return errors.New("quota image size changed")
	}
	return nil
}
func quotaMountSource(mount, image string) (bool, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	if len(raw) > 4<<20 {
		return false, errors.New("mount table too large")
	}
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\")
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || decode.Replace(fields[4]) != mount {
			continue
		}
		sep := strings.Index(line, " - ext4 ")
		if sep < 0 {
			return false, errors.New("quota mount has wrong filesystem")
		}
		raw, err := os.ReadFile(filepath.Join("/sys/dev/block", fields[2], "loop/backing_file")) //nolint:gosec // kernel-provided major:minor from mountinfo, fixed sysfs suffix
		if err != nil || filepath.Clean(strings.TrimSpace(string(raw))) != image {
			return false, fmt.Errorf("quota mount is not the recorded image")
		}
		return true, nil
	}
	return false, nil
}

func quotaInodes(budget protocol.WorkspaceBudget) int {
	blocks := budget.CheckoutBytes / 4096
	groups := max(int64(1), (blocks+32767)/32768)
	// ext4 rounds inode tables per block group. Round down before mkfs, so its
	// per-group alignment cannot silently exceed the aggregate API ceiling.
	return int((int64(budget.CheckoutEntries) / groups / 32) * 32 * groups)
}

func validQuotaLoopDevice(device string) bool {
	suffix, ok := strings.CutPrefix(device, "/dev/loop")
	if !ok || suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
