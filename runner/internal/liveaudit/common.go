// Package liveaudit runs source-indexed guard mutations exclusively in private archives.
package liveaudit

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Object = map[string]any

func str(m Object, k string) string { s, _ := m[k].(string); return s }
func num(m Object, k string) int {
	switch n := m[k].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func rows(m Object, k string) []Object {
	var out []Object
	switch a := m[k].(type) {
	case []Object:
		return a
	case []any:
		for _, v := range a {
			if r, ok := v.(map[string]any); ok {
				out = append(out, r)
			}
		}
	}
	return out
}
func stringsOf(v any) []string {
	switch a := v.(type) {
	case []string:
		return a
	case []any:
		out := []string{}
		for _, s := range a {
			if value, ok := s.(string); ok {
				out = append(out, value)
			}
		}
		return out
	}
	return nil
}
func clone(m Object) Object {
	r := Object{}
	for k, v := range m {
		r[k] = v
	}
	return r
}
func digest(s []byte) string { h := sha256.Sum256(s); return hex.EncodeToString(h[:]) }
func load(path string) (Object, error) {
	b, e := os.ReadFile(path) //nolint:gosec // Audit CLI input path explicitly chosen by its operator.
	if e != nil {
		return nil, e
	}
	var m Object
	e = json.Unmarshal(b, &m)
	return m, e
}
func save(path string, m Object) error {
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func source(root, file string) (string, error) {
	p, e := safePath(root, file)
	if e != nil {
		return "", e
	}
	b, e := os.ReadFile(p) //nolint:gosec // safePath resolved symlinks and verified repository containment.
	return string(b), e
}
func safePath(root, file string) (string, error) {
	p, e := filepath.EvalSymlinks(filepath.Join(root, file))
	if e != nil {
		return "", e
	}
	rel, e := filepath.Rel(root, p)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("source escapes repository")
	}
	return p, nil
}
func Root() (string, error) {
	r, _, _, e := command("", nil, 0, "git", "rev-parse", "--show-toplevel")
	return strings.TrimSpace(r), e
}
func command(dir string, env []string, timeout time.Duration, name string, args ...string) (string, int, bool, error) {
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	c := exec.CommandContext(ctx, name, args...) //nolint:gosec // Local audit commands and explicit argv; no shell or remote requests.
	c.Dir = dir
	c.Env = env
	var b bytes.Buffer
	c.Stdout = &b
	c.Stderr = &b
	e := c.Run()
	exit := 0
	if e != nil {
		exit = 1
		var ee *exec.ExitError
		if errors.As(e, &ee) {
			exit = ee.ExitCode()
		} else if ctx.Err() == nil {
			return b.String(), exit, false, e
		}
	}
	return b.String(), exit, ctx.Err() != nil, nil
}
func envSet(env []string, key, value string) []string {
	out := []string{}
	for _, v := range env {
		if !strings.HasPrefix(v, key+"=") {
			out = append(out, v)
		}
	}
	return append(out, key+"="+value)
}
func archive(root, tree string) error {
	c := exec.CommandContext(context.Background(), "git", "archive", "HEAD")
	c.Dir = root
	pipe, e := c.StdoutPipe()
	if e != nil {
		return e
	}
	if e = c.Start(); e != nil {
		return e
	}
	tr := tar.NewReader(pipe)
	for {
		h, er := tr.Next()
		if er == io.EOF {
			break
		}
		if er != nil {
			return er
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe archive member")
		}
		p := filepath.Join(tree, name)
		switch h.Typeflag {
		case tar.TypeDir:
			e = os.MkdirAll(p, 0700)
		case tar.TypeReg:
			if e = os.MkdirAll(filepath.Dir(p), 0700); e == nil {
				var f *os.File
				f, e = os.OpenFile(p, os.O_CREATE|os.O_WRONLY, os.FileMode(h.Mode)&0777) //nolint:gosec // Cleaned archive name checked for absolute paths and parent traversal above.
				if e == nil {
					_, e = io.CopyN(f, tr, h.Size) // Git archive supplies an exact uncompressed member length.
					ce := f.Close()
					if e == nil {
						e = ce
					}
				}
			}
		case tar.TypeSymlink: // Preserve repository symlinks only when their target stays in the archive.
			target := filepath.Clean(filepath.Join(filepath.Dir(name), h.Linkname)) //nolint:gosec // Target is checked for absolute paths and traversal immediately below.
			if filepath.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
				return errors.New("unsafe archive symlink")
			}
			e = os.MkdirAll(filepath.Dir(p), 0700)
			if e == nil {
				e = os.Symlink(h.Linkname, p)
			}
		}
		if e != nil {
			return e
		}
	}
	return c.Wait()
}
func matches(pattern, s string) []string {
	out := []string{}
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}
func unique(a []string) []string {
	s := map[string]bool{}
	for _, v := range a {
		s[v] = true
	}
	out := []string{}
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func goOutcome(exit int, output string) string {
	if strings.Contains(output, "panic: test timed out") {
		return "timeout"
	}
	if exit != 0 && len(matches(`--- FAIL: ([^\s(]+)`, output)) > 0 && !strings.Contains(output, "panic:") {
		return "killed"
	}
	if exit == 0 {
		return "survived"
	}
	return "harness_or_panic"
}
func summary(guards []Object) Object {
	counts := Object{}
	for _, g := range guards {
		k := str(g, "status")
		counts[k] = num(counts, k) + 1
	}
	return counts
}
func selectGuards(guards []Object, filter, idsFile string) ([]Object, error) {
	var ids map[string]bool
	if idsFile != "" {
		b, e := os.ReadFile(idsFile) //nolint:gosec // Explicit operator-selected local list of guard IDs.
		if e != nil {
			return nil, e
		}
		var a []string
		if e = json.Unmarshal(b, &a); e != nil {
			return nil, e
		}
		ids = map[string]bool{}
		for _, s := range a {
			ids[s] = true
		}
	}
	out := []Object{}
	for _, g := range guards {
		if filter != "" && !strings.Contains(str(g, "file"), filter) && !strings.Contains(str(g, "id"), filter) {
			continue
		}
		if ids != nil && !ids[str(g, "id")] {
			continue
		}
		out = append(out, g)
		delete(ids, str(g, "id"))
	}
	if len(ids) > 0 {
		return nil, fmt.Errorf("unknown guard IDs: %s", strings.Join(mapKeys(ids), ","))
	}
	if len(out) == 0 {
		return nil, errors.New("no guards selected")
	}
	return out, nil
}
func mapKeys[V any](m map[string]V) []string {
	a := []string{}
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
func rounded(d time.Duration) float64 { return float64(d.Milliseconds()) / 1000 }
