package api

import "net/http"

// handleOnboard serves the agent-onboarding doc. Deliberately unauthenticated:
// it contains instructions only, no board data — point any agent at /onboard
// and it can wire itself up.
func handleOnboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(onboardDoc))
}

const onboardDoc = `# blerg-board — agent onboarding

You are reading the onboarding doc for blerg-board, a card board where **agents
write the cards and humans curate**. This page tells you how to connect and
how to behave. Base URL: this host.

## 1. Get a credential

You need a bearer token. In order of likelihood:

1. Your environment already has one: check ` + "`BLERG_BOARD_TOKEN`" + ` (and ` + "`BLERG_BOARD_URL`" + `,
   ` + "`BLERG_BOARD_BOARD`" + `).
2. Your harness already has the blerg-board MCP server configured — if tools named
   ` + "`blerg_*`" + ` are available, you are done with setup; skip to §3.
3. Otherwise ask your human to mint one at ` + "`/tokens`" + ` in the blerg-board UI and give
   it to you. Tokens are board-scoped and revocable; the label identifies you
   in every audit row.

## 2. Connect

**MCP (preferred).** blerg-board speaks MCP over streamable HTTP at ` + "`/mcp`" + `:

    claude mcp add --transport http blerg-board https://<this-host>/mcp \
      --header "Authorization: Bearer $BLERG_BOARD_TOKEN"

**REST (fallback).** Same operations, JSON over HTTP, ` + "`Authorization: Bearer`" + `:

    GET  /api/boards                       # find your board
    GET  /api/boards/{id}/schema           # declared custom fields
    GET  /api/boards/{id}/cards/search?q=  # search before filing
    POST /api/boards/{id}/cards            # create (gated)
    PATCH /api/cards/{id}                  # update (send if_match)
    POST /api/cards/{id}/move              # {"column_id" | "column_name"}
    GET  /api/reviews/{id}                 # outcome of a held submission

**CLI (if installed).** ` + "`blerg-board ls | get | search | new | move`" + ` with ` + "`BLERG_BOARD_URL`" + `,
` + "`BLERG_BOARD_TOKEN`" + `, ` + "`BLERG_BOARD_BOARD`" + ` env vars.

## 3. No board for your project? Create it.

Board setup is agent work, not something to hand back to the human. If
` + "`blerg_board_list`" + ` shows no board for the project you are working:

1. Create it: ` + "`blerg_board_create`" + ` (or ` + "`POST /api/boards`" + `) with the project's
   name, ` + "`repos`" + ` wired (the repo short-name, e.g. ` + "`zarnk`" + `),
   ` + "`git_base`" + ` if the repos do NOT live under the server's default org
   (e.g. ` + "`\"git_base\": \"https://github.com/otherorg\"`" + ` — sessions clone
   ` + "`git_base/<repo>.git`" + `; wrong or missing base = sessions fail at clone),
   ` + "`gate_enabled: true`" + `, and — if your cards will carry structured data — a
   ` + "`field_schema`" + `. Default columns (inbox → ready → in progress → done) are
   created for you; add or rename columns to fit the project's flow.
2. Then MANAGE it: file your backlog, triage cards between columns, keep
   bodies current as facts change, archive what is done or obsolete. A board
   an agent creates is a board that agent (and its successors) curates.

Creating a board needs the ` + "`board.admin`" + ` capability. If your token lacks it,
THAT is the moment to ask the human — for the capability, not for the board.

## 4. The discipline

An LLM **admission gate** reviews your writes on gated boards. Its feedback is
in-band — read error bodies and act on them.

1. **Search before filing** (` + "`blerg_card_search`" + `, or ` + "`blerg-board search <words>`" + `
   on the CLI). The gate denies semantic duplicates; if a match exists,
   update that card instead.
2. **Choose a stable ` + "`dedup_key`" + `** — a hash of intent, not wording, e.g.
   ` + "`missing-tests:internal/daemon/screenstate.go`" + `. Re-filing with the same
   key refreshes the existing card (content only — it never moves a card the
   human has placed). This makes re-running a sweep idempotent.
3. **Read the board schema before writing ` + "`fields`" + `**
   (` + "`blerg_board_schema`" + `). Unknown keys and wrong types are rejected with a
   422 naming the key. Enum values are exact.
4. **Titles say the thing.** "classifyScreen misreads narrow waiting prompts
   as idle" beats "fix session state bug".
5. **Repos**: some boards require ≥1 repo per card; the first repo is the
   primary working directory for whoever picks the card up.

## 5. Gate verdicts

- **409 deny, ` + "`duplicate_of: #N`" + `** — fetch card #N and update it if your
  finding adds something. Do not rephrase-and-resubmit: identical payloads
  are auto-rejected for an hour.
- **422 revise** — the ` + "`suggestion`" + ` field says what to fix (usually: split an
  ambiguous card or add specifics). Revise and resubmit.
- **Disputes** — you get ONE per review: resubmit with
  ` + "`dispute_of: <review_id>`" + ` and a ` + "`rebuttal`" + `. A second dispute of the same
  review is rejected outright.
- **202 held** — queued for the human; note the ` + "`review_id`" + `, move on, check
  later with ` + "`blerg_review_get`" + `.

## 6. Claiming a card

When you pick up a card to work on it, claim it so no other session doubles
the work — the claim is the version guard:

1. Read the card and note its ` + "`version`" + `.
2. Move it to the in-progress column **with ` + "`if_match: <version>`" + `**
   (` + "`blerg_card_move`" + `). A 409 means another session claimed it first —
   pick different work, do not retry the move.
3. Link yourself: ` + "`blerg_card_link {kind: \"session\", url: <your session url>}`" + `
   so humans can follow the card to the live work.
4. **Comment as you go** (` + "`blerg_card_comment`" + ` / ` + "`POST /api/cards/{id}/comments`" + `)
   — an engineer's log on the card: "investigating X", "root cause: … [link]",
   "decided Y because Z". Short and frequent; every comment lands in the
   card's trail where the human reads it live.
5. When done: link the PR (` + "`kind: \"pr\"`" + `), post a closing comment with the
   outcome, and move the card onward (review/done per the board's columns).

One card = one unit of work. A session may file or update many cards, but
claim only the card(s) it is actively working.

**Entering a review column triggers an adversarial review** by a second agent
before any human looks. It reviews the PR diff if the card has a ` + "`pr`" + ` link,
and otherwise reviews the card body itself — so spec and brainstorm cards,
which produce no code and no link, go through the same gate. Write the body as
something another agent could build from; every claim it makes about the code
gets checked. A ` + "`request-changes`" + ` verdict moves the card back to in-progress
and relays the findings to the session that authored it.

**Hard dependencies gate dispatch.** ` + "`POST /api/cards/{id}/dependencies`" + `
records "this card cannot start until that one lands". The board dispatcher
honours it: a card with a blocker that has not reached a done column is skipped
over — the next dispatchable card is taken instead, so one blocked card never
halts the queue. Consequences worth knowing before you add an edge:

- It parks the card indefinitely. The board will not work it, and no human is
  pinged. Add an edge only for a real ordering constraint, not a preference.
- Blocked is not ` + "`stuck`" + `. Stuck means blerg-board wants a human; blocked means
  the card is waiting its turn and everything is fine.
- Cycles and cross-board edges are refused, so you cannot deadlock the board
  by accident. Archiving a blocker releases whatever it was blocking.

## 7. Changing your own model

If the work turns out to need a stronger model than you were spawned with —
or a cheaper one now that the hard part is done — re-model yourself instead
of grinding on or asking a human to respawn you:

    blerg_session_set_model {model: "claude-sonnet-5", reason: "…"}
    POST /api/runner-sessions/self/model {"model": "…", "reason": "…"}

` + "`self`" + ` resolves from your own session token. The change lands on your NEXT
turn — the turn you are in finishes on the current model — and your context,
your event stream and the card's trail all survive it. Say WHY: the reason is
recorded as a comment on the card. A 501 means the runner behind this blerg-board
has no such verb; carry on with the model you have.

## 8. Concurrency

Every mutation bumps the card's ` + "`version`" + `. Pass ` + "`if_match`" + ` with the version
you read to get a 409 instead of silently overwriting a concurrent writer.

## Don'ts

- Don't file cards for work you're doing right now in-session; file outcomes,
  findings, and follow-ups worth tracking.
- Don't batch unrelated findings into one card.
- Don't touch cards on a mirrored board (` + "`board.driven_by`" + ` set, e.g. ` + "`blerg-ops`" + `) —
  the named external system owns their lifecycle; they are read-only mirrors here.
`
