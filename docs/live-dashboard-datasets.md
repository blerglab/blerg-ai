# Live dashboard scalar datasets

Dashboard version 1 accepts named Blerg datasets on stat and gauge cards. Chart
and text cards still require inline rows. Live sparklines are refused as
unsupported-live; retained sparkline changesets belong to the renderer slice.

For a worker stat, use data {"kind":"dataset","name":"blerg.workers","parameters":{}}
and view {"value":"running","selection":"latest"}. For a usage gauge, use data
{"kind":"dataset","name":"blerg.engine.usage","parameters":{"engine":"codex"}}
and view {"value":"percent","selection":"latest","unit":"%","min":0,"max":100}.

| Dataset | Parameters | Row fields | Visibility and refresh |
|---|---|---|---|
| blerg.workers | none | key, running, idle | Visible descendants in the session's workspace ancestry; each worker passes session privacy before aggregation. Session changes trigger refresh; 15-second reconciliation. |
| blerg.engine.usage | engine: claude, codex, or hermes | key, percent, reset_at | Current desktop owner only; daemon's measured 7d window and fetch timestamp. No report means unknown, never zero. Reports trigger refresh. |
| blerg.cards.merged | bucket: day or hour; IANA timezone; optional release, base, member | key, bucket, end, count, release | Session owner and current authorized repository/daemon only. Initial snapshot and History marks refresh Git. No constant Git polling. |

A release requires an explicit base tag/revision; the range is base..refs/heads/main.
A missing or unrelated boundary is unavailable, never an empty release. The
member parameter selects a member of a multi-repository workspace. Release labels
alone never infer membership. Without a release/base, the dataset covers bounded
main history.

Merge coverage is **first-parent Git merge commits on main**, keyed by SHA and
timestamped by committer date. Squash and fast-forward commits are excluded.
Missing days in retained history are not synthesized; the current bucket has an
observed zero when no merge occurred. Bucket boundaries are UTC, calculated in
the declared timezone, including repeated DST hours and 23/25-hour days. At most
10,000 merge commits are accepted; excessive history is unavailable rather than
silently truncated.

## Delivery and failures

The authenticated browser socket accepts subscribe_dataset, unsubscribe_dataset,
dataset_heartbeat, and allow-listed dataset_diagnostic causes. A subscription
names the session, an opaque UUID, and the validated dataset reference. It grants
no permission by itself. No SQL, URL loaders, filesystem paths, executable
expressions, or credentials belong in a dashboard.

A dataset_delta carries an epoch, base/next sequence, keyed upserts/removals,
observation time, status and snapshot flag. New connections rebuild from the
source and start a new epoch; no payload cache survives restart. A sequence gap
requests an authorized snapshot, reconciled in the mounted card. Values and
meter indicators retain their nodes. Motion lasts 180 ms and is disabled under
reduced motion. Inline chart cards share the card surface, use one clock format,
and keep day labels horizontal. Temporal domains spanning days show dates. Usage
becomes delayed after eleven minutes; merge cards say Observed and preserve the
actual Git-read timestamp while history is cached.

Only intersecting cards in visible documents/views subscribe. A 30-second
heartbeat renews a 60-second lease. Closing or hiding a view stops demand and
retains its last authorized scalar sample. Each browser has at most 32 subscriptions;
refresh concurrency is bounded to two, with one-second event coalescing and
bounded exponential retries, waiting for a refresh slot with a context instead of refusing a busy dashboard. Unknown/transient failure retains only the last
authorized sample and its true observation time. Disconnection and delayed data
are labeled. Session/desktop authority is rechecked at collection and queued
delivery; revocation purges values. Account changes purge subscriptions and rows.

Migration 151 records subscription open/close events, recording only owner,
session, source, timing and outcome. Refreshes do not write audit rows. Audit
metadata is pruned hourly after 180 days. Dataset rows are record-derived, rebuildable, memory-only snapshots,
not durable external provider payloads. Failures use rate-limited typed cause
logs; browser diagnostics use the same authenticated socket and fixed codes.

This slice does not implement Board event feeds, session-less external provider
grants, MCP polling, or live chart/sparkline rendering.

Worker lifecycle triggers are workspace-scoped, usage triggers are desktop-scoped,
and merge marks invalidate their own workspace only. Transcript broadcasts do not
refresh datasets. Worker queries read only descendants of the selected workspace.
