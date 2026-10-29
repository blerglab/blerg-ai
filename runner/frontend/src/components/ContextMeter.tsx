// The context meter (card #191): how full a session's context window is, from its latest turn's
// report (lib/liveUsage contextMeter). On a Running now row it is a small glyph beside the token
// chip, filled to the share in use: "62%", amber near compaction, ⇊ when the engine compacted in
// that turn. In the Session section it is a line with a bar. Its words are in the tooltip.
import type { CSSProperties } from 'react'
import { RunGlyph } from './RunCard'
import type { ContextMeter } from '../lib/liveUsage'

/** The compacted marker's glyph. */
export const COMPACTED_GLYPH = '⇊'

const fill = (m: ContextMeter): CSSProperties => ({ '--ctx-fill': `${m.percent ?? 0}%` } as CSSProperties)

export function ContextGlyph({ meter, testId }: { meter: ContextMeter; testId: string }) {
  return (
    <RunGlyph label={meter.label} testId={testId} style={fill(meter)}
      className={`context${meter.warn ? ' warn' : ''}${meter.compacted ? ' compacted' : ''}`}>
      {meter.compacted && <span className="ctx-mark" data-testid={`${testId}-compacted`}>{COMPACTED_GLYPH}</span>}{meter.text}
    </RunGlyph>
  )
}

export function ContextFact({ meter }: { meter: ContextMeter }) {
  return (
    <p className={`rail-context${meter.warn ? ' warn' : ''}`} data-testid="rail-context" title={meter.label}>
      <span className="rail-fact-label">Context</span>{' '}
      {meter.percent !== null && <span className="rail-context-bar" role="meter" aria-label="Context in use" aria-valuemin={0} aria-valuemax={100} aria-valuenow={meter.percent}
        aria-valuetext={meter.label.split('\n')[0]} style={fill(meter)} />}
      <span className="rail-context-text">{meter.percent !== null ? `${meter.text} · ${meter.size}` : 'compacted, size known after the next turn'}</span>
      {meter.compacted && meter.percent !== null && <span className="rail-context-compacted" data-testid="rail-context-compacted">{COMPACTED_GLYPH} compacted</span>}
    </p>
  )
}
