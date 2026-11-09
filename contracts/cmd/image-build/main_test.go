package main

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildAndCleanup(t *testing.T) {
	for _, fail := range []string{"", "create", "build", "image-rm", "rm"} {
		t.Run(fail, func(t *testing.T) {
			var calls [][]string
			var config string
			run := func(_ time.Duration, args ...string) error {
				calls = append(calls, args)
				if len(args) > 1 && args[1] == "create" {
					config = args[len(args)-1]
					data, err := os.ReadFile(config)
					if err != nil || !strings.Contains(string(data), "max-parallelism = 1") {
						t.Fatalf("builder config: %q %v", data, err)
					}
				}
				if len(args) > 1 && (args[1] == fail || fail == "image-rm" && args[0] == "image" && args[1] == "rm") {
					return errors.New("fixture failure")
				}
				return nil
			}
			err := build(run, "runner", "fixture", &bytes.Buffer{})
			if _, statErr := os.Stat(config); !os.IsNotExist(statErr) {
				t.Fatalf("config not removed: %v", statErr)
			}
			if (err != nil) != (fail != "") {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(calls[len(calls)-1], []string{"buildx", "rm", "--force", "fixture"}) {
				t.Fatalf("missing builder cleanup: %v", calls)
			}
			if fail == "" || fail == "build" {
				var command string
				for _, args := range calls {
					if len(args) > 1 && args[1] == "build" {
						command = strings.Join(args, " ")
					}
				}
				if !strings.Contains(command, "--file runner/Dockerfile.server .") || strings.Contains(command, "--cache-to") {
					t.Fatalf("build command: %s", command)
				}
			}
		})
	}
}

func TestUnavailableDockerSkipsWithoutBuilding(t *testing.T) {
	var output bytes.Buffer
	calls := 0
	err := build(func(_ time.Duration, _ ...string) error { calls++; return errors.New("unavailable") }, "core", "fixture", &output)
	if !errors.Is(err, errUnavailable) || calls != 1 || output.String() != "SKIP core-image: Docker is unavailable\n" {
		t.Fatalf("err=%v calls=%d output=%q", err, calls, output.String())
	}
}
