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
| Startup | Time from "start requested" to "ready", split into **start** and **resume**, per runtime: p50, p90, max, failures. A bar per step (queued, scheduling, pulling image, connecting, cloning, plugins, starting the agent) shows where the time goes. The slowest starts link to their sessions. |
| Sessions | Started, ended, alive, by runtime and by how they ended. Lifetime p50/p90, and **busy time**: how long sessions spent running, waiting for you, or idle. |
| Tokens | Input, output, cache read and cache write, by model and by day, the cache hit rate, and the biggest sessions. |
| Cost | An **estimate**: tokens times the price you entered for each model. |
| Cluster | Active and maximum pods, and the peak concurrency in the range. |
| Crons | Runs, late runs, manual runs, outcomes and duration. |

Busy time counts from a session's first status change in the range, so a session that stays idle
the whole range with no events adds nothing. Durations come from event timestamps, so they are accurate to the event, not to the millisecond. A
"resume" is the second or later start of one session.

### Who sees what

- A **member** sees only the sessions they started.
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
required). `GET /api/insights/prices` lists the prices (any signed-in person);
`PUT /api/insights/prices` and `DELETE /api/insights/prices?model=...` change them (admin only).
