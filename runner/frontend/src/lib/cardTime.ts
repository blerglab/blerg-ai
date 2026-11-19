// A card's worker time and review-round summary (BLG-444). The glyph that reads this is shown
// only once a card has crossed its first budget multiple; a card under budget never calls these
// with a summary at all ("no linked workers shows nothing, not zero" extended to "under budget
// shows nothing, not 0h"). Pure formatting, no DOM, so it is testable on its own
// (cardTime.test.ts) separate from the React rendering in SessionRail/PlanChecklist.
export interface CardTimeSummary {
  /** Worker time, exactly what Insights calls card active time (owner's decision 1). */
  seconds: number
  /** Count of distinct accepted review marks, never max(round) (L8). */
  rounds: number
  /** The latest round's verdict only, not cumulative across rounds (owner's decision 2). */
  verdict?: string
  openHigh?: number
  openMedium?: number
}

/** "4h", "6h42m", "45m" — whole minutes, no seconds (nobody needs second-level budget precision). */
export function formatCardTimeDuration(seconds: number): string {
  const totalMinutes = Math.round(Math.max(0, seconds) / 60)
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours === 0) return `${minutes}m`
  if (minutes === 0) return `${hours}h`
  return `${hours}h${minutes}m`
}

const VERDICT_WORDS: Record<string, string> = { changes: 'changes requested', approved: 'approved', rejected: 'rejected' }

/**
 * The glyph's title/aria-label text: total time, round count, latest verdict, and open high/medium
 * finding counts — in that order, each clause present only when it has something to say.
 */
export function cardTimeGlyphLabel(summary: CardTimeSummary | null | undefined): string | null {
  if (!summary) return null
  const parts = [`${formatCardTimeDuration(summary.seconds)} worker time`]
  if (summary.rounds > 0) {
    parts.push(`${summary.rounds} review round${summary.rounds === 1 ? '' : 's'}`)
  }
  if (summary.verdict) {
    parts.push(VERDICT_WORDS[summary.verdict] ?? summary.verdict)
  }
  const high = summary.openHigh ?? 0
  const medium = summary.openMedium ?? 0
  if (high > 0 || medium > 0) {
    const findings: string[] = []
    if (high > 0) findings.push(`${high} high`)
    if (medium > 0) findings.push(`${medium} medium`)
    parts.push(`${findings.join(', ')} open`)
  }
  return parts.join(' · ')
}
