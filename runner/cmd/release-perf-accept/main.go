// release-perf-accept promotes a release-perf candidate baseline to the
// official, committed one — the explicit, owner-gated step
// docs/design/release-performance.md calls "Baseline acceptance (M5)". It
// refuses a candidate whose own run was not clean.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/blerglab/blerg-ai/runner/internal/releaseperf"
)

func main() { os.Exit(run()) }

func run() int {
	candidate := flag.String("candidate", "", "path to the candidate baseline JSON")
	out := flag.String("out", "", "path to write the official baseline JSON (e.g. docs/performance-baselines/0.6.0.json)")
	releaseTag := flag.String("release-tag", "", "the release tag being cut (e.g. v0.6.0)")
	flag.Parse()
	if *candidate == "" || *out == "" || *releaseTag == "" {
		fmt.Fprintln(os.Stderr, "usage: release-perf-accept --candidate <path> --out <path> --release-tag <tag>")
		return 2
	}
	if err := releaseperf.AcceptBaseline(*candidate, *out, *releaseTag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Accepted:", *out)
	return 0
}
