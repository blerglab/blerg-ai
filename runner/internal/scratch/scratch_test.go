package scratch

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{".scratch-granite-3f9a", true},
		{".scratch-A1_b-2", true},
		{".scratch-x", true},
		{".scratch-" + strings.Repeat("a", 64), true},
		{".scratch-" + strings.Repeat("a", 65), false},
		{".scratch-", false},
		{"scratch-granite", false},
		{".scratch--x", false},
		{".scratch-_x", false},
		{".scratch-a/b", false},
		{".scratch-../x", false},
		{".scratch-a..b", false},
		{".scratch-a b", false},
		{".scratch-é", false},
		{"", false},
	} {
		if got := Valid(tc.name); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewNameIsValidAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		n := NewName()
		if !Valid(n) {
			t.Fatalf("NewName() = %q, not Valid", n)
		}
		if seen[n] {
			t.Fatalf("NewName() repeated %q", n)
		}
		seen[n] = true
	}
}

// NewName carries 64 random bits (16 hex digits).
func TestNewNameHas64Bits(t *testing.T) {
	suffix := strings.TrimPrefix(NewName(), Prefix)
	if len(suffix) != 16 || strings.Trim(suffix, "0123456789abcdef") != "" {
		t.Errorf("NewName suffix = %q, want 16 hex digits", suffix)
	}
}

// Retry keeps the taken name recognisable, adds 64 fresh bits, and always
// yields a valid, different name — even from a maximum-length one.
func TestRetry(t *testing.T) {
	for _, name := range []string{".scratch-granite-8000", ".scratch-x", ".scratch-" + strings.Repeat("a", 64), ".scratch-" + strings.Repeat("b", 47) + "-_c"} {
		got := Retry(name)
		if !Valid(got) || got == name {
			t.Errorf("Retry(%q) = %q, want a different valid name", name, got)
		}
		if again := Retry(name); again == got {
			t.Errorf("Retry(%q) repeated %q", name, got)
		}
	}
	if got := Retry(".scratch-granite-8000"); !strings.HasPrefix(got, ".scratch-granite-8000-") {
		t.Errorf("Retry lost the taken name: %q", got)
	}
}

func TestIsNoRepo(t *testing.T) {
	for repo, want := range map[string]bool{
		"":                  true,
		".scratch-3f9a01c2": true,
		"myrepo":            false,
		"org/name":          false,
	} {
		if got := IsNoRepo(repo); got != want {
			t.Errorf("IsNoRepo(%q) = %v, want %v", repo, got, want)
		}
	}
}
