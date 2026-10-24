// Fixed runtime-local owner-shell supervisor. It accepts no command argv.
package main

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--probe" {
		if shellruntime.Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		os.Exit(2)
	}
	if err := shellruntime.ServeHelper(os.Stdin, os.Stdout); err != nil {
		if errors.Is(err, shellruntime.ErrIdentityChanged) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}
