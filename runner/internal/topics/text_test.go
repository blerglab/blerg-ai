package topics

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCleanText(t *testing.T) {
	long := strings.Repeat("é", 100)
	for _, c := range []struct{ in, want string }{
		{"  Build   the\tHistory\n\ntab ", "Build the History tab"},
		{"\u202eevil\u200b\x07 title", "evil title"},
		{"a\u2066b\u2069c\ufeffd\u200fe", "abcde"},
		{"\x00\x1b[31m red", "[31m red"},
		{"\u200b\u202e\x07 \t", ""},
		{"caf\u0085e", "caf e"}, // NEL (C1) is whitespace
	} {
		if got := CleanText(c.in, MaxTitle); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	got := CleanText(long, MaxTitle)
	if utf8.RuneCountInString(got) != MaxTitle || !strings.HasSuffix(got, "…") {
		t.Errorf("a 100-rune title = %q (%d runes), want 80 ending in …", got, utf8.RuneCountInString(got))
	}
	if got := CleanText(strings.Repeat("x", 80), MaxTitle); got != strings.Repeat("x", 80) {
		t.Errorf("an 80-rune title is kept whole, got %q", got)
	}
}

func TestHarnessMarkersFixture(t *testing.T) {
	if len(harness.Prefixes) != 4 || harness.RestartNote == "" {
		t.Fatalf("markers = %+v", harness)
	}
}

func TestIsPersonMessage(t *testing.T) {
	msg := func(source, text string) agent.UserMessagePayload {
		return agent.UserMessagePayload{Text: text, Source: source}
	}
	for _, c := range []struct {
		name  string
		p     agent.UserMessagePayload
		first bool
		want  bool
	}{
		{"chat", msg("chat", "Build the History tab"), false, true},
		{"human (a proxy)", msg("human", "Fix the drawer"), false, true},
		{"harness text recorded as chat", msg("chat", "  <task-notification><task-id>x</task-id>"), false, false},
		{"system reminder recorded as chat", msg("chat", "<system-reminder>be brief"), false, false},
		{"system", msg("system", "Continue the task you were working on."), false, false},
		{"old restart note", msg("chat", "[system] Session resumed after a restart. Check your work."), false, false},
		{"ask answer", msg("ask_answer", "yes"), false, false},
		{"note reply", msg("note_reply", "ok"), false, false},
		{"model change", msg("chat", "/model claude-sonnet-5"), false, false},
		{"effort change", msg("chat", " /effort high"), false, false},
		{"slash skill keeps its title", msg("chat", "/review plan.md"), false, true},
		{"review with a version", msg("chat", "Review of report.md (v3): 2 requests, 1 edit."), false, false},
		{"review without a version", msg("chat", "Review of notes.md: 1 request."), false, false},
		{"mark-up with pins", msg("chat", "Marked up s.png: 1 pin.\n\n1. (top) here"), false, false},
		{"mark-up without pins", msg("chat", "Marked up s.png (see the attached image)."), false, false},
		{"runner, first message", msg("runner", "Ship the release notes"), true, true},
		{"runner, later", msg("runner", "Ship the release notes"), false, false},
		{"board, first message (a card's task)", msg("blerg-board", "Card: tidy the docs"), true, true},
		{"board, later", msg("blerg-board", "moved to Done"), false, false},
		{"parent session, first message (a child's task)", msg("session", "Port the tests"), true, true},
		{"parent session, later", msg("session", "also the docs"), false, false},
		{"cron, first message (its prompt)", msg("cron", "Summarize my inbox"), true, true},
		{"cron, later", msg("cron", "again"), false, false},
		{"unknown source", msg("webhook", "hello"), true, false},
	} {
		if got := IsPersonMessage(c.p, c.first); got != c.want {
			t.Errorf("%s: IsPersonMessage = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsFeedback(t *testing.T) {
	for _, s := range []string{
		"Review of report.md (v3): 2 requests, 1 edit.",
		"Review of notes.md: 1 request.",
		"Review of r.md (v2): 0 requests, 1 edit.\n\nEdited copy attached as r.md",
		"Marked up screenshot-3.png: 3 pins.",
		"Marked up s.png (see the attached image).",
	} {
		if !IsFeedback(s) {
			t.Errorf("IsFeedback(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"Review of the plan: looks good", "Marked up the doc, please check", "Please review notes.md",
		// A person's own prose that starts like a review: the chat's first line ends at the count.
		"Review of the API: 3 requests came in today, can you look?",
		"Review of the API: 3 requests.\nThey came in today.",
		"Marked up the mockup: 2 pins. Please fix both.",
		"Marked up s.png (see the attached image). Also the header."} {
		if IsFeedback(s) {
			t.Errorf("IsFeedback(%q) = true, want false", s)
		}
	}
}

func TestRequestText(t *testing.T) {
	msg := "\n\n  Fix   the drawer on phones \nand the badge\n\nAttached files (fetch them with: blerg-runner fetch --all): a.png (2 KB)"
	if got := RequestText(msg); got != "Fix the drawer on phones and the badge" {
		t.Errorf("RequestText = %q", got)
	}
	if got := RequestText(strings.Repeat("word ", 100)); len([]rune(got)) != maxRequest {
		t.Errorf("a long message is cut to %d runes, got %d", maxRequest, len([]rune(got)))
	}
}

func TestOpenTopic(t *testing.T) {
	raw := func(p agent.TopicPayload) []byte { b, _ := json.Marshal(p); return b }
	if p, ok := OpenTopic(raw(agent.TopicPayload{ID: "tp_1", Title: "A", Action: "start"})); !ok || p.ID != "tp_1" || p.Title != "A" {
		t.Errorf("a start leaves its topic open: %+v %v", p, ok)
	}
	if _, ok := OpenTopic(raw(agent.TopicPayload{ID: "tp_1", Title: "A", Action: "done"})); ok {
		t.Error("a done leaves nothing open")
	}
	if _, ok := OpenTopic(nil); ok {
		t.Error("no topic event leaves nothing open")
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if !strings.HasPrefix(a, "tp_") || len(a) != 11 || a == b {
		t.Errorf("NewID = %q, %q", a, b)
	}
}

func TestCleanCommits(t *testing.T) {
	c := func(shas ...string) []agent.TopicCommit {
		out := []agent.TopicCommit{}
		for _, s := range shas {
			out = append(out, agent.TopicCommit{Sha: s})
		}
		return out
	}
	sha40 := "0123456789abcdef0123456789abcdef01234567"
	got, err := CleanCommits(c("9F8E7D6", "1a2b3c4", sha40, "9f8e7d6", "1A2B3C4"))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "lower-cased, deduplicated, in order", got, c("9f8e7d6", "1a2b3c4", sha40))
	if got, err := CleanCommits(nil); err != nil || got != nil {
		t.Errorf("no commits = %v %v, want nil", got, err)
	}
	for _, bad := range []string{"", "1a2b3c", "g123456", sha40 + "8", "1a2b 3c4", " 1a2b3c4", "1a2b3c4\n"} {
		if _, err := CleanCommits(c(bad)); err == nil || !strings.Contains(err.Error(), "7 to 40 hex") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	var many []string
	for i := range MaxCommits + 1 {
		many = append(many, fmt.Sprintf("%07x", i))
	}
	if _, err := CleanCommits(c(many...)); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Errorf("51 commits: err = %v", err)
	}
	if got, err := CleanCommits(c(append(many[:MaxCommits], many[0])...)); err != nil || len(got) != MaxCommits {
		t.Errorf("50 after the duplicate goes: %d %v", len(got), err)
	}
}

// Every Unicode format character (Cf) goes, not only the bidi and zero-width ones.
func TestCleanTextDropsEveryFormatCharacter(t *testing.T) {
	in := "a\u061cb\u00adc\u180ed\u2060\u2061\u2064e\ufff9\ufffbf\U000E0001\U000E0041\U000E007Fg\u200dh"
	if got := CleanText(in, 100); got != "abcdefgh" {
		t.Fatalf("CleanText = %q", got)
	}
}
