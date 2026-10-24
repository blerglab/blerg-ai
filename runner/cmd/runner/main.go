// blerg-runner: PID 1 of a per-session agent pod. See internal/runner.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/runner"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

var version = "dev"

func main() {
	if handled, code := runner.ServeClusterWriter(os.Args[1:]); handled {
		os.Exit(code)
	}
	if len(os.Args) >= 2 && os.Args[1] == "cluster-shell-supervisor" {
		if len(os.Args) == 3 && os.Args[2] == "--probe" {
			if shellruntime.Probe(os.Stdout) != nil {
				os.Exit(1)
			}
			return
		}
		if len(os.Args) != 2 || shellruntime.ServeClusterHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		return
	}

	if len(os.Args) == 3 && os.Args[1] == "shell-supervisor" && os.Args[2] == "--probe" {
		if shellruntime.Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "shell-supervisor" {
		if err := shellruntime.ServeHelper(os.Stdin, os.Stdout); err != nil {
			if errors.Is(err, shellruntime.ErrIdentityChanged) {
				os.Exit(3)
			}
			os.Exit(1)
		}
		return
	}
	cfg := runner.ConfigFromEnv()
	cfg.Version = version
	if err := runner.ConfigureClusterRuntime(&cfg); err != nil {
		log.Fatal("cluster runtime isolation unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		log.Println("runner: SIGTERM — shutting down")
		cancel()
	}()

	if err := runner.Run(ctx, cfg); err != nil {
		log.Fatalf("runner: %v", err)
	}
}
