// Package prompts holds the session briefs handed to spawned agents — the
// worker, reviewer, specreview, discuss, board, and bootstrap roles — as
// text/template files. Defaults are embedded into the binary; when BLERG_BOARD_PROMPTS_DIR is
// set, a same-named file there overrides the embedded default for that role.
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed *.md
var defaultFS embed.FS

// CardInfo is the card fields available to templates as {{.Card...}}.
type CardInfo struct {
	Number int
	Title  string
	Body   string
}

// BoardInfo is the board fields available to templates as {{.Board...}}.
type BoardInfo struct {
	Name string
	// Snapshot is a pre-rendered block of board state (id, repos, columns,
	// a one-line index of every card) taken when the session was spawned —
	// board.md emits it verbatim so a board chat can answer "what's on the
	// board" without API reads. Empty for roles that don't carry one, and
	// for a board chat whose state could not be read; templates must guard
	// with {{if}}.
	Snapshot string
}

// Data is the field set passed to every template. Not every role uses every
// field — e.g. only reviewer.md references {{.PR}}.
type Data struct {
	Card  CardInfo
	Board BoardInfo
	PR    string // reviewer: link to the pull request under review
	Extra string // worker/discuss/board/bootstrap: the human's extra instruction or opening message

	// InfraDocsURL/InfraDocsNote render the optional infra-reference
	// paragraph appended to every session prompt: InfraDocsNote is a
	// one-line description of the deployment environment, InfraDocsURL is
	// where the session should curl to read more. Both must be set for the
	// paragraph to appear — either empty omits it entirely, so vanilla
	// (self-hosted) deployments get clean prompts with no environment
	// assumptions baked in.
	InfraDocsURL  string
	InfraDocsNote string

	// GitCredentialEnv names the environment variable, already present in
	// spawned sessions, that holds a git/GitHub credential — used by
	// bootstrap.md to instruct how to create and push the initial repo.
	// Empty has the session ask the human for a credential instead.
	GitCredentialEnv string
}

// Store is a validated, ready-to-render set of role templates.
type Store struct {
	templates map[string]*template.Template
}

// Load parses the embedded default template for every role, then — if
// overrideDir is non-empty — overlays any same-named .md file found there.
// Every template (default or override) is both parsed and dry-run executed
// against a zero-value Data so a typo'd field reference fails Load instead of
// surfacing mid-session; callers should treat a non-nil error as fatal.
func Load(overrideDir string) (*Store, error) {
	entries, err := fs.ReadDir(defaultFS, ".")
	if err != nil {
		return nil, fmt.Errorf("prompts: read embedded defaults: %w", err)
	}
	s := &Store{templates: map[string]*template.Template{}}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		role := strings.TrimSuffix(e.Name(), ".md")
		text, err := defaultFS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("prompts: read embedded %s: %w", e.Name(), err)
		}
		src := string(text)
		if overrideDir != "" {
			path := filepath.Join(overrideDir, e.Name())
			switch b, err := os.ReadFile(path); { //nolint:gosec // G304: overrideDir is operator configuration and the file name comes from the embedded prompt set
			case err == nil:
				src = string(b)
			case !os.IsNotExist(err):
				return nil, fmt.Errorf("prompts: read override %s: %w", path, err)
			}
		}
		tmpl, err := template.New(role).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("prompts: parse %s: %w", e.Name(), err)
		}
		if err := tmpl.Execute(discardWriter{}, Data{}); err != nil {
			return nil, fmt.Errorf("prompts: %s references an unknown field: %w", e.Name(), err)
		}
		s.templates[role] = tmpl
	}
	return s, nil
}

// discardWriter is an io.Writer that throws away everything written to it —
// used for the startup dry-run render.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Render executes the named role's template against data.
func (s *Store) Render(role string, data Data) (string, error) {
	tmpl, ok := s.templates[role]
	if !ok {
		return "", fmt.Errorf("prompts: unknown role %q", role)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("prompts: render %q: %w", role, err)
	}
	return buf.String(), nil
}
