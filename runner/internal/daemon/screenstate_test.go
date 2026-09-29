package daemon

import "testing"

// These fixtures are synthetic screens modelled on the bottom region of Claude
// Code's terminal layout. The distinguishing signal is the status hint bar (the
// last non-empty line): it ends with "· esc to interrupt" while a turn is in
// progress and with something else (e.g. "· ← for agents") when idle.

const idleScreen = `● Bash(git push)
  ⎿  Running…

✻ Cooked for 24s

─────────────────────────────────────────────────────────────
❯ push it
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

const runningScreen = `● Bash(make test)
  ⎿  Running…

* Architecting… (31s · ↓ 1.2k tokens · thinking with medium effort)
  ⎿  Tip: /goal keeps Claude working until a condition is met.

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt
`

// idleWithEscInScrollback is the trap that broke glyph-based detection: an idle
// screen whose scrollback contains the literal text "esc to interrupt" (e.g. the
// user pasted it, or a transcript echoed it). Only the hint bar must count.
const idleWithEscInScrollback = `  - Working session: … · esc to interrupt
  - Idle session: … · ← for agents

✻ Cooked for 24s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

const waitingScreen = `● Edit(main.go)

╭─────────────────────────────────────────────────────────────╮
│ Do you want to make this edit to main.go?                     │
│ ❯ 1. Yes                                                      │
│   2. Yes, allow all edits during this session                 │
│   3. No, and tell Claude what to do differently               │
╰─────────────────────────────────────────────────────────────╯
`

// waitingSelectionScreen mirrors the brainstorming/picker dialogs where each
// option has a multi-line description. With a tall terminal the ❯ 1. cursor
// can sit more than 15 lines from the bottom, so the hint bar
// "Enter to select" is the reliable detection signal.
const waitingSelectionScreen = `
 ☐ Visibility

When should the shortcut hints be visible?

❯ 1. Always on small screens
     A persistent hint row pinned at the bottom of the window whenever the screen
     is narrow. Always there when you need it, costs a strip of vertical space.
  2. Only when 'editing'
     Show the hints only when the note is in edit mode. Reclaims space the
     rest of the time, but menus that don't flip the mode leave the hints hidden.
  3. Toggle button
     A small always-present button that expands the hint row on demand.
     Minimal footprint, but one extra tap each time you open a menu.
  4. Type something.
─────────────────────────────────────────────────────────────────────────────────────
  5. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel
`

// runningWithSelectionInScrollback is the false-positive trap introduced by
// widening the region: prose explaining the fix contained "❯ 1." and was
// captured within the bottom 30 lines, flipping a running session to "waiting".
// With region=15 the prose is outside the window; the hint bar wins.
const runningWithSelectionInScrollback = `  ... and the hint bar is the reliable detection signal.
  2. Defense-in-depth: region bumped for dialogs whose ❯ 1. cursor might drift.

  The next state poll will reclassify.

✻ Baked for 7m

❯ some user message

● Let me capture the screens.

● Bash(capture-pane…)
  ⎿  Running…

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt
`

// idleWithEmbeddedTurnSummary reproduces the false-positive where the Edit
// tool renders test-fixture code inline in the conversation. The rendered diff
// output contains lines like "115 +✻ Crunched for 48s" and the prose response
// contains "the last ✻ turn summary is examined". Neither starts with "✻ " so
// HasPrefix correctly ignores them; only the real turn summary at the end matches.
const idleWithEmbeddedTurnSummary = `      113 +  Which approach?
      114 +
      115 +✻ Crunched for 48s
  - 9c41e7a2: conversationalQuestion() finds Which approach? before ✻ Crunched for 48s.

  The key insight: only the last ✻ turn summary is examined (not earlier ones
  in scrollback), and only the immediate non-empty line before it is checked, so
  old question-and-answer exchanges in scrollback don't trigger
  false positives.

✻ Churned for 6m 11s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingConversationalQuestion mirrors a session where Claude ends its response
// with a prose question ("Which approach?") rather than a TUI selection dialog.
// The only signal is the "?" line immediately before the ✻ turn-summary.
const waitingConversationalQuestion = `  1. Subagent-Driven (recommended) — fresh subagent per task, review between each

  2. Inline — execute tasks in this session with checkpoints

  Which approach?

✻ Crunched for 48s

● How is Claude doing this session? (optional)
  1: Bad    2: Fine   3: Good   0: Dismiss

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ctrl+t to hide tasks · ← for agents
`

// waitingExplicitHandoff mirrors a session where the handoff phrase is separated
// from the ✻ summary by a footer note — the "let me know" line is not the
// immediately preceding non-empty line.
const waitingExplicitHandoff = `● Bash(git add docs/spec.md && git commit -q -m "docs: add spec")
  ⎿  abc1234 docs: add spec

● Spec written and committed. Please review it and let me know if you want any changes before we move on.

  (Note: your working-tree changes are still uncommitted; I left them as-is.)

✻ Churned for 50s

─────────────────────────────────────────────────────────────
❯ write the implementation plan
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingHandoffBuriedInList mirrors a session where the handoff phrase precedes
// a multi-item list — "you need to do" is many lines above the ✻ summary.
const waitingHandoffBuriedInList = `  Commits landed:
  - 3b9d2e1 — export button added

  Before you leave, you need to do manually:
  1. Install the example CLI on the build machine
  2. Enable the export flag in the admin settings
  3. Confirm the export folder path
  4. make deploy to push the updated bundle
  5. Fill in EXAMPLE_EXPORT_TOKEN in the local config
  6. Test: make run-client → confirm the export button appears in the toolbar

✻ Baked for 5m 17s

─────────────────────────────────────────────────────────────
❯ make deploy
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingQuestionWithLetterOptions mirrors the real-session bug: Claude asks a
// question ("…should we use?") and then lists lettered options. The "?" line is
// no longer the line immediately before the ✻ summary — the last option is — so a
// classifier that only inspects the immediate preceding line misclassifies this
// as idle.
const waitingQuestionWithLetterOptions = `● Next question: which export format should be the default?

  a) Markdown — plain text, easy to diff, exact round trip, zero extra dependencies, works perfectly for a note-taking tool
  b) PDF — fixed layout, e.g. A4 pages, needs a renderer on each machine
  c) HTML — simple but the styling can differ between viewers and printers

✻ Baked for 8s

  4 tasks (1 done, 1 in progress, 2 open)
  ✔ Explore project context
  ◼ Ask clarifying questions
  ◻ Propose approaches and present design
  ◻ Write design doc and transition to implementation plan

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ctrl+t to hide tasks · ← for agents
`

// waitingQuestionWithNumberOptions is the same pattern with a numbered option
// list, the other shape Claude uses for enumerated choices.
const waitingQuestionWithNumberOptions = `● Which storage format should we use?

  1. JSON — zero-config, single file, perfect for a small tool
  2. A database — heavier, but ready for many users later

✻ Cooked for 12s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingQuestionMidLineBeforePreview mirrors the brainstorming checkpoint
// pattern: Claude closes a turn with a question that is NOT at the end of the
// line — "This look right? Next: <preview of what's coming>." The "?" sits
// mid-line followed by a sentence previewing the next steps, so a classifier
// that only checks HasSuffix(line, "?") misses it and falls through to idle.
const waitingQuestionMidLineBeforePreview = `  Server → Browser: note_updated, note_created, export_finished { result }. The open note still uses the existing editor_* events.

  This look right? Next: the toolbar (compact filter row, tag menu, sort picker), then the export end-to-end walkthrough + testing/config/errors.

✻ Brewed for 20s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingQuestionMidLineBeforeParenthetical mirrors a turn that closes with a
// direct question followed by a parenthetical note on the same line ("Should I
// merge to main…? (Branch stays untouched until you confirm.)"). The line ends
// in ")", not "?", and "Should I merge" is not a stock confirmation phrase — so
// only a general "clause-ending ? mid-line" rule catches it.
const waitingQuestionMidLineBeforeParenthetical = `● Pushed the branch and opened the PR.

  This is complete. Should I merge to main and push to origin/main? (Branch stays untouched until you confirm.)

✻ Churned for 53m 44s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · gh auth login · ← for agents
`

// waitingTwoQuestionsThenRationale mirrors a turn that asks two questions and
// then closes with a rationale sentence ("Want me to capture that…? And — what
// is the smallest screen you support? That's the number that tells us…"). The
// line ends in a statement, but two genuine questions precede it; the first "?"
// is followed by "And", not a self-answer, so the line is awaiting the human.
const waitingTwoQuestionsThenRationale = `● Reindex finished; the search index rebuilt without the timeout.

  Want me to capture that as a memory note (filter/sort checklist) so the next large-list change doesn't repeat the slow-scroll dance? And — what is the smallest screen you support? That's the number that tells us whether your layout is right-sizing or quietly over-sizing.

✻ Churned for 38s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// idleStatementWithMidLineQuestionMark guards the mid-line "?" relaxation
// against false positives: a rhetorical question Claude answers in the same
// breath ("Why split it? Because the client owns the clone.") is not awaiting
// the human — the turn resolved itself and the session is idle.
const idleStatementWithMidLineQuestionMark = `● Done — the clone now happens client-side.

  Why split it here? Because the client owns the working tree, so cloning anywhere else would race the session spawn.

✻ Churned for 30s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// idleListNoQuestion guards the option-skipping logic against false positives:
// an enumerated list that is a summary (no preceding "?") must stay idle.
const idleListNoQuestion = `● Here is what I landed:

  1. Added the version field to ClientHello
  2. Baked the git hash in at build time
  3. Surfaced drift as a badge in the sidebar

✻ Churned for 30s

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

// waitingSelectionWithTasksBelowFooter mirrors the real-session bug: a selection
// dialog is open ("Enter to select" footer) but Claude Code renders the agent task
// list BELOW the footer, so the footer is no longer the last non-empty line. The
// ❯ 1. cursor also sits >15 lines from the bottom (pushed up by the option
// descriptions plus the task panel), so neither the "Enter to select" hint nor the
// screenHasPrompt cursor scan fires and the session misclassifies as idle.
const waitingSelectionWithTasksBelowFooter = ` ☐ Sequencing

When should I dig into the toolbar right-edge clipping bug?

❯ 1. After the sidebar branch
     Finish the 7 remaining sidebar tasks + final review first, then start a fresh
     systematic-debugging session on the clipping. Keeps the two workstreams clean.
  2. Pause and debug now
     Stop the sidebar flow at this checkpoint and investigate the clipping now.
  3. Quick look now, decide after
     I spend a few minutes confirming the root cause (no fix yet), report what it is.
  4. Type something.
─────────────────────────────────────────────────────────────────────────────────────
  5. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel

  9 tasks (2 done, 7 open)
  ◻ Task 3: Server broadcasts pre-save errors
  ◻ Task 4: Auto-navigate on create with loading/error views
  ◻ Task 5: Delete navigates home
  ◻ Task 6: Compact note card
  ◻ Task 7: Toolbar focus pills + persisted collapse
   … +2 pending, 2 completed
`

// runningWithBackgroundAgents mirrors the real-session bug: a turn is in progress
// ("esc to interrupt" on the hint bar) but Claude Code renders the background-agent
// task list ("● main", "◯ general-purpose …") BELOW the hint bar. The hint bar is
// therefore no longer the last non-empty line, so a classifier that reads only the
// last non-empty line misses "esc to interrupt" and misclassifies as idle.
const runningWithBackgroundAgents = `● Running 4 agents… (ctrl+o to expand)
   ├ Filter rollout group 4 · 26 tool uses · 95.9k tokens
     ◐  Update: src/screens/NoteDetailScreen.tsx

✻ Incubating… (7m 41s · ↑ 57.8k tokens)

─────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · gh auth login · esc to interrupt · ↓ to manage

  ● main
  ◯ general-purpose  Filter rollout group 3                        5m 15s · ↓ 99.4k tokens
  ◯ general-purpose  Filter rollout group 4                        5m 6s · ↓ 99.9k tokens
`

// runningNarrowTruncatedHint mirrors the real-session bug on a narrow (mobile-
// width ~46 col) pane: a turn is actively in progress, but Claude Code truncates
// its own status bar so "(shift+tab to cycle) · esc to interrupt" is cut to
// "(shift+tab to  ·" — the "esc to interrupt" running signal is gone. The
// width-independent signal is the live working line "✢ Misting… (1m 26s · ↓ 3.6k
// tokens)", whose elapsed-timer + token-arrow only appears during an active turn.
const runningNarrowTruncatedHint = `● Bash(make test)
  ⎿  Running…

✢ Misting… (1m 26s · ↓ 3.6k tokens)

──────────────────────────────────────────────
❯
──────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to  ·
`

// runningBackgroundShells mirrors the real-session bug where the main turn has
// finished ("✻ Sautéed for 12s") but background shells are still running, shown as
// the "· N shells still running" suffix on the summary. Work is ongoing, so the
// session is running, not idle.
const runningBackgroundShells = `● Live — new bundle serving (200). Merged to main.

  Hard-refresh and try it.

✻ Sautéed for 12s · 3 shells still running

──────────────────────────────────────────────
❯
──────────────────────────────────────────────
  ⏵⏵ bypass permissions on · gh auth login ·
`

// runningNarrowSpinnerNoTimer mirrors the real-session bug at narrow (mobile)
// width: a turn is in progress, but Claude Code truncates BOTH the hint bar's
// "esc to interrupt" AND the working line's "(7m 35s · ↓ 11.2k tokens)" tail, so
// the only surviving running signal is the spinner line's "<glyph> Gerund…" (the …
// ellipsis). The "… +N lines" expansion hint and the idle summary "✻ … for Ns"
// must NOT be mistaken for it. Modelled on a 46-column pane.
const runningNarrowSpinnerNoTimer = `● Bash(hostname -I)
  ⎿  === Host IPv4 address ===
     203.0.113.114
     === Guest IP (forward target) ===
     … +4 lines (ctrl+o to expand)

✶ Architecting… (thinking with high effort)

──────────────────────────────────────────────
❯
──────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to  ·
`

func TestClassifyScreen(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   string
	}{
		{"idle prompt", idleScreen, "idle"},
		{"active turn", runningScreen, "running"},
		{"running turn with background agents listed below hint bar", runningWithBackgroundAgents, "running"},
		{"active turn at narrow width with truncated hint bar", runningNarrowTruncatedHint, "running"},
		{"background shells still running after the turn", runningBackgroundShells, "running"},
		{"active turn at narrow width with truncated spinner (no timer)", runningNarrowSpinnerNoTimer, "running"},
		{"esc-to-interrupt only in scrollback stays idle", idleWithEscInScrollback, "idle"},
		{"confirmation dialog", waitingScreen, "waiting"},
		{"selection dialog with multi-line options", waitingSelectionScreen, "waiting"},
		{"selection dialog with agent task list below footer", waitingSelectionWithTasksBelowFooter, "waiting"},
		{"running session with ❯ 1. in scrollback prose is not waiting", runningWithSelectionInScrollback, "running"},
		{"embedded ✻ in tool output and prose does not trigger waiting", idleWithEmbeddedTurnSummary, "idle"},
		{"conversational question before turn summary", waitingConversationalQuestion, "waiting"},
		{"question followed by lettered option list", waitingQuestionWithLetterOptions, "waiting"},
		{"question followed by numbered option list", waitingQuestionWithNumberOptions, "waiting"},
		{"confirmation question mid-line before next-steps preview", waitingQuestionMidLineBeforePreview, "waiting"},
		{"direct question mid-line before parenthetical note", waitingQuestionMidLineBeforeParenthetical, "waiting"},
		{"two questions then closing rationale sentence", waitingTwoQuestionsThenRationale, "waiting"},
		{"rhetorical mid-line question answered in same line stays idle", idleStatementWithMidLineQuestionMark, "idle"},
		{"enumerated summary with no question stays idle", idleListNoQuestion, "idle"},
		{"explicit handoff phrase separated from ✻ by footer note", waitingExplicitHandoff, "waiting"},
		{"handoff phrase buried before multi-item list", waitingHandoffBuriedInList, "waiting"},
		// A blank pane means Claude hasn't rendered its first frame yet (startup,
		// before raw-mode input exists). "idle" here made the state poller flush a
		// queued initial prompt into a PTY nothing was reading — the bytes were
		// silently dropped. Blank must classify as running (starting up), never idle.
		{"empty screen", "", "running"},
		{"whitespace-only startup screen", "\n\n   \n\n", "running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyScreen(tt.screen); got != tt.want {
				t.Errorf("classifyScreen() = %q, want %q", got, tt.want)
			}
		})
	}
}
