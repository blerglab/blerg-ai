// Package topics builds a session's highlight reel: the pieces of work ("topics") it did, each with
// where it started, its documents with every version, its subagents and how it ended
// (docs/design/session-rail-history.md). It is pure: the server reads the events and the artifact
// rows and hands them to Build (reel.go). This file holds the text rules: which user message is the
// person's (a marker's request), how text is cleaned.
package topics

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const (
	// MaxTitle is a topic title's length in runes; MaxOutcome an outcome's.
	MaxTitle   = 80
	MaxOutcome = 200
	// MaxEvents is how many topic events one session may hold.
	MaxEvents = 200
	// maxRequest is how much of the opening message a request row carries.
	maxRequest = 280
	// MaxCommits is how many commits one done may name.
	MaxCommits = 50
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// CleanCommits checks the commits an agent names on a done: each sha 7 to 40 hex characters
// (lower-cased), duplicates dropped, its order kept, at most MaxCommits. nil when there are none.
func CleanCommits(in []agent.TopicCommit) ([]agent.TopicCommit, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]agent.TopicCommit, 0, min(len(in), MaxCommits))
	seen := make(map[string]bool, len(in))
	for _, c := range in {
		sha := strings.ToLower(c.Sha)
		if !shaRe.MatchString(sha) {
			return nil, fmt.Errorf("a commit sha is 7 to 40 hex characters, got %q", Cut(c.Sha, 48))
		}
		if seen[sha] {
			continue
		}
		seen[sha] = true
		out = append(out, agent.TopicCommit{Sha: sha})
	}
	if len(out) > MaxCommits {
		return nil, fmt.Errorf("at most %d commits on one topic, got %d", MaxCommits, len(out))
	}
	return out, nil
}

// Kinds are the event kinds Build reads: the kinds of the partial index agent_events_reel_idx
// (migration 041), in its order.
var Kinds = []string{"user_message", "topic", "artifact", "subagent_started", "subagent_update", "subagent_done", "turn_done", "status_changed"}

// The text Claude Code writes into the user role itself, which older pods recorded as source "chat".
// The chat package reads the same file in a test (harnessText.test.ts), so the two lists agree.
//
//go:embed testdata/harness_markers.json
var harnessJSON []byte

type harnessMarkers struct {
	Prefixes    []string `json:"prefixes"`
	RestartNote string   `json:"restart_note"`
}

var harness = func() harnessMarkers {
	var m harnessMarkers
	if err := json.Unmarshal(harnessJSON, &m); err != nil || len(m.Prefixes) == 0 || m.RestartNote == "" {
		panic("topics: testdata/harness_markers.json is unreadable")
	}
	return m
}()

// CleanText makes agent-written text safe to draw: whitespace (any kind) becomes one space; C0, DEL,
// C1 and every Unicode format character (category Cf: bidi controls, zero-width characters, U+FEFF,
// U+061C, U+00AD, U+2060–2064, tag characters U+E0000–E007F, …) go, so nothing invisible survives;
// the result is cut to max runes with "…".
func CleanText(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case r < 0x20, r >= 0x7f && r <= 0x9f:
		case unicode.Is(unicode.Cf, r), r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
		default:
			b.WriteRune(r)
		}
	}
	return Cut(strings.Join(strings.Fields(b.String()), " "), limit)
}

// Cut shortens s to limit runes, the last one "…", when it is longer.
func Cut(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit-1]) + "…"
}

// HarnessCommand reports whether a chat message is one of the commands the daemon handles itself
// (AgentHost.UserInput): a model or effort change. The chat intercepts the same two.
func HarnessCommand(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "/model ") || strings.HasPrefix(t, "/effort ")
}

// feedbackRe matches the whole first line of the messages the chat composes for a review or a
// mark-up (packages/chat/src/model/feedback.ts): "Review of <name>[ (v<N>)]: <n> request(s)[, 1 edit].",
// "Marked up <name>: <n> pin(s).", "Marked up <name> (see the attached image).".
var feedbackRe = regexp.MustCompile(`^(?:Review of .+?(?: \(v\d+\))?: \d+ requests?(?:, 1 edit)?\.|Marked up .+?(?:: \d+ pins?\.| \(see the attached image\)\.))$`)

// IsFeedback reports whether a message is a review or a mark-up the chat sent: its first line is
// exactly the chat's header, and whatever follows starts after a blank line (as the chat writes it,
// and as the send path adds the attachment note).
func IsFeedback(text string) bool {
	t := strings.TrimSpace(text)
	first, rest, more := strings.Cut(t, "\n")
	if !feedbackRe.MatchString(strings.TrimRight(first, "\r")) {
		return false
	}
	return !more || strings.HasPrefix(rest, "\n") || strings.HasPrefix(rest, "\r\n")
}

// personShape mirrors the chat's userMessageShape(...).as === 'person': never source "system", never
// harness text, never the restart note an older daemon queued as the person's.
func personShape(p agent.UserMessagePayload) bool {
	if p.Source == "system" {
		return false
	}
	lead := strings.TrimLeftFunc(p.Text, unicode.IsSpace)
	for _, m := range harness.Prefixes {
		if strings.HasPrefix(lead, m) {
			return false
		}
	}
	return !strings.HasPrefix(strings.TrimSpace(p.Text), harness.RestartNote)
}

// IsPersonMessage is §3.2's four request checks: drawn as the person's; from "chat" or "human" ("runner",
// "blerg-board", "session" and "cron" only when first: the start prompt, a card's task, a parent's task
// for its child, a cron's prompt); not a harness command; not
// feedback. first: this is the session's first message drawn as the person's.
func IsPersonMessage(p agent.UserMessagePayload, first bool) bool {
	if !personShape(p) {
		return false
	}
	switch p.Source {
	case "chat", "human":
	case "runner", "blerg-board", "session", "cron":
		if !first {
			return false
		}
	default:
		return false
	}
	return !HarnessCommand(p.Text) && !IsFeedback(p.Text)
}

// attachmentLine starts the note the send path adds for attached files.
const attachmentLine = "Attached files ("

// RequestText is what a request row shows of the opening message: the message without its
// attachment note, on one line, cut to maxRequest runes.
func RequestText(text string) string {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), attachmentLine) {
			continue
		}
		keep = append(keep, line)
	}
	return CleanText(strings.Join(keep, "\n"), maxRequest)
}

// OpenTopic reads the session's latest topic event: the topic it leaves open, if any.
func OpenTopic(payload []byte) (agent.TopicPayload, bool) {
	var p agent.TopicPayload
	if len(payload) == 0 || json.Unmarshal(payload, &p) != nil || p.ID == "" {
		return agent.TopicPayload{}, false
	}
	return p, p.Action == "start"
}

// NewID is a new topic id: "tp_" and 8 hex digits.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "tp_" + hex.EncodeToString(b[:])
}
