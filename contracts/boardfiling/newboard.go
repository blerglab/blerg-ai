package boardfiling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A session proposes a new board; the person approves it in Runner. The board is then created
// with the person's own authority through one Core-signed, one-use capability that binds the
// exact approved draft (BoardDigest). It grants no other read, write or administration.

// BoardCapabilityPrefix names the one-board creation capability.
const BoardCapabilityPrefix = "board.create_once:"

// Limits of a proposed board. Columns and repositories are short lists on purpose.
const (
	MaxBoardNameRunes        = 100
	MaxBoardDescriptionRunes = 2000
	MaxBoardRepos            = 20
	MaxBoardColumns          = 20
	MaxBoardColumnRunes      = 64
)

// BoardDraft is the exact board a person approved. Template and Columns are exclusive: a
// template owns the board's columns and fields.
type BoardDraft struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Template    string   `json:"template,omitempty"`
	Columns     []string `json:"columns,omitempty"`
}

var (
	repoSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
	templateID  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// ValidRepoName reports whether repo is a folder name ("app") or owner/name ("team/app"): one or
// two segments of letters, digits, '_', '.' and '-', none starting with '.' or '-'.
func ValidRepoName(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !repoSegment.MatchString(p) || strings.HasSuffix(p, ".git") {
			return false
		}
	}
	return true
}

// invisible reports a format character (Unicode Cf: bidi controls such as U+202E and U+2066–U+2069,
// zero-width characters, U+FEFF): text that would reorder or hide what the person reads.
func invisible(r rune) bool { return unicode.Is(unicode.Cf, r) }

func oneLine(s string, maxRunes int) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxRunes && !strings.ContainsFunc(s, unicode.IsControl) &&
		!strings.ContainsFunc(s, invisible) && strings.TrimSpace(s) == s
}

// ValidateBoardDraft checks every field of a proposed board. It is the same check on the
// Runner (when the session proposes and when the person approves) and on Board (before the
// approved board is written).
func ValidateBoardDraft(d BoardDraft) error {
	if !oneLine(d.Name, MaxBoardNameRunes) {
		return errors.New("name is required: one line of at most 100 characters, without leading or trailing spaces or bidi or invisible characters")
	}
	if d.Description != "" {
		if !utf8.ValidString(d.Description) || utf8.RuneCountInString(d.Description) > MaxBoardDescriptionRunes ||
			strings.ContainsFunc(d.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' || invisible(r) }) {
			return errors.New("description is at most 2000 characters, without control, bidi or invisible characters")
		}
	}
	if len(d.Repos) > MaxBoardRepos {
		return errors.New("at most 20 repositories")
	}
	seen := map[string]bool{}
	for _, repo := range d.Repos {
		if !ValidRepoName(repo) {
			return errors.New("each repository is a folder name or owner/name (letters, digits, '_', '.' and '-')")
		}
		if seen[strings.ToLower(repo)] {
			return errors.New("a repository is listed twice")
		}
		seen[strings.ToLower(repo)] = true
	}
	if d.Template != "" && !templateID.MatchString(d.Template) {
		return errors.New("template is a template id such as focus-board")
	}
	if d.Template != "" && len(d.Columns) > 0 {
		return errors.New("give either a template or columns, not both: a template brings its own columns")
	}
	if len(d.Columns) > MaxBoardColumns {
		return errors.New("at most 20 columns")
	}
	cols := map[string]bool{}
	for _, c := range d.Columns {
		if !oneLine(c, MaxBoardColumnRunes) {
			return errors.New("each column is one line of at most 64 characters, without bidi or invisible characters")
		}
		if cols[strings.ToLower(c)] {
			return errors.New("a column is listed twice")
		}
		cols[strings.ToLower(c)] = true
	}
	return nil
}

// BoardDigest binds every approved field. All callers marshal this same typed structure.
func BoardDigest(d BoardDraft) string {
	payload := struct {
		Kind string `json:"kind"`
		BoardDraft
	}{"board.create", d}
	raw, _ := json.Marshal(payload) // Strings and string slices cannot fail JSON encoding.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// BoardCapability encodes the approved intent and draft digest in one capability.
func BoardCapability(intent, digest string) (string, error) {
	if !intentPattern.MatchString(intent) || !digestPattern.MatchString(digest) {
		return "", errors.New("invalid board creation intent or digest")
	}
	return BoardCapabilityPrefix + intent + ":" + digest, nil
}

// ParseBoardCapability accepts exactly one canonical board creation capability and nothing more.
func ParseBoardCapability(caps []string) (intent, digest string, ok bool) {
	if len(caps) != 1 || !strings.HasPrefix(caps[0], BoardCapabilityPrefix) || len(caps[0]) != len(BoardCapabilityPrefix)+36+1+64 {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(caps[0], BoardCapabilityPrefix), ":")
	if len(parts) != 2 || !intentPattern.MatchString(parts[0]) || !digestPattern.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}
