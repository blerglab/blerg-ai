// Package workbranch resolves immutable session branch names without invoking Git.
package workbranch

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Context struct {
	Username, Session, Title, Card string
	Date                           time.Time
}

var placeholder = regexp.MustCompile(`\{[^{}]+\}`)
var slugCharacters = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	value := slugCharacters.ReplaceAllString(strings.ToLower(s), "-")
	if len(value) > 64 {
		value = value[:64]
	}
	return strings.Trim(value, "-")
}

// Valid implements git-check-ref-format --branch, also bounding names on the wire.
func Valid(s string) bool {
	if !utf8.ValidString(s) || s == "" || len(s) > 240 || s == "@" || s == "HEAD" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for _, c := range s {
		if c <= 32 || c == 127 || strings.ContainsRune(`~^:?*[\`, c) {
			return false
		}
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Effective preserves pre-template sessions and never reinterprets a recorded name.
func Effective(session, recorded string) string {
	if recorded != "" {
		return recorded
	}
	return "wip/" + session
}

func Resolve(template string, c Context) (string, error) {
	if template == "" || template == "wip/{session}" {
		branch := Effective(c.Session, "")
		// Validate the identity even when only its short prefix appears in the ref.
		if !Valid(branch) {
			return "", errors.New("invalid default work branch")
		}
		if template == "" {
			title := slug(c.Title)
			if title == "" {
				title = "session"
			}
			suffix := c.Session
			if len(suffix) > 8 {
				suffix = suffix[:8]
			}
			branch = "wip/" + title + "-" + suffix
		}
		if !Valid(branch) {
			return "", errors.New("invalid default work branch")
		}
		return branch, nil
	}
	if strings.ContainsRune(template, 0) {
		return "", errors.New("invalid branch template")
	}
	if len(template) > 180 {
		return "", errors.New("branch template is too long")
	}
	short := c.Session
	if len(short) > 8 {
		short = short[:8]
	}
	title := slug(c.Title)
	if title == "" {
		title = short
	}
	values := map[string]string{"{username}": slug(c.Username), "{session}": short, "{slug}": title, "{date}": c.Date.UTC().Format("2006-01-02"), "{card}": slug(c.Card)}
	missingCard := strings.Contains(template, "{card}") && values["{card}"] == ""
	if missingCard {
		values["{card}"] = "\x00"
	}
	unknown := false
	out := placeholder.ReplaceAllStringFunc(template, func(key string) string {
		v, ok := values[key]
		if !ok {
			unknown = true
		}
		return v
	})
	if unknown || strings.ContainsAny(out, "{}") {
		return "", errors.New("unknown branch template placeholder")
	}
	if missingCard {
		out = omitCard(out)
	}
	if len(out) > 190 {
		out = strings.TrimRight(strings.ToValidUTF8(out[:190], ""), "/.")
	}
	if out == "" {
		out = c.Session
	}
	if missingCard && !Valid(out) {
		out = c.Session
	}
	if !Valid(out) {
		return "", errors.New("branch template must produce a valid Git branch")
	}
	return out, nil
}

func ValidateTemplate(template string) error {
	_, err := Resolve(template, Context{Username: "person", Session: "12345678-1234-1234-1234-123456789abc", Title: "session", Card: "BLG-1", Date: time.Now()})
	return err
}

// Collapse only separators adjacent to the omitted card, preserving literal
// separators elsewhere in the person's template. NUL cannot occur in a template.
func omitCard(value string) string {
	for {
		at := strings.IndexByte(value, 0)
		if at < 0 {
			return value
		}
		left, right := at, at+1
		for left > 0 && strings.ContainsRune("/-_.", rune(value[left-1])) {
			left--
		}
		for right < len(value) && strings.ContainsRune("/-_.", rune(value[right])) {
			right++
		}
		separators := value[left:at] + value[at+1:right]
		replacement := ""
		if left > 0 && right < len(value) {
			if strings.Contains(separators, "/") {
				replacement = "/"
			} else if separators != "" {
				replacement = separators[:1]
			}
		}
		value = value[:left] + replacement + value[right:]
	}
}
