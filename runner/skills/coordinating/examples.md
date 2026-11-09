# Coordinating: worked examples

Each example is a real failure from building Blerg, what it cost, and the rule it produced.
Read the one that matches the moment you're in.

## A card nobody stopped (redesign, design first, card time)

A worker spent about 14 hours over 10 review rounds on a process that keeps terminal shells
alive across a daemon restart. Every round found the same problem in a new place: the new
process could block installs and agent stops. The rule that mattered ("if it fails, only
shells are lost") was written down only at round 8. Its 24-case test matrix was mostly mocked,
so it passed while the real behavior was broken. Nobody looked at elapsed hours; everyone
looked at rounds.

- Write the failure rule and the failure cases before code, and get the design reviewed.
- A second high finding in the same area means redesign, not another patch round.
- Prove the failure cases with real processes; a mocked matrix proves nothing.
- Past about four hours on a card, bring the person the state, the cause and a choice.

## Two things in one card (small increments)

A top-bar change and a new onboarding screen shared one card because the second was asked
for while the first was running. The top bar was done in under an hour and then waited
three more behind the onboarding's review. The person asked why moving a few buttons took
four hours.

- When the person asks for more while a card runs, file a new card.
- Merge and deploy the smallest visible increment, then the next.

## Verified on the wrong page (real-app screenshots)

A worker's first screenshots came from a stripped test page: light theme, the app squeezed
into a corner, no real layout. They looked fine. On the real page, the phone title was cut to
three letters, a menu ran off the screen, and the page scrolled sideways. That cost a whole
rework round.

- Name the real-app screenshot harness in every UI brief.
- Look at every screenshot yourself before it reaches the person.

## A gate that always said yes (check every result)

The gate script printed a per-half result to a file and then exited 0 whatever happened.
The summary said green while one suite had failed.

- Read each half's result, not the summary. A tool that reports success must prove it ran.

## Tests eating the day (test scope, change-based gates)

One worker spent 76% of its time running full suites on every small change. Full gates of
about 72 minutes ran for batches that only touched the frontend.

- Workers run only the tests their change touches until the end, then the region once.
- Gate by what changed; run the full gate for shared code, the daemon and before a cut.
- Read the gate's timing warnings, and file a card for any growth you can't explain.

## A second copy that drifted (one source of truth)

A per-card time total kept its own ledger table, updated by triggers. Two review rounds found
it drifting: a shared session hid a whole card's time, and removing a link never restored it.
Replacing the ledger with one query over data that already existed removed every finding.

- Before adding a copy of data, ask whether one query over the source would do.

## A design that needed the person's GitHub settings (canonical first)

Letting cluster workers push their own branches looked simple, until review showed every
branch rule would block the coordinator too, because both pushed as the same person. Two
more designs (sending history in bundles, a shared cache) grew complicated. The answer was
the standard one: workers clone with a read-only token, and their commits come back as a
file the coordinator imports and verifies.

- Prefer the canonical approach other systems use, and say when a design is getting
  complicated.

## Workers that never woke (verify the platform)

Workers ended their turns saying they would wait for a background test, and nothing ever
resumed them. It happened five times in one night before anyone noticed the pattern.

- Check idle workers directly; a turn ending is not completion.
- When the same failure repeats, file it and add a stopgap rule the same day.

## Overnight work in the person's zone

The next input says “Person's local time: Sun 2026-11-01 01:30 EST
(America/New_York); quiet hours; person likely away.” The fall-back hour has already
changed from EDT to EST. Use the zone in this turn, not the previous offset.

Continue the authorized test fix and collect its evidence. If choosing the next feature
needs the person, batch it with other pending decisions into one `ask --no-wait` in
Needs you. Do not wait on a reply or send repeat reminders overnight. At the morning
handoff, say what completed, what passed, the commits and the decisions still needed.
Write scheduled times with their zone. A destructive step still waits for the person's
explicit decision; use `ask --person --urgent` only if waiting itself would cause harm.

## Inspect worker screenshots

Run `blerg-runner session files <child>`, then
`blerg-runner session download <child> <name|id> -o <path>` and view the downloaded image.
`fetch` downloads the person's attachments, not worker artifacts. Listing files alone
never proves that a screenshot was inspected.

## Optional side conversation

For several exchanges, Main may run `blerg-runner side-channel "Topic" --from <seq> --to <seq> --context "Scope and desired outcome"`. Select relevant events only (maximum 101 events/24 KB); sequences refer to events, not turns. Suggest sparingly; never interrupt a person mid-decision. Only the person starts the channel. A Keep decision ends that suggestion permanently; do not rename it and retry. Continue authorized work while it waits.
A blackboard recommendation is Blerg's own deterministic relay, not judgment: see the planning skill.

## Rules only one engine could read (AGENTS.md first)

A Claude Main set up a new repository's rules in `CLAUDE.md`; the team's Codex workers read `AGENTS.md` and never saw them. Claude Code reads `CLAUDE.md`, while Codex and most other agents read `AGENTS.md`. Put repository rules in `AGENTS.md`, the single source; make `CLAUDE.md` a single `@AGENTS.md` line (Claude Code expands it) plus anything that truly applies to Claude only; never copy the rules into both. When a repository has only one of the files, offer this layout; don't rewrite the person's rules without asking. In worker briefs, say "the repository's agent instructions", not a filename: each engine loads its own.
