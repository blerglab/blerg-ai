import type { UsageBudgetSample } from './insights'

// Spending's burndown (BLG-462): a weekly usage window read as a percent-of-budget trace rather
// than dollars. Built purely from usage_budget samples (InsightsResponse), so it can be unit
// tested without rendering.

export const WEEK = 7 * 24 * 3600_000
// The daemon samples every ~10 minutes (plus turn-triggered reads); a gap over this is a real gap
// in reporting, not routine jitter between two 10-minute polls.
const SAMPLE_GAP_MS = 25 * 60_000
// Projecting on under an hour of evidence is noise, matching the existing weekly-pace text.
const MIN_PROJECTION_EVIDENCE_MS = 3600_000

export interface BurndownPoint { at: number; value: number }
export interface BurndownSegment { points: BurndownPoint[] }
export interface BurndownProjection { points: BurndownPoint[]; exceeds: boolean; crossingAt?: number }

export interface BurndownSeries {
  provider: string
  windowStart: number
  resetsAt: number
  /** The window's actual trace, split into segments at any gap where usage wasn't sampled. */
  actual: BurndownSegment[]
  /** The even-pace reference: 0% at window start, 100% at reset. */
  pace: BurndownPoint[]
  /** Projected at the current rate from the latest sample; null with under an hour of evidence. */
  projection: BurndownProjection | null
  /** The previous window's trace, shifted forward by one window length so it overlays the
   *  current window's timeline; null with no previous window recorded. */
  previous: BurndownSegment[] | null
}

function splitGaps(points: BurndownPoint[]): BurndownSegment[] {
  if (!points.length) return []
  const segments: BurndownSegment[] = [{ points: [points[0]] }]
  for (let i = 1; i < points.length; i++) {
    if (points[i].at - points[i - 1].at > SAMPLE_GAP_MS) segments.push({ points: [points[i]] })
    else segments[segments.length - 1].points.push(points[i])
  }
  return segments
}

/** Builds provider's burndown from the samples carried on InsightsResponse.usage_budget (every
 *  provider's weekly window together); null when provider has reported none (a metered engine,
 *  or one whose daemon has not reported a weekly window). */
export function buildBurndown(samples: UsageBudgetSample[], provider: string): BurndownSeries | null {
  const rows = samples.filter(s => s.provider === provider && s.window_id === '7d')
  if (!rows.length) return null
  const byStart = new Map<number, UsageBudgetSample[]>()
  for (const r of rows) {
    const start = Date.parse(r.window_start)
    if (!Number.isFinite(start)) continue
    if (!byStart.has(start)) byStart.set(start, [])
    byStart.get(start)!.push(r)
  }
  const starts = [...byStart.keys()].sort((a, b) => b - a)
  if (!starts.length) return null
  const currentStart = starts[0]
  const currentRows = byStart.get(currentStart)!.slice().sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at))
  const resetsAt = Date.parse(currentRows[0].resets_at)
  const actualPoints = currentRows.map(r => ({ at: Date.parse(r.sampled_at), value: r.used_percent }))
  const pace: BurndownPoint[] = [{ at: currentStart, value: 0 }, { at: resetsAt, value: 100 }]

  const last = actualPoints[actualPoints.length - 1]
  const elapsed = last.at - currentStart
  let projection: BurndownProjection | null = null
  if (elapsed >= MIN_PROJECTION_EVIDENCE_MS && last.value > 0) {
    const rate = last.value / elapsed // percent per ms
    const projectedAtReset = rate * (resetsAt - currentStart)
    const exceeds = projectedAtReset > 100
    const crossingAt = exceeds ? currentStart + 100 / rate : undefined
    const endAt = exceeds ? crossingAt! : resetsAt
    const endValue = exceeds ? 100 : projectedAtReset
    projection = { points: [{ at: last.at, value: last.value }, { at: endAt, value: endValue }], exceeds, crossingAt }
  }

  let previous: BurndownSegment[] | null = null
  const previousStart = starts[1]
  if (previousStart !== undefined) {
    const shift = currentStart - previousStart
    const prevRows = byStart.get(previousStart)!.slice().sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at))
    previous = splitGaps(prevRows.map(r => ({ at: Date.parse(r.sampled_at) + shift, value: r.used_percent })))
  }

  return { provider, windowStart: currentStart, resetsAt, actual: splitGaps(actualPoints), pace, projection, previous }
}
