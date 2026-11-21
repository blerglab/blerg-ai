package releaseperf

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HostFingerprint is recorded with every baseline (docs/design/release-performance.md,
// "Host fingerprint (H3)"). A baseline is only ever valid on the exact
// machine that recorded it: a faster or slower host would otherwise look
// like a regression-free release, or a false regression, for the wrong
// reason.
type HostFingerprint struct {
	CPUModel    string
	LogicalCPUs int
	MemoryGiB   float64
	Kernel      string
	WSL         bool
	Browser     string
}

// Mismatch returns the first field name that differs between two
// fingerprints, if any. Browser is compared last since it's the one field
// the caller usually fills in after this is read (once a browser is
// launched), not at read time.
func (a HostFingerprint) Mismatch(b HostFingerprint) (field string, mismatched bool) {
	switch {
	case a.CPUModel != b.CPUModel:
		return "cpu_model", true
	case a.LogicalCPUs != b.LogicalCPUs:
		return "logical_cpus", true
	case a.MemoryGiB != b.MemoryGiB:
		return "memory_gib", true
	case a.Kernel != b.Kernel:
		return "kernel", true
	case a.WSL != b.WSL:
		return "wsl", true
	case a.Browser != b.Browser:
		return "browser", true
	default:
		return "", false
	}
}

// ReadHostFingerprint reads this host's CPU model, logical CPU count, total
// memory, kernel release and WSL status. Browser is left empty: the caller
// fills it in once a browser is actually launched for the Playwright
// scenarios.
func ReadHostFingerprint() (HostFingerprint, error) {
	cpuModel, err := readCPUModel("/proc/cpuinfo")
	if err != nil {
		return HostFingerprint{}, err
	}
	memGiB, err := readTotalMemGiB("/proc/meminfo")
	if err != nil {
		return HostFingerprint{}, err
	}
	kernel, err := readKernelRelease()
	if err != nil {
		return HostFingerprint{}, err
	}
	return HostFingerprint{
		CPUModel:    cpuModel,
		LogicalCPUs: runtime.NumCPU(),
		MemoryGiB:   memGiB,
		Kernel:      kernel,
		WSL:         isWSL(kernel),
	}, nil
}

func readCPUModel(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: callers pass the fixed /proc/cpuinfo constant above
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "model name") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		return strings.TrimSpace(parts[1]), nil
	}
	return "unknown", scanner.Err()
}

func readTotalMemGiB(path string) (float64, error) {
	f, err := os.Open(path) //nolint:gosec // G304: callers pass the fixed /proc/meminfo constant above
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kib, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, err
		}
		return kib / (1024 * 1024), nil
	}
	return 0, scanner.Err()
}

func readKernelRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isWSL matches on the kernel release string (WSL2 kernels are suffixed
// "-microsoft-standard-WSL2") and, as a fallback, the presence of the WSL
// interop binfmt entry — either is sufficient evidence.
func isWSL(kernel string) bool {
	if strings.Contains(strings.ToLower(kernel), "microsoft") {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}
