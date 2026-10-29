//go:build linux

package runner

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
)

// Only launcher-created support directories are transferred. Runtime snapshots,
// marker keys, public member paths and arbitrary argument paths are excluded.
// A root already transferred to the agent is never traversed as trusted state.
func projectClusterLaunchFiles(cmd *exec.Cmd) error {
	paths := append([]string{cmd.Dir}, cmd.Args...)
	for _, v := range cmd.Env {
		key, path, ok := strings.Cut(v, "=")
		if ok && (key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR") {
			paths = append(paths, path)
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		var root string
		switch {
		case strings.HasPrefix(path, "/tmp/blerg-"):
			tail := strings.TrimPrefix(path, "/tmp/")
			root = "/tmp/" + strings.Split(tail, "/")[0]
		case strings.HasPrefix(path, "/workspace/.blerg/"):
			root = "/workspace/.blerg/" + strings.Split(strings.TrimPrefix(path, "/workspace/.blerg/"), "/")[0]
		default:
			continue
		}
		if seen[root] {
			continue
		}
		seen[root] = true

		var authority *os.Root
		var err error
		if strings.HasPrefix(root, "/workspace/") {
			home, e := os.OpenRoot("/workspace")
			if e != nil {
				return e
			}
			authority, err = home.OpenRoot(strings.TrimPrefix(root, "/workspace/"))
			_ = home.Close()
		} else {
			authority, err = os.OpenRoot(root)
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = transferClusterSupportRoot(authority)
		_ = authority.Close()
		if err != nil {
			return err
		}

	}
	return nil
}

func transferClusterSupportRoot(root *os.Root) error {
	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != clusterSupervisorUID {
		return nil
	}
	if info.Mode().Perm()&0022 != 0 {
		return clusterworkspace.ErrPlan
	}
	// Pin traversal to this descriptor. Agent-controlled parent names and links
	// cannot redirect a later chown outside this exact support directory.
	if err = root.Chmod(".", 0700); err != nil {
		return err
	}
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		count++
		if count > 200000 {
			return clusterworkspace.ErrPlan
		}
		if path == "." {
			return nil
		}
		return root.Lchown(path, 1000, 1000)
	})
	if err != nil {
		return err
	}
	if err = root.Chmod(".", info.Mode().Perm()); err != nil {
		return err
	}
	return root.Lchown(".", 1000, 1000)
}
