package agentsmanifest

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderComponentMarkdown(t *testing.T) {
	got := RenderComponentMarkdown(fullEntry())

	if !strings.HasPrefix(got, "# blerg-runner\n") {
		t.Errorf("want title heading first, got:\n%s", got)
	}
	for _, want := range []string{
		"Runs Claude Code sessions against a repository.",
		"## Authenticate",
		"blerg-runner",
		"agent_token",
		"human_session",
		"runner_key",
		"run-sessions",
		"https://core.example.test/api/tokens",
		"## Operations",
		"| Method | Path | Summary | Cap | Idempotent |",
		"| POST | `/api/runner/start` | Start a session. | `session.start` | yes |",
		"| GET | `/api/runner/sessions/{id}` | Read session status. | — | no |",
		"## Machine-readable",
		"https://runner.example.test/openapi.json",
		"https://runner.example.test/mcp",
		"## UI",
		"`@blerglab/chat@0.1.0`",
		"https://runner.example.test/packages/blerglab-chat-0.1.0.tgz",
		"https://runner.example.test/packages/@blerglab/chat/README.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown is missing %q, got:\n%s", want, got)
		}
	}
}

func TestRenderComponentMarkdownWithoutOptionalSections(t *testing.T) {
	got := RenderComponentMarkdown(ComponentEntry{Name: "blerg-board", BaseURL: "https://board.example.test"})
	if !strings.HasPrefix(got, "# blerg-board\n") {
		t.Errorf("want title heading first, got:\n%s", got)
	}
	for _, unwanted := range []string{"## Authenticate", "## Operations", "## Machine-readable", "## UI"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("section %q should be omitted when empty, got:\n%s", unwanted, got)
		}
	}
}

func TestRenderAggregateMarkdown(t *testing.T) {
	quickstart := "1. Read this document (`GET /agents`).\n2. Get a token."
	got := RenderAggregateMarkdown(AggregateManifest{
		ContractVersion: ContractVersion,
		CoreBaseURL:     "https://core.example.test",
		Quickstart:      quickstart,
		Components:      []ComponentEntry{fullEntry()},
	})

	if !strings.HasPrefix(got, "# Blerg — agent guide\n") {
		t.Errorf("want aggregate title first, got:\n%s", got)
	}
	qs := strings.Index(got, quickstart)
	if qs < 0 {
		t.Fatalf("quickstart not rendered verbatim, got:\n%s", got)
	}
	if !strings.Contains(got, "## Quickstart") {
		t.Errorf("missing Quickstart heading, got:\n%s", got)
	}
	comp := strings.Index(got, "## blerg-runner")
	if comp < 0 {
		t.Fatalf("component section not demoted to '## blerg-runner', got:\n%s", got)
	}
	if qs > comp {
		t.Errorf("quickstart must come before any component section, got:\n%s", got)
	}
	for _, want := range []string{"### Authenticate", "### Operations", "### Machine-readable"} {
		if !strings.Contains(got, want) {
			t.Errorf("component subsections must be demoted: missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n# blerg-runner") {
		t.Errorf("component heading must be demoted, got:\n%s", got)
	}
	if !strings.Contains(got, "https://core.example.test") {
		t.Errorf("missing core base URL, got:\n%s", got)
	}
}

func TestRenderAggregateMarkdownNoComponents(t *testing.T) {
	got := RenderAggregateMarkdown(AggregateManifest{ContractVersion: ContractVersion, Quickstart: "go"})
	if !strings.Contains(got, "# Blerg — agent guide") {
		t.Errorf("missing title, got:\n%s", got)
	}
}

func TestWantsMarkdown(t *testing.T) {
	cases := []struct {
		name   string
		target string
		accept string
		want   bool
	}{
		{"accept markdown", "/agents", "text/markdown", true},
		{"accept markdown with params", "/agents", "text/markdown;q=0.9, application/json;q=0.1", true},
		{"format query", "/agents?format=md", "", true},
		{"format query alongside json accept", "/agents?format=md", "application/json", true},
		{"json default", "/agents", "application/json", false},
		{"no accept header", "/agents", "", false},
		{"other format value", "/agents?format=json", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			if got := WantsMarkdown(r); got != tc.want {
				t.Errorf("WantsMarkdown() = %v, want %v", got, tc.want)
			}
		})
	}
}
