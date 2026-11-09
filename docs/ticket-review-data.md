# Ticket and review data

Every Board card has optional size (S/M/L/XL), risk (low/medium/high) and
complexity (low/medium/high). Existing XS values remain readable for compatibility; new estimates use S/M/L/XL. Set estimates before starting work:

```sh
blerg-runner ticket create --title "Add caching" --repos example-org/app --size M --risk medium --complexity low
blerg-runner ticket update 7 --size M --risk medium --complexity low
blerg-runner ticket list --risk high --complexity high
```

`ticket file` and the `blerg_ticket_file` proposal tool accept the same estimates.
The signed approval digest binds all three; editing other draft fields preserves
them unless explicitly changed.

The fields are first-class because board custom fields require a declared schema;
these estimates must work on every board with no configuration. Custom fields
remain available for project-specific data. An omitted estimate is permitted;
ticket create warns. Dedup refresh preserves existing estimates, just as it
preserves placement; use an explicit update to revise them.

Each estimate has a server-owned `*_first_set_at` timestamp. It survives changing
or clearing the value. Historical estimates have null timestamps because their
original estimation time is unknown. Revising or clearing a legacy estimate keeps its first-set time unknown; a later edit never fabricates the original estimation time. Callers cannot backdate
these timestamps. Compare these timestamps with flow entries to distinguish
estimation before work from estimation after work.

Record every review, including approvals with no findings:

```sh
blerg-runner mark review "Two findings" --card 7 --verdict changes --findings high,medium --by codex --target abcdef1 --round 2
```

`findings` has one severity per finding (repeats count); omit for none. `by` names
the engine the author says reviewed the target. It is attribution, not verified
provenance. The summary is plain text. A worker posts to its own session. The
record appears in that session's History and the owner-scoped card data.

Use the selected board context for live CLI marks. Historical JSONL imports have
explicit `board_id`, `card`, `at`, `kind: "review"`, `text`, `by`, `verdict`,
`findings`, `target` and `round`. Re-importing the same historical record is
idempotent; changing the round or reviewed commit creates a distinct record.

Convert a review ledger without changing it:

```sh
python3 scripts/convert-review-rounds.py path/to/review-rounds.csv --board BOARD_ID --card 368 > review-marks.jsonl
blerg-runner mark --import review-marks.jsonl
```

The converter expects columns `at,summary,engine,target,round,verdict,high,medium,low`.
Severity counts must be explicit nonnegative integers, including zero; missing counts are unknown, not silently zero.
If headers differ, map them with repeated `--column FIELD=HEADER`, for example
`--column engine=reviewer`. Card and board can come from CSV columns `card,board_id`
instead of flags. Missing actual dates, timezone, reviewed commits or verdicts
are errors, not guessed values. Conversion validates the whole file before
writing output. The source ledger is not included in this checkout.

The human read endpoint is `GET /api/board-cards/{board}/{number}/data`. It checks
the signed-in person's live Board read permission, then returns:

- `card`: estimates and first-set timestamps, plus all flow entries. Each entry
  snapshots the column name and whether it represented in-progress or done at
  entry, including reopenings. Reordering in the same column is excluded.
  Historical entries have unknown semantics instead of borrowing today's name.
- `activity.sessions`: this owner's linked sessions, their engine/model and
  native worker lifecycle/model records, observed model events, plus same-owner child channels without a different explicit card link. Pin/review links survive
  unpin. A #N title gets its board from the launch context or a pin; no global
  number lookup guesses across boards. Empty stored engine IDs retain their documented Claude default; missing model IDs remain unknown.
- `activity.reviews`: structured review mark payloads and their session and
  recording time. The optional `at` preserves the actual historical review time.
- `activity.waiting_on_person`: explicit blocking ask intervals and their union
  duration in seconds. Nonblocking questions and idle time are excluded.
  If a linked channel names multiple cards, or a worker ask can be answered by its parent rather than only its owner, `attribution_ambiguous` is true and the aggregate seconds are null: raw channel intervals, owner-only flags and parent links remain available without claiming a card-specific duration. This is observable waiting evidence, not a claim to measure every interruption.

No charts are part of this data slice. Session prompts carry the canonical ticket
skill in native, sandbox and cluster runtimes. Workstation provisioning also links
it into Claude, Codex, standard agent and Hermes skill directories. The Board and
Runner skill bodies are checked for consistency.

### Local gate telemetry

`scripts/pre_merge.py` appends one run record to
`.superpowers/gate-runs.jsonl` (ignored), or `$BLERG_GATE_TELEMETRY`.
Records include the start, commit, worker count, start/end load averages,
per-suite duration/outcome, exact test counts, the slowest 30 test attempts per
suite, and observed fail-then-pass reruns. Go identity includes the package.
Vitest emits a separate JSON report; its millisecond durations become seconds.
No test output or credentials enter the recorder. Existing full logs stay local.

For a focused worker run, use the same recorder:

```sh
python3 scripts/gate_record.py --suite board-db -- go test -json ./internal/db
python3 scripts/gate_record.py --suite board-unit -- npm test -- --maxWorkers=1
python3 scripts/gate_record.py --suite board-unit --rerun-of '<prior run identifier>' -- npm test -- --maxWorkers=1
python3 scripts/gate_report.py
python3 scripts/gate_report.py --run -1 --compare -2
```

Run the wrapper from the module/app being tested (use its absolute script path
when outside the repository root). Vitest commands acquire the shared repository UI lock under the user cache. Reruns are recorded, never automatically started.
Records have a stable run ID for `--rerun-of`. The worker wrapper adds Go JSON output automatically. Vitest retry counts and final outcomes come from a small companion reporter alongside its JSON timing report. Separate reruns retain their supplied relationship; an observed fail-then-pass
in a single Go JSON stream is listed under `flake_reruns`. A passing retry is
evidence of a rerun, not a claim that the underlying failure was harmless.
Comparisons use retained test costs, not tests outside each suite's slowest 30.

### Failure records and timing evidence

Record a failed attempt immediately:

```sh
blerg-runner mark failure "Database gate failed" --card 368 --type gate --severity high --suite "board database"
blerg-runner mark failure "Database gate passed on rerun" --card 368 --type gate --severity high --suite "board database" --rerun-passed
```

Types are test, gate, flake, review, session, engine, deploy and merge. Failure
severity is a single high/medium/low; finding severities remain review fields.
Imports accept `type`, `severity`, `suite` and boolean `rerun_passed`. They require
an explicit board ID and real `at` time. Rerun marks supplement the original
failure; they never replace it. Existing History finding rows display these
marks with a failure prefix and retain structured failure fields. No new UI is
required. Server session-failure alerts appear automatically for owned sessions
linked through a pin or an unambiguous #N title (and inherited worker links).
Their severity and rerun outcome are unknown unless separately recorded.
Automatic alert notifications do not count as person-wait questions.

Each card-data session has `timing.seconds` and `timing.phases` in seconds.
The server pairs tool calls/results and sweeps interval boundaries, clipping to
the session lifetime. Unfinished tools are clipped to the read time or session
end and counted in `incomplete_tools`. Overlap has deterministic precedence:
explicit person waits, tests, build/lint, git, edit, read, other tools, running
outside tools (model), explicit idle, then unknown. Thus categories partition
session time, and nested/long tools cannot multiply it. These are complete
linked-session durations, not an invented allocation across several cards.
Tool commands and outputs are consumed privately and excluded from the response.

`runner/internal/activity` owns the tested static classification rules. They
recognize command positions, quoted separators, known launch wrappers, Go's
`-C`, common test/build/lint commands and named read/edit tools. Dynamic shell
programs and unknown tools remain other. Model time means recorded running time
outside instrumented tools, including uninstrumented engine overhead; it is
not billed token-generation latency. Missing running/idle evidence stays unknown.

Phase timing uses the same disjoint partition: tests and build/lint are gates;
**git commit tool time is a gate**, including hooks, except with `--no-verify` or `-n` (their individual runtime
cannot be separated without hook events). Explicit engine review commands are
reviewing. A child session with a structured review record uses launch through verdict as its reviewing phase (`review_phase_basis`); non-gate tool/model time after a failure or a changes/rejected review until a passed rerun or an
approved review is fixing; other active time is building. Explicit asks,
idle and unknown retain their own buckets. These phase labels are static
proxies backed by events, not a claim that uninstrumented review work can be
reconstructed. Backdated marks use their recorded historical time.

The date-only review tracker can also be exported without inventing missing data:

```sh
python3 scripts/convert-review-rounds.py review-rounds.csv --audit > review-rounds.jsonl
```

This lossless export preserves every source column, reviewer labels, card
references and finding counts. Unknown exact timestamps and board IDs are null;
suffixed card references retain their label and need explicit numeric mapping.
`import_ready` is false and `unresolved` lists the missing inputs. This export
is data for inspection, not a file to pass directly to `mark --import`.

Both the ticket and coordinating skills use the same distribution: engine
skill links on workstations and embedded shared guidance for native, sandbox
and cluster sessions. Coordinating instructions apply when acting as Main;
workers retain their assigned scope. Board estimate migration is **035** after
main's hygiene and retention migrations 033–034; Runner retains 106–108.


Card activity detail is bounded per session: the latest 100 model events and
100 worker events, and at most 2,000 timing events in the private JSON batch.
`model_events_truncated`, `workers_truncated` and `timing_events_truncated`
disclose detail limits. Long-session timing uses an ordered row stream over the
complete history, retaining only active calls and interval boundaries rather
than event payloads. Adjacent equivalent intervals are coalesced. Complete time
totals remain available; `timing_event_count` retains the full count. `timing_scope` says whole-session time is not additive across cards;
`shared_with_other_cards` and `card_links` identify shared attribution.
Tool result identities are cached on write so reads never extract them from
large result payloads. Commands above 64 KiB remain unclassified.

Structured marks require the workspace's card link or board grant, including
same-owner ancestor context for a worker. Session-token `--by person` is stored
as `claimed_by: person`, with `by: agent` and `posted_by: agent`; it cannot
become an authenticated person's review or failure.

UI gates use a repository-specific lock under the user cache, shared by Git
worktrees. `BLERG_UI_SUITE_LOCK` can override it. An inherited lock descriptor
is reused by nested recorders. Telemetry is best-effort and does not change gate
outcomes; the local summary is written first. Test file paths are repository-relative.

### Insights reports

`GET /api/insights/activity?range=7d` projects the BLG-368 reads for the signed-in
human owner only, including unlinked conversations. Administrators do not widen
this report with `scope=all`. A live account-wide human sign-in is required;
agent and board-limited credentials are refused. Every linked card is read with
the person's live Board permission before its estimates, flow or marks appear.
Revoked/unreadable cards are counted in `omitted_cards`; an interrupted deadline
returns completed results with `truncated`, while other database failures remain errors. Commands, outputs, mark prose, tokens and dollar
cost are excluded. The existing token/cost endpoints keep their owner/admin rules.

Ranges are 24h, 7d, 30d and 90d. The global cohort consists of conversations
started in the range, with their whole-conversation timing. `workspace=<id>`
restricts it to that owner's conversation and owned descendants. `range=all`
is accepted only with a workspace, preserving lifetime reports for old workspaces.
Card completions and review/failure marks use their recorded effective dates
within the range independently of conversation launch dates. Even an old Main
can contribute a failure today or a card completed today. Up to 1,000 owned card
candidates are checked, ranked by latest known link/mark/automatic-alert and live Board flow/update
activity; the latest 50 cards are shared by every report section. The timing cohort
is capped at 1,000 conversations. All limits and the 30-second partial-read deadline
are disclosed.

Timing uses one metadata/wait query and one shared ordered event query for the
whole cohort. Each conversation is computed once; no per-card timing recomputation
or per-conversation database stream is opened. Owner/start and owner/parent indexes
keep cohort and descendant reads scoped. Completed timing survives interruption;
remaining conversations retain unknown timing.
Risk slices omit multiply linked/shared conversation time and omitted-card
attribution instead of inventing a risk. Truly unlinked conversations remain
in the unknown-risk slice. Flow is wall time from a recorded in-progress entry
to done, using the latest completed cycle per card in the range. Reopened work
is measured as its own cycle instead of adding previous completed cycles. Unknown historical column
semantics break a cycle. Size groups default to estimates made before work;
late or undated estimates require explicit inclusion. Boxes require five cards;
smaller groups show the actual observed values as dots (with matching accessible
descriptions), and every group shows n. Engine observations require an explicit
risk tag, show classified active conversation time with n, and state that
mission-critical work goes to one engine. Assignment is not random; these
observations do not establish which engine is faster.

Review rounds count observed distinct round numbers per card; missing rounds
are not fabricated. Round buckets sort numerically; missing round numbers have
their own bucket and remain explicit in record details. Findings count every reported severity. Failure records may
describe the same attempt; passed reruns supplement rather than erase failures.
Automatic alert severity remains unknown. Gate cost reports measured time and
share of classified active time, including test/build/lint and inferred commit
hooks. Idle, person-wait and unknown time are excluded from that denominator.
Unknown or incomplete gate coverage is not drawn as a measured zero. Gate and
worker lists cap at 50 rows and have bounded scroll heights. Dollar cost by phase
is uninstrumented and remains unknown. Worker splits retain explicit unknown,
idle and person-wait buckets and identify unfinished tools clipped at read/end.

Screenshot tests may read an ignored local audit export produced by
`convert-review-rounds.py --audit`. It preserves date-only review dates and
unmapped reviewer/card labels. Those records are never imported into live marks
or shipped in production; screenshots identify the imported review fixture and
synthetic flow, failure and timing measurements separately.

The activity report stays mounted across section navigation, reuses a loaded
answer, and aborts pending reads when hidden or unmounted. A sign-in identity
change drops the cached owner report and aborts its read before requesting new data. Shared viewers see
that only the owner's activity is available rather than a retry loop; 401
responses ask the person to sign in. Worker segment tooltips use title attributes
and never replace the document title.
