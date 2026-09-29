package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFrontmatterBoundedAndLenient(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Quoted values; nested keys don't override top-level ones.
	name, desc, err := ReadFrontmatter(write("a.md", "---\nname: \"quoted\"\ndescription: 'single'\nmetadata:\n  name: nested\n---\nbody"))
	if err != nil || name != "quoted" || desc != "single" {
		t.Errorf("quoted = %q %q %v", name, desc, err)
	}
	// Folded block scalar.
	_, desc, _ = ReadFrontmatter(write("b.md", "---\nname: b\ndescription: >-\n  line one\n  line two\nother: x\n---\n"))
	if desc != "line one line two" {
		t.Errorf("folded = %q", desc)
	}
	// Frontmatter past the read limit is not found.
	if _, _, err := ReadFrontmatter(write("c.md", strings.Repeat("x", FrontmatterReadLimit)+"\n---\nname: c\n---\n")); err == nil {
		t.Error("frontmatter beyond the read limit must not be read")
	}
	// Not a regular file.
	if _, _, err := ReadFrontmatter(dir); err == nil {
		t.Error("a directory must be refused")
	}
}
