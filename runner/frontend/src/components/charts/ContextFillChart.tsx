import { useEffect, useId, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import '../IconButton.css'
import './TimeChart.css'
import './ContextFillChart.css'
import type { ContextFillSeries } from '../../lib/contextFill'
import { useExpand } from '../../hooks/useExpand'
import { ShowAllButton } from '../InsightsExpand'
import { COMPACTED_GLYPH } from '../ContextMeter'

const OTHER_LIMIT = 5

// BLG-463/BLG-464 round 2: one line per conversation, not a single workspace-wide maximum (the
// first version's single line attributed any conversation's compaction to the whole workspace, so
// anyone's compaction made the whole workspace look full). Main is emphasized in the accent token;
// channels and workers are subdued and capped to the fullest few, the rest behind the same
// cap-and-expand pattern as every other Insights list (useExpand/ShowAllButton). Hovering or
// focusing a line or its name shows that conversation's own latest reading and time; its name
// opens the conversation.

function dateLabel(at: number, hourly: boolean): string {
 return new Date(at).toLocaleString([], hourly ? { hour: 'numeric', timeZone: 'UTC' } : { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

function pathFor(points: { at: number; percent: number }[], x: (t: number) => number, y: (v: number) => number): string {
 return points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.at)},${y(p.percent)}`).join(' ')
}

// The workspace's Main conversation is named "Main" everywhere visible on the chart, whatever its
// own title is (round 2: naming it by title made the chart read like a conversation about itself,
// and repeated a long title twice in the summary sentence). Its title still identifies it, but
// only in a tooltip, the same way a long channel or worker title is truncated visually and kept
// in full in its own tooltip.
function displayName(s: ContextFillSeries): string {
 return s.role === 'main' ? 'Main' : s.title || s.sessionId.slice(0, 8)
}

// The native-tooltip/aria-label name: Main's actual title, when it has one; otherwise the same
// short name shown on screen.
function tooltipName(s: ContextFillSeries): string {
 return s.role === 'main' && s.title ? `Main — ${s.title}` : displayName(s)
}

function readingLabel(name: string, s: ContextFillSeries, hourly: boolean): string {
 const last = s.points.at(-1)
 return last ? `${name}: ${Math.round(last.percent)}% as of ${dateLabel(Date.parse(last.at), hourly)}` : name
}

export default function ContextFillChart({ series, from, to, hourly }: { series: ContextFillSeries[]; from: string; to: string; hourly: boolean }) {
 const id = useId()
 const ref = useRef<HTMLElement>(null)
 const [width, setWidth] = useState(700)
 const [hovered, setHovered] = useState<string | null>(null)
 useEffect(() => {
  const el = ref.current
  if (!el) return
  const update = () => setWidth(Math.max(280, el.clientWidth))
  update()
  const observer = new ResizeObserver(update)
  observer.observe(el)
  return () => observer.disconnect()
 }, [])

 const main = series.find(s => s.role === 'main')
 const others = series.filter(s => s.role !== 'main')
 const peak = (s: ContextFillSeries) => s.points.reduce((m, p) => Math.max(m, p.percent), -1)
 const sortedOthers = [...others].sort((a, b) => peak(b) - peak(a))
 const expand = useExpand(sortedOthers.length, OTHER_LIMIT)
 const shownOthers = sortedOthers.slice(0, expand.shown)
 const shown = main ? [main, ...shownOthers] : shownOthers
 const hoverOf = (sessionId: string) => ({
  onMouseEnter: () => setHovered(sessionId), onFocus: () => setHovered(sessionId),
  onMouseLeave: () => setHovered(h => (h === sessionId ? null : h)), onBlur: () => setHovered(h => (h === sessionId ? null : h)),
 })

 const left = 56, right = 14, top = 18
 const height = Math.min(420, Math.max(240, width * 0.28))
 const bottom = height - 20
 const start = Date.parse(from), end = Date.parse(to)
 const span = Math.max(1, end - start)
 const x = (t: number) => left + ((t - start) / span) * (width - left - right)
 const y = (v: number) => bottom - (Math.max(0, Math.min(100, v)) / 100) * (bottom - top)
 const tickCount = width < 500 ? 4 : 7
 const ticks = Array.from({ length: tickCount }, (_, i) => start + (span * i) / (tickCount - 1))

 const totalCompactions = series.reduce((n, s) => n + s.points.filter(p => p.compacted).length, 0)
 const mainCompactions = main ? main.points.filter(p => p.compacted).length : 0
 const mainLast = main?.points.at(-1)
 const head = mainLast
  ? `Main is ${Math.round(mainLast.percent)}% full as of ${dateLabel(Date.parse(mainLast.at), hourly)}.`
  : 'No readings for Main in this period.'
 const compactionClause = totalCompactions === 0
  ? 'No compactions were recorded in this period.'
  : `${totalCompactions} compaction${totalCompactions === 1 ? '' : 's'} in this period${mainCompactions > 0 ? `, ${mainCompactions} in Main` : ''}.`
 const summary = series.length === 0 ? 'No reading yet this period.' : `${head} ${compactionClause}`

 const label = 'Context fill'
 const hoveredSeries = shown.find(s => s.sessionId === hovered)

 return <figure ref={ref} className="time-chart context-fill-chart" aria-label={label}>
  <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={id}>
   <title id={id}>{label}: {summary}</title>
   {[0, 50, 100].map(pct => <g key={pct}>
    <line className="time-chart-grid" x1={left} x2={width - right} y1={y(pct)} y2={y(pct)} />
    <text x={left - 10} y={y(pct) + 4} textAnchor="end">{pct}%</text>
   </g>)}
   {ticks.map((t, i) => <text key={i} x={x(t)} y={bottom + 20} textAnchor="middle">{dateLabel(t, hourly)}</text>)}
   {shown.map(s => {
    const points = s.points.map(p => ({ at: Date.parse(p.at), percent: p.percent }))
    const a11yLabel = readingLabel(tooltipName(s), s, hourly)
    return <g key={s.sessionId}
     className={`context-fill-series ${s.role === 'main' ? 'context-fill-series-main' : 'context-fill-series-other'}${hovered === s.sessionId ? ' context-fill-series-hovered' : ''}`}
     tabIndex={0} aria-label={a11yLabel} {...hoverOf(s.sessionId)}>
     <title>{a11yLabel}</title>
     {points.length > 1
      ? <>
       <path className="context-fill-hit" d={pathFor(points, x, y)} fill="none" />
       <path className="context-fill-line" d={pathFor(points, x, y)} fill="none" />
      </>
      : <circle className="context-fill-dot" cx={x(points[0].at)} cy={y(points[0].percent)} r={3} />}
     {s.points.filter(p => p.compacted).map(p => <g key={p.at} className="context-fill-compacted">
      <circle cx={x(Date.parse(p.at))} cy={y(p.percent)} r={4} />
      <text x={x(Date.parse(p.at))} y={y(p.percent) - 10} textAnchor="middle" aria-hidden="true">{COMPACTED_GLYPH}</text>
     </g>)}
    </g>
   })}
  </svg>
  <figcaption>{summary}</figcaption>
  {hoveredSeries && <p className="context-fill-hover" role="status">{readingLabel(displayName(hoveredSeries), hoveredSeries, hourly)}</p>}
  {shown.length > 0 && <ul id={expand.id} className="context-fill-legend">
   {main && <li className="context-fill-legend-main">
    <i className="context-fill-swatch context-fill-swatch-main" aria-hidden="true" />
    <Link to={`/sessions/${main.sessionId}`} title={tooltipName(main)} {...hoverOf(main.sessionId)}>{displayName(main)}</Link>
   </li>}
   {shownOthers.map(s => <li key={s.sessionId}>
    <i className="context-fill-swatch context-fill-swatch-other" aria-hidden="true" />
    <Link to={`/sessions/${s.sessionId}`} title={tooltipName(s)} {...hoverOf(s.sessionId)}>{displayName(s)}</Link>
   </li>)}
  </ul>}
  {expand.hasMore && <ShowAllButton id={expand.id} total={sortedOthers.length} limit={OTHER_LIMIT} expanded={expand.expanded} onToggle={expand.toggle} />}
 </figure>
}
