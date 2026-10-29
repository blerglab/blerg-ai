package runner

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestPodToolResourceDefaults(t *testing.T) {
	tests := []struct{ cpu, mem, workers, heap string }{
		{"1000", "4294967296", "1", "1024"},
		{"500", "4294967296", "1", "1024"},
		{"1500", "4294967296", "1", "1024"},
		{"16000", "4294967296", "2", "512"},
		{"4000", "12884901888", "4", "768"},
	}
	for _, tt := range tests {
		t.Run(tt.cpu+"_"+tt.mem, func(t *testing.T) {
			env, err := podToolEnvironment(tt.cpu, tt.mem, map[string]string{"GOFLAGS": "-tags=fixture", "NODE_OPTIONS": "--trace-warnings", "MAKEFLAGS": "--no-print-directory"})
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"BLERG_TOOL_WORKERS", "VITEST_MAX_WORKERS", "UV_THREADPOOL_SIZE", "PLAYWRIGHT_WORKERS", "RAYON_NUM_THREADS", "OMP_NUM_THREADS", "CMAKE_BUILD_PARALLEL_LEVEL"} {
				if env[key] != tt.workers {
					t.Fatalf("%s=%q want %s", key, env[key], tt.workers)
				}
			}
			if env["GOFLAGS"] != "-tags=fixture -p="+tt.workers || env["NODE_OPTIONS"] != "--trace-warnings --max-old-space-size="+tt.heap || env["MAKEFLAGS"] != "--no-print-directory -j"+tt.workers {
				t.Fatal(env)
			}
		})
	}
}

func TestPodToolResourceValidationAndOverrides(t *testing.T) {
	for _, pair := range [][2]string{{"0", "4294967296"}, {"-1", "4294967296"}, {"1000", "0"}, {"1.5", "4Gi"}, {"1000", "oops"}} {
		if _, err := podToolEnvironment(pair[0], pair[1], nil); err == nil {
			t.Fatal("invalid downward limits accepted", pair)
		}
	}
	env, err := podToolEnvironment("1000", "4294967296", map[string]string{"GOFLAGS": "-p=99 -tags=fixture", "NODE_OPTIONS": "--trace-warnings --max_old_space_size=8192", "VITEST_MAX_WORKERS": "16"})
	if err != nil || env["VITEST_MAX_WORKERS"] != "1" || !strings.HasSuffix(env["GOFLAGS"], "-p=1") || strings.Contains(env["NODE_OPTIONS"], "8192") {
		t.Fatal(env, err)
	}
}

func TestPodToolResourcesReachChildProcess(t *testing.T) {
	if os.Getenv("BLERG_POD_RESOURCE_SUITE") == "1" {
		// The opt-in host-only resource smoke executes real suites under Docker's
		// 1-CPU/4Gi cgroup using exactly the production environment initializer.
		if err := ConfigurePodToolResources(); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", os.Getenv("BLERG_POD_RESOURCE_SCRIPT"))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	before := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(before) })
	t.Setenv("BLERG_RUNNER_POD_CPU_MILLICORES", "1000")
	t.Setenv("BLERG_RUNNER_POD_MEMORY_BYTES", "4294967296")
	for _, key := range []string{"GOMAXPROCS", "GOFLAGS", "NODE_OPTIONS", "MAKEFLAGS", "BLERG_TOOL_WORKERS", "VITEST_MAX_WORKERS", "UV_THREADPOOL_SIZE", "PLAYWRIGHT_WORKERS", "RAYON_NUM_THREADS", "OMP_NUM_THREADS", "CMAKE_BUILD_PARALLEL_LEVEL"} {
		t.Setenv(key, "")
	}
	if err := ConfigurePodToolResources(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", `printf '%s/%s/%s' "$GOMAXPROCS" "$GOFLAGS" "$VITEST_MAX_WORKERS"`).Output()
	if err != nil || string(out) != "1/-p=1/1" {
		t.Fatalf("%s %v", out, err)
	}
}
