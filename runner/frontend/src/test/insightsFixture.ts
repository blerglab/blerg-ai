// A rich report for the Insights page, every section populated, with the long names that stress a
// narrow screen: shared by the page preview and the real-browser layout test.
import type { InsightsResponse, ModelPrice, StepSpread } from '../lib/insights'

const q = (p50: number, p90: number, max: number, count: number) => ({ count, p50, p90, max })
const tok = (turns: number, input: number, output: number, cache_read: number, cache_write: number, cost_usd: number | null) =>
  ({ turns, input, output, cache_read, cache_write, cost_usd })
const step = (step: string, count: number, min: number, p25: number, p50: number, p75: number, p90: number, max: number, slow: number): StepSpread =>
  ({ runtime: 'cluster', step, count, min, p25, p50, p75, p90, max, slow })

const days = Array.from({ length: 14 }, (_, i) => {
  const day = `2026-10-${String(i + 1).padStart(2, '0')}`
  const k = 1 + ((i * 7) % 5)
  return { day, ...tok(20 * k, 9_000 * k, 61_000 * k, 4_100_000 * k, 160_000 * k, 1.9 * k) }
})

export const richInsights: InsightsResponse = {
  range: { label: '30d', from: '2026-09-14T00:00:00Z', to: '2026-10-14T00:00:00Z' },
  scope: 'all',
  is_admin: true,
  generated_at: '2026-10-14T12:00:00Z',
  sessions: {
    started: 184, ended: 171, alive: 6,
    by_runtime: [{ key: 'cluster', count: 151 }, { key: 'daemon', count: 33 }],
    by_outcome: [{ key: 'stopped_by_user', count: 92 }, { key: 'not_resumed', count: 61 }, { key: 'error', count: 18 }],
    lifetime_seconds: q(9_360, 41_000, 190_000, 171),
    running_seconds: 402_000, waiting_seconds: 61_000, idle_seconds: 730_000, busy_fraction: 0.34,
    by_day: days.map((d) => ({ day: d.day, started: d.turns / 8 })),
  },
  startup: {
    groups: [
      { runtime: 'cluster', kind: 'start', attempts: 151, failed: 4, seconds: q(42.3, 110.7, 1_490, 147) },
      { runtime: 'cluster', kind: 'resume', attempts: 63, failed: 1, seconds: q(18.2, 31.4, 95, 62) },
      { runtime: 'daemon', kind: 'start', attempts: 33, failed: 0, seconds: q(3.1, 5.8, 12, 33) },
    ],
    steps: [
      step('queued', 147, 0, 0.1, 0.2, 0.4, 0.9, 2.1, 6),
      step('schedule', 147, 0.6, 0.9, 1.2, 1.8, 3.1, 7, 4),
      step('image', 147, 1.1, 1.6, 2.4, 4.5, 31, 1_490, 7),
      step('clone', 147, 4, 7, 11, 16, 24, 48, 2),
      step('plugins', 147, 2, 3, 4, 6, 9, 14, 1),
      step('engine', 3, 5, 5.5, 6, 9, 11.4, 12, 0),
    ],
    failures: [{ key: 'image', count: 3 }, { key: 'clone', count: 1 }],
    slowest: [
      { session_id: 'sess-slow-1', title: 'Refactor the session store so a daemon restart keeps every agent session alive', runtime: 'cluster', seconds: 1_490, at: '2026-10-09T10:00:00Z' },
      { session_id: 'sess-slow-2', title: 'Board: drag a card between columns on a phone', runtime: 'cluster', seconds: 312, at: '2026-10-11T10:00:00Z' },
    ],
  },
  tokens: {
    totals: tok(2_310, 512_000, 3_420_000, 241_000_000, 9_800_000, 412.37),
    cache_hit_rate: 0.96,
    by_model: [
      { model: 'claude-opus-5-5', priced: true, ...tok(1_402, 401_000, 2_610_000, 188_000_000, 7_400_000, 371.2) },
      { model: 'claude-sonnet-5', priced: true, ...tok(711, 96_000, 690_000, 47_000_000, 2_100_000, 41.17) },
      { model: 'claude-haiku-5-20261001-experimental', priced: false, ...tok(197, 15_000, 120_000, 6_000_000, 300_000, null) },
    ],
    daily: days,
    top_sessions: [
      { session_id: 'sess-top-1', title: 'Refactor the session store so a daemon restart keeps every agent session alive', runtime: 'cluster', started_at: '2026-10-09T09:00:00Z', ...tok(212, 41_000, 262_000, 15_900_000, 940_000, 29.1) },
      { session_id: 'sess-top-2', title: 'Postgres for managed apps', runtime: 'daemon', started_at: '2026-10-10T09:00:00Z', ...tok(140, 30_000, 190_000, 11_000_000, 610_000, 21.4) },
    ],
    unpriced_models: ['claude-haiku-5-20261001-experimental'],
  },
  cluster: { active: 6, max: 12, peak_concurrent: 11 },
  crons: { runs: 58, late: 2, manual: 3, by_status: [{ key: 'started', count: 55 }, { key: 'failed', count: 3 }], seconds: q(300, 600, 900, 58) },
}

export const richPrices: ModelPrice[] = [
  { model: 'claude-opus-5-5', input_per_mtok: 15, output_per_mtok: 75, cache_read_per_mtok: 1.5, cache_write_per_mtok: 18.75 },
  { model: 'claude-sonnet-5', input_per_mtok: 3, output_per_mtok: 15, cache_read_per_mtok: 0.3, cache_write_per_mtok: 3.75 },
]
