// Command image-build verifies a shipping image using a disposable Buildx builder.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

var errUnavailable = errors.New("docker is unavailable")

type runner func(time.Duration, ...string) error

func docker(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // Fixed executable; arguments are assembled by build and cleanup below.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if len(args) > 0 && (args[0] == "info" || args[0] == "image" && len(args) > 1 && args[1] == "inspect") {
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	}
	return cmd.Run()
}

func cleanup(run runner, name string) error {
	var imageErr error
	if run(30*time.Second, "image", "inspect", name+":test") == nil {
		imageErr = run(time.Minute, "image", "rm", "--force", name+":test")
	}
	return errors.Join(imageErr, run(time.Minute, "buildx", "rm", "--force", name))
}

func build(run runner, app, name string, output io.Writer) (err error) {
	files := map[string]string{"core": "core/Dockerfile", "board": "board/Dockerfile", "runner": "runner/Dockerfile.server"}
	file, ok := files[app]
	if !ok {
		return fmt.Errorf("unknown app %q", app)
	}
	if run(30*time.Second, "info") != nil {
		if _, err := fmt.Fprintf(output, "SKIP %s-image: Docker is unavailable\n", app); err != nil {
			return err
		}
		return errUnavailable
	}
	defer func() { err = errors.Join(err, cleanup(run, name)) }()
	config, err := os.CreateTemp("", "blerg-image-*.toml")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(config.Name())) }()
	_, writeErr := config.WriteString("[worker.oci]\n  max-parallelism = 1\n")
	if err = errors.Join(writeErr, config.Close()); err != nil {
		return err
	}
	if err = run(time.Minute, "buildx", "create", "--name", name, "--driver", "docker-container", "--buildkitd-config", config.Name()); err != nil {
		return err
	}
	return run(55*time.Minute, "buildx", "build", "--builder", name, "--load", "--tag", name+":test", "--file", file, ".")
}

func main() {
	cleanupName := flag.String("cleanup", "", "remove an owned builder and test image")
	flag.Parse()
	var err error
	switch {
	case *cleanupName != "":
		err = cleanup(docker, *cleanupName)
	case flag.NArg() == 2:
		err = build(docker, flag.Arg(0), flag.Arg(1), os.Stdout)
	default:
		err = errors.New("usage: image-build APP BUILDER or image-build -cleanup BUILDER")
	}
	if errors.Is(err, errUnavailable) {
		os.Exit(77)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
