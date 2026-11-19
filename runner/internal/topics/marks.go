package topics

// marks.go: the strokes an agent marks with `blerg-runner mark` (a decision, a milestone, a finding),
// checked before the server records them (server/marks.go). The text is data: plain text, cleaned as a
// topic title is (CleanText), never markup. Nothing here is guessed or summarised (static over LLM).

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const (
	// MaxMarkText is a mark's length in runes.
	MaxMarkText = 240
	// MaxMarks bounds retained history while allowing a Main to run for weeks.
	// At 125 marks/day (the observed Main rate), this holds about 400 days.
	MaxMarks = 50_000
	// MaxMarkCommits is how many commits one mark may name.
	MaxMarkCommits = 10
	// MaxImport is how many marks one import may carry.
	MaxImport = 200
	// MaxCard is the highest card number a mark may name.
	MaxCard = 10_000_000
	// markFutureSkew is how far ahead of the server's clock a back-dated mark may claim to be.
	markFutureSkew = 5 * time.Minute
)

// markEarliest is the earliest moment a back-dated mark may record.
var markEarliest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// MarkKinds are the strokes an agent may mark (attention strokes are Blerg's own: asks, approvals,
// proposals and reviews it records).
var MarkKinds = map[string]bool{"decision": true, "milestone": true, "finding": true, "review": true, "failure": true}

var reviewSHA = regexp.MustCompile(`^[a-fA-F0-9]{7,40}$`)

var boardIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// MarkInput is one mark as a request carries it. Commit is the single-sha shorthand an import line
// uses; Commits the list the CLI sends.
type MarkInput struct {
	FailureType string `json:"type,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Suite       string `json:"suite,omitempty"`
	RerunPassed bool   `json:"rerun_passed,omitempty"`

	Verdict  string   `json:"verdict,omitempty"`
	Findings []string `json:"findings,omitempty"`
	Target   string   `json:"target,omitempty"`
	Round    int      `json:"round,omitempty"`

	Kind    string   `json:"kind"`
	By      string   `json:"by"`
	Text    string   `json:"text"`
	Commit  string   `json:"commit"`
	Commits []string `json:"commits"`
	Card    int      `json:"card"`
	BoardID string   `json:"board_id"`
	At      string   `json:"at"`
}

// ParseMarkTime reads a back-dated mark's time: RFC 3339, with or without seconds ("2026-10-21T15:56Z").
func ParseMarkTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("at %q is not an RFC 3339 time (2026-10-21T15:56:00Z)", Cut(s, 40))
}

// CleanMark checks one mark and makes its payload. poster is "agent" (the session's own token) or
// "owner" (the person, signed in); it is also whose the mark is when By is not given. By "owner" is
// read as "person" (an import written by hand says so).
func CleanMark(in MarkInput, poster string, now time.Time) (agent.MarkPayload, error) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !MarkKinds[kind] {
		return agent.MarkPayload{}, fmt.Errorf(`kind must be "decision", "milestone", "finding", "review" or "failure", got %q`, Cut(in.Kind, 24))
	}
	by := strings.ToLower(strings.TrimSpace(in.By))
	switch by {
	case "":
		by = "agent"
		if poster == "owner" {
			by = "person"
		}
	case "owner", "person":
		by = "person"
	case "agent":
	default:
		if (kind != "review" && kind != "failure") || !boardIDRe.MatchString(by) {
			return agent.MarkPayload{}, errors.New("by must name an engine for a review")
		}
	}
	if kind == "review" {
		if in.Card < 1 || in.Round < 1 || in.Round > 10000 {
			return agent.MarkPayload{}, errors.New("a review needs a card and a round from 1 to 10000")
		}
		if in.By == "" || by == "agent" {
			return agent.MarkPayload{}, errors.New("a review needs --by naming its engine")
		}
		switch in.Verdict {
		case "approved", "changes", "rejected":
		default:
			return agent.MarkPayload{}, errors.New("review verdict must be approved, changes or rejected")
		}
		if !reviewSHA.MatchString(in.Target) {
			return agent.MarkPayload{}, errors.New("review target must be a sha of 7 to 40 hex characters")
		}
		if len(in.Findings) > 1000 {
			return agent.MarkPayload{}, errors.New("too many review findings")
		}
		for _, v := range in.Findings {
			if v != "high" && v != "medium" && v != "low" {
				return agent.MarkPayload{}, errors.New("review findings must be high, medium or low")
			}
		}
	} else if in.Verdict != "" || len(in.Findings) > 0 || in.Target != "" || in.Round != 0 {
		return agent.MarkPayload{}, errors.New("review fields require kind review")
	}
	if kind == "failure" {
		if in.Card < 1 {
			return agent.MarkPayload{}, errors.New("a failure needs a card")
		}
		switch in.FailureType {
		case "test", "gate", "flake", "review", "session", "engine", "deploy", "merge":
		default:
			return agent.MarkPayload{}, errors.New("invalid failure type")
		}
		switch in.Severity {
		case "high", "medium", "low":
		default:
			return agent.MarkPayload{}, errors.New("failure severity must be high, medium or low")
		}
		in.Suite = CleanText(in.Suite, 120)
		if in.Suite == "" {
			return agent.MarkPayload{}, errors.New("a failure needs a suite")
		}
	} else if in.FailureType != "" || in.Severity != "" || in.Suite != "" || in.RerunPassed {
		return agent.MarkPayload{}, errors.New("failure fields require kind failure")
	}
	text := CleanText(in.Text, MaxMarkText)
	if text == "" {
		return agent.MarkPayload{}, errors.New("a mark needs a text")
	}
	shas := append([]string(nil), in.Commits...)
	if c := strings.TrimSpace(in.Commit); c != "" {
		shas = append(shas, c)
	}
	var commits []agent.TopicCommit
	for _, s := range shas {
		commits = append(commits, agent.TopicCommit{Sha: strings.TrimSpace(s)})
	}
	commits, err := CleanCommits(commits)
	if err != nil {
		return agent.MarkPayload{}, err
	}
	if len(commits) > MaxMarkCommits {
		return agent.MarkPayload{}, fmt.Errorf("at most %d commits on one mark, got %d", MaxMarkCommits, len(commits))
	}
	if in.Card < 0 || in.Card > MaxCard {
		return agent.MarkPayload{}, fmt.Errorf("card must be a card number from 1 to %d", MaxCard)
	}
	board := strings.TrimSpace(in.BoardID)
	if board != "" && !boardIDRe.MatchString(board) {
		return agent.MarkPayload{}, errors.New("board_id is not a board id")
	}
	if in.Card == 0 {
		board = ""
	}
	p := agent.MarkPayload{Kind: kind, By: by, Text: text, Commits: commits, Card: in.Card, BoardID: board, PostedBy: poster, Verdict: in.Verdict, Findings: in.Findings, Target: strings.ToLower(in.Target), Round: in.Round, FailureType: in.FailureType, Severity: in.Severity, Suite: in.Suite, RerunPassed: in.RerunPassed}
	if (kind == "review" || kind == "failure") && poster != "owner" && by == "person" {
		p.ClaimedBy, p.By = "person", "agent"
	}
	if strings.TrimSpace(in.At) != "" {
		at, err := ParseMarkTime(in.At)
		if err != nil {
			return agent.MarkPayload{}, err
		}
		if at.Before(markEarliest) {
			return agent.MarkPayload{}, fmt.Errorf("at %s is before 2000", at.Format(time.RFC3339))
		}
		if at.After(now.Add(markFutureSkew)) {
			return agent.MarkPayload{}, fmt.Errorf("at %s is in the future", at.Format(time.RFC3339))
		}
		p.At = at.Format(time.RFC3339)
	}
	return p, nil
}

// MarkEventID is a mark's client event id (a UUID). A back-dated mark's is derived from what it says
// (a name-based UUID), so importing the same file twice records each mark once; a live mark's is
// random.
func MarkEventID(sessionID string, p agent.MarkPayload) string {
	var b [16]byte
	if p.At == "" {
		_, _ = rand.Read(b[:])
		b[6] = (b[6] & 0x0f) | 0x40
	} else {
		h := sha256.New()
		parts := []string{"blerg-mark", sessionID, p.Kind, p.By, p.At, p.Text, strconv.Itoa(p.Card), p.BoardID}
		if p.Kind == "failure" {
			parts = append(parts, p.FailureType, p.Severity, p.Suite, strconv.FormatBool(p.RerunPassed))
		}
		if p.Kind == "review" {
			parts = append(parts, p.Verdict, p.Target, strconv.Itoa(p.Round), strings.Join(p.Findings, ","))
		}
		for _, c := range p.Commits {
			parts = append(parts, c.Sha)
		}
		for _, part := range parts {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
		copy(b[:], h.Sum(nil))
		b[6] = (b[6] & 0x0f) | 0x50
	}
	b[8] = (b[8] & 0x3f) | 0x80
	x := hex.EncodeToString(b[:])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

// NewMarkID is a new mark id: "mk_" and 8 hex digits.
func NewMarkID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "mk_" + hex.EncodeToString(b[:])
}
