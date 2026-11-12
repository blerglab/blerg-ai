import type { ContextFillReading } from './insights'

export interface ContextFillPoint { at: string; percent: number; compacted: boolean }

/** One conversation's own context readings (BLG-464 round 2): a single workspace-wide line mixed
 *  every conversation's readings into one daily maximum, so any one conversation's compaction made
 *  the whole workspace look full. Grouping by session keeps each conversation's own story legible,
 *  using its real turn_done points, not a bucketed maximum. */
export interface ContextFillSeries { sessionId: string; title: string; role: ContextFillReading['role']; points: ContextFillPoint[] }

/** Splits readings into one series per conversation, restricted to [from, to) and sorted
 *  chronologically within each conversation (the backend's own ordering already keeps this true,
 *  but a caller should not depend on that going forward). */
export function contextFillSeries(readings: ContextFillReading[], from: string, to: string): ContextFillSeries[] {
  const start = Date.parse(from), end = Date.parse(to)
  const bySession = new Map<string, ContextFillSeries>()
  for (const r of readings) {
    const t = Date.parse(r.at)
    if (t < start || t >= end) continue
    let s = bySession.get(r.session_id)
    if (!s) { s = { sessionId: r.session_id, title: r.title, role: r.role, points: [] }; bySession.set(r.session_id, s) }
    s.points.push({ at: r.at, percent: Math.min(100, Math.max(0, r.percent)), compacted: r.compacted })
  }
  for (const s of bySession.values()) s.points.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
  return [...bySession.values()]
}
