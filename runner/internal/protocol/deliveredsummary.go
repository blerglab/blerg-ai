package protocol

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DeliveredSummary contains bounded metadata only, collected from delivered objects.
type DeliveredSummary struct {
	Commits   []BranchCommit `json:"commits"`
	Files     []CommitFile   `json:"files"`
	Truncated bool           `json:"truncated,omitempty"`
}

// Valid refuses oversized or malformed metadata before durable result ingestion.
func (s *DeliveredSummary) Valid() bool {
	if s == nil {
		return true
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > 96<<10 || len(s.Commits) > 100 || len(s.Files) > CommitFilesMax || s.Commits == nil || s.Files == nil {
		return false
	}
	text := func(v string, n int) bool {
		return len(v) <= n && utf8.ValidString(v) && !strings.ContainsFunc(v, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
		})
	}
	path := func(v string) bool {
		if v == "" || !text(v, CommitPathMaxBytes) || strings.HasPrefix(v, "/") || strings.Contains(v, "\\") {
			return false
		}
		for _, p := range strings.Split(v, "/") {
			if p == "" || p == "." || p == ".." {
				return false
			}
		}
		return true
	}
	seen := map[string]bool{}
	for _, c := range s.Commits {
		if !ValidSessionOriginCommit(c.SHA) || seen[c.SHA] || !text(c.Message, CommitSubjectMaxBytes) || !text(c.Author, CommitAuthorMaxBytes) {
			return false
		}
		if _, err := time.Parse(time.RFC3339, c.Date); err != nil {
			return false
		}
		seen[c.SHA] = true
	}
	for _, f := range s.Files {
		if !path(f.Path) || (f.OldPath != "" && !path(f.OldPath)) || f.Adds < 0 || f.Dels < 0 || f.Adds > 1000000000 || f.Dels > 1000000000 || (f.Binary && (f.Adds != 0 || f.Dels != 0)) {
			return false
		}
	}
	return true
}
