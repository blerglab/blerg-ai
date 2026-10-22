package boardfiling

import (
	"strings"
	"testing"
)

func TestBoardCapabilityIsOneCanonicalIntentAndDigest(t *testing.T) {
	intent := "12345678-1234-4321-8123-123456789abc"
	digest := BoardDigest(BoardDraft{Name: "Launch"})
	c, err := BoardCapability(intent, digest)
	if err != nil {
		t.Fatal(err)
	}
	if i, d, ok := ParseBoardCapability([]string{c}); !ok || i != intent || d != digest {
		t.Fatal("board capability failed roundtrip")
	}
	card, _ := Capability(intent, digest)
	for _, caps := range [][]string{nil, {c, c}, {c, "board.admin"}, {card}, {"board.create_once"}, {c + ":x"}} {
		if _, _, ok := ParseBoardCapability(caps); ok {
			t.Fatalf("accepted %v", caps)
		}
	}
	// A one-card capability is never a board capability, and the reverse.
	if _, _, ok := ParseCapability([]string{c}); ok {
		t.Fatal("a board capability passed as a card capability")
	}
}

func TestBoardDigestBindsEveryField(t *testing.T) {
	base := BoardDraft{Name: "Launch", Description: "d", Repos: []string{"team/app"}, Columns: []string{"todo"}}
	seen := map[string]bool{BoardDigest(base): true}
	for _, d := range []BoardDraft{
		{Name: "Launch2", Description: "d", Repos: []string{"team/app"}, Columns: []string{"todo"}},
		{Name: "Launch", Description: "e", Repos: []string{"team/app"}, Columns: []string{"todo"}},
		{Name: "Launch", Description: "d", Repos: []string{"team/web"}, Columns: []string{"todo"}},
		{Name: "Launch", Description: "d", Repos: []string{"team/app"}, Columns: []string{"done"}},
		{Name: "Launch", Description: "d", Repos: []string{"team/app"}, Template: "focus-board"},
	} {
		if seen[BoardDigest(d)] {
			t.Fatalf("digest ignores a field: %+v", d)
		}
		seen[BoardDigest(d)] = true
	}
}

func TestValidateBoardDraft(t *testing.T) {
	ok := []BoardDraft{
		{Name: "Launch"},
		{Name: "Launch", Repos: []string{"app", "team/web", "a.b_c-d"}, Template: "focus-board"},
		{Name: "Launch", Description: "line one\nline two", Columns: []string{"Inbox", "Doing", "Done"}},
	}
	for _, d := range ok {
		if err := ValidateBoardDraft(d); err != nil {
			t.Fatalf("%+v: %v", d, err)
		}
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = "r" + strings.Repeat("x", i)
	}
	bad := []BoardDraft{
		{}, {Name: " x"}, {Name: "a\nb"}, {Name: strings.Repeat("x", 101)},
		{Name: "x", Description: "a\x00b"}, {Name: "x", Description: strings.Repeat("y", 2001)},
		{Name: "x", Repos: []string{"../etc"}}, {Name: "x", Repos: []string{"a/b/c"}}, {Name: "x", Repos: []string{"-flag"}},
		{Name: "x", Repos: []string{"https://host/a"}}, {Name: "x", Repos: []string{"a", "A"}}, {Name: "x", Repos: []string{""}},
		{Name: "x", Repos: many}, {Name: "x", Template: "Focus Board"}, {Name: "x", Template: "focus-board", Columns: []string{"a"}},
		{Name: "x", Columns: []string{"a", "a"}}, {Name: "x", Columns: []string{""}}, {Name: "x", Columns: many},
		{Name: "x", Repos: []string{"team/app.git"}},
		// Bidi and invisible format characters (Cf) could make a name, column or description read
		// differently from what is stored.
		{Name: "Release\u202eeganam"}, {Name: "Done\u200b"}, {Name: "\ufeffLaunch"}, {Name: "a\u2066b\u2069"},
		{Name: "x", Columns: []string{"Done\u200d"}}, {Name: "x", Description: "ok\u202etext"},
	}
	for _, d := range bad {
		if ValidateBoardDraft(d) == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
}
