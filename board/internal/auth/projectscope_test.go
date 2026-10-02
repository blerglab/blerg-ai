package auth

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/coreauth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// A core-issued AGENT token carrying a Project claim is scoped to exactly
// that board (Project = board id); everything else about it is unchanged.
func TestCorePrincipal_ProjectScopesAgentToOneBoard(t *testing.T) {
	p := corePrincipal(coreauth.Synthesized{
		Sub: "tok-1", Kind: "agent", Project: "board-A",
		Caps: []string{"card.read", "card.write", "column.write"},
	})
	if id, ok := p.ProjectScoped(); !ok || id != "board-A" {
		t.Fatalf("ProjectScoped() = %q, %v; want board-A, true", id, ok)
	}
	if err := p.RequireBoard("board-A", "card.write"); err != nil {
		t.Fatalf("in-scope RequireBoard = %v, want nil", err)
	}
	if err := p.RequireBoard("board-B", "card.read"); err == nil {
		t.Fatal("out-of-scope RequireBoard = nil, want error")
	}
	if err := p.RequireBoard("", "card.read"); err == nil {
		t.Fatal("instance-wide RequireBoard(\"\") = nil, want error")
	}
	if err := p.RequireGlobalRead(); err == nil {
		t.Fatal("RequireGlobalRead = nil, want error for a scoped token")
	}
	if err := p.RequireAdmin(""); err == nil {
		t.Fatal("RequireAdmin(\"\") = nil, want error for a scoped token")
	}
}

// Tokens WITHOUT a Project claim are not narrowed (human and Project-less
// agent tokens behave exactly as before).
func TestCorePrincipal_NoProjectNotScoped(t *testing.T) {
	cases := []coreauth.Synthesized{
		{Sub: "a", Kind: "agent", Caps: []string{"card.read"}},
		{Sub: "h", Kind: "human", Caps: []string{"card.read"}},
		{Sub: "s", Kind: "service", Caps: []string{"card.read"}},
	}
	for _, s := range cases {
		p := corePrincipal(s)
		if id, ok := p.ProjectScoped(); ok {
			t.Errorf("kind=%s: ProjectScoped = %q, true; want unscoped", s.Kind, id)
		}
		if p.Token.BoardID != nil {
			t.Errorf("kind=%s: Token.BoardID set, want nil", s.Kind)
		}
		if err := p.RequireBoard("any-board", "card.read"); err != nil {
			t.Errorf("kind=%s: RequireBoard = %v, want nil (unscoped)", s.Kind, err)
		}
		if err := p.RequireGlobalRead(); err != nil {
			t.Errorf("kind=%s: RequireGlobalRead = %v, want nil", s.Kind, err)
		}
	}
}

// A non-empty Project claim scopes ANY core-issued token kind, not only
// "agent": a human or service token minted with a Project is confined too.
func TestCorePrincipal_ProjectScopesEveryKind(t *testing.T) {
	for _, kind := range []string{"agent", "human", "service", "cron", "member", "weird"} {
		p := corePrincipal(coreauth.Synthesized{Sub: "x", Kind: kind, Project: "board-A",
			Caps: []string{"card.read", "card.write"}})
		if id, ok := p.ProjectScoped(); !ok || id != "board-A" {
			t.Errorf("kind=%s: ProjectScoped = %q, %v; want board-A, true", kind, id, ok)
		}
		if err := p.RequireBoard("board-B", "card.read"); err == nil {
			t.Errorf("kind=%s: out-of-scope RequireBoard = nil", kind)
		}
		if err := p.RequireGlobalRead(); err == nil {
			t.Errorf("kind=%s: RequireGlobalRead = nil, want error", kind)
		}
	}
}

// A malformed Project fails closed: scoped to NOTHING, never unscoped.
func TestCorePrincipal_MalformedProjectFailsClosed(t *testing.T) {
	bad := map[string]string{
		"newline":  "board\nA",
		"nul":      "board\x00A",
		"tab":      "a\tb",
		"del":      "a\x7fb",
		"too long": strings.Repeat("a", 65),
	}
	for name, project := range bad {
		for _, kind := range []string{"agent", "human"} {
			p := corePrincipal(coreauth.Synthesized{Sub: "x", Kind: kind, Project: project, Caps: []string{"card.read"}})
			if _, ok := p.ProjectScoped(); !ok {
				t.Errorf("%s/%s: ProjectScoped = false, want scoped-to-nothing", name, kind)
			}
			for _, b := range []string{project, "board-A", "", "00000000-0000-4000-8000-000000000001"} {
				if err := p.RequireBoard(b, "card.read"); err == nil {
					t.Errorf("%s/%s: RequireBoard(%q) = nil, want error", name, kind, b)
				}
			}
			if err := p.RequireGlobalRead(); err == nil {
				t.Errorf("%s/%s: RequireGlobalRead = nil, want error", name, kind)
			}
		}
	}
	// exactly 64 characters is fine.
	ok64 := strings.Repeat("a", 64)
	p := corePrincipal(coreauth.Synthesized{Sub: "x", Kind: "agent", Project: ok64, Caps: []string{"card.read"}})
	if err := p.RequireBoard(ok64, "card.read"); err != nil {
		t.Errorf("64-char project: RequireBoard = %v, want nil", err)
	}
}

// A native board-scoped agent token is not a "project scoped" principal (the
// REST/MCP pre-check only applies to core-issued ones, leaving native
// behaviour exactly as before).
func TestProjectScopedExcludesNativeAgentToken(t *testing.T) {
	b := "board-A"
	p := Principal{Kind: KindAgent, Token: &db.Token{BoardID: &b}}
	if _, ok := p.ProjectScoped(); ok {
		t.Fatal("native agent token reported ProjectScoped")
	}
}
