package runner

import (
	"errors"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var nodeHeapOption = regexp.MustCompile(`(?:^|\s)--max[-_]old[-_]space[-_]size(?:=|\s+)[0-9]+`)

// podToolEnvironment avoids host CPU discovery: the downward API supplies CPU
// millicores and memory bytes. Leave room for the engine, compiler and browser
// together, and do not let a per-worker Node heap consume the whole pod.
func podToolEnvironment(cpuRaw, memoryRaw string, existing map[string]string) (map[string]string, error) {
	milli, err := strconv.ParseInt(cpuRaw, 10, 64)
	if err != nil || milli <= 0 {
		return nil, errors.New("invalid pod CPU limit")
	}
	memory, err := strconv.ParseInt(memoryRaw, 10, 64)
	if err != nil || memory <= 0 {
		return nil, errors.New("invalid pod memory limit")
	}
	cpus := max(int64(1), milli/1000)
	workers := min(cpus, max(int64(1), memory/(2<<30)))
	heap := max(int64(16), min(int64(4096), memory/(4*workers*(1<<20))))
	count := strconv.FormatInt(workers, 10)
	appendFlag := func(old, flag string) string { return strings.TrimSpace(old + " " + flag) }
	return map[string]string{
		"GOMAXPROCS":                 strconv.FormatInt(cpus, 10),
		"GOFLAGS":                    appendFlag(existing["GOFLAGS"], "-p="+count),
		"BLERG_TOOL_WORKERS":         count,
		"VITEST_MAX_WORKERS":         count,
		"UV_THREADPOOL_SIZE":         strconv.FormatInt(min(workers, 128), 10),
		"PLAYWRIGHT_WORKERS":         count,
		"RAYON_NUM_THREADS":          count,
		"OMP_NUM_THREADS":            count,
		"CMAKE_BUILD_PARALLEL_LEVEL": count,
		"MAKEFLAGS":                  appendFlag(existing["MAKEFLAGS"], "-j"+count),
		"NODE_OPTIONS":               appendFlag(nodeHeapOption.ReplaceAllString(existing["NODE_OPTIONS"], ""), "--max-old-space-size="+strconv.FormatInt(heap, 10)),
	}, nil
}

// ConfigurePodToolResources runs in the image entry point before any agent or
// build tool starts. Non-pod callers with neither downward variable are unchanged.
// Partial/invalid downward data fails rather than sizing tools from host cores.
func ConfigurePodToolResources() error {
	cpu, memory := os.Getenv("BLERG_RUNNER_POD_CPU_MILLICORES"), os.Getenv("BLERG_RUNNER_POD_MEMORY_BYTES")
	if cpu == "" && memory == "" {
		return nil
	}
	existing := map[string]string{}
	for _, key := range []string{"GOFLAGS", "NODE_OPTIONS", "MAKEFLAGS"} {
		existing[key] = os.Getenv(key)
	}
	env, err := podToolEnvironment(cpu, memory, existing)
	if err != nil {
		return err
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	cpus, _ := strconv.Atoi(env["GOMAXPROCS"])
	runtime.GOMAXPROCS(cpus)
	return nil
}
