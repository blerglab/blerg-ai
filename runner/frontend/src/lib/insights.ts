// The shape of GET /api/insights and the small formatters the Insights page uses. Pure.

export interface Quantiles {
  count: number
  p50: number
  p90: number
  max: number
}

/** StepSpread is one step of a start as a box: seconds, fastest to slowest; slow is how many starts took more than twice the median. */
export interface StepSpread {
  runtime: string
  step: string
  count: number
  min: number
  p25: number
  p50: number
  p75: number
  p90: number
  max: number
  slow: number
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
    steps: StepSpread[]
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

export type ModelTokens = TokenCounts & { model: string; priced: boolean }

/** The shape of GET /api/sessions/{id}/insights: one session's figures. */
export interface SessionInsightsResponse {
  session_id: string
  title: string
  kind: string
  status: string
  runtime: string
  engine: string
  model: string
  started_at: string
  ended_at: string | null
  generated_at: string
  lifetime_seconds: number
  running_seconds: number
  waiting_seconds: number
  idle_seconds: number
  turns: number
  tool_calls: number
  tool_calls_by_tool: CountRow[]
  messages_from_person: number
  subagents: { count: number; by_type: CountRow[]; by_model: CountRow[]; tool_calls: number }
  // null, with tokens_hidden, for anyone but the session's owner and an administrator
  tokens: {
    totals: TokenCounts
    main: TokenCounts
    subagents: TokenCounts
    by_model: ModelTokens[]
    cache_hit_rate: number
    unpriced_models: string[]
  } | null
  tokens_hidden?: boolean
  artifacts: number
  proposals: { pending: number; approved: number; declined: number; expired: number }
  mcp_calls: { total: number; by_outcome: CountRow[] }
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

/** TimeAxis places durations on one shared horizontal axis: pos maps seconds to 0..1. */
export interface TimeAxis {
  kind: 'linear' | 'log'
  ticks: number[]
  pos: (seconds: number) => number
}

// The tick spacings a linear axis picks from, in seconds, and the ticks a log axis may show.
const LINEAR_STEPS = [0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14_400, 43_200, 86_400]
const LOG_TICKS = [0.1, 0.5, 1, 5, 15, 60, 300, 900, 3600, 14_400, 86_400, 604_800]

/** LOG_RATIO: an axis goes log only when the slowest start is more than this many times the largest median. */
export const LOG_RATIO = 20

/**
 * timeAxis builds the axis for start steps. It is linear from zero, unless one start was so slow
 * (over LOG_RATIO times the largest step median) that a linear axis would squash every box into
 * its first few percent; then it is logarithmic, from the round tick under the fastest box. Values
 * outside the axis clamp to its ends.
 */
export function timeAxis(steps: Pick<StepSpread, 'min' | 'p25' | 'p50' | 'max'>[]): TimeAxis {
  const max = Math.max(0, ...steps.map((s) => s.max))
  const bigMedian = Math.max(0, ...steps.map((s) => s.p50))
  const clamp = (f: number) => (Number.isFinite(f) ? Math.min(1, Math.max(0, f)) : 0)
  if (bigMedian > 0 && max > LOG_RATIO * bigMedian) {
    const positive = steps.flatMap((s) => [s.min, s.p25, s.p50]).filter((v) => v > 0)
    const low = positive.length ? Math.min(...positive) : LOG_TICKS[0]
    const lo = [...LOG_TICKS].reverse().find((t) => t <= low) ?? LOG_TICKS[0]
    const hi = LOG_TICKS.find((t) => t >= max) ?? max
    let ticks = LOG_TICKS.filter((t) => t >= lo && t <= hi)
    if (ticks.length > 7) ticks = ticks.filter((_, i) => i % 2 === 0 || i === ticks.length - 1)
    const span = Math.log(hi) - Math.log(lo)
    return { kind: 'log', ticks, pos: (v) => (v <= lo ? 0 : clamp((Math.log(v) - Math.log(lo)) / span)) }
  }
  const step = LINEAR_STEPS.find((st) => Math.ceil(max / st) <= 6) ?? LINEAR_STEPS[LINEAR_STEPS.length - 1]
  const top = Math.max(step, Math.ceil(max / step) * step)
  const ticks: number[] = []
  for (let t = 0; t <= top + 1e-9; t += step) ticks.push(t)
  return { kind: 'linear', ticks, pos: (v) => clamp(v / top) }
}

/** formatTick labels an axis tick compactly: "0", "500 ms", "15 s", "1.5 min", "2 h". */
export function formatTick(seconds: number): string {
  const one = (n: number) => trimZeros(n.toFixed(1))
  if (seconds <= 0) return '0'
  if (seconds < 1) return `${Math.round(seconds * 1000)} ms`
  if (seconds < 60) return `${one(seconds)} s`
  if (seconds < 3600) return `${one(seconds / 60)} min`
  if (seconds < 86_400) return `${one(seconds / 3600)} h`
  return `${one(seconds / 86_400)} d`
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
