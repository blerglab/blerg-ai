package daemon

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/skills"
)

// skillPack is session scoped: neither a disabled skill nor Main's coordination
// instructions are installed in a shared engine home. The same paths are visible
// to native subagents. Container files are created after the runtime is ready.
type skillPack struct {
	dir     string
	prefix  sandboxExec
	entries []skills.Entry
}

func prepareSkillPack(prefix sandboxExec, policy skills.Policy) (*skillPack, error) {
	return prepareSkillPackAt(prefix, policy, "")
}

// The path belongs to the Blerg session, not a particular engine process.
// Retained native conversations continue to reference it after thread/resume.
func sessionSkillDir(stateDir, session string) string {
	return filepath.Join(stateDir, "skill-packs", fmt.Sprintf("%x", sha256.Sum256([]byte(session))))
}

// PodSkillStateDir is separate from credentials and beneath the pod's sticky
// /tmp mount, so the agent cannot rename the supervisor-owned skill root.
func PodSkillStateDir(home string) string {
	return filepath.Join("/tmp", fmt.Sprintf("blerg-session-skills-%x", sha256.Sum256([]byte(home))))
}

func prepareSkillPackAt(prefix sandboxExec, policy skills.Policy, stableDir string) (*skillPack, error) {
	return prepareSkillPackWithAccess(prefix, policy, stableDir, false)
}

func prepareSkillPackWithAccess(prefix sandboxExec, policy skills.Policy, stableDir string, groupRead bool) (*skillPack, error) {
	p := &skillPack{prefix: prefix, entries: skills.Inventory(policy)}
	var err error
	if prefix.enabled() {
		if stableDir == "" {
			stableDir = ccUUID()
		}
		p.dir = sandboxMCPDirBase + "/blerg-skills-" + fmt.Sprintf("%x", sha256.Sum256([]byte(stableDir)))
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		manifest := []byte("{\"name\":\"blerg\",\"version\":\"1.0.0\"}")
		if err = tw.WriteHeader(&tar.Header{Name: ".claude-plugin/plugin.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
			return nil, err
		}
		if _, err = tw.Write(manifest); err != nil {
			return nil, err
		}

		for _, e := range p.entries {
			name := "skills/" + e.Name + "/SKILL.md"
			if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(e.Body))}); err != nil {
				return nil, err
			}
			if _, err = tw.Write([]byte(e.Body)); err != nil {
				return nil, err
			}
			if e.Examples == "" {
				continue
			}
			examplesName := "skills/" + e.Name + "/examples.md"
			if err = tw.WriteHeader(&tar.Header{Name: examplesName, Mode: 0600, Size: int64(len(e.Examples))}); err != nil {
				return nil, err
			}
			if _, err = tw.Write([]byte(e.Examples)); err != nil {
				return nil, err
			}
		}
		if err = tw.Close(); err != nil {
			return nil, err
		}
		// Only generated relative archive entries and a session-private root.
		err = dockerRunStdin(buf.Bytes(), "exec", "-i", prefix.container(), "sh", "-c", `umask 077 && test ! -L "$1" && mkdir -p -m 700 "$1" && rm -rf "$1/skills" "$1/.claude-plugin" && tar -xf - -C "$1"`, "sh", p.dir)
	} else {
		p.dir = stableDir
		if p.dir == "" {
			p.dir, err = os.MkdirTemp("", "blerg-skills-")
		} else {
			if groupRead {
				// This state root contains skills only, never credentials or markers.
				for _, dir := range []string{filepath.Dir(filepath.Dir(p.dir)), filepath.Dir(p.dir)} {
					if err = secureGroupSkillDir(dir); err != nil {
						// No directory authority was acquired. Cleanup through this
						// rejected parent could follow a link into unrelated state.
						failurelog.Default.Event("session_skill_pack", slog.LevelError, "cause", "unsafe_directory")
						return nil, fmt.Errorf("prepare session skill directory: %w", err)
					}
				}
			}
			if err == nil {
				err = os.MkdirAll(p.dir, 0700)
			}
			if err == nil {
				info, statErr := os.Lstat(p.dir)
				if statErr != nil || !info.IsDir() || !safeSkillPackMode(info, groupRead) || !ownedByDaemon(info) {
					failurelog.Default.Event("session_skill_pack", slog.LevelError, "cause", "unsafe_directory")
					return nil, fmt.Errorf("session skill directory is not private")
				}
				err = os.RemoveAll(filepath.Join(p.dir, "skills"))
			}
			if err == nil {
				err = os.RemoveAll(filepath.Join(p.dir, ".claude-plugin"))
			}
		}
		if err == nil {
			if err = os.MkdirAll(filepath.Join(p.dir, ".claude-plugin"), 0700); err == nil {
				err = os.WriteFile(filepath.Join(p.dir, ".claude-plugin/plugin.json"), []byte("{\"name\":\"blerg\",\"version\":\"1.0.0\"}"), 0600)
			}
			if err != nil {
				p.remove()
				failurelog.Default.Event("session_skill_pack", slog.LevelError, "cause", failurelog.Cause(err))
				return nil, err
			}
			for _, e := range p.entries {
				dir := filepath.Join(p.dir, "skills", e.Name)
				if err = os.MkdirAll(dir, 0700); err != nil {
					break
				}
				if err = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(e.Body), 0600); err != nil {
					break
				}
				if e.Examples == "" {
					continue
				}
				if err = os.WriteFile(filepath.Join(dir, "examples.md"), []byte(e.Examples), 0600); err != nil {
					break
				}
			}
		}
	}
	if err == nil && groupRead && !prefix.enabled() {
		err = shareSkillPack(p.dir)
	}
	if err != nil {
		p.remove()
		failurelog.Default.Event("session_skill_pack", slog.LevelError, "cause", failurelog.Cause(err))
		return nil, fmt.Errorf("prepare session skills: %w", err)
	}
	return p, nil
}
func (p *skillPack) remove() {
	if p == nil || p.dir == "" {
		return
	}
	if p.prefix.enabled() {
		if err := dockerRun("exec", p.prefix.container(), "rm", "-rf", p.dir); err != nil {
			failurelog.Default.Event("session_skill_pack_cleanup", slog.LevelError, "cause", failurelog.Cause(err))
		}
	} else {
		if err := os.RemoveAll(p.dir); err != nil {
			failurelog.Default.Event("session_skill_pack_cleanup", slog.LevelError, "cause", failurelog.Cause(err))
		}
	}
}
func (p *skillPack) guide() string {
	var out []string
	for _, e := range p.entries {
		out = append(out, "blerg:"+e.Name+": "+p.path(e.Name))
	}
	return " Load applicable skills before work; pass this catalog to subagents. Claude uses the namespaced Skill tool (blerg:<name>); Codex and Hermes must read the exact private file paths below, not skills/list, skill_view or repository skills. An explicit Codex /blerg:<name> input is already loaded and needs no second read. " + strings.Join(out, "; ") + "."
}
func (p *skillPack) path(name string) string { return p.dir + "/skills/" + name + "/SKILL.md" }

// examplesPath names a skill's linked worked-examples file, shipped beside
// SKILL.md in the same private session directory on every runtime.
func (p *skillPack) examplesPath(name string) string {
	return p.dir + "/skills/" + name + "/examples.md"
}

func (p *skillPack) read(ctx context.Context, name string) ([]byte, error) {
	return p.readPath(ctx, p.path(name))
}

func (p *skillPack) readExamples(ctx context.Context, name string) ([]byte, error) {
	return p.readPath(ctx, p.examplesPath(name))
}

func (p *skillPack) readPath(ctx context.Context, path string) ([]byte, error) {
	const limit = 2 << 20
	var raw []byte
	var err error
	if !p.prefix.enabled() {
		var file *os.File
		file, err = os.Open(path) // #nosec G304 -- the daemon's own pack directory joined with a catalog skill name, never caller input.
		if err == nil {
			defer func() { _ = file.Close() }()
			raw, err = io.ReadAll(io.LimitReader(file, limit+1))
		}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, err = p.prefix.command(ctx, "", "head", "-c", "2097153", "--", path).Output()
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("skill exceeds context limit")
	}
	return raw, err
}

// Group access is opt-in for the separate pod skill tree. It never permits writes.
func safeSkillPackMode(info fs.FileInfo, groupRead bool) bool {
	private := info.Mode().Perm()&0077 == 0
	if !groupRead {
		return private
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Gid) == os.Getgid() && (private || info.Mode().Perm() == 0750)
}

func secureGroupSkillDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByDaemon(info) || !safeSkillPackMode(info, true) {
		return fmt.Errorf("session skill directory is not private")
	}
	return os.Chmod(path, 0750) //nolint:gosec // G302: only the supervisor-owned skills directory grants agent-group read/traverse.
}

func shareSkillPack(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !ownedByDaemon(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsafe session skill entry")
		}
		mode := os.FileMode(0640)
		if info.IsDir() {
			mode = 0750
		}
		return root.Chmod(name, mode)
	})
}
