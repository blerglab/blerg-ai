# Visualizing: worked templates

One minimal, working template per chart type from SKILL.md. Copy one, change the data, title and
field names — don't build a spec from scratch.

## Time series

```json
{
  "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
  "title": "Latency doubled after the 03-04 deploy",
  "data": {"values": [
    {"at": "2026-03-01", "p50": 42}, {"at": "2026-03-04", "p50": 48}, {"at": "2026-03-05", "p50": 91}
  ]},
  "mark": {"type": "line", "point": true},
  "encoding": {
    "x": {"field": "at", "type": "temporal", "title": "Date"},
    "y": {"field": "p50", "type": "quantitative", "title": "p50 latency (ms)"},
    "tooltip": [{"field": "at", "type": "temporal", "title": "Date"}, {"field": "p50", "type": "quantitative", "title": "p50 (ms)"}]
  }
}
```

## Breakdown (sorted; arc only for a handful of slices)

```json
{
  "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
  "title": "Timeouts are almost all from the search service",
  "data": {"values": [
    {"service": "search", "timeouts": 311}, {"service": "billing", "timeouts": 22}, {"service": "auth", "timeouts": 9}
  ]},
  "mark": "bar",
  "encoding": {
    "x": {"field": "timeouts", "type": "quantitative", "title": "Timeouts"},
    "y": {"field": "service", "type": "nominal", "title": "Service", "sort": "-x"},
    "tooltip": [{"field": "service"}, {"field": "timeouts", "title": "Timeouts"}]
  }
}
```

## Comparison (grouped, sorted by value)

```json
{
  "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
  "title": "Build time dropped most on the runner module",
  "data": {"values": [
    {"module": "runner", "seconds": 48}, {"module": "chat", "seconds": 61}, {"module": "board", "seconds": 73}
  ]},
  "mark": "bar",
  "encoding": {
    "x": {"field": "module", "type": "nominal", "title": "Module", "sort": "-y"},
    "y": {"field": "seconds", "type": "quantitative", "title": "Build time (s)"},
    "tooltip": [{"field": "module"}, {"field": "seconds", "title": "Seconds"}]
  }
}
```

## Distribution (boxplot)

```json
{
  "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
  "title": "Review rounds spread widest on high-risk cards",
  "data": {"values": [
    {"risk": "low", "rounds": 1}, {"risk": "low", "rounds": 2}, {"risk": "low", "rounds": 1},
    {"risk": "high", "rounds": 2}, {"risk": "high", "rounds": 6}, {"risk": "high", "rounds": 4}
  ]},
  "mark": "boxplot",
  "encoding": {
    "x": {"field": "risk", "type": "nominal", "title": "Risk"},
    "y": {"field": "rounds", "type": "quantitative", "title": "Review rounds"}
  }
}
```

## Flow (Mermaid flowchart; a card key as its own node links)

```
flowchart LR
  start[Card filed] --> review{Reviewed?}
  review -->|changes| fix[BLG-123]
  review -->|approved| merge[Merge]
  fix --> review
```

## Sequence (Mermaid sequence diagram)

```
sequenceDiagram
  participant Owner
  participant Main
  participant Worker
  Owner->>Main: File BLG-123
  Main->>Worker: Brief + acceptance criteria
  Worker->>Main: Commits + test evidence
  Main->>Owner: Review summary
```

## Inline dashboard

Save as `release.dash.json` and publish like any other file (`--live` updates in place).
Native views require a value field and explicit `single` (at most one row) or `latest`
selection. A missing/null value is unknown; zero remains zero. Stat views allow a unit,
comparison text field (`delta`) and numeric trend field (`sparkline`, in row order).
Gauges require finite `min < max`; optional numeric thresholds remain labeled.
Text views show plain text. Chart specs omit `data`; rows are inserted by the adapter.

```json
{
  "format": "blerg-dashboard",
  "version": 1,
  "title": "Release activity",
  "cards": [
    {"id": "merged", "kind": "stat", "title": "Merged today", "span": 1,
     "data": {"kind": "inline", "rows": [{"count": 12, "comparison": "+3 versus yesterday"}]},
     "view": {"value": "count", "selection": "single", "delta": "comparison"}},
    {"id": "usage", "kind": "gauge", "title": "Weekly usage", "span": 1,
     "data": {"kind": "inline", "rows": [{"percent": 71}]},
     "view": {"value": "percent", "selection": "single", "unit": "%", "min": 0, "max": 100, "thresholds": [80, 90, 95]}},
    {"id": "coverage", "kind": "text", "title": "Coverage", "span": 2,
     "data": {"kind": "inline", "rows": [{"text": "Snapshot from local observations."}]},
     "view": {"value": "text", "selection": "single"}},
    {"id": "daily", "kind": "chart", "title": "Merges per day", "span": 4,
     "data": {"kind": "inline", "rows": [{"day": "Mon", "merges": 9}, {"day": "Tue", "merges": 12}]},
     "spec": {"mark": "bar", "encoding": {
       "x": {"field": "day", "type": "ordinal", "sort": null, "title": "Day"},
       "y": {"field": "merges", "type": "quantitative", "title": "Merges"}}}}
  ]
}
```

Limits: 1 MiB per document, 10,000 scalar rows per card and 50,000 total. Embedded specs also
pass the existing stricter chart budgets. Unknown fields, versions and kinds are refused;
Source remains available. Source download is supported; whole-dashboard image export and
live source bindings are later chunks.
