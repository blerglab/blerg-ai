package main

import (
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"os"
)

func main() {
	if !daemon.WorkspaceWorkerMain(os.Args[1:]) {
		os.Exit(125)
	}
}
