package protocol

import (
	"strings"
	"testing"
)

func TestDeliveredSummaryBounds(t *testing.T) {
	valid := func() *DeliveredSummary {
		return &DeliveredSummary{Commits: []BranchCommit{{SHA: strings.Repeat("a", 40), Message: "Commit", Author: "Fixture", Date: "2026-11-01T00:00:00Z"}}, Files: []CommitFile{{Path: "app.go", Adds: 1}}}
	}
	if !valid().Valid() {
		t.Fatal("valid metadata refused")
	}
	for _, tc := range []struct {
		name   string
		change func(*DeliveredSummary)
	}{
		{"too many commits", func(s *DeliveredSummary) {
			for len(s.Commits) <= 100 {
				s.Commits = append(s.Commits, s.Commits[0])
			}
		}},
		{"too many files", func(s *DeliveredSummary) {
			for len(s.Files) <= 300 {
				s.Files = append(s.Files, s.Files[0])
			}
		}},
		{"path traversal", func(s *DeliveredSummary) { s.Files[0].Path = "../private" }},
		{"absolute path", func(s *DeliveredSummary) { s.Files[0].Path = "/private" }},
		{"newline", func(s *DeliveredSummary) { s.Commits[0].Message = "one\ntwo" }},
		{"bad date", func(s *DeliveredSummary) { s.Commits[0].Date = "bad" }},
		{"bad sha", func(s *DeliveredSummary) { s.Commits[0].SHA = "bad" }},
		{"negative stat", func(s *DeliveredSummary) { s.Files[0].Adds = -1 }},
		{"binary stat", func(s *DeliveredSummary) { s.Files[0].Binary = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(s)
			if s.Valid() {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestDeliveredSummaryUnsafeUnicode(t *testing.T) {
	for _, bad := range []string{"\u202e", "\u0085", "\u2028", "\u2029", string([]byte{0xff})} {
		s := &DeliveredSummary{Commits: []BranchCommit{{SHA: strings.Repeat("a", 40), Message: bad, Author: "Example", Date: "2026-11-01T00:00:00Z"}}, Files: []CommitFile{}}
		if s.Valid() {
			t.Errorf("accepted unsafe text %q", bad)
		}
	}
}
