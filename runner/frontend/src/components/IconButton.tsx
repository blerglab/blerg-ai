import {useEffect, useRef, useState, type ReactNode, type RefObject} from 'react'
import './IconButton.css'

// One style for the app's header controls (the session header, the sidebar's bell): a square
// outlined button holding one line icon, the word in its tooltip and aria label. `.icon-btn` is the
// class; `.danger` marks the one control that changes the session (End session).

function Svg({children, filled = false}: {children: ReactNode; filled?: boolean}) {
  return (
    <svg className="icon-btn-svg" width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" fill={filled ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      {children}
    </svg>
  )
}

export function StarIcon({filled = false}: {filled?: boolean}) {
  return <Svg filled={filled}><path d="M12 3.5l2.6 5.3 5.9.9-4.3 4.1 1 5.8L12 16.8l-5.2 2.8 1-5.8-4.3-4.1 5.9-.9z" /></Svg>
}

export function InsightsIcon() {
  return <Svg><path d="M5 20V13M12 20V5M19 20v-9M3 20h18" /></Svg>
}

export function PanelToggleIcon() {
  return <Svg><rect x="3" y="4.5" width="18" height="15" rx="2" /><path d="M15 4.5v15" /></Svg>
}

/** The robot face that marks a subagent: on a Running now subagent row and on the panel toggle's
 *  "work inside this session" count, so the two read as the same thing. With a label it is an image
 *  with that name; without one it is decoration beside a count that is named elsewhere. */
export function SubagentFace({label, className}: {label?: string; className?: string}) {
  return label
    ? <span className={className} role="img" aria-label={label}>🤖</span>
    : <span className={className} aria-hidden="true">🤖</span>
}

export function EndSessionIcon() {
  return <Svg><path className="end-session-x" d="M6 6l12 12M18 6L6 18" strokeWidth="2.5" /></Svg>
}

/** Stop the agent's current turn: a filled square (■). */
export function StopTurnIcon() {
  return <Svg filled><rect x="6.5" y="6.5" width="11" height="11" rx="1.5" /></Svg>
}

export function MoreIcon() {
  return <Svg filled><circle cx="5" cy="12" r="1.6" stroke="none" /><circle cx="12" cy="12" r="1.6" stroke="none" /><circle cx="19" cy="12" r="1.6" stroke="none" /></Svg>
}

export function BellIcon() {
  return <Svg><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" /><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" /></Svg>
}

/** A ⋯ menu of the rarer actions. Its items stay mounted while it is closed (hidden), so a dialog
 *  an item opened outlives the menu. Each item is a `role="menuitem"` button; IconMenuItem draws one. */
export function IconMenu({label, children, testId, triggerRef}: {label: string; children: ReactNode; testId?: string; triggerRef?: RefObject<HTMLButtonElement | null>}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const own = useRef<HTMLButtonElement>(null)
  const trigger = triggerRef ?? own
  const items = () => [...(root.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])]
  useEffect(() => {
    if (!open) return
    items()[0]?.focus()
    const outside = (event: PointerEvent) => {if (!root.current?.contains(event.target as Node)) setOpen(false)}
    document.addEventListener('pointerdown', outside)
    return () => document.removeEventListener('pointerdown', outside)
  }, [open])
  return (
    <div ref={root} className="icon-menu" onKeyDown={event => {
      // Keys from a dialog an item opened (a portal: React bubbles its events here, the DOM does
      // not) are the dialog's own.
      if (!root.current?.contains(event.target as Node)) return
      if (event.key === 'Escape' && open) {event.stopPropagation(); setOpen(false); trigger.current?.focus()}
      if (event.key === 'Tab') setOpen(false)
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault()
        if (!open) {setOpen(true); return}
        const list = items()
        const at = list.indexOf(document.activeElement as HTMLButtonElement)
        const next = event.key === 'ArrowDown' ? (at + 1) % list.length : (at - 1 + list.length) % list.length
        list[next]?.focus()
      }
    }}>
      <button ref={trigger} type="button" className="icon-btn" data-testid={testId} aria-label={label} title={label} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(value => !value)}>
        <MoreIcon />
      </button>
      {/* Clicking an item closes the menu (its own handler runs first). */}
      <div className="icon-menu-list" role="menu" aria-label={label} hidden={!open} onClick={() => setOpen(false)}>
        {children}
      </div>
    </div>
  )
}

export function IconMenuItem({label, hint, onSelect, danger = false, testId}: {label: string; hint?: string; onSelect(): void; danger?: boolean; testId?: string}) {
  return (
    <button type="button" role="menuitem" className={danger ? 'icon-menu-item danger' : 'icon-menu-item'} data-testid={testId} onClick={onSelect}>
      <span className="icon-menu-label">{label}</span>
      {hint && <span className="icon-menu-hint">{hint}</span>}
    </button>
  )
}
