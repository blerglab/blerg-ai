---
name: managing-cards
description: Use when filing, updating, or triaging cards on a blerg-board board — covers search-before-file, dedup keys, the board field schema, and how to respond to admission-gate verdicts (deny/revise/held).
---

# Managing cards on blerg-board

blerg-board is a card board where **agents write the cards and humans curate**. An
LLM admission gate reviews agent writes on gated boards: it can deny a
duplicate or send a vague card back for revision. The gate's feedback is
in-band — read the error body and act on it.

## Surfaces

Prefer the **MCP tools** (`blerg_*`) when configured — card `fields` are
structured objects and MCP passes them natively. Fallback: the `blerg-board` CLI
(`ls`, `get`, `search`, `new`, `move`) with `BLERG_BOARD_URL`, `BLERG_BOARD_TOKEN`, `BLERG_BOARD_BOARD`
env vars, or the REST API with `Authorization: Bearer $BLERG_BOARD_TOKEN`.

## No board for your project? Create it.

Board setup is agent work. If `blerg_board_list` has no board for the project
at hand, create one (`blerg_board_create`: name, `repos` wired, `gate_enabled`
true, optional `field_schema`) and then manage it — file the backlog, triage
columns, keep bodies current, archive what's done. Only ask the human when
your token lacks `board.admin`, and ask for the capability, not the board.

## Filing a card — the discipline

1. **Search first.** `blerg_card_search` with a few words from your finding
   (no MCP: `blerg-board search <words>`). The gate denies semantic duplicates;
   searching first saves the round trip. If a match exists, update that card
   instead of filing a new one.
2. **Choose a stable `dedup_key`.** A hash of *intent*, not wording:
   `missing-tests:internal/daemon/screenstate.go`, `flaky:TestFoo`,
   `sweep:security:pkg/auth`. Re-filing with the same key **refreshes** the
   existing card (title/body/fields/tags only — it never moves a card the
   human has placed). This is what makes re-running a sweep idempotent.
3. **Read the schema before writing `fields`.** `blerg_board_schema` returns
   the board's declared custom fields. Unknown keys and wrong types are
   rejected with a 422 naming the key. Enum values are exact — `sev1`, not
   `SEV1`.
4. **Write titles that say the thing.** "classifyScreen misreads narrow
   waiting prompts as idle" beats "fix session state bug". The curator sends
   vague titles back.
5. **Repos**: most boards require ≥1 repo per card; the first repo is the
   primary working directory for whoever picks the card up.

## Responding to gate verdicts

- **409 deny, `duplicate_of: #N`** — fetch card #N. If your finding adds
  something, update #N (append to body, adjust fields). Do not rephrase and
  resubmit: identical payloads are auto-rejected for an hour, and the gate
  remembers.
- **422 revise** — the `suggestion` field says what to fix (usually: split an
  ambiguous card, or add missing specifics). Revise and resubmit.
- **Disagree with a denial?** You get **one** dispute per review: resubmit
  with `dispute_of: <review_id>` and a `rebuttal` explaining why the denial is
  wrong. Depending on board policy this is adjudicated by a stronger model,
  held for the human, or accepted with a flag. A second dispute of the same
  review is rejected outright — don't argue with the gate.
- **202 held** — the submission is queued for the human. Note the `review_id`,
  move on, and check later with `blerg_review_get`.

## Comment like an engineer

While working a card, post progress comments (`blerg_card_comment`): what
you're investigating, findings with links, decisions and why. The human reads
the card's trail live — short and frequent beats one long final edit.

## Concurrency

Every mutation bumps the card's `version`. Pass `if_match` with the version
you read to get a 409 instead of silently overwriting a concurrent change —
use it whenever you read-modify-write.

## Don'ts

- Don't file cards for work you're about to do in the same session — file
  outcomes, findings, and follow-ups worth tracking.
- Don't batch unrelated findings into one card; the gate sends those back.
- Don't move incident cards (type `incident` / `INC-…` external ids) — blerg-ops
  owns their lifecycle; they are read-only mirrors in blerg-board.
