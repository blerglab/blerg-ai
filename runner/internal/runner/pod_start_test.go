package runner

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type capturePodSpawn struct{ spawn protocol.SpawnSession }

func (c *capturePodSpawn) Spawn(s protocol.SpawnSession) { c.spawn = s }
func (c *capturePodSpawn) SpawnResumed(s protocol.SpawnSession, _ []agent.RestoredEvent, _ string) {
	c.spawn = s
}

func TestStartPodSessionKeepsPersonPrompt(t *testing.T) {
	plan := &clusterworkspace.Plan{Manifest: protocol.WorkspaceManifest{Runtime: "cluster", Members: []protocol.WorkspaceMember{{ID: "one", Path: "app"}}}}
	for _, prompt := range []string{"", "fix the build"} {
		for _, resume := range []bool{false, true} {
			capture := &capturePodSpawn{}
			cfg := Config{Workspace: plan, InitialPrompt: prompt, Resume: resume}
			startPodSession(t.Context(), cfg, "/workspace/projects/app", "prepared-members", capture, func(context.Context, Config) ([]agent.RestoredEvent, error) { return nil, nil })
			if capture.spawn.InitialPrompt != prompt {
				t.Fatalf("spawn prompt = %q, want %q", capture.spawn.InitialPrompt, prompt)
			}
		}
	}
}

// Run passes the person's prompt into the tested constructor unchanged. Guard
// that boundary as well as the constructor so a prepend in Run cannot bypass it.
func TestRunDoesNotRewriteInitialPrompt(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "runner.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Run" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if field, ok := lhs.(*ast.SelectorExpr); ok && field.Sel.Name == "InitialPrompt" {
					if x, ok := field.X.(*ast.Ident); ok && x.Name == "cfg" {
						t.Error("Run must not rewrite the person's prompt")
					}
				}
			}
			return true
		})
		return
	}
	t.Fatal("Run not found")
}

func TestClusterGuidePreservesWorkspaceRules(t *testing.T) {
	guide := clusterWorkspaceGuide(clusterworkspace.Plan{})
	for _, want := range []string{"grants no shell permission", "report every commit ID", "not launch members", "shell grants are never inherited"} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide missing %q", want)
		}
	}
}
