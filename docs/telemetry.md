# Telemetry

The runner's **Insights** page and its Prometheus endpoint answer: how long do pods take to start,
how long do sessions live, how much do they run, and how many tokens (and roughly how many dollars)
do they use.

Nothing new is collected. Every number is derived from what the runner already records: the
`sessions` table, the start-progress events, status changes and the per-turn token usage.

## Insights page

Open **Insights** in the sidebar (or the bottom bar on a phone). Pick a range: 24 hours, 7, 30 or
90 days.

| Section | What it shows |
| --- | --- |
| Startup | Time from "start requested" to "ready", split into **start** and **resume**, per runtime: p50, p90, max, failures. A box-and-whisker per step (queued, scheduling, pulling image, connecting, cloning, plugins, starting the agent) shows where the time goes. The slowest starts link to their sessions. |
| Sessions | Started, ended, alive, by runtime and by how they ended. Lifetime p50/p90, and **busy time**: how long sessions spent running, waiting for you, or idle. |
| Tokens | Input, output, cache read and cache write, by model and by day, the cache hit rate, and the biggest sessions. A Claude Code subagent's tokens count under the subagent's own model (they are not turns). |
| Cost | An **estimate**: tokens times the price you entered for each model. |
| Cluster | Active and maximum pods, and the peak concurrency in the range. |
| Crons | Runs, late runs, manual runs, outcomes and duration. |

The start steps share one time axis. For each step the whisker runs from the fastest start to the
slowest, the box covers the middle half of starts (25th to 75th percentile), a tick marks the
median and a hollow mark the 90th percentile. Beside it: how many starts the step has (`n`), the
slowest, and how many were **slow**, meaning they took more than twice the step's median, so one bad
image pull reads as "1 slow" and not as a long bar. A step with fewer than five starts shows its
fastest, median and slowest start as dots, labeled "too few starts for a box". The axis is linear
unless the slowest start is more than 20 times the largest step median, when it turns logarithmic
and says so; hover or focus a step for every figure.

Busy time counts from a session's first status change in the range, so a session that stays idle
the whole range with no events adds nothing. Durations come from event timestamps, so they are accurate to the event, not to the millisecond. A
"resume" is the second or later start of one session.

### One session

The **Insights** button in a session's header opens that session's figures: how long it has lived
and how that time split between working, waiting for you and idle; its turns, tool calls (the top
five tools), the messages you sent it, the files it published, its MCP calls by outcome and its
proposals; its subagents by type and model; and its tokens and estimated cost by model, with the
main agent and its subagents side by side. A subagent's own tool calls are counted under the
subagents, not as the session's. The rules are the page's (busy time, token counting, prices); the
dialog refreshes every 30 seconds while the session is live. A terminal session shows its lifetime
and files only: it runs the engine's CLI, which reports no turns or tokens.

Who sees it: anyone who can open the session (every session but a private one, which is its owner's
alone) can open its insights, so a member sees figures for sessions other people started. Tokens and
cost are the exception and follow this page's rule: they are shown only to the session's owner (the
account that started it) and to an admin; anyone else sees "Token figures are visible to the
session's owner and administrators." in their place.

### Who sees what

- On this page, a **member** sees only the sessions they started. (A session's own Insights dialog,
  above, follows the session page instead, except for its tokens and cost.)
- An **admin** (anyone who can manage accounts) sees everyone's, and can switch back to **Mine**.
- A **private** session (a cron, or one with an MCP connection) counts toward an admin's totals but is
  never named to anyone but its owner: it cannot appear in "slowest starts" or "biggest sessions"
  for another person.

### Dollar estimates

The runner does not know what a model costs, so it ships **no prices**. An admin enters them once
per model in the price editor at the bottom of the page (US dollars per million tokens, for input,
output, cache read and cache write). Until a model has a price, its tokens are shown but its cost is
"—", and the total says how many models are unpriced. Changing a price re-prices history, because
cost is computed when you look, not stored.

`blerg_runner_cost_usd_total` is recomputed from lifetime tokens at the current price, so editing a
price moves the whole series. For a stable record, multiply the token counters by your own prices in
PromQL.

Codex sessions report `input_tokens` that may already include the cached tokens, so their input
cost and cache hit rate can be off. Treat the figure as an estimate. It ignores discounts, batch pricing and anything billed outside the
session.

## How events are recorded

Everything above, and the chat itself, reads a session's transcript events. How they get there
decides whether the times and the order you see can be trusted:

- **Time.** An event keeps the time the daemon gave it when it happened, not the time the server
  stored it, so an event sent late (after a network drop, say) still reads at the right time, and
  live and reloaded views show the same one. The server keeps that time within ten minutes of its own
  clock; an event with no time, or an unreadable one, gets the time it was stored. After an outage
  longer than ten minutes, the oldest re-sent events read ten minutes back, not at their true time.
- **Order.** Events are numbered in the order they are stored, and the daemon sends them in the order
  they happened. If the database refuses one, the session's later events wait, unacknowledged, until
  that one is stored; the daemon keeps them and sends them again, oldest first, so a message of yours
  is never drawn after its own reply. The wait lasts as long as the daemon keeps re-sending the
  refused event and ends two minutes after it stops, so a daemon that lost the event cannot freeze a
  transcript; a payload the database rejects outright (malformed data) is never held for. An older
  daemon is held to the same order.
- **Re-sent events.** A daemon re-sends what the server has not acknowledged. An event the server
  already has is acknowledged and otherwise ignored: it is not shown twice and does not change the
  session's status or model, send a notification or stop the session again.
- **Dropped live events.** A browser that falls too far behind a busy session loses some live
  events. The server tells it, and the chat fetches them again from the stored transcript; the event
  stream for tools (`…/events/live`) refills such a gap by itself. Nothing is missing from the stored
  transcript.

## Prometheus

`GET /metrics` on the runner serves the standard text format. It is **off** until you set
`BLERG_RUNNER_METRICS_TOKEN`; without it the path answers `404`. With it, a scraper must send
`Authorization: Bearer <token>`; anything else is `401`.

The metrics are cumulative over all recorded history (counters and histograms), so a scraper can
`rate()` them. They are recomputed from the database, so deleting a session removes its contribution
and a counter can then go down; Prometheus treats that as a reset. If the runner is published through an ingress, `/metrics` is reachable there too and the token is its only protection, so use a long random one. Labels are bounded: runtime, status, kind, result, step, stage, model, type. No
session id, account or title ever appears. The body is cached for 15 seconds.

| Metric | Type | Labels |
| --- | --- | --- |
| `blerg_runner_sessions` | gauge | `runtime`, `status` |
| `blerg_runner_sessions_started_total` | counter | `runtime` |
| `blerg_runner_session_lifetime_seconds` | histogram | |
| `blerg_runner_start_attempts_total` | counter | `runtime`, `kind`, `result` |
| `blerg_runner_start_duration_seconds` | histogram | `runtime`, `kind` |
| `blerg_runner_start_step_duration_seconds` | histogram | `runtime`, `step` |
| `blerg_runner_start_failures_total` | counter | `runtime`, `stage` |
| `blerg_runner_turns_total` | counter | `model` |
| `blerg_runner_tokens_total` | counter | `model`, `type` (`input`, `output`, `cache_read`, `cache_write`) |
| `blerg_runner_cost_usd_total` | counter | `model` (only models with a price) |
| `blerg_runner_cluster_sessions_active` / `_max` | gauge | |
| `blerg_runner_cron_runs_total` | counter | `status` |
| `blerg_runner_cron_run_duration_seconds` | histogram | |

### Scrape config

Keep the token in a file, not in the config:

```yaml
scrape_configs:
  - job_name: blerg-runner
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/secrets/blerg-runner-metrics-token
    static_configs:
      - targets: ["blerg-runner.blerg.svc:8080"]
```

Set the token with `SECRET_BLERG_RUNNER_METRICS_TOKEN` when running `install/k8s/deploy.sh`, or add
the key `BLERG_RUNNER_METRICS_TOKEN` to the `blerg-secrets` Secret and restart the runner. A later
deploy keeps an existing value. For the desktop stack, set it in `.env`.

### Useful queries

```promql
# p90 time to a ready pod, last hour
histogram_quantile(0.9, sum by (le) (rate(blerg_runner_start_duration_seconds_bucket{runtime="cluster"}[1h])))

# where startup time goes
histogram_quantile(0.9, sum by (le, step) (rate(blerg_runner_start_step_duration_seconds_bucket[1h])))

# output tokens per hour, by model
sum by (model) (increase(blerg_runner_tokens_total{type="output"}[1h]))

# estimated spend per day (re-prices history if you edit a price)
sum(increase(blerg_runner_cost_usd_total[1d]))

# cluster headroom
blerg_runner_cluster_sessions_active / blerg_runner_cluster_sessions_max
```

## API

`GET /api/insights?range=24h|7d|30d|90d[&scope=mine]` returns what the page shows (browser sign-in
required). `GET /api/sessions/{id}/insights` returns one session's figures (a signed-in
person who can see the session; one they cannot see answers 404, like an unknown one). Its `tokens`
is `null`, with `"tokens_hidden": true`, unless the caller started the session or is an admin.
`GET /api/insights/prices` lists the prices (any signed-in person);
`PUT /api/insights/prices` and `DELETE /api/insights/prices?model=...` change them (admin only).

### Live child telemetry

The Live panel's child rows include `tool_calls`, a bounded `activity` line,
`activity_since`, `last_activity_at`, and `usage_status` (`pending`, `reported`,
`hidden`). Counts describe recorded tool calls and reported tokens across model
requests, not context size or a claim that the task succeeded. An idle worker
remains idle even while its session process is kept alive.

Engines can publish `usage_updated` with `scope: "turn"`, `model`, and a complete
`usage` object (`input_tokens`, `output_tokens`, `cache_read_tokens`,
`cache_write_tokens`). This is a replacement snapshot of the current turn,
**not a billable event**. Explicit `usage: null` clears progress when a new turn
starts or resumes. Hermes translates the native gateway's lifetime counters to
this current-turn scope; valid idle lifetime reports establish the next baseline
without resurrecting a finished turn. Missing native figures remain unknown. An additive `usage_reported: false` on
completion distinguishes an unreported final usage object from a real zero;
older producers may omit it. It does not alter ledger sums.

Live display combines the latest eligible snapshot with completed-turn totals
at one event-sequence boundary. Completion replaces the snapshot instead of
adding it twice; Insights and billing continue to read only their established
completed-turn/subagent ledger. An old daemon reports no snapshots: tools and
completed usage still appear, with pending usage until a report exists. Older
renderers ignore the unknown event kind.

Child visibility is checked independently from parent visibility. Token/cost
figures follow the owner/admin Insights rule; other viewers receive hidden
usage. Activity is the same bounded tool-input summary already available to a
viewer of that child's transcript; it adds no credential, environment or output
extraction. `local: true` is the owning daemon's observed endpoint classification,
not an inferred dollar price or a guarantee about hardware costs. Unknown costs
remain absent; monetary values, when provided, use configured model prices.
