# Insights trends

Insights opens inside the existing workspace tabs. All four views use
`runner/frontend/src/components/charts/TimeChart.tsx`, which workspace planning
can reuse without depending on Insights response types. It accepts time buckets,
category labels and timestamped events; category controls are also its color key.
Bars share one UTC axis, with totals, a Y axis and a dashed today line. Hourly
buckets cover 24h, daily buckets cover 7d/30d and weekly buckets cover 90d.

Agent health counts original failures by their effective date and type. A passed
rerun does not erase the failed attempt. Card cost charts measured active time
by size, excluding ambiguous shared-card allocation. Time shows phase shares per
bucket. Hours are classified from actual interval boundaries, including work
crossing midnight; lifetime totals are never distributed across dates.

The activity response includes a previous period of equal duration under
`history`, with the same owner restrictions and live card redaction. Missing
history stays unavailable rather than becoming an invented zero. The existing
1,000-conversation and 50-card limits remain; incomplete reports explain how to
narrow the cohort.

Spending uses all range-scoped usage, partitioned by the recorded engine and
hour, rather than the largest-workspace sample or a model-name guess. Dollar
bars include only completely priced engine/hour groups. Model prices are edited
under Spending → Model prices. Claude and Codex weekly pace uses the provider's
reported share, reading timestamp and reset time, assuming a seven-day window.
It includes work outside Blerg and does not combine readings from different
machines into one account. Readings older than twenty minutes cannot establish
a projection. The first hour after a reset also remains unprojected.

Milestone marks and the Board's deployment and release records appear above
their corresponding time bucket. `blerg_board_get` accepts optional RFC3339
`events_from` and `events_to` arguments, bounded to 180 days. This retains its
existing board-read authorization and default response. History excludes deploy
detail text, credentials and release author identities. Older Board versions or
failed reads show an explicit history-unavailable note.

Title attribution resolves a bare #N only against one board context on the
conversation or its nearest same-owner ancestor. Launch boards, active grants
and pins establish context; permission level does not establish identity.
Explicit checklist assignments naming #N index the assigned same-owner worker.
Scoped reads backfill historical titles and checklist assignments in batches of
100, outside the migration transaction and its trigger locks. Pending history
keeps the report incomplete. A later parent pin relinks same-owner worker
titles, preserving their original launch date for ranking. Ambiguous
boards and foreign workers remain unlinked; every read still checks live Board
authorization.

Summary and activity requests start together. In-memory reports redraw cached
tabs immediately while fresh reads run. Ended-conversation timing is cached in
`session_insight_timings`, keyed by evidence and wait-state fingerprints; new
events/wait changes invalidate it. Running conversations reuse timing for at most
one minute and invalidate immediately when their evidence changes. A bounded
in-memory timing cache avoids repeatedly decoding the durable hourly JSON. Hourly
partitions retain at most 180 days and are built only for Insights. Independent
board metadata reads overlap through six workers, preserving live authorization and the
existing card-ranking rules. Deploy history has a board/date index.

Each board needs one authorized exchange for its candidate card metadata and
events, rather than one exchange per card. Permission checks stay live on every
poll. Completed UTC hours form equal current and previous comparison spans;
hourly evidence outside either span is excluded. The API sends daily sums split
at those boundaries while retaining lifetime totals for the detail section.
The original startup, token, runtime, live, review and failure detail remains
below its view in one labeled disclosure. No detail drawer is needed.

Time's Gates and Building bars and adjacent arrows open inline drill-downs;
their legend keys retain show/hide. Building groups
recorded building, rework and wait tools; human waiting and idle stay separate.
The sweep records joint phase/tool categories, so their parts reconcile exactly.
Gate suite selectors are captured on new command events; old missing selectors
remain a gap. Explicit tool error flags, exit codes and background completion
records supply outcomes. An asynchronous launch is not a passed run. Correlated
background work counts once; overlapping uncorrelated suite/tool observations
keep their time but leave run counts unknown. Repeats compare normalized gate
identities within a conversation; a rerun follows a failure. Go and Vitest test
selectors remain part of that identity. Instrumented suite
intervals also contribute without treating an unspecified kind as tests.

Both drill-downs lead with the same period comparison and dated trend. Lists are
ordered by time and capped at ten. Unknown timing and card attribution stay in
the totals as labeled gaps. Test-first red runs remain first build. A gate failure
after a recorded non-test source edit establishes rework until a matching or
broader gate passes. A selected test cannot resolve a failed whole suite. Gate
failures never override reviewing. An earlier unresolved same-owner card review requesting
changes or failed check establishes rework for a later worker. Later approvals
and matching passed-rerun marks resolve that evidence before the worker starts.
Foreign records cannot change that classification. Unknown-kind suite intervals
yield only to recorded tests, build/lint and commit-hook tool calls.

Operations taking at least 100 ms log fixed operation names, elapsed milliseconds
and suppressed-repeat counts, at most once per operation per minute. Diagnostics
contain no query text, parameters, identities, workspace paths or raw errors.
These improvements do not by themselves establish the live 500 ms target;
measure the owner-authenticated 7d API on the deployed installation.
The round 3 local 420-conversation fixture measured 392 ms cold, 228 ms warm
and 230 ms fresh. These are controlled fixture results, not production measurements.
Requests read cached ended history, compute running conversations only, and label
missing ended conversations as pending. End status materializes timing; turn
completion queues a refresh after browser fan-out in a coalescing queue capped at
128 conversations. One background worker processes at most fifty completions per
batch. If the queue fills, ended history remains eligible for database backfill;
running conversations still compute live on requests.
Historical backfill uses an indexed pending marker, at most fifty ended
conversations per owner per tick, newest first with locked rows skipped. Failures
back off for one then two minutes and stop after three attempts, with a log entry;
later metadata changes reset eligibility. Completed rows survive a later failure.
The seeded benchmark uses normalized persisted call IDs and asserts that gate
timing and run records reach the HTTP response.
