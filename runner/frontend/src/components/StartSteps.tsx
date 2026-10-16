import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { formatDuration, formatTick, stepLabel, timeAxis, type StepSpread } from '../lib/insights'

// Under this many starts a box (the middle half) would be read off one or two samples: a step
// with fewer shows its fastest, median and slowest start as dots instead.
export const MIN_FOR_BOX = 5

const pct = (f: number) => `${(f * 100).toFixed(3)}%`

/**
 * StartSteps draws each step of a start as a box-and-whisker on one shared time axis: the whisker
 * runs fastest to slowest, the box is the middle half, a tick marks the median and a hollow mark
 * the 90th percentile. Beside each row: how many starts, the slowest, and how many were slow.
 * A row's tooltip carries every number: it shows on hover and keyboard focus, and a click or tap
 * pins it open (there is no hover on a touch screen) until the row is tapped again, Esc is pressed
 * or the person taps anywhere else.
 */
export default function StartSteps({ runtime, steps }: { runtime: string; steps: StepSpread[] }) {
  const [active, setActive] = useState<string | null>(null)
  // a click or tap pinned the active row's tooltip: leaving the row no longer closes it
  const [pinned, setPinned] = useState(false)
  const rowsRef = useRef<HTMLUListElement | null>(null)
  const close = () => { setActive(null); setPinned(false) }

  // Esc closes an open tooltip wherever focus is; a pinned one also closes on a press outside its row.
  useEffect(() => {
    if (active === null) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { setActive(null); setPinned(false) } }
    const onDown = (e: PointerEvent) => {
      if (!pinned) return
      const row = rowsRef.current?.querySelector('li.pinned')
      if (row && e.target instanceof Node && row.contains(e.target)) return
      setActive(null)
      setPinned(false)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('pointerdown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('pointerdown', onDown)
    }
  }, [active, pinned])

  const axis = timeAxis(steps)
  const x = (v: number) => pct(axis.pos(v))
  const span = (a: number, b: number) => ({ left: x(a), width: pct(Math.max(0, axis.pos(b) - axis.pos(a))) })
  const grid = axis.ticks.map((t) => <i key={t} className="sb-grid" style={{ left: x(t) }} aria-hidden="true" />)

  return (
    <div className="start-steps">
      <h3>Start steps ({runtime}): how long each one takes</h3>
      <div className="sb-legend" data-testid="start-steps-legend">
        <span><i className="sb-key box" aria-hidden="true" />middle half of starts (25th–75th percentile)</span>
        <span><i className="sb-key median" aria-hidden="true" />median</span>
        <span><i className="sb-key p90" aria-hidden="true" />90th percentile</span>
        <span><i className="sb-key whisker" aria-hidden="true" />fastest to slowest</span>
        <span className="sb-note">slow = more than twice the median</span>
      </div>
      <ul className="sb-rows" data-testid="start-steps" ref={rowsRef}>
        {steps.map((s) => {
          const few = s.count < MIN_FOR_BOX
          const tipId = `sb-tip-${s.step}`
          const open = active === s.step
          // hover and focus show a row's tooltip; moving to another row unpins the one before
          const show = () => { if (!open) { setActive(s.step); setPinned(false) } }
          const leave = () => { if (open && !pinned) close() }
          const toggle = () => { if (open && pinned) close(); else { setActive(s.step); setPinned(true) } }
          return (
            <li key={s.step} className={open ? (pinned ? 'on pinned' : 'on') : undefined} data-testid={`step-${s.step}`}
              onMouseEnter={show} onMouseLeave={leave} onFocus={show} onBlur={leave} onClick={toggle}>
              <button type="button" className="sb-name" aria-expanded={open} aria-controls={open ? tipId : undefined}
                aria-describedby={open ? tipId : undefined}>{stepLabel(s.step)}</button>
              <span className="sb-plot" role="img" aria-label={describe(s)}>
                {grid}
                <i className="sb-whisker" data-part="whisker" style={span(s.min, s.max)} />
                {few ? (
                  [...new Set([s.min, s.p50, s.max])].map((v) => <i key={v} className="sb-dot" data-part="dot" style={{ left: x(v) }} />)
                ) : (
                  <>
                    <i className="sb-cap" style={{ left: x(s.min) }} />
                    <i className="sb-cap" style={{ left: x(s.max) }} />
                    <i className="sb-box" data-part="box" style={span(s.p25, s.p75)} />
                    <i className="sb-median" data-part="median" style={{ left: x(s.p50) }} />
                    <i className="sb-p90" data-part="p90" style={{ left: x(s.p90) }} />
                  </>
                )}
              </span>
              <span className="sb-stats">
                n {s.count} · max {formatDuration(s.max)}
                {few ? ' · too few starts for a box' : s.slow > 0 ? ` · ${s.slow} slow` : ''}
              </span>
              {open && <Tip id={tipId} s={s} />}
            </li>
          )
        })}
        <li className="sb-axis" aria-hidden="true">
          <span className="sb-name" />
          <span className="sb-plot">
            {axis.ticks.map((t) => <span key={t} className="sb-tick" style={{ left: x(t) }}>{formatTick(t)}</span>)}
          </span>
          <span className="sb-stats sb-axis-note">{axis.kind === 'log' ? 'log scale: a far outlier' : 'linear scale'}</span>
        </li>
      </ul>
    </div>
  )
}

function describe(s: StepSpread): string {
  const d = formatDuration
  const slow = s.p50 > 0 ? `, ${s.slow} slow` : ''
  return `${stepLabel(s.step)}: ${s.count} starts, fastest ${d(s.min)}, median ${d(s.p50)}, 90th percentile ${d(s.p90)}, slowest ${d(s.max)}${slow}`
}

// EDGE is the gap a tooltip keeps from the edges of the window.
const EDGE = 8

function Tip({ id, s }: { id: string; s: StepSpread }) {
  const ref = useRef<HTMLDivElement | null>(null)
  // Placed under its row; moved sideways to stay inside the window, and above the row when there is
  // no room beneath it but there is above. Measured and moved before the first paint, so it never jumps.
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    const vw = document.documentElement.clientWidth || window.innerWidth
    const vh = window.innerHeight
    let dx = 0
    if (r.right > vw - EDGE) dx = vw - EDGE - r.right
    if (r.left + dx < EDGE) dx = EDGE - r.left
    const row = el.parentElement?.getBoundingClientRect()
    const up = r.bottom > vh - EDGE && row !== undefined && row.top - r.height - 4 >= EDGE
    if (dx !== 0) el.style.transform = `translateX(${Math.round(dx)}px)`
    if (up) el.classList.add('up')
  }, [])
  const rows: [string, string][] = [
    [formatDuration(s.min), 'fastest'],
    [formatDuration(s.p25), '25th percentile'],
    [formatDuration(s.p50), 'median'],
    [formatDuration(s.p75), '75th percentile'],
    [formatDuration(s.p90), '90th percentile'],
    [formatDuration(s.max), 'slowest'],
    [String(s.count), s.count === 1 ? 'start' : 'starts'],
    [String(s.slow), s.p50 > 0 ? `slow (over ${formatDuration(2 * s.p50)})` : 'slow'],
  ]
  return (
    <div role="tooltip" id={id} ref={ref} className="sb-tip">
      <div className="sb-tip-title">{stepLabel(s.step)}</div>
      <ul>
        {rows.map(([v, k]) => <li key={k}><b>{v}</b> {k}</li>)}
      </ul>
    </div>
  )
}
