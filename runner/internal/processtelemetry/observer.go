// Package processtelemetry keeps bounded, non-authoritative process evidence.
// It never retains paths, command lines, environment, or arbitrary program names.
package processtelemetry

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MaxSpans     = 256
	MaxProcesses = 4096
	QueueSize    = 4096
)

type Kind uint8

const (
	Fork Kind = iota
	Exec
	Exit
)

type Event struct {
	Kind        Kind
	PID, Parent int
	At          time.Time
}
type Span struct {
	PID         int       `json:"pid,omitempty"`
	Root        int       `json:"root,omitempty"`
	Branch      int       `json:"branch,omitempty"`
	CallID      string    `json:"call_id,omitempty"`
	Program     string    `json:"program"`
	Class       string    `json:"class"`
	Start       time.Time `json:"started_at"`
	End         time.Time `json:"ended_at"`
	Approximate bool      `json:"approximate"`
	Basis       string    `json:"basis"`
}
type Batch struct {
	Spans   []Span `json:"spans"`
	Dropped uint64 `json:"dropped,omitempty"`
}
type process struct {
	branch                   int
	program, class, identity string
	at                       time.Time
}
type Observer struct {
	// Identity names a live process's kernel start. Nil leaves Sweep without work.
	Identity func(int) string

	mu      sync.Mutex
	read    func(int) (string, string)
	live    map[int]process
	spans   []Span
	dropped uint64
}

func NewObserver(read func(int) (string, string)) *Observer {
	return &Observer{read: read, live: map[int]process{}}
}

// Apply takes its /proc reads before the lock: a read can stall on a process in
// exec or uninterruptible sleep, and that may only ever delay telemetry.
func (o *Observer) Apply(e Event) {
	var program, class, identity string
	if e.Kind == Exec {
		if program, class = o.read(e.PID); program != "" && o.Identity != nil {
			identity = o.Identity(e.PID)
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p := o.live[e.PID]
	finish := func() {
		if p.program != "" && e.At.After(p.at) {
			o.add(Span{PID: e.PID, Branch: p.branch, Program: p.program, Class: p.class, Start: p.at, End: e.At, Approximate: true, Basis: "exec_observed_async_image"})
		}
	}
	switch e.Kind {
	case Fork:
		if len(o.live) >= MaxProcesses {
			o.dropped++
			return
		}
		branch := o.live[e.Parent].branch
		if branch == 0 {
			branch = e.PID
		}
		o.live[e.PID] = process{branch: branch, at: e.At}
	case Exec:
		finish()
		if len(o.live) >= MaxProcesses && p.at.IsZero() {
			o.dropped++
			return
		}
		p.program, p.class, p.identity = program, class, identity
		p.at = e.At
		o.live[e.PID] = p
	case Exit:
		finish()
		delete(o.live, e.PID)
	}
}

// Sweep forgets processes that are gone without an Exit: the queue dropped it, or
// the PID now names another process. Without this Drain would extend their spans
// forever. Routine sweeps read only classified entries; all also reads the
// unclassified ones, which matters only after the queue has dropped events.
func (o *Observer) Sweep(all bool) {
	if o.Identity == nil {
		return
	}
	o.mu.Lock()
	check := make(map[int]string, len(o.live))
	for pid, p := range o.live {
		if all || p.program != "" {
			check[pid] = p.identity
		}
	}
	o.mu.Unlock()
	for pid, identity := range check {
		current := o.Identity(pid)
		if current != "" && (identity == "" || current == identity) {
			continue
		}
		o.mu.Lock()
		if p, ok := o.live[pid]; ok && p.identity == identity {
			delete(o.live, pid)
		}
		o.mu.Unlock()
	}
}
func (o *Observer) add(s Span) {
	if len(o.spans) >= MaxSpans {
		o.dropped++
		return
	}
	o.spans = append(o.spans, s)
}

// Drain never waits: a busy observer yields an empty batch and the next one
// carries its spans.
func (o *Observer) Drain(now time.Time) Batch {
	if !o.mu.TryLock() {
		return Batch{}
	}
	defer o.mu.Unlock()
	for pid, p := range o.live {
		if p.program != "" && now.After(p.at) {
			o.add(Span{PID: pid, Branch: p.branch, Program: p.program, Class: p.class, Start: p.at, End: now, Approximate: true, Basis: "exec_observed_async_image"})
			p.at = now
			o.live[pid] = p
		}
	}
	b := Batch{Spans: o.spans, Dropped: o.dropped}
	o.spans = nil
	o.dropped = 0
	return b
}

// Classify examines only fixed executable/script basenames and known action
// selectors. Callers discard argv immediately; arbitrary names never escape.
func Classify(exe string, argv []string) (string, string) {
	name := filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
	if strings.HasSuffix(name, ".test") {
		return "go-test", "tests"
	}
	switch name {
	case "vitest", "vitest.mjs", "playwright", "playwright-cli":
		return strings.Split(name, ".")[0], "tests"
	case "golangci-lint", "tsc", "tsc.js":
		return strings.TrimSuffix(name, ".js"), "build_lint"
	case "go":
		for i := 1; i < len(argv); i++ {
			a := argv[i]
			if a == "-C" {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			switch a {
			case "test":
				return "go", "tests"
			case "build", "vet":
				return "go", "build_lint"
			}
			break
		}
	case "npm", "npm-cli.js":
		for i := 1; i < len(argv); i++ {
			a := argv[i]
			if a == "--prefix" || a == "--workspace" || a == "-w" || a == "--cache" || a == "--userconfig" {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") || a == "run" {
				continue
			}
			if a == "test" || strings.HasPrefix(a, "test:") {
				return "npm", "tests"
			}
			if a == "build" || a == "lint" || a == "typecheck" {
				return "npm", "build_lint"
			}
			break
		}
	case "node", "nodejs":
		// Node executes these known entrypoints; the directory is never retained.
		for i := 1; i < len(argv); i++ {
			a := argv[i]
			if a == "-e" || a == "--eval" || a == "-p" || a == "--print" {
				return "", ""
			}
			if a == "-r" || a == "--require" || a == "--import" || a == "--loader" {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			base := filepath.Base(a)
			switch base {
			case "vitest.mjs", "vitest.js", "vitest":
				return "vitest", "tests"
			case "playwright", "playwright-cli", "cli.js":
				if base != "cli.js" || strings.Contains(filepath.ToSlash(a), "/playwright/") || strings.Contains(filepath.ToSlash(a), "/playwright-core/") {
					return "playwright", "tests"
				}
			case "tsc", "tsc.js":
				return "tsc", "build_lint"
			case "npm-cli.js":
				return Classify("npm", append([]string{"npm"}, argv[i+1:]...))
			}
			break
		}
	}
	return "", ""
}
func Valid(program, class string) bool {
	switch program {
	case "go-test", "vitest", "playwright", "playwright-cli":
		return class == "tests"
	case "golangci-lint", "tsc":
		return class == "build_lint"
	case "go", "npm":
		return class == "tests" || class == "build_lint"
	}
	return false
}
