import { useEffect, useId, useRef, useState } from 'react'
import '../IconButton.css'
import './TimeChart.css'
import './BudgetBurndown.css'
import type { BurndownSeries } from '../../lib/budgetBurndown'

// Spending's weekly-budget burndown (BLG-462): percent of a subscription plan's weekly usage
// window, read against an even pace and a projection, with last week faint for comparison. Shares
// TimeChart's visual language (grid, ticks, figure/figcaption) so the two modes of the same
// Spending surface read as one chart, not two looks.

// The same first two colors TimeChart assigns 'claude' and 'codex' in the dollar view, so a
// provider keeps its color whether Spending is showing it in dollars or in percent.
const PROVIDER_COLOR: Record<string, string> = { claude: 'var(--chart-git)', codex: 'var(--chart-read)' }
const PROVIDER_NAMES: Record<string, string> = { claude: 'Claude', codex: 'Codex' }

function dateLabel(at: number): string {
  return new Date(at).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', timeZone: 'UTC' })
}

function pathFor(points: { at: number; value: number }[], x: (t: number) => number, y: (v: number) => number): string {
  return points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.at)},${y(p.value)}`).join(' ')
}

export default function BudgetBurndown({ series, now: nowProp }: { series: BurndownSeries; now?: number }) {
  // The clock is read once, when the chart mounts, never during a render.
  const [mountedAt] = useState(() => Date.now())
  const now = nowProp ?? mountedAt
  const id = useId()
  const ref = useRef<HTMLElement>(null)
  const [width, setWidth] = useState(700)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const update = () => setWidth(Math.max(280, el.clientWidth))
    update()
    const observer = new ResizeObserver(update)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const left = 56, right = 14, top = 18, bottom = 220, height = 240
  const span = Math.max(1, series.resetsAt - series.windowStart)
  const x = (t: number) => left + ((t - series.windowStart) / span) * (width - left - right)
  const y = (v: number) => bottom - (Math.max(0, Math.min(100, v)) / 100) * (bottom - top)
  const provider = series.provider
  const color = PROVIDER_COLOR[provider] ?? 'var(--chart-build)'
  const name = PROVIDER_NAMES[provider] ?? provider
  const nowClamped = Math.max(series.windowStart, Math.min(series.resetsAt, now))
  const showNow = now >= series.windowStart && now <= series.resetsAt
  const lastActual = series.actual.at(-1)?.points.at(-1)
  const label = `${name} weekly budget`

  return <figure ref={ref} className="time-chart budget-burndown" aria-label={label} data-testid={`budget-burndown-${provider}`}>
    <div className="time-chart-key" role="group" aria-label={`${label} key`}>
      <span className="time-chart-key-item"><i style={{ background: color }} />Actual</span>
      <span className="time-chart-key-item"><i className="budget-burndown-key-pace" />Even pace</span>
      {series.projection && <span className="time-chart-key-item"><i className={series.projection.exceeds ? 'budget-burndown-key-over' : 'budget-burndown-key-projection'} />Projected</span>}
      {series.previous && <span className="time-chart-key-item"><i className="budget-burndown-key-previous" />Last week</span>}
    </div>
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={id}>
      <title id={id}>{label}: {lastActual ? `${Math.round(lastActual.value)}% used` : 'no reading yet'} of the window ending {dateLabel(series.resetsAt)}.
        {series.projection?.exceeds && series.projection.crossingAt && ` Projected to reach 100% by ${dateLabel(series.projection.crossingAt)}, before reset.`}
      </title>
      {[0, 50, 100].map(pct => <g key={pct}>
        <line className="time-chart-grid" x1={left} x2={width - right} y1={y(pct)} y2={y(pct)} />
        <text x={left - 10} y={y(pct) + 4} textAnchor="end">{pct}%</text>
      </g>)}
      <text x={left} y={bottom + 20}>{dateLabel(series.windowStart)}</text>
      <text x={width - right} y={bottom + 20} textAnchor="end">{dateLabel(series.resetsAt)}</text>
      {series.previous && series.previous.map((seg, i) => seg.points.length>1
        ? <path key={`prev-${i}`} className="budget-burndown-previous" d={pathFor(seg.points, x, y)} fill="none" />
        : <circle key={`prev-${i}`} className="budget-burndown-previous-dot" cx={x(seg.points[0].at)} cy={y(seg.points[0].value)} r={2.5} />)}
      <path className="budget-burndown-pace" d={pathFor(series.pace, x, y)} fill="none" />
      {series.actual.map((seg, i) => seg.points.length>1
        ? <path key={`actual-${i}`} d={pathFor(seg.points, x, y)} fill="none" stroke={color} strokeWidth={2} />
        : <circle key={`actual-${i}`} cx={x(seg.points[0].at)} cy={y(seg.points[0].value)} r={3} fill={color} />)}
      {series.projection && <path className={series.projection.exceeds ? 'budget-burndown-over' : 'budget-burndown-projection'} d={pathFor(series.projection.points, x, y)} fill="none" />}
      {series.projection?.exceeds && series.projection.crossingAt && <circle className="budget-burndown-over-dot" cx={x(series.projection.crossingAt)} cy={y(100)} r={4} />}
      {showNow && <line className="time-chart-today" x1={x(nowClamped)} x2={x(nowClamped)} y1={top} y2={bottom} />}
    </svg>
    <figcaption>
      {lastActual ? `${Math.round(lastActual.value)}% used as of ${dateLabel(lastActual.at)}` : 'No reading yet this window'}.{' '}
      {series.projection
        ? series.projection.exceeds && series.projection.crossingAt
          ? `At this pace, the budget runs out around ${dateLabel(series.projection.crossingAt)}, before the ${dateLabel(series.resetsAt)} reset.`
          : 'On pace to stay within the weekly budget.'
        : 'Needs more than an hour of readings this window to project a pace.'}
      {series.previous && ' Last week is shown faintly for comparison.'}
    </figcaption>
  </figure>
}
