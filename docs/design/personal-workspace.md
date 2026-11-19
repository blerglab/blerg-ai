# Personal saved responses and notes (BLG-256)

Status: exploration, 2026-11-01. **No implementation is authorized by this
proposal.** Target: discussion for 0.6.0, not a release commitment. The owner's
2026-10-27 idea starts with keeping a useful technical answer after its session
is gone; general notes follow later.

This document uses the supplied BLG-256 spec. Board access was unavailable,
including after checking access, so BLG-380's blackboard contract and current
card state could not be verified. No dependency is claimed to have landed.
Estimate for this design exploration: M, low risk, medium complexity. Those
estimates are recorded here because first-class board fields were unavailable;
they do not estimate the eventual feature.

## User outcome and first slice

After asking “What do cached and uncached tokens mean in Insights?”, the person
chooses **Save response** on the completed answer. They can find and read that
answer later without reopening, resuming, or retaining the original session.

Proposed first slice: save a completed assistant text response, list saved
responses, search title and body, open, rename, export and delete. One account
owns each saved item. No shared library, tags, freeform note editor, agent access,
automatic summaries, attachment copying, or board publishing in this slice.
“Saved” is a working label: “pin” already means a ticket in the session panel and
an annotation on an image. The owner chooses final wording and placement.

## Snapshot or link

| Choice | Benefit | Cost |
| --- | --- | --- |
| Transcript bookmark | Small; always shows the live source | Cannot meet survival after source deletion without retaining that source |
| Independent snapshot with optional source link | Survives deletion; preserves what was saved | Duplicates text; deletion and export must cover a second copy |
| Editable note immediately | Supports curation | Adds revisions, conflicts and a distinction between original and edited text |

Recommendation for owner approval: an immutable text snapshot with an editable
personal title and a best-effort source link. Save only a completed response;
streaming answers offer the action after completion. Capture the canonical
renderable response body at the server, not browser HTML, hidden reasoning,
tool output, the whole turn, or engine-native history. A chat bubble may combine
events: implementation must first establish a stable response identity and
completion boundary across engines rather than assume one event is one answer.
The save preview must show the exact captured text and disclose omitted media.

Saving the same source response twice returns the existing item; an idempotency
key also handles a retry after a lost response. A later source correction does
not silently change the saved copy. Editing the body would be an explicit
future “Create note from response” operation preserving the snapshot and its
provenance, subject to the owner's decision on revisions.

## Finding and reading

Recommend an account-level **Saved** entry, reachable without a selected session,
with newest-saved-first pagination and title/body search. Ctrl+K may offer
“Open saved responses”; global search integration can follow later. Do not put
personal results into board or repository search. Search and counts must apply
account authorization before returning results or snippets.

The detail view shows the title, saved time, body and **Open source** when the
source remains readable. If it is missing or access has ended, say “Source
unavailable” and keep the snapshot readable. Never resume a session to read it.
Use the existing safe Markdown rendering rules; do not fetch external images,
execute embedded HTML, or automatically resolve file links. A saved response is
text, not a promise to retain the things its links reference. The preview should
say “Linked files and images are not saved.” Preserve inline code and code fences.

At 390 px, the list and detail use separate views with a labeled Back control;
at 3840 px, constrain reading width while allowing a list beside the detail.
Actions need keyboard access, visible focus, accessible labels and focus return
after dialogs. No UI is built or layout acceptance claimed in this exploration.
Tags and folders are deferred until actual retrieval needs justify them.

## Ownership, storage and lifecycle

“Personal workspace” here means account-owned reference data. It is distinct
from a runtime workspace's folders, repositories, shell grants and prepared root
(see [workspace membership](workspace-membership.md)). Saving an answer must not
create a runtime, a repository member, a filesystem mount, or a shell permission.
An account library spans multi-repository sessions and runtime types.

Existing [architecture](../architecture.md) puts account identity in core and
session records and artifact storage in runner. Candidate storage homes are:

| Home | Fit | Decision needed |
| --- | --- | --- |
| Runner account-owned records | Close to transcript authorization and existing UI | Keep lifecycle independent from session cleanup; avoid a second library if blackboard owns it |
| Core account-owned records | Durable account boundary | New content responsibility and authenticated transfer from runner |
| BLG-380 blackboard | Potential common durable content layer | Verify whether private ownership, retention and grants are part of its contract |

Recommendation: define the account-owned saved-item contract first, then select
one authoritative store with BLG-380. Do not build parallel stores or use runtime
files, a hidden board card, or session artifacts as the database. This is a design
dependency on BLG-380, not a claim that blackboard already supports private notes.

A candidate logical record contains an opaque item ID, immutable owner account
ID, kind (`saved_response`), personal title, copied body and format version,
saved timestamp, revision for title updates, and optional source session and
stable response identifiers. Source display metadata must be minimal and included
in privacy/export decisions. Source identifiers must not be mandatory foreign
keys whose deletion cascades to saved content. Final schema, limits, API paths
and migration ownership wait for the storage decision.

Proposed save boundary: authenticate the current person; authorize reading the
source response; validate its completed status and size; copy the exact body;
commit the item durably before acknowledging success. Copy and source deletion
must serialize: either a complete snapshot commits or save reports the source
unavailable. Never acknowledge a record whose body still needs background copying.
Set explicit body/title limits and account quotas before implementation; reject
oversized content clearly rather than truncate silently. Use bounded pagination
and search. Title changes and deletion require conditional revisions to prevent
another device from silently overwriting changes.

Source session stop, discard, hard deletion, runtime cleanup and loss of a daemon
must not delete the saved item. Deleting a saved item must not delete its source.
A source link is always reauthorized on use; owning the snapshot grants no access
to the session. Account deletion must remove saved bodies, metadata and search
entries through the chosen store's account-deletion contract. Backups and restore
must cover the new store and document the backup retention window; immediate
removal from historical backups cannot be promised.

## Adjacent features and boundaries

| Existing or proposed piece | Relationship |
| --- | --- |
| [Published artifacts](../artifacts.md) | Session file delivery and versions; saving text does not copy attachments or adopt artifact retention |
| [History markers](session-rail-history.md) | Session activity and navigation; not a personal reference library |
| Board cards | Shared team memory; a personal save neither creates a card nor inherits board visibility |
| Reply and anchor ideas | Locate context in a conversation or document; a source anchor may help navigation but cannot be the retained body |
| Multi-repository sessions | Provenance may identify the source session; repository membership does not define library ownership |
| BLG-227 IDE direction | Can host a future Saved view; panel placement must not tie retention to an open workspace |
| BLG-380 blackboard | Resolve durable content ownership, authorization, search and deletion before selecting persistence |

No assumed reply/anchor or IDE API is introduced here. Their current contracts
must be checked when implementation is planned.

## Agent access and personal data

Recommend human-only reads and writes in the first slice. Session tokens, child
inheritance, board grants and cron grants must not imply library access. Agents
can suggest that a person save a response without reading the person's library.

A later opt-in “Use in this session” should identify the exact items and disclose
that their text will be sent to the selected model service. Scope the grant to
that session; no children, crons, other accounts or connectors inherit it. Define
revocation and explain that it cannot retract text already delivered. Agent note
creation, if added, needs explicit write authorization and visible provenance;
a private note must never become shared through a board operation implicitly.
Treat all saved content as data, not authority or agent instructions.

The first slice stores and searches within the Blerg install and sends no content
to model, embedding, analytics or connector services. Do not log response bodies,
titles or search text. Provide single-item and whole-library export as Markdown
plus machine-readable metadata, with an explicit download action. Export only
the person's authorized items, without following links or packaging session files.
Delete needs a clear confirmation describing removal of the independent copy;
account deletion and export must include this data from the first release.
Whether an administrative operator can inspect stored content follows the
installation's documented storage controls; “personal” must not imply encryption
from the operator or protection against an already authorized host process.

## Owner decisions and stop condition

The following are unresolved product choices, with recommendations rather than
recorded owner approvals:

1. Accept independent snapshots, with session deletion leaving copies intact?
   Recommend yes; explain that behavior in save and session deletion flows.
2. Preserve bodies unchanged or allow editing immediately? Recommend unchanged
   bodies and title edits first; notes and revision history later.
3. Choose “Saved” or “Pins”, navigation placement and search scope. Recommend
   Saved at account level, with local title/body search and a Ctrl+K entry.
4. Confirm per-account ownership and no team sharing in the first slice.
   Define whether saving responses from shared sessions is permitted and how
   later source access revocation affects a previously authorized snapshot.
5. Confirm no agent access initially; decide explicit context grants and writes
   separately. “Never sent to other services” conflicts with model context unless
   the person explicitly chooses and understands that transfer.
6. Agree quotas, title/body limits, deletion confirmation and retention disclosure;
   confirm export format and account deletion behavior before shipping.
7. With BLG-380's owner, choose the authoritative store, service authorization,
   backup/deletion integration and private search contract. With BLG-227's owner,
   confirm the library is reachable outside any one runtime workspace.

**Stop here.** There are both owner choices and an unverified BLG-380 dependency.
Do not add migrations, endpoints, UI, agent tools or a placeholder store. No new
implementation tickets are filed by this exploration. Once the choices are
approved, plan a single end-to-end saved-response slice; leave notes, sharing
and agent context as separately scoped work with explicit dependencies.

## Acceptance evidence for a future implementation

Before building, turn the approved choices into failing tests. Cover save, retry
and concurrent save/delete through the authenticated public API and UI; verify
the response identity and completion boundary for every engine. Then prove saved
text survives source hard deletion and runtime cleanup, retains exact code/text,
and remains readable with a missing or newly unauthorized source.

Exercise cross-account list/search/read/update/delete/export refusal, session and
child token denial, account erasure, pagination and quota boundaries, title edit
conflicts, malicious Markdown and link handling. Restore a backup containing saved
items and verify their bodies and ownership. Test the complete save/search/read/
export/delete flow in the browser, including keyboard use, 390 px and 3840 px.
Use touched package tests and bounded builds for implementation rounds; Main owns
the integrated gate. This documentation change requires scrub and link checks,
not Go builds, frontend tests or browser suites.
