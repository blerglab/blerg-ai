// blerg-runner: PID 1 of a per-session agent pod. See internal/runner.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/runner"
)

var version = "dev"

func main() {
	cfg := runner.ConfigFromEnv()
	cfg.Version = version

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
