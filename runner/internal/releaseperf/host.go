package releaseperf

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ReadHostState reads the live host's load average, CPU count and available
// memory from /proc, for CheckHostQuiet. Linux only, matching the rest of
// this project's gate tooling.
func ReadHostState() (HostState, error) {
	load1, err := readLoad1("/proc/loadavg")
	if err != nil {
		return HostState{}, err
	}
	memGiB, err := readAvailableMemGiB("/proc/meminfo")
	if err != nil {
		return HostState{}, err
	}
	return HostState{Load1: load1, LogicalCPUs: runtime.NumCPU(), AvailableMemGiB: memGiB}, nil
}

func readLoad1(path string) (float64, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: callers pass the fixed /proc/loadavg constant below
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("%s: unexpected format %q", path, string(data))
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readAvailableMemGiB(path string) (float64, error) {
	f, err := os.Open(path) //nolint:gosec // G304: callers pass the fixed /proc/meminfo constant below
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("%s: unexpected MemAvailable line %q", path, line)
		}
		kib, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, err
		}
		return kib / (1024 * 1024), nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s: no MemAvailable line found", path)
}
