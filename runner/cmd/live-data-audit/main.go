// live-data-audit produces private, source-indexed live-data mutation evidence.
package main

import (
	"flag"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/liveaudit"
	"os"
	"path/filepath"
)

type many []string

func (m *many) String() string     { return fmt.Sprint([]string(*m)) }
func (m *many) Set(v string) error { *m = append(*m, v); return nil }
func run() (int, error) {
	if len(os.Args) < 2 {
		return 2, fmt.Errorf("usage: live-data-audit backend|web|summarize [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	root, e := liveaudit.Root()
	if e != nil {
		return 2, e
	}
	home, _ := os.UserHomeDir()
	o := liveaudit.Options{Root: root}
	fs.StringVar(&o.Output, "output", "", "private JSON output (required)")
	fs.StringVar(&o.Go, "go", filepath.Join(home, "go/bin/go"), "Go installation")
	fs.StringVar(&o.Filter, "filter", "", "file substring or guard ID")
	fs.StringVar(&o.IDsFile, "ids-file", "", "JSON array for targeted reproofs")
	fs.StringVar(&o.TestRun, "test-run", "", "explicit Go test selector")
	fs.BoolVar(&o.List, "list", false, "catalogue only")
	var catalogs, records many
	fs.Var(&catalogs, "catalog", "current catalogue (repeatable)")
	fs.Var(&records, "record", "phase record, oldest to newest (repeatable)")
	if e = fs.Parse(os.Args[2:]); e != nil {
		return 2, e
	}
	if o.Output == "" {
		return 2, fmt.Errorf("--output is required")
	}
	switch os.Args[1] {
	case "backend":
		return liveaudit.Backend(o)
	case "web":
		return liveaudit.Web(o)
	case "summarize":
		if len(catalogs) == 0 || len(records) == 0 {
			return 2, fmt.Errorf("--catalog and --record are required")
		}
		return liveaudit.Summarize(root, catalogs, records, o.Output)
	}
	return 2, fmt.Errorf("unknown subcommand")
}
func main() {
	code, e := run()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
	}
	os.Exit(code)
}
