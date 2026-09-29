package daemon

import (
	"regexp"
	"strings"
)

// selectionCursor matches the cursor Claude Code draws on the highlighted option
// of a confirmation/permission dialog, e.g. "❯ 1. Yes". The numbered form is what
// distinguishes an active selection prompt from the ordinary input line ("❯ ").
var selectionCursor = regexp.MustCompile(`❯\s+\d+\.`)

// optionLine matches an enumerated choice Claude lists under a question, e.g.
// "a) Git commit hash…", "1. SQLite…", "2: Postgres…". These sit between the
// question and the ✻ turn summary, so conversationalQuestion skips over them when
// hunting for the "?" that signals a real prompt.
var optionLine = regexp.MustCompile(`^(?:[a-zA-Z]|\d+)[.):]\s`)

// workingIndicator matches Claude Code's live in-progress status line, e.g.
// "✢ Misting… (1m 26s · ↓ 3.6k tokens)": an elapsed timer followed by " · " and a
// token I/O arrow. This is the width-independent "running" signal — at narrow
// (mobile) pane widths the hint bar's "esc to interrupt" is truncated away, but this
// line survives. The idle turn summary ("✻ Cooked for 24s") has no "· ↓/↑", so it
// never matches.
var workingIndicator = regexp.MustCompile(`\d+s · [↓↑]`)

// workingSpinner matches Claude Code's live in-progress spinner line, e.g.
// "✶ Architecting…" or "· Warping…": a spinner glyph, a space, then a gerund ending
// in the … ellipsis. This is the most truncation-resistant running signal — at
// narrow (mobile) widths the trailing "(7m 35s · ↓ 11.2k tokens)" and the hint
// bar's "esc to interrupt" are both cut off, but this leading "<glyph> Word…"
// survives. The idle turn summary ("✻ Cooked for 24s") shares the ✻ glyph but has
// no ellipsis after its word, and the "… +N lines" expansion hint starts with the
// ellipsis (no preceding word), so neither matches.
var workingSpinner = regexp.MustCompile(`^\s*[·*✢✣✤✥✦✧✱✲✳✴✵✶✷✸✹✺✻✼✽✾✿] \S*…`)

// backgroundShells matches the "· N shell(s) still running" suffix Claude Code
// appends to the turn summary while background shells keep running after the main
// turn ends. Work is still ongoing, so the session is running, not idle.
var backgroundShells = regexp.MustCompile(`shells? still running`)

// classifyScreen maps a captured terminal screen to a session state. It reads the
// rendered screen rather than the raw byte stream, so it is immune to the
// repaint-vs-animation ambiguity that made glyph detection flaky: a resize redraw
// re-emits whatever is on screen (including the static "✻ Cooked for 24s" summary
// and "… +N lines" indicators), but the rendered screen itself tells the truth.
//
// Signals (verified against live Claude Code captures):
//   - waiting: a numbered confirmation/permission dialog is on screen.
//   - running: the status hint bar (last non-empty line) offers "esc to
//     interrupt" — present only while a turn is actively in progress.
//   - idle: neither.
//
// "esc to interrupt" is matched only on the hint bar, never anywhere on screen,
// because it can also appear in scrollback (a transcript or pasted text).
func classifyScreen(screen string) string {
	// A completely blank pane means Claude hasn't rendered its first frame yet
	// (startup, before the TUI enables raw-mode input). It must never classify
	// as idle: the state poller flushes queued initial prompts on idle, and
	// bytes written to the PTY at this point are silently dropped. Report
	// running ("starting up") until the first real frame appears.
	if strings.TrimSpace(screen) == "" {
		return "running"
	}

	lines := strings.Split(screen, "\n")

	if screenHasPrompt(lines) {
		return "waiting"
	}

	hint := hintBar(lines)
	if strings.Contains(hint, "esc to interrupt") {
		return "running"
	}
	// Width-independent running signals, scanned only in the bottom region so
	// scrollback/body prose can't trigger them (mirrors screenHasPrompt's scoping).
	// At narrow mobile widths the hint bar's "esc to interrupt" is truncated off, but
	// the live working line ("✢ Misting… (1m 26s · ↓ 3.6k tokens)") survives; and a
	// finished turn with background shells still running is work in progress too.
	if bottomRegionMatches(lines, workingIndicator) ||
		bottomRegionMatches(lines, workingSpinner) ||
		bottomRegionMatches(lines, backgroundShells) {
		return "running"
	}
	// "Enter to select" is the hint bar for Claude Code's interactive selection
	// dialogs (brainstorming, /model picker, etc.). It is as uniquely identifying
	// as "esc to interrupt" is for running turns, so we read it from the hint bar
	// rather than scanning the body, where the dialog may extend past the region.
	if strings.Contains(hint, "Enter to select") {
		return "waiting"
	}
	// Conversational question: Claude ended a response with "Which approach?" etc.
	// No structural TUI signal exists for this case, so we look for the closest
	// non-empty line before the most recent ✻ turn-summary ending with "?".
	if conversationalQuestion(lines) {
		return "waiting"
	}
	// Explicit handoff: Claude ended with a phrase like "let me know if you want
	// any changes" — no "?" but clearly awaiting the human.
	if conversationalHandoff(lines) {
		return "waiting"
	}

	return "idle"
}

// hintBar returns the Claude Code status hint bar — the line carrying the
// running/idle/selection signals ("esc to interrupt", "← for agents", "Enter to
// select"). It is normally the last non-empty line, but when background agents are
// running their task list ("● main", "◯ general-purpose …") is rendered BELOW the
// hint bar, so the last non-empty line is a task row, not the hint bar. The normal
// hint bar always carries the permission-mode toggle "(shift+tab to cycle)" (stable
// across modes) and a selection dialog replaces it with its own "Enter to select"
// footer; both are uniquely identifying, so we scan from the bottom for either
// marker first (the agent task list can render below the dialog footer too) and only
// fall back to the last non-empty line when neither is present.
func hintBar(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "(shift+tab to cycle)") ||
			strings.Contains(lines[i], "Enter to select") {
			return lines[i]
		}
	}
	return lastNonEmptyLine(lines)
}

// bottomRegionMatches reports whether any line in the last `region` lines matches
// re. Scoped like screenHasPrompt so a signal sitting in scrollback or earlier
// conversation body cannot trigger a false positive.
func bottomRegionMatches(lines []string, re *regexp.Regexp) bool {
	const region = 15
	start := len(lines) - region
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// lastNonEmptyLine returns the last line with non-whitespace content — the status
// hint bar, which capture-pane emits above any trailing blank rows.
func lastNonEmptyLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// conversationalQuestion returns true when Claude ended its most recent response
// turn with a question (e.g. "Which approach?"). There is no TUI structural
// signal for this, so we use a proximity heuristic: find the last ✻ turn-summary
// line and walk backward to the nearest "real" line, checking whether it ends
// with "?". Blank lines and enumerated option lines (e.g. "a) …", "1. …") are
// skipped, because Claude frequently poses a question and then lists choices —
// the "?" then sits above the option list, not immediately before the summary.
// Tool-output lines (starting with ⎿ after trimming) are excluded so that bash
// output ending in "?" doesn't produce false positives.
func conversationalQuestion(lines []string) bool {
	lastSummary := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "✻ ") {
			lastSummary = i
		}
	}
	if lastSummary < 0 {
		return false
	}
	for j := 1; j <= 12 && lastSummary-j >= 0; j++ {
		candidate := strings.TrimSpace(lines[lastSummary-j])
		if candidate == "" || optionLine.MatchString(candidate) {
			continue
		}
		return closesWithQuestion(candidate)
	}
	return false
}

// questionStop matches a question mark that ends a clause — one followed by
// whitespace, a closing paren, or end of line. This is what separates a real
// question ("…right? Next: …", "…merge?", "…on? That's…") from a "?" embedded in
// a token such as a URL query string ("…/api?ref=foo"), which must not count.
var questionStop = regexp.MustCompile(`\?(\s|\)|$)`)

// rhetoricalAnswer reports whether the text right after a question mark begins by
// answering it — a self-answered "Why split it here? Because …" rhetorical, where
// the model is explaining rather than awaiting the human. Kept deliberately
// narrow (the unambiguous causal openers) so genuine questions are not missed.
func rhetoricalAnswer(rest string) bool {
	lower := strings.ToLower(strings.TrimSpace(rest))
	return strings.HasPrefix(lower, "because") || strings.HasPrefix(lower, "since")
}

// closesWithQuestion reports whether a closing response line poses a question to
// the human. Any clause-ending "?" qualifies — Claude commonly asks and then
// continues on the same line with a preview ("This look right? Next: …"), a
// parenthetical ("…push to origin/main? (Branch stays untouched…)"), or a
// rationale after a second question ("Want me to…? And what is prod running on?
// That's the number…") — so the line need not end in "?". A line whose only
// questions are self-answered rhetoricals ("Why split it here? Because …") does
// not count, and tool-output lines (⎿) never count.
func closesWithQuestion(candidate string) bool {
	if strings.HasPrefix(candidate, "⎿") {
		return false
	}
	for _, loc := range questionStop.FindAllStringIndex(candidate, -1) {
		if !rhetoricalAnswer(candidate[loc[0]+1:]) {
			return true
		}
	}
	return false
}

// handoffPhrases are lowercase substrings that indicate Claude is explicitly
// handing control back to the human without ending with "?".
var handoffPhrases = []string{
	"let me know",
	"please review",
	"feel free to",
	"whenever you're ready",
	"when you're ready",
	"you need to",
}

// conversationalHandoff returns true when Claude ended its most recent response
// with an explicit hand-off phrase (e.g. "let me know if you want any changes").
// Scans up to 20 lines before the last ✻ summary, skipping blank lines but
// stopping at the first tool-output line (⎿) to avoid scanning into prior turns.
func conversationalHandoff(lines []string) bool {
	lastSummary := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "✻ ") {
			lastSummary = i
		}
	}
	if lastSummary < 0 {
		return false
	}
	for j := 1; j <= 20 && lastSummary-j >= 0; j++ {
		candidate := strings.TrimSpace(lines[lastSummary-j])
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "⎿") {
			return false
		}
		lower := strings.ToLower(candidate)
		for _, phrase := range handoffPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	return false
}

// screenHasPrompt reports whether the bottom region shows an interactive
// confirmation/selection dialog awaiting the user. Scoped to the last lines so a
// numbered list or "[y/n]" sitting in scrollback isn't mistaken for a live prompt.
func screenHasPrompt(lines []string) bool {
	const region = 15
	start := len(lines) - region
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		if selectionCursor.MatchString(l) {
			return true
		}
		ls := strings.ToLower(l)
		if strings.Contains(ls, "[y/n]") || strings.Contains(ls, "(y/n)") {
			return true
		}
	}
	return false
}
