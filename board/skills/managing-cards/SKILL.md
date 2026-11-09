---
name: managing-cards
description: File, estimate, start and review Blerg Board cards using MCP, REST or the session CLI.
---

# Manage tickets consistently

These rules apply to every workspace, channel, engine and worker.

Workers reach Main with `blerg-runner ask` for decisions and `blerg-runner note` for information. Plain chat replies mid-turn do not notify Main. Main reads notes with `blerg-runner session result <child>`; use `publish` for long content.

1. Read the board and search before filing. Prefer board MCP tools when connected; inside a session use `blerg-runner ticket get/list/create/update/move`. Check `blerg-runner board access` after a refusal: permissions can change while you work.
2. Before filing or starting, estimate: size S/M/L/XL, risk low/medium/high, and complexity low/medium/high. Use `ticket create/update --size M --risk medium --complexity low`, or the same top-level MCP fields. Do this before moving into progress or starting a worker. Read existing estimates; explain revisions. Fill missing estimates before starting. The service records first-set times; never backdate them.
3. One idea per card, with a specific title, intent, acceptance criteria and workspace repos. Search first, use a stable intent-based dedup key when available, and update an existing match. A dedup refresh preserves estimates and placement: use update to change them.
4. Custom fields are separate: read the board schema before writing `fields`. Size, risk and complexity need no schema or board setup. Pass the version you read as `if_match` on a read-modify-write.
5. Record dependencies instead of relying on card order. Pin the card you work on and name its #N in worker titles using the correct board context. Move completed work to a terminal column. Do not archive, split, or remove another person's work without authorization.
6. Record every completed review, including from a worker: `blerg-runner mark review "<summary>" --card N --verdict approved|changes|rejected --by <engine> --target <sha> --round N`. Add `--findings high,medium,low` with one entry per finding, preserving repeated severities; omit it for no findings. Use the actual reviewed commit and engine, and increment the round for another review of that work. Set the board context; card numbers alone are ambiguous across boards. Record the review even if it approves with no findings. Historical reviews may be imported with `mark --import` and their actual `at` time.
7. Record a failure when a test, gate, flaky rerun, review, session, engine, deploy or merge fails: `blerg-runner mark failure "<summary>" --card N --type test|gate|flake|review|session|engine|deploy|merge --severity high|medium|low --suite "<name>"`. Record the failed attempt immediately; after a successful rerun record another failure mark for the same suite with `--rerun-passed`. Do not erase the failed attempt or call a failure a flake without rerun evidence. Review finding severities stay on review records. Server-recorded session failure alerts link automatically through pins and #N titles; do not duplicate those alerts. Use real times for historical imports.
8. Reconcile the board after every merge and deploy, not from memory: list open cards (in progress, ready) and check each against first-parent merges on main and the deployed SHA. Move merged work to done, add `to-test` once deployed, and return stalled in-progress cards with no worker to ready. Workers send questions, decisions and work for review to Main with `blerg-runner ask`, never `--person`, `review request` or `--request-review`. Main reviews and asks the person only for their decisions, in plain words. Merge a card's branch with `git merge --no-ff` and a subject that starts `Merge BLG-N:`, so the board follows the merge by itself; a fast-forward leaves the card where it was. After a successful deploy, mark `blerg-runner mark milestone "Deployed: <environment>" --commit <full deployed SHA>` with exactly one commit; that prefix is the deploy evidence that adds `to-test`.
9. Respond to admission feedback: search and update the match on duplicate denial; revise vague submissions; track held proposals. Do not rephrase and resubmit a denied duplicate. Keep progress comments short and factual.

## Admission and mirrored cards

- A 409 duplicate denial names `duplicate_of: #N`: fetch it and update the match if you have new evidence. A 422 revise response names the missing specifics. A 202 held response has a `review_id`; check it later instead of assuming the write succeeded.
- A disputed denial may be resubmitted once with `dispute_of` and a factual `rebuttal`; never dispute the same review twice or rephrase the same denied filing.
- Custom field keys and enum values must match the board schema exactly. Unknown keys and wrong types are rejected.
- Incident cards (`type: incident` or `INC-…` external IDs) may be read-only mirrors owned by another service. Do not move those cards.
- Do not delete a nonempty column; move its work deliberately or rename it. Keep a version guard on read-modify-write updates so another worker's change is not lost.
