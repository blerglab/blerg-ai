# Blerg dataset row contract (BLG-519)

All references have `kind: "dataset"`, a catalogued `name` and string-valued
`parameters`. None accepts SQL, expressions, URLs, paths or credentials. The
viewing session supplies workspace scope; current viewer authority applies before
aggregation and again at delivery. Rows have stable string `key` values. Snapshots
replace previous rows; upserts replace by key and removals delete keys. Consumers
must discard previous-epoch state and request a snapshot after a sequence gap.

This extends the BLG-512 socket contract. No new browser message types are emitted:
`DatasetDelta` remains in the privacy scope registry and the existing
`runDatasetSubscription:emitDatasetDelta` decision covers these sources. Gate
report ingestion has its own explicit route decision. Descendant record sources
compare collection privacy/revision fences against current authorization metadata at delivery, without collecting aggregates again; the renderer can still reconcile those snapshots by key.

| Dataset | Parameters | Row fields beyond `key` | Meaning |
|---|---|---|---|
| `blerg.gates.durations` | Optional `suite`, `since` | `run_id`, `suite`, `seconds`, `outcome`, `commit`, `start`, `end` | `total` is run wall time; suite seconds may overlap. UTC timestamps. |
| `blerg.cards.time` | `board`, `card` | `seconds` | Current known active worker seconds under existing card-time attribution. Pending/unknown is unavailable, never zero. |
| `blerg.reviews.pairs` | `board`, `card` | `author_engine`, `reviewer_engine`, `reviews`, `approved`, `changes`, `rejected`, `high`, `medium`, `low` | Current latest target/reviewer findings grouped by engine pair. |
| `blerg.gates.bins` | `width` (integer seconds, 1–86400), optional `suite` (default `total`), `since` | `start`, `end`, `count`, `suite` | Sparse half-open numeric bins; missing historical reports remain gaps. |
| `blerg.cards.merged.heatmap` | Same as `blerg.cards.merged` | `day`, `hour`, `count`, `bucket`, `end`, `timezone` | Local calendar day/hour cells, UTC boundaries; repeated DST hours combine. |
| `blerg.workers.timeline` | Optional `since` | `worker`, `start`, `end`, `status` | Opaque worker lane, UTC intervals from recorded status facts; no titles, paths or reason prose. Before the first fact is `unknown`. |

Card numbers are canonical positive decimal strings, at most nine digits. Board
IDs contain only letters, digits, underscore or hyphen. These card sources require
a human viewer's current Core sign-in and live Board card visibility on collection
and queued delivery; only that account's source sessions are read. Author engine
is derived from an owned card-linked recorded delivery matching the full target
SHA or its prefix. Matches spanning multiple author engines remain `unknown`, as
does missing evidence. Reviewer engine is an attributed
mark claim, not proof that an engine executed the review.

The latest round wins for each target/reviewer; a later record in the same round
replaces earlier findings. A higher round with approval clears the older findings.
Gate correction replaces the entire run, removing deleted suites and moving its
histogram contribution. Heatmaps rebuild from deduplicated merge SHAs; corrected
history moves/removes cells. Timeline facts at the same timestamp use the greatest
sequence; interval keys use worker and start time. Unknown coverage is not filled
with synthetic zero or inferred activity.

## Gate report ingestion

`POST /api/sessions/{id}/gate-reports` requires the running session's own messaging
credential (or the existing daemon authority). Other-session and browser tokens
are refused. Body size is at most 128 KiB. Wrap `scripts/pre_merge.py`'s summary:

```json
{
  "run_id": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
  "revision": 1,
  "commit": "0123456789012345678901234567890123456789",
  "started_at": "2026-11-01T00:00:00Z",
  "finished_at": "2026-11-01T00:01:00Z",
  "report": {
    "seconds": 60,
    "complete": true,
    "suites": [{"suite": "runner-unit", "seconds": 45, "outcome": "pass"}]
  }
}
```

The typed projection discards every other field, including logs, host, arguments,
paths and diagnostics. Durations must be finite, nonnegative and at most 30 days;
there are at most 128 uniquely named suites (bounded identifier labels, excluding
reserved `total`). Outcome is `pass`, `fail` or `skip`. Missing durations and
completeness are invalid. Run outcome is incomplete unless the report declares
complete; a failed suite makes a complete run fail. No duration is inferred from
failure marks or sums of concurrent suites.

These reports are **reported by the agent, not verified**. Gate duration and bin
queries accept optional RFC3339 `since`, default seven days, clamped to 30 days;
responses select the newest 77 whole run records, at most 9933 suite/total rows.
Timeline `since` defaults to 24 hours, clamped to seven days; output is newest first,
at most 10,000 intervals, clamped to the window with a recorded baseline when known.
Row keys are fixed-length SHA-256 identifiers, including maximum-length suite/board
IDs. Scalar gate rows are ordered by end time in the client, not hashed key order.

`blerg-runner gate report gate-report.json` posts the sanitized envelope above.
`scripts/pre_merge.py` calls it automatically in a Blerg session with the same run
ID; upload failures are reported safely and do not alter the gate result. No raw
logs, paths, host metadata or credentials are submitted. Finish times beyond five seconds of clock skew and
run duration/interval differences exceeding one second are refused. The one-second
tolerance covers rounding and producer timestamp overhead.

Transactional limits are 30 requests per minute and 256 stored runs per session,
including concurrent submissions. Existing runs may be corrected at the count cap.
Reports expire 30 days after stored `observed_at`, pruned hourly. That stored time
is also gate freshness; reads never make an old report appear fresh.

Storage key is session/run ID. A greater positive revision atomically replaces the
record; equal revision and equal sanitized content is an idempotent retry (200).
New/corrected records return 201. Stale or conflicting revisions return 409. Source
records are removed with their session and the 30-day gate retention; dataset
materializations are rebuildable and browser-only. Subscription audit retention
remains 180 days. Responses cover the documented newest bounded window, not all history.

Ingestion invalidates only the affected workspace's gate duration/bin scopes.
Lifecycle/privacy changes invalidate descendant record scopes. Repeated reads
observe the source under current authority; only actual Git reads advance merge
observation times. Missing gate history produces an empty ready snapshot, not an
observed duration of zero. Hidden cards unsubscribe; leases, reconnect, safe
failure/backoff and purge follow BLG-512.

## Renderer handoff

The branch currently accepts named data on stat/gauge cards only. Live chart
admission belongs to chunk 5; no fallback is silently called live. The aggregate
rows above can feed its heatmap (`hour`, `day`, `count`), histogram (`start`, `end`,
`count`) and timeline (`start`, `end`, `worker`, `status`) encodings without client
transforms. Scalar duration cards should use `{suite: "total"}` and `latest`; rows are ordered
by finish time, with stable session/run tie breaks; run `total` must not be added to its suite rows.

Delivery performs only visibility and privacy/revision checks. Temporary database,
Board or fence failures emit `unavailable` without removals or a replacement
snapshot, preserving prior rows and requesting a full authorized refresh. Denial
purges. A successful Board check can be reused by immediate delivery for one second,
scoped to the human sign-in, account, project, board and card; collection checks live.
Accepted marks invalidate active card-time/review subscriptions across workspace roots.
Empty/invalid review targets are excluded; reviewer labels normalize before deduplication.

Privacy fences compare authority per session identity. A private-flag or owner
change on an existing identity purges in-flight data; added/removed identities
request a fresh snapshot without purging or changing the last displayed status.
Ordinary source revisions do not fence delivery: busy timelines may keep showing
an authorized frame while their next update is collected.

A successful bounded sample carries `status: "ready", cause: "truncated"` if
additional visible runs or intervals exist beyond its cap. Gate bins inherit
that coverage marker from the runs used to build them. Removing the cut emits a
ready frame with the cause cleared, even if the returned rows have not changed.
