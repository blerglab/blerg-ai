// workspace-explorer is an in-runtime read-only helper. Its private stdin and
// stdout belong to the trusted runtime coordinator, never an interactive shell.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
)

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) == 2 && os.Args[1] == "--probe" {
		if daemon.ProbeWorkspaceExplorer(os.Stdout) != nil {
			return 1
		}
		return 0
	}
	root := flag.String("public-root", "", "trusted public root")
	instructions := flag.Bool("instructions", false, "discover runtime instruction metadata")
	flag.Parse()
	if *root == "" || flag.NArg() != 0 {
		return 2
	}
	if *instructions {
		return daemon.WriteRuntimeInstructions(os.Stdout, *root)
	}
	private, err := os.MkdirTemp("", "blerg-explorer-")
	if err != nil {
		return 1
	}
	defer os.RemoveAll(private) //nolint:errcheck // private helper-owned temporary directory
	if os.Setenv("TMPDIR", private) != nil {
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	// Close only our inherited input to wake the scanner on termination. Git's
	// context cancellation then kills its own process group before exit.
	go func() { <-ctx.Done(); _ = os.Stdin.Close() }()
	if err := daemon.ServeWorkspaceExplorer(ctx, *root, os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "workspace explorer: %v\n", err)
		return 1
	}
	return 0
}
