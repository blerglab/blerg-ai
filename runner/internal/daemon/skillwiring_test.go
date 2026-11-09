package daemon

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	agentSkills "github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/skills"
)

func TestClaudeStreamingSkillGuideAndPlugin(t *testing.T) {
	pack, err := prepareSkillPack(sandboxExec{}, skills.Resolve(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pack.remove)
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.workspaceGuide = pack.guide()
	d.pluginDirs = []string{pack.dir}
	var args string
	refused := errors.New("capture only")
	d.startCommand = func(cmd *exec.Cmd) error {
		args = strings.Join(cmd.Args, "\n")
		for _, arg := range cmd.Args {
			if path, ok := strings.CutPrefix(arg, "--append-system-prompt-file="); ok {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				args += "\n" + string(raw)
			}
		}
		return refused
	}
	if _, err = d.startProc(context.Background()); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	for _, want := range []string{pack.path("coordinating"), "pass this catalog to subagents", "blerg:coordinating", "--plugin-dir", pack.dir} {
		if !strings.Contains(args, want) {
			t.Fatalf("stream launch omits %q", want)
		}
	}
}

func TestClaudePluginReloadKeepsWorkflowPack(t *testing.T) {
	w, _ := newTestWorkshop(t)
	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.workflowPluginDir = "private-workflow-pack"
	apply, _ := d.prepareSessionPlugins(context.Background(), w, "session", nil, false)
	apply()
	if len(d.pluginDirs) != 1 || d.pluginDirs[0] != d.workflowPluginDir {
		t.Fatal("plugin reload lost workflow pack")
	}
}

func TestSkillSandboxArchive(t *testing.T) {
	oldRun, oldStdin := dockerRun, dockerRunStdin
	defer func() { dockerRun, dockerRunStdin = oldRun, oldStdin }()
	dockerRun = func(...string) error { return nil }
	var archive []byte
	dockerRunStdin = func(raw []byte, args ...string) error {
		archive = append([]byte(nil), raw...)
		if !strings.Contains(strings.Join(args, " "), "tar -xf") {
			t.Fatal("pack not extracted")
		}
		return nil
	}
	p, err := prepareSkillPack(sandboxExecPrefix("fixture"), skills.Resolve(true))
	if err != nil {
		t.Fatal(err)
	}
	defer p.remove()
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = string(raw)
	}
	if !strings.Contains(files[".claude-plugin/plugin.json"], `"name":"blerg"`) {
		t.Fatal("plugin manifest missing")
	}
	foundExamples := false
	for _, e := range p.entries {
		if files["skills/"+e.Name+"/SKILL.md"] != e.Body {
			t.Fatalf("archive missing %s", e.Name)
		}
		if e.Examples == "" {
			continue
		}
		foundExamples = true
		if files["skills/"+e.Name+"/examples.md"] != e.Examples {
			t.Fatalf("sandbox archive missing or wrong examples.md for %s", e.Name)
		}
	}
	if !foundExamples {
		t.Fatal("no skill shipped a linked examples file to verify")
	}
}

func TestOpenclawGuideConsumesReplayBudget(t *testing.T) {
	d := &openclawDriver{skillGuide: strings.Repeat("g", 80), contextChars: 100, history: []openclawTurn{{role: "assistant", text: "old reply must not fit"}}}
	got := d.buildPrompt("new")
	if len(got) > 100 || strings.Contains(got, "old reply") {
		t.Fatal("guide escaped context budget")
	}
}

func TestAgentHostClaudeInstallsPrivatePluginAndGuide(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(root, "proj", ".claude", "skills", "coordinating")
	if err := os.MkdirAll(shadow, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shadow, "SKILL.md"), []byte("---\nname: blerg:coordinating\ndescription: repository shadow\n---\nRepository replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), ClaudeCode: true})
	policy := skills.Resolve(true)
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "skill-wiring", Repo: "proj", Kind: "agent", SkillPolicy: &policy})
	defer host.Kill("skill-wiring")
	host.mu.Lock()
	sess := host.sessions["skill-wiring"]
	host.mu.Unlock()
	if sess == nil {
		t.Fatal("session missing")
	}
	d, ok := sess.loop.(*claudeCodeDriver)
	if !ok {
		t.Fatal("Claude driver missing")
	}
	if len(d.pluginDirs) != 1 || d.pluginDirs[0] != d.workflowPluginDir || d.workflowPluginDir == "" {
		t.Fatal("private plugin not attached by spawn")
	}
	if !strings.Contains(d.workspaceGuide, d.workflowPluginDir+"/skills/coordinating/SKILL.md") || !strings.Contains(d.workspaceGuide, "pass this catalog to subagents") {
		t.Fatal("spawn lost pack guide")
	}
	body, _, err := agentSkills.Load(sess.skills, "blerg:coordinating")
	if err != nil || body != skills.Coordinating {
		t.Fatal("repository replaced reserved Blerg skill in shared catalog")
	}
}
