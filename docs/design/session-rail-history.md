# Session rail, History tab: the highlight reel — design (v1)

**Status:** v1, 2026-10-15, reworked after an adversarial review (3 blockers, 17 should-fix, 12
minor; §8); the heuristic topics were replaced by one unmarked summary the same day (R7). Cut from
`session-rail-live`, whose panel, `model/subagents.ts` and `scrollToEvent` it uses.

## 1. The job

A long session does several pieces of work ("build x", "fix y"). Their results are scattered over
the transcript, Files and subagent cards. The History tab lists the ones the agent marked as
**topics**, newest first. Each topic expands to where it started, its documents with every version,
its subagents and its outcome. The work no topic holds is one summary line after them (R7).

**v1:** the `topic` event (routes, CLI, agent instruction), a Go reducer, and a History tab that
refetches. **F1** commits on the Done row, **F2** feedback rows and **F3** the Files and Skills tabs
follow as separate branches before v0.2.0 (shapes in Appendix A). A jump route and a client-side
fold are cut (R6).

## 2. Rulings

- **Owner:** a marker plus a heuristic, and no model judges. Per session. A tabbed inspector (Live ·
  History · Files · Skills), with Insights kept as a modal. On a phone, a drawer.
- **Controller, earlier:** (1) a main-agent commit is a work signal, moved to F1 by R5. (2) Mocks
  are html and images; a PDF is a document. (3) The server does not refuse `topic` from a subagent.
  (4) `push` stays, in F1.
- **R1:** v1 is the cut in §1: refetch only (no TS reducer, no client fallback), an older server
  detected by a non-JSON reply, "Show in conversation" only for held seqs. **R2:** F1–F3 are
  separate branches before v0.2.0. **R3:** `is_error` goes on `subagent_done`; no JSONB scan.
  **R4:** the managed block is gated, so the `topic` line also goes into the session guide. **R5:**
  the main-agent commit as a work signal moves to F1.
- **R6 (owner, after the final review):** the F3 jump route is cut and the client-side fold is
  dropped: "Show in conversation" appears only where the row is loaded, and the refetch keeps the
  tab current. F1 is the cheap version: `blerg-runner topic --done --commit <sha>…` carries the
  commits on the `done` event and the Done row shows them as chips; no output parsing, no `commit`
  or `push` events. F3 is the Files · Skills tabs only. R6 supersedes controller rulings 1 and 4
  and R5: a commit is not a work signal.
- **R7 (owner, 2026-10-15, on a real 3,000-event session with no markers):** "History looks like
  trash, just looks like noise". The heuristic made 31 topics there, 20 of them one subagent titled
  by an unrelated remark ("ok, restarted daemon"): in an interactive session work interleaves and
  most messages are asides, so "a person message followed by a subagent launch" cannot recover its
  structure. Markers are the product: only a marker makes a topic. Everything outside a marker
  window is folded into one **unmarked** summary (§3.2), drawn as one collapsed line. The heuristic
  openers, the `auto` tag and "Untitled work" are gone; the person-message checks stay for a
  marker's request. R7 supersedes the owner's first ruling's "plus a heuristic".

## 3. Topic boundaries

### 3.1 The explicit marker

`blerg-runner topic "<title>"` starts a topic and ends the open one. `blerg-runner topic --done
["<outcome>"]` closes the open topic. The CLI
(`runner/.claude/skills/session-messaging/blerg-runner`) prints `Topic started: …` (with `(closed:
…)` when a topic was open) or `Topic done: …`. It refuses a title together with `--done`.

**Route.** `POST /api/sessions/{id}/topics` with `{"action":"start","title"}` or
`{"action":"done","outcome"}`; auth `authMessaging` as for `publish` (a session-bound token or the
daemon token). It replies with the payload, plus `closed: {id, title}|null` on a start; `409` for
`done` with nothing open, or an ended session.

**Ordering.** In one transaction the handler takes `pg_advisory_xact_lock(hashtext('topics:'||id))`,
reads the latest `topic`, and appends exactly one event with a new `db.AppendAgentEventTx`. It
commits, then fans out as `recordServerAgentEvent` does: the artifact insert's pattern, chosen over
an in-process mutex because it holds across runner processes. Any daemon works. A `start` over an
open topic is itself the close, so no extra `done` is written. The reducer tolerates `start, start`
and a stray `done`. **Text:** the handler strips C0/C1, bidi (U+202A–E, U+2066–9) and zero-width
(U+200B–F, U+FEFF) characters, and collapses whitespace. A title is 1–80 runes, cut with `…`; an
outcome is 0–200 runes. A session holds at most 200 topic events.

**Telling the agent (R4).** `ccSessionGuide` (`daemon/claudecode.go`) already introduces `publish`.
It is appended to every unrestricted Claude Code turn, on the desktop and in the cluster, and gains
this line:

> When you start a piece of work the user asked for that takes several steps, run
> `blerg-runner topic "<short imperative title>"`; when it is finished or dropped,
> `blerg-runner topic --done "<one-line outcome>"`, and when the work landed commits, pass them:
> `--commit <sha>`. One topic at a time; not for a quick question, and not from a subagent.

The same line goes into `blerg-runner --help`, `AGENTS.md` ("Inside a session") and the managed
block in `provision.go`. The block is secondary: it is written only on a desktop with
`BLERG_RUNNER_PROVISION_CLAUDE_MD=true` (off by default), never in a pod; there the bullet adds
"Without markers the History tab shows only a summary." (R7). Restricted turns (crons, MCP sessions)
have no shell, so all their work is unmarked.

### 3.2 Requests and unmarked work

**Person message:** a `user_message` that passes all four checks (a marker may take it as its
request):

1. It is drawn as the person's (`userMessageShape(...).as === 'person'`): no harness text that old
   pods recorded as `chat` (`<task-notification>`, `<system-reminder>`, …), no restart note. Go
   reads the markers from `topics/testdata/harness_markers.json`; a chat test checks `isHarnessText`
   against it.
2. Its source is `chat` or `human`. `runner` and `blerg-board` count only on the first message (the
   start prompt or a card's task), since Insights excludes `runner` too.
3. It is not a `harnessCommand` (`/model`, `/effort`).
4. It is not feedback. Feedback is a message whose whole first line matches the chat's header
   (`model/feedback.ts`): `Review of <name>[ (v<N>)]: <n> request(s)[, 1 edit].`, `Marked up <name>:
   <n> pin(s).` or `Marked up <name> (see the attached image).`, with anything after it past a
   blank line. A person's "Review of the API: 3 requests came in" is not feedback.

The reel is built in this order:

1. **Marker windows.** A `topic start` opens a window that ends at its `done`, the next `start`, or
   session end. The window's start moves back to the person message just before the marker, if no
   other topic event and no turn end (a `turn_done`, or a change to `idle` or `waiting`) lies in
   between; otherwise the marker is the request.
2. **Unmarked work (R7).** Every document and subagent no window holds is folded into one summary,
   `unmarked`: the subagent count and rows, each file once at its newest version (`in_topic` on a
   version: published outside every window), and `first_seq`/`last_seq`/`started_at`/`ended_at`
   of those rows. It has no title, request, status or outcome, and is `null` when nothing is
   outside a window. No message opens a topic: no opener, no work signal, no `h-<seq>` id.
3. **Ids and status.** Ids are the marker's. Status is `open`, `done` (closed by `--done`), or
   `ended` (cut by the next topic or by session end). `ended_at` is that boundary.

## 4. What a topic collects

Each item joins the window that holds its anchor `seq`; a document or subagent no window holds goes
to `unmarked` (§3.2), with the same row shapes.

- **Request and done rows.** The request is the opening person message, or else the marker. The done
  row carries the `topic done` outcome.
- **Documents.** An agent-origin artifact name (not `.review.json`), shown once per topic that
  published a version of it, at that version, with its versions: `in_topic` if published here,
  `deleted` once its row is gone (a hard `DELETE`, no event). A row lists the name's newest 20
  versions plus every version its topic published; `versions_total` is the name's full count. A mock (`view` `html` or `image`) gets
  a thumbnail, or an icon for html.
- **Subagents.** One row per `subagent_started`: title (`summary`, the Agent call's `description`),
  `agent_type`, model, usage and `tool_uses` from the newest update or done, `duration_ms`.
- **Subagent status.** A plain done is `error` if it has `is_error` (R3) or an `error:` summary,
  otherwise `done`. While the session lives, a background launch with no plain done after it is
  `background`, and no done at all is `running`. Anything else is `stopped`.
- **Tokens** follow the Insights ruling, not the chat card. For anyone but the owner or an admin,
  `usage` is null and `tokens_hidden` is true.

## 5. Server

**Event.** A `topic` event has the payload `{"id":"tp_5f1c2a9e","title":"…","action":"start"}`. A
done adds `"action":"done"`, an optional `"outcome"` and optional `"commits": [{"sha": "1a2b3c4"}]`
(F1: the agent's `--commit`; on a done only, each sha 7 to 40 hex characters, lower-cased,
deduplicated in order, at most 50, else `400`). `agent.SubagentPayload` gains `IsError bool
json:"is_error,omitempty"`. `endSubagent` sets it from the Agent call's result, and a background
notice sets it when its status is `failed` (`killed` and `stopped` mean a person stopped it, so not
an error).

**`GET /api/sessions/{id}/topics`.** Access matches `/insights` (`canSeeAccount`): an agent token
gets `403`, and an unknown or private session gets the uniform `404`. The `ETag` is
`"<through_seq>.<artifact count>.<newest created_at>.<live|end>.<ended_at>.<repo>.<t|h>"` (`live|end`, not the raw status, so a turn's running/idle/waiting keeps the tag; `<repo>` a hash of the repository the commit links point at, so learning it busts the tag). The last field is
the viewer class, so session end and visibility changes bust it. Three cheap reads come first:
`SELECT max(seq)` on `agent_events_session_seq_idx`; `SELECT count(*), max(created_at)` on
`session_artifacts_session_created_idx` (index-only, not `ListArtifacts`); and the session row by
primary key. `If-None-Match` gets `304`.

```json
{"session_id": "…", "through_seq": 1834, "generated_at": "…", "tokens_hidden": false,
 "unmarked": {"agents": 42, "first_seq": 3, "last_seq": 405, "started_at": "…", "ended_at": "…",
   "documents": [{"type": "document", "seq": 120, "…": "a topic's document row"}],
   "subagents": [{"type": "agent", "seq": 90, "…": "a topic's agent row"}]},
 "topics": [{"id": "tp_5f1c2a9e", "source": "marker", "title": "Build the History tab", "status": "done",
   "outcome": "Shipped on main", "start_seq": 410, "end_seq": 1290, "started_at": "…", "ended_at": "…",
   "counts": {"documents": 2, "agents": 3, "commits": 2}, "items": [
    {"type": "request", "seq": 410, "event_id": "…", "ts": "…", "text": "Build the History tab per the spec"},
    {"type": "document", "seq": 455, "event_id": "…", "ts": "…", "name": "plan.md", "view": "markdown", "mock": false,
     "latest_version": 3, "versions_total": 3, "versions": [{"version": 1, "artifact_id": "…", "created_at": "…", "in_topic": true, "deleted": false}]},
    {"type": "agent", "seq": 520, "event_id": "…", "ts": "…", "call_id": "toolu_…", "title": "Implement task 3",
     "agent_type": "general-purpose", "model": "claude-…", "usage": {"…": "agent.Usage"}, "tool_uses": 37,
     "duration_ms": 412000, "status": "done"},
    {"type": "done", "seq": 1290, "event_id": "…", "ts": "…", "outcome": "Shipped on main",
     "commits": [{"sha": "1a2b3c4", "url": "https://github.com/example-org/example-repo/commit/1a2b3c4"},
                 {"sha": "9f8e7d6", "url": "https://github.com/example-org/example-repo/commit/9f8e7d6"}]}]}]}
```

`topics` holds marker topics only (`source` is always `marker`), newest first; `unmarked` is the
summary of §3.2 or `null` (it replaces v1's `untitled`); `counts` omits zeros. A done's `commits`
are absent when it named none; each `url` is the commit's page
through the provider registry (`gitprovider.Registry.CommitURL`) when the session's repository is
known: the one its clone URL (`sessions.git_url`) names, else the origin its daemon reports for its
folder while connected. Otherwise `url` is `null`; nothing is guessed from the name alone.

**Storage.** No new table. Migration `041` adds a partial index, as 034 did: `agent_events_reel_idx
(session_id, seq)` where kind is `user_message`, `topic`, `artifact`, `subagent_started`,
`subagent_update`, `subagent_done`, `turn_done` or `status_changed` (F1 adds no kind: its commits
ride on `topic`).
Without it a session scan reads every event's heap tuple. Bar: 100k seeded events, an index plan, a
build under 200 ms.

**Privacy.** Only what the session page already shows the same people; tokens per Insights; no
costs. **Compatibility.** An older daemon has no `is_error` (a failed subagent may read `done`) and
no guide line; its sessions, an older CLI's and the other engines' have unmarked work only, as do
old sessions.

## 6. Client

**Tabs (the smallest change).** `SessionRail` gains a `role="tablist"` row, Live · History. Live
keeps today's three sections. History renders `<HistoryTab>` in place of the placeholder section.
The tab persists beside the open pref (`readRailTab`/`writeRailTab` in `lib/sessionRail.ts`). The
header badge still counts from Live. Files and Skills stay as links in Session until F3.

**Fetching.** `useTopics` (the runner's API client) works only while History is visible and the rail
is open. It fetches each time History becomes visible, on `visibilitychange` and window focus, and
when the Files list changes (`useArtifacts` refetches it; deletions have no event). It also fetches
when the transcript store (`useTranscriptStore`) receives a `topic`, `subagent_started`,
`subagent_done`, `artifact` or `turn_done` event. `subagent_update` is not one: usage shows at the
next done. Every fetch sends `If-None-Match`, with one request at a time, starts at least 5 s apart,
and a trailing fetch. "No route" is a reply that is not `application/json` (an older server's SPA
fallback: `index.html`, status 200) or a 404/405. The tab then says "History needs a newer runner
server".

**List.** Cards are the marker topics, newest first, the open topic on top with a "now" dot. A card
shows the title, the outcome, the span ("10:42–11:30 · 48 min", with the date when not today), and
`2 docs · 3 agents`. Zero counts are never shown, which also hides the F1/F2 counts on old daemons.
After the cards, one compact line, collapsed: "Unmarked work · 42 agents · 8 files" (zeros left
out), only when `unmarked` is set. It opens onto the file rows (name and version chips, as in a
card) and one line per subagent (status mark, title, model, tokens); no request rows, no "Show in
conversation". Empty state, above that line when there is one: "No topics yet. The agent marks a
topic when it starts a piece of work: `blerg-runner topic "<title>"`."

**Expanded.** A timeline in `seq` order: "You: …"; each document with its icon or thumbnail, name
and version chips (filled in-topic, outlined otherwise, struck and disabled when deleted; each a
link to `?artifact=<id>`); the live rail's subagent row for each agent; "Done: …".

**Show in conversation** appears only when the anchor is held (`seq >= firstSeq`). It calls
`scrollToEvent(event_id)`, or `scrollToCall(call_id)` for an agent. Enablement derives from the
store each render (it can reset); the rail's 4 s "not found" state is reused. An older row shows its
time without a link (tooltip: "Earlier than what is loaded"). There is no jump route that loads the
transcript around a row and no client-side fold of the reel (R6): the refetch keeps the tab current. The chat package adds `data-event-id`
to person bubbles and artifact cards. It adds `topic` to `CARD_KINDS` (`toolGroups.ts`) with a card
in `renderEvent`, drawn as a muted divider ("▸ Build the History tab" / "✓ Done: Shipped on main").
That needs a version bump.

**Mobile.** The same tabs in the drawer, full-width cards, wrapping chips; "Show in conversation"
closes the drawer first. **Non-goals:** a cross-session reel, LLM titles, hand-edited topics (a
later "Rename topic" could add `action: "rename"`), topics over the v1 contract, background-Bash
commits.

## 7. Tests

- **Go reducer** fixtures: markers, marker absorbing its request, each message that is no request
  (harness text, restart note, `ask_answer`, `/model`, feedback shapes, later `runner`, an earlier
  turn), `start, start`, stray `done`, session end, a session with no markers (no topic, one
  unmarked summary), work around a marker, a two-topic document, a deleted version, each subagent
  status.
- **Server:** `GET` access (owner, shared viewer, other's private, agent token, unknown id),
  `tokens_hidden`, `ETag`/`304` incl. session end; `POST` binding, `409`s, sanitizer, cap, two
  concurrent starts; the 100k plan and timing.
- **Daemon:** `is_error`, both sources. **CLI** (`test_blerg_runner.py`): start, done ± outcome, a
  title with `--done` refused. **UI:** tab persistence; fetching only while visible; every trigger
  and the throttle; the HTML/404 note; zero counts; chip links; held-only links; the drawer; the
  divider.
- **Docs:** "History" in `talking-to-an-agent.md`; `topic` in `artifacts.md` and `AGENTS.md`;
  CHANGELOG; the chat version bump.

## 8. Review findings

- **Applied.** B1–B3, S12, N3, m1, m3: §6. S1–S3, S5's opener filter, m4: §3.2. S9, S14, S15, S10,
  N2, m2, m5: §4–§5. S11, S16, N1: §3.1. S17: §1. M1–M7, M9, M10, M12: inline.
- **Dropped by R6:** S6, S7, S8 (parsing commits and pushes from tool output) and controller
  rulings 1 and 4, since F1 takes the commits from the agent's `--done --commit`; B2 (a TS fold over
  the full state), B3 (`loadAround` and a non-contiguous store) and S13 (a reveal store with
  backfill), since the jump route and the client-side fold are cut.
- **F1, done (cheap version, owner 2026-10-15):** `topic --done --commit <sha>…` puts `commits` on the
  `done` event; the reel's done row carries `commits: [{sha, url}]` (§5) and counts them; the Done row
  shows each as a 7-character mono chip (full sha as its title), a link when `url` is set, else a
  button that copies the sha. No output parsing, no repo guessing, no push events.
- **R7, done (owner 2026-10-15):** the heuristic topics are replaced by the unmarked summary (§2,
  §3.2, §6); S1–S3 and S5's opener filter now apply only to a marker's request. On the session that
  prompted it the reel goes from 31 heuristic topics to none, with one unmarked line of 69 agents
  and 4 files.
- **Deferred to F2:** S4 (join by order, then by clock with 60 s slack); S5's feedback rows.
  **Deferred to F3:** S12 (Files and Skills tabs).

## Appendix A: follow-up shapes (reserved)

```json
{"kind": "topic", "payload": {"id": "tp_1a2b3c4d", "title": "Build the History tab", "action": "done", "outcome": "Shipped on main", "commits": [{"sha": "1a2b3c4"}, {"sha": "9f8e7d6"}]}}
{"type": "feedback", "kind": "review", "seq": 610, "event_id": "…", "ts": "…", "file": "plan.md", "file_version": 2, "artifact_id": "…", "requests": 5, "open": 1, "done": 3, "declined": 1}
```

- **F1, commits (R6), shipped.** `blerg-runner topic --done "<outcome>" --commit <sha> [--commit <sha>]…`
  adds `commits` to the `done` event's payload (hex shas, checked and capped by the server). The
  reel's `done` item carries them with their links (§5) and the Done row shows each as a chip. Nothing is
  parsed from tool output, there are no `commit` or `push` events, and a commit is not a work
  signal.
- **F2, feedback.** A `markup` item carries `pins`. Documents gain `review: {submits, requests,
  open, done, declined}`, and topics gain `counts.feedback`.
- **F3, Files · Skills.** The panel's Files and Skills tabs (Live · History · Files · Skills), in
  place of the links in Session. No reply change: no jump route and no live fold (R6).
