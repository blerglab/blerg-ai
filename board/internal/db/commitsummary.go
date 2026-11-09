package db

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CommitSummary is explicitly published card content, readable by card readers.
// Only metadata is accepted: no patch, blob, local checkout path or provider URL.
type CommitSummary struct {
	Repository       string          `json:"repository"`
	SessionID        string          `json:"session_id"`
	MemberID         string          `json:"member_id,omitempty"`
	Generation       int64           `json:"generation,omitempty"`
	LaunchGeneration string          `json:"launch_generation,omitempty"`
	Branch           string          `json:"branch"`
	Base             string          `json:"base"`
	Commits          []SummaryCommit `json:"commits"`
	Files            []SummaryFile   `json:"files"`
	Truncated        bool            `json:"truncated,omitempty"`
}
type SummaryCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}
type SummaryFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Adds    int    `json:"adds"`
	Dels    int    `json:"dels"`
	Binary  bool   `json:"binary,omitempty"`
}

// ValidateCommitSummary runs before admission persists a proposed card payload,
// as well as at the database boundary. Invalid patch fields must never be held.
func ValidateCommitSummary(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	bad := func() error {
		return &FieldError{Key: "commit_summary", Msg: "expected a bounded list of repository commit metadata and file counts; patches are not accepted"}
	}
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return bad()
	}
	var summaries []CommitSummary
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&summaries) != nil || summaries == nil || len(summaries) > 20 {
		return bad()
	}
	text := func(s string, limit int) bool {
		return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
		})
	}
	path := func(s string) bool {
		if s == "" || !text(s, 1024) || strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
			return false
		}
		for _, part := range strings.Split(s, "/") {
			if part == "" || part == "." || part == ".." {
				return false
			}
		}
		return true
	}
	for _, s := range summaries {
		if !path(s.Repository) || !artifactSessionRe.MatchString(s.SessionID) || !text(s.Branch, 256) || !text(s.Base, 256) || s.Branch == "" || s.Base == "" || len(s.Commits) > 100 || len(s.Files) > 300 || s.Commits == nil || s.Files == nil {
			return bad()
		}
		if !text(s.MemberID, 80) || !text(s.LaunchGeneration, 80) || s.Generation < 0 {
			return bad()
		}
		if s.MemberID != "" && (s.Generation == 0 || s.LaunchGeneration == "") {
			return bad()
		}
		for _, c := range s.Commits {
			if len(c.SHA) != 40 && len(c.SHA) != 64 {
				return bad()
			}
			for _, r := range c.SHA {
				if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
					return bad()
				}
			}
			if !text(c.Message, 512) || !text(c.Author, 256) {
				return bad()
			}
			if _, err := time.Parse(time.RFC3339, c.Date); err != nil {
				return bad()
			}
		}
		for _, f := range s.Files {
			if !path(f.Path) || (f.OldPath != "" && !path(f.OldPath)) || f.Adds < 0 || f.Dels < 0 || f.Adds > 1000000000 || f.Dels > 1000000000 || (f.Binary && (f.Adds != 0 || f.Dels != 0)) {
				return bad()
			}
		}
	}
	return nil
}
