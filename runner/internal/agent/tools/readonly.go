// Package tools implements the core tool set for blerg-runner agent sessions.
// Each tool resolves paths inside a workspace directory and caps its output.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const outputCap = 16384

func capOutput(s string) string {
	if len(s) <= outputCap {
		return s
	}
	return s[:outputCap] + "\n[truncated]"
}

// resolve joins rel onto workDir and rejects escapes, including via
// symlinks that exist inside the workspace but point outside it.
func resolve(workDir, rel string) (string, error) {
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	abs, err := filepath.Abs(filepath.Join(workDir, rel))
	if err != nil {
		return "", err
	}
	inside := func(p string) bool {
		return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
	}
	if !inside(abs) && !insideAfterSymlinks(abs, inside) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	// Re-check the real path: a symlink inside the workspace must not point
	// outside it. Walk up to the deepest existing ancestor (the target may
	// not exist yet, e.g. write_file creating a new file).
	resolved := abs
	for {
		if r, err := filepath.EvalSymlinks(resolved); err == nil {
			if !inside(r) {
				return "", fmt.Errorf("path %q escapes workspace via symlink", rel)
			}
			break
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			break
		}
		resolved = parent
	}
	return abs, nil
}

// insideAfterSymlinks reports whether abs normalizes into root once the
// workspace root's own symlink (e.g. /tmp on macOS) is resolved.
func insideAfterSymlinks(abs string, inside func(string) bool) bool {
	r, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return false
	}
	return inside(filepath.Join(r, filepath.Base(abs)))
}

// fnTool adapts a func to agent.Tool.
type fnTool struct {
	def      agent.ToolDef
	mutating bool
	run      func(ctx context.Context, input json.RawMessage) (string, error)
}

func (t fnTool) Def() agent.ToolDef { return t.def }
func (t fnTool) Mutating() bool     { return t.mutating }
func (t fnTool) Execute(ctx context.Context, in json.RawMessage) (string, error) {
	return t.run(ctx, in)
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// ReadFile returns the "read_file" tool.
func ReadFile(workDir string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "read_file",
			Description: "Read a file from the workspace. Returns its content (capped at 16KB).",
			InputSchema: schema(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`),
		},
		run: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			p, err := resolve(workDir, args.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(p) //nolint:gosec // path confined to the session workspace by resolve() (symlink-aware containment check)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(data), "\n")
			if args.Offset >= len(lines) {
				return "", nil // offset beyond EOF
			}
			if args.Offset > 0 {
				lines = lines[args.Offset:]
			}
			if args.Limit > 0 && args.Limit < len(lines) {
				lines = lines[:args.Limit]
			}
			return capOutput(strings.Join(lines, "\n")), nil
		},
	}
}

// Glob returns the "glob" tool.
func Glob(workDir string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "glob",
			Description: "List workspace files matching a glob pattern (path/*.go style; ** is not supported).",
			InputSchema: schema(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`),
		},
		run: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			matches, err := filepath.Glob(filepath.Join(workDir, args.Pattern))
			if err != nil {
				return "", err
			}
			rels := make([]string, 0, len(matches))
			for _, m := range matches {
				rel, err := filepath.Rel(workDir, m)
				if err == nil {
					rels = append(rels, rel)
				}
			}
			return capOutput(strings.Join(rels, "\n")), nil
		},
	}
}

// Grep returns the "grep" tool.
func Grep(workDir string) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "grep",
			Description: "Search workspace file contents with a Go regexp. Returns file:line:text matches.",
			InputSchema: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`),
		},
		run: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			re, err := regexp.Compile(args.Pattern)
			if err != nil {
				return "", err
			}
			root := workDir
			if args.Path != "" {
				root, err = resolve(workDir, args.Path)
				if err != nil {
					return "", err
				}
			}
			var b strings.Builder
			err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if d != nil && d.IsDir() && d.Name() == ".git" {
						return fs.SkipDir
					}
					return err
				}
				if info, err := d.Info(); err != nil || info.Size() > 2<<20 {
					return nil //nolint:nilerr // a file that cannot be statted is skipped, the search goes on
				}
				data, err := os.ReadFile(p) //nolint:gosec // p comes from WalkDir under a root validated by resolve(); files over 2 MB are skipped just above
				if err != nil {
					return nil //nolint:nilerr // an unreadable file is skipped, the search goes on
				}
				if bytes.IndexByte(data, 0) >= 0 {
					return nil // skip binary files
				}
				rel, _ := filepath.Rel(workDir, p)
				for i, line := range strings.Split(string(data), "\n") {
					if re.MatchString(line) {
						fmt.Fprintf(&b, "%s:%d:%s\n", rel, i+1, line)
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			return capOutput(b.String()), nil
		},
	}
}
