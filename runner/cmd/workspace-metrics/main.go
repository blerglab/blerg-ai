// workspace-metrics supplies the same Go CLI inside local sandbox images.
package main

import (
	"github.com/blerglab/blerg-ai/runner/internal/plancli"
	"github.com/blerglab/blerg-ai/runner/internal/testreportcli"
	"github.com/blerglab/blerg-ai/runner/internal/workspacecli"
	"io"
	"os"
)

func main() {
	os.Exit(runCommand(os.Args[1:], os.Stdout, os.Stderr))
}

func runCommand(args []string, out, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "test" {
		return testreportcli.Main(args[1:], out, stderr)
	}
	if len(args) > 0 && args[0] == "plan" {
		return plancli.Main(args[1:], out, stderr)
	}
	if len(args) > 0 && args[0] == "workspace" {
		args = args[1:]
	}
	return workspacecli.Main(args, out, stderr)
}
