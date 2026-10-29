// A Running now card in two lines: the state glyph, the title and the elapsed time; then the current
// step (mono, cut with an ellipsis) and small glyphs (engine or model, tokens, a plan item). The words
// the card used to spell out (in the background, the subagent's type, its effort) are in the title's
// tooltip. The title is the card's one control: a click anywhere on the card (or Enter on the title)
// does what it does, usually bringing the card's place in the conversation into view.
import { useState, type CSSProperties, type MouseEvent, type ReactNode } from 'react'
import { useHovercard, type RunTarget } from '@blerglab/chat'

export interface RunCardProps {
  testId: string
  className?: string
  /** Data attributes the tests and the page read (data-key, data-status, data-task-id). */
  data?: Record<string, string>
  /** The state glyph (a spinner while running), with its name for a screen reader. */
  state: ReactNode
  title: ReactNode
  /** The title's tooltip and the description of the control: everything the card does not spell out. */
  tooltip: string
  /** What the title's control does, for a screen reader ("Show in conversation"). */
  action: string
  onActivate: () => void
  /** The title control's test id (defaults to rail-show). */
  actionTestId?: string
  elapsed?: ReactNode
  /** Line 2: the current step (mono, cut) and the glyphs. */
  step?: ReactNode
  glyphs?: ReactNode
  /** Controls of their own on line 1, right of the clock (Dismiss). */
  controls?: ReactNode
  /** The card the worker is on ("↳ <plan item> · #122"): a third line, only when there is one. */
  work?: ReactNode
  /** Anything that must stay in the card but is not one of the two lines (a status note). */
  after?: ReactNode
  /** It runs inside the main agent (a subagent, a background command): indented under the Main
   *  card, behind a ↳. */
  nested?: boolean
  /** The row's hovercard (card #129): everything the row cuts short, on hover. */
  hover?: RunTarget
}

export default function RunCard(p: RunCardProps) {
  const hc = useHovercard(p.hover ?? null)
  // The card describes the title (the control that takes the focus), not the row.
  const { 'aria-describedby': describedBy, ...rowProps } = hc.props
  const data = Object.fromEntries(Object.entries(p.data ?? {}).map(([k, v]) => [`data-${k}`, v]))
  // A click on the card does what the title does, unless it landed on a control of its own.
  const onClick = (e: MouseEvent<HTMLLIElement>) => {
    // (The title's own click handles a click on it; a dialog the card opened is portaled out of it.)
    if (!e.currentTarget.contains(e.target as Node)) return
    const control = (e.target as HTMLElement).closest('button, a, input, textarea, select, [role="button"], [role="dialog"]')
    if (control && e.currentTarget.contains(control)) return
    // Selecting text to copy it is not a click on the card.
    if (window.getSelection()?.toString()) return
    p.onActivate()
  }
  return (<>
    <li className={`rail-item rail-run${p.nested ? ' rail-nested' : ''}${p.className ? ` ${p.className}` : ''}`} data-testid={p.testId} {...data} onClick={onClick} {...rowProps}>
      <div className="rail-run-line">
        {p.nested && <span className="rail-run-nest" aria-hidden="true">↳</span>}
        <span className="rail-run-state">{p.state}</span>
        {/* With the row's hovercard, it says what the tooltip did (both at once would cover each other). */}
        <button type="button" className="rail-run-title" data-testid={p.actionTestId ?? 'rail-show'} title={hc.enabled ? undefined : p.tooltip} aria-describedby={describedBy}
          aria-description={`${p.nested ? 'Runs inside Main. ' : ''}${p.action}. ${p.tooltip}`} onClick={p.onActivate}>
          {p.title}
        </button>
        {/* With no step, the glyphs stay on the first line and the card is one line. */}
        {!p.step && p.glyphs && <span className="rail-run-glyphs">{p.glyphs}</span>}
        {p.elapsed}
        {p.controls}
      </div>
      {p.step && (
        <div className="rail-run-line rail-run-second">
          <span className="rail-run-step">{p.step}</span>
          {p.glyphs && <span className="rail-run-glyphs">{p.glyphs}</span>}
        </div>
      )}
      {p.work && <div className="rail-run-line rail-run-work" data-testid="rail-run-work">{p.work}</div>}
      {p.after}
    </li>
    {hc.card}
  </>)
}

/** A small glyph with its words in the tooltip and the accessible name. */
export function RunGlyph({ label, children, testId, className, style }: { label: string; children: ReactNode; testId?: string; className?: string; style?: CSSProperties }) {
  return <span className={`rail-run-glyph${className ? ` ${className}` : ''}`} role="img" aria-label={label} title={label} data-testid={testId} style={style}>{children}</span>
}

/** The card's step line: one mono line cut with an ellipsis. A tap (or Enter) shows it whole in place,
 *  `full` when given (a long command, its description and its result), in a box that scrolls; a
 *  second tap folds it. This is how touch reaches a long command; the title still jumps. */
export function RunStep({ text, full, testId, className }: { text: string; full?: string; testId?: string; className?: string }) {
  const [open, setOpen] = useState(false)
  const whole = full ?? text
  return (
    <button type="button" className={`rail-run-doing rail-run-stepbtn${className ? ` ${className}` : ''}`} data-testid={testId}
      aria-expanded={open} title={whole} onClick={() => setOpen(o => !o)}>
      {open ? <span className="rail-run-whole">{whole}</span> : text}
    </button>
  )
}
