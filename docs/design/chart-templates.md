# Native chart templates

The chat package exports `VISUAL_TEMPLATES` and `templateDashboard()`. Each catalog
entry includes its question, required scalar schema, units, stable keys, domain
policy, freshness, empty state, accessibility summary and renderer capability.
The example cards use the existing four dashboard kinds: chart, stat, gauge and
text. Metadata is separate from the strict dashboard wire format.

Use `templateDashboard()` for a fresh composed example. To publish one chart,
copy its card's spec, set the root title from the card title, and put its inline rows in `data.values`; save as
`name.vl.json`. To embed it, copy the card into a `.dash.json` document.
Replace the simulated observations and title before publishing a finding.
All examples use inline data, with no authored colors or transform code.

| Form | Input and behavior |
| --- | --- |
| Time series | Keyed observations, elapsed seconds since a declared observation start, latency in milliseconds. |
| Comparison | Precomputed counts, sorted descending upstream; zero baseline. |
| Breakdown | Precomputed totals in minutes, ordered by value upstream. |
| Distribution | Supplied samples grouped into a boxplot; native static rendering. |
| Stat with sparkline | Latest value, delta and supplied trend samples; static sparkline. |
| Gauge | Explicit percentage bounds and threshold. |
| Heatmap | Ordered day/hour cells, quantitative counts. Null means unknown and has an outlined cell; zero remains a measured value. |
| Status timeline | Keyed entity/start/end/status intervals with explicit timestamp offsets. Native ticks display UTC. Unknown gaps stay gaps. |
| Histogram | Precomputed lower/upper/count rows with actual numeric bin widths. No client binning or aggregation. |
| Text | Source coverage explanation, including unknown observations. |
| Composed dashboard | The catalog cards in the existing responsive dashboard grid. |

Open timeline intervals must carry `open: true` and an effective `end` equal to
the source's observation time; include that flag in inspection. The renderer
never invents an end from its own clock. Closed intervals use `open: false`.
Dates without an explicit offset are outside the timeline template contract.
A supplied unknown status is a labeled interval; absent intervals remain gaps.

Histogram bins use `[lower, upper)` throughout. The catalog uses equal-width bins and a Count axis title. Null counts remain native, visibly outlined unknown values; zero counts keep a small hit region above the baseline.
The BLG-519 bins producer (`runner/internal/server/datasets_aggregates.go`, `gateBinRows`) assigns `floor(value / width)`, so an exact upper edge belongs to the next bin, matching this rule. Overlapping or reversed
bins and negative, nonnumeric or nonfinite counts are refused by the native renderer. Missing or null counts remain unknown. Unknown
counts must never be encoded as zero. Keep the unit and count meaning in the
card title and inspection fields.

Heatmaps, status timelines and histograms use the retained native scene and
chunk 3's keyed data motion. An explicit `id` or `key` overrides inferred
coordinate identity. Without one, heatmap identity is column+row, timeline
identity is start+entity, and histogram identity is lower+upper. End/status/count
corrections retain marks when the key stays stable. Native exports snapshot the
current completed scene. Timeline labels, token-derived colors and matching
border dash styles distinguish nine or more statuses without color alone.

The catalog's boxplot and sparkline are explicitly static; publishing a file
live does not add a data-only adapter to an unsupported form.

## Live files

Publish supported chart files with `blerg-runner publish name.vl.json --live`.
A rows-only revision uses the mounted native adapter: chart, axes, legend and
surviving datum nodes stay in place. Color-only heatmap changes use the same
250 ms eased clock as positional changes. Reduced motion commits immediately.

Changes to title, encodings or configuration prepare a replacement away from
the visible chart and swap only when ready. Invalid policy input, ambiguous keys
or rejected native rows keep the previous complete drawing. A rejected data-only
changeset cannot bypass validation by recompiling into a new scene. The file tab
keeps the last committed chart's observation timestamp until a valid scene commits.

A chart without a native data adapter shows `Static snapshot · unsupported-live`
and its snapshot time. Both its preview and open tab stop reading later live-file
revisions. The source download for that snapshot uses the displayed bytes.
Native stat/gauge/text dashboard files may follow; a dashboard containing a
boxplot, static sparkline or fallback-only chart remains a static snapshot.

Heatmap values use the theme's sequential ramp (`--chat-chart-sequential-low` and
`--chat-chart-sequential-high`), resolved to at least 3:1 against the plot surface.
Native marks and Vega heatmap/ramp defaults share these resolved endpoints.
Integer observations use integer legend ticks. Full-tab text scaling is capped at
1.2× so large screens retain a quiet label hierarchy.

Timeline axes and temporal tooltips use UTC. Sub-minute axes include seconds;
both endpoints remain visible, staggering if their labels cannot share a row.
Open intervals have a terminal chevron as well as an open label/tooltip. A refused
rows-only revision is remembered; resize, theme and font changes still update the
retained valid model without retrying that rejected source. Runtime live support
is re-evaluated on each committed scene. Dashboard freshness advances only after
all its current cards commit; a rejected chart preserves the previous drawn time.
