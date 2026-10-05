// The shape of GET /api/insights and the small formatters the Insights page uses. Pure.

export interface Quantiles {
  count: number
  p50: number
  p90: number
  max: number
}

export interface CountRow {
  key: string
  count: number
}

export interface TokenCounts {
  turns: number
  input: number
  output: number
  cache_read: number
  cache_write: number
  cost_usd: number | null
}

export interface InsightsResponse {
  range: { label: string; from: string; to: string }
  scope: 'all' | 'mine'
  is_admin: boolean
  generated_at: string
  sessions: {
    started: number
    ended: number
    alive: number
    by_runtime: CountRow[]
    by_outcome: CountRow[]
    lifetime_seconds: Quantiles
    running_seconds: number
    waiting_seconds: number
    idle_seconds: number
    busy_fraction: number
    by_day: { day: string; started: number }[]
  }
  startup: {
    groups: { runtime: string; kind: 'start' | 'resume'; attempts: number; failed: number; seconds: Quantiles }[]
    steps: { runtime: string; step: string; count: number; p50: number; p90: number }[]
    failures: CountRow[]
    slowest: { session_id: string; title: string; runtime: string; seconds: number; at: string }[]
  }
  tokens: {
    totals: TokenCounts
    cache_hit_rate: number
    by_model: (TokenCounts & { model: string; priced: boolean })[]
    daily: (TokenCounts & { day: string })[]
    top_sessions: (TokenCounts & { session_id: string; title: string; runtime: string; started_at: string })[]
    unpriced_models: string[]
  }
  cluster: { active: number; max: number; peak_concurrent: number } | null
  crons: { runs: number; late: number; manual: number; by_status: CountRow[]; seconds: Quantiles }
}

export interface ModelPrice {
  model: string
  input_per_mtok: number
  output_per_mtok: number
  cache_read_per_mtok: number
  cache_write_per_mtok: number
}

export const RANGES = ['24h', '7d', '30d', '90d'] as const
export type RangeLabel = (typeof RANGES)[number]

/** formatDuration renders seconds as a short human duration: "850 ms", "42 s", "3 min 20 s", "2 h 5 min", "3 d 4 h". */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  if (seconds < 1) return `${Math.round(seconds * 1000)} ms`
  if (seconds < 60) return `${seconds < 10 ? seconds.toFixed(1).replace(/\.0$/, '') : Math.round(seconds)} s`
  const s = Math.round(seconds)
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const r = s % 60
    return r ? `${m} min ${r} s` : `${m} min`
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    const m = Math.round((s % 3600) / 60)
    return m ? `${h} h ${m} min` : `${h} h`
  }
  const d = Math.floor(s / 86400)
  const h = Math.round((s % 86400) / 3600)
  return h ? `${d} d ${h} h` : `${d} d`
}

// trimZeros drops trailing zeros after a decimal point only: "4.60" -> "4.6", "440" stays "440".
const trimZeros = (s: string) => (s.includes('.') ? s.replace(/\.?0+$/, '') : s)

/** formatTokens abbreviates a token count: 950, 12.3k, 440k, 4.6M, 1.2B. */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n)) return '—'
  const abs = Math.abs(n)
  if (abs < 1000) return String(Math.round(n))
  if (abs < 1_000_000) return `${trimZeros((n / 1000).toFixed(abs < 10_000 ? 2 : abs < 100_000 ? 1 : 0))}k`
  if (abs < 1_000_000_000) return `${trimZeros((n / 1_000_000).toFixed(abs < 10_000_000 ? 2 : abs < 100_000_000 ? 1 : 0))}M`
  return `${trimZeros((n / 1_000_000_000).toFixed(2))}B`
}

/** formatUSD renders an estimate: cents under $100, whole dollars above, "—" when there is none. */
export function formatUSD(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  if (v < 0.01 && v > 0) return '< $0.01'
  if (v >= 100) return `$${Math.round(v).toLocaleString('en-US')}`
  return `$${v.toFixed(2)}`
}

/** formatPercent renders a 0..1 fraction. */
export function formatPercent(f: number): string {
  if (!Number.isFinite(f)) return '—'
  return `${Math.round(f * 100)}%`
}

const STEP_LABELS: Record<string, string> = {
  queued: 'Queued',
  schedule: 'Scheduling',
  image: 'Pulling image',
  connect: 'Connecting',
  clone: 'Cloning repo',
  plugins: 'Plugins',
  engine: 'Starting agent',
}

/** stepLabel names a start step for people. */
export function stepLabel(step: string): string {
  return STEP_LABELS[step] ?? step
}

const OUTCOME_LABELS: Record<string, string> = {
  stopped_by_user: 'Ended by a person',
  not_resumed: 'Not resumed in time',
  stopped: 'Stopped',
  error: 'Failed',
  ended: 'Ended',
  idle_timeout: 'Idle timeout',
}

/** outcomeLabel names how a session ended. */
export function outcomeLabel(key: string): string {
  return OUTCOME_LABELS[key] ?? key.replace(/_/g, ' ')
}

/** shortDay renders "2026-10-01" as "Oct 1". */
export function shortDay(day: string): string {
  const d = new Date(`${day}T00:00:00Z`)
  if (Number.isNaN(d.getTime())) return day
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}
