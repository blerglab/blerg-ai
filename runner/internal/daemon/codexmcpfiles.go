package daemon

// Restricted Codex sessions have no native tools. These dynamic file tools
// operate through an anchored workspace root and never execute a command.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const codexFileLimit = 1 << 20
const codexFileOutputLimit = 32 << 10

type codexRestrictedFiles struct{ root *os.Root }

func newCodexRestrictedFiles(work string) (*codexRestrictedFiles, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("restricted Codex tools require Linux")
	}
	r, err := os.OpenRoot(work)
	if err != nil {
		return nil, errors.New("restricted workspace is unavailable")
	}
	return &codexRestrictedFiles{root: r}, nil
}
func (f *codexRestrictedFiles) Close() error { return f.root.Close() }

// Deny the repository's executable control state and credential files even
// when nested. Symlinks are rejected atomically by codexOpenRestrictedFile,
// preventing an innocent-looking alias from bypassing these name rules.
func codexFilePath(name string, write bool) (string, error) {
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\x00\\") || strings.HasPrefix(name, "/") {
		return "", errors.New("a relative workspace path is required")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("parent traversal is not allowed")
		}
	}
	clean := path.Clean(name)
	for _, part := range strings.Split(clean, "/") {
		switch part {
		case ".git", ".codex", ".claude", ".agents", ".ssh", ".aws", ".gnupg", ".kube", ".docker", ".vscode", ".config", ".husky", ".githooks", ".git-credentials", ".gitconfig", ".netrc", ".npmrc", ".pypirc", ".mcp.json", ".envrc":
			return "", errors.New("workspace control state is not available to restricted tools")
		}
		if write && (part == "AGENTS.md" || part == "CLAUDE.md") {
			return "", errors.New("agent instructions cannot be changed by restricted tools")
		}
	}
	return clean, nil
}
func codexReadRegular(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil || !codexRestrictedRegular(info) {
		return nil, errors.New("only regular workspace files are supported")
	}
	if info.Size() > codexFileLimit {
		return nil, errors.New("file exceeds the restricted tool size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, codexFileLimit+1))
	if err != nil || len(data) > codexFileLimit {
		return nil, errors.New("file exceeds the restricted tool size limit")
	}
	return data, nil
}
func codexCapFileOutput(s string) string {
	if len(s) > codexFileOutputLimit {
		return s[:codexFileOutputLimit] + "\n[truncated]"
	}
	return s
}
func (f *codexRestrictedFiles) Execute(name string, raw json.RawMessage) (string, error) {
	return f.ExecuteContext(context.Background(), name, raw)
}
func (f *codexRestrictedFiles) ExecuteContext(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.New("file tool canceled")
	}
	if len(raw) > 2*codexFileLimit {
		return "", errors.New("file tool arguments exceed the restricted size limit")
	}
	var in struct {
		Path, Pattern, Content string
		OldString              string `json:"old_string"`
		NewString              string `json:"new_string"`
		Offset, Limit          int
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", errors.New("invalid file tool arguments")
	}
	switch name {
	case "read_file", "write_file", "edit_file":
		write := name != "read_file"
		rel, err := codexFilePath(in.Path, write)
		if err != nil {
			return "", err
		}
		if len(in.Content) > codexFileLimit || len(in.NewString) > codexFileLimit {
			return "", errors.New("content exceeds the restricted tool size limit")
		}
		file, err := codexOpenRestrictedFile(f.root, rel, write, name == "write_file", false)
		if err != nil {
			return "", errors.New("workspace file cannot be opened (symlinks are not allowed)")
		}
		defer func() { _ = file.Close() }()
		if name == "read_file" {
			if in.Offset < 0 || in.Limit < 0 {
				return "", errors.New("offset and limit must be nonnegative")
			}
			data, err := codexReadRegular(file)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(data), "\n")
			if in.Offset >= len(lines) {
				return "", nil
			}
			lines = lines[in.Offset:]
			if in.Limit > 0 && in.Limit < len(lines) {
				lines = lines[:in.Limit]
			}
			return codexCapFileOutput(strings.Join(lines, "\n")), nil
		}
		info, err := file.Stat()
		if err != nil || !codexRestrictedRegular(info) {
			return "", errors.New("only regular workspace files are supported")
		}
		content := in.Content
		if name == "edit_file" {
			data, err := codexReadRegular(file)
			if err != nil {
				return "", err
			}
			if in.OldString == "" || strings.Count(string(data), in.OldString) != 1 {
				return "", errors.New("old_string must occur exactly once")
			}
			content = strings.Replace(string(data), in.OldString, in.NewString, 1)
			if len(content) > codexFileLimit {
				return "", errors.New("edited file exceeds the restricted tool size limit")
			}
		}
		if err := file.Truncate(0); err != nil {
			return "", errors.New("workspace file cannot be written")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", errors.New("workspace file cannot be written")
		}
		if _, err := io.WriteString(file, content); err != nil {
			return "", errors.New("workspace file cannot be written")
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(content), rel), nil
	case "glob", "grep":
		if len(in.Pattern) > 2048 {
			return "", errors.New("pattern is too long")
		}
		var re *regexp.Regexp
		if name == "grep" {
			var err error
			re, err = regexp.Compile(in.Pattern)
			if err != nil {
				return "", errors.New("invalid search pattern")
			}
		}
		files, err := f.listFiles(ctx)
		if err != nil {
			return "", err
		}
		var out strings.Builder
		readBytes := 0
		for _, p := range files {
			if ctx.Err() != nil {
				return "", errors.New("file tool canceled")
			}
			if name == "glob" {
				matched, err := path.Match(in.Pattern, p)
				if err != nil {
					return "", errors.New("invalid glob pattern")
				}
				if matched {
					out.WriteString(p + "\n")
				}
			} else {
				file, err := codexOpenRestrictedFile(f.root, p, false, false, false)
				if err != nil {
					continue
				}
				data, err := codexReadRegular(file)
				_ = file.Close()
				if err != nil {
					continue
				}
				readBytes += len(data)
				if readBytes > 8<<20 {
					return codexCapFileOutput(out.String() + "[search stopped at the restricted read limit]"), nil
				}
				for i, line := range strings.Split(string(data), "\n") {
					if re.MatchString(line) {
						fmt.Fprintf(&out, "%s:%d:%s\n", p, i+1, line)
						if out.Len() > codexFileOutputLimit {
							break
						}
					}
				}
			}
			if out.Len() > codexFileOutputLimit {
				break
			}
		}
		return codexCapFileOutput(out.String()), nil
	default:
		return "", errors.New("file tool is not allowed")
	}
}
func (f *codexRestrictedFiles) listFiles(ctx context.Context) ([]string, error) {
	out := []string{}
	visited := 0
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if ctx.Err() != nil {
			return errors.New("file tool canceled")
		}
		if depth > 64 || visited >= 10000 {
			return errors.New("workspace file listing exceeds the restricted tool limit")
		}
		file, err := codexOpenRestrictedFile(f.root, dir, false, false, true)
		if err != nil {
			return nil
		}
		defer func() { _ = file.Close() }()
		for {
			entries, err := file.ReadDir(128)
			if err != nil && err != io.EOF {
				return errors.New("workspace directory cannot be read")
			}
			for _, e := range entries {
				visited++
				if visited > 10000 {
					return errors.New("workspace file listing exceeds the restricted tool limit")
				}
				rel := path.Join(dir, e.Name())
				if _, err := codexFilePath(rel, false); err != nil {
					continue
				}
				if e.Type()&os.ModeSymlink != 0 {
					continue
				}
				if e.IsDir() {
					if err := walk(rel, depth+1); err != nil {
						return err
					}
				} else if e.Type().IsRegular() {
					out = append(out, rel)
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
