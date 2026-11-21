import {useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject} from 'react'
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

/** Opens something new (a tab): an arrow going into a frame, distinct from the ⋯ menu. Used by the
 *  collapsed "Open" header control (card BLG-450) at a narrow width, where shell/board/Insights/
 *  blackboard collapse into one menu. */
export function OpenIcon() {
  return <Svg><rect x="3.5" y="5.5" width="17" height="13" rx="2" /><path d="M8 12h7M12 8.5 15.5 12 12 15.5" /></Svg>
}

/** New shell: a terminal prompt, the same row-icon size as the header's other open-a-tab rows. */
export function ShellIcon() {
  return <Svg><rect x="3" y="4.5" width="18" height="15" rx="2" /><path d="M7.5 10l2.5 2-2.5 2M12.5 14h4" /></Svg>
}

/** Search: used by the collapsed "Open" menu's Search row (card BLG-450), mirroring the header's
 *  own Search button icon. */
export function SearchIcon() {
  return <Svg><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></Svg>
}

/** Worker purpose marks: keep these distinct from board, panel, star and refresh actions. */
export function TicketIcon() {
  return <Svg><path d="M3 5h18v4a3 3 0 0 0 0 6v4H3v-4a3 3 0 0 0 0-6V5Z" /><path d="M15 5v2m0 3v2m0 3v4" /></Svg>
}

export function LoopIcon() {
  return <Svg><path d="m17 3 4 4-4 4M3 11V9a2 2 0 0 1 2-2h16M7 21l-4-4 4-4m14 0v2a2 2 0 0 1-2 2H3" /></Svg>
}

export function EyeIcon() {
  return <Svg><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z" /><circle cx="12" cy="12" r="3" /></Svg>
}

export function BellIcon() {
  return <Svg><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" /><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" /></Svg>
}

/** A ⋯ menu of the rarer actions. Its items stay mounted while it is closed (hidden), so a dialog
 *  an item opened outlives the menu. Each item is a `role="menuitem"` button; IconMenuItem draws one. */
export function IconMenu({label, children, testId, triggerRef, icon, align = 'right'}: {label: string; children: ReactNode; testId?: string; triggerRef?: RefObject<HTMLButtonElement | null>; icon?: ReactNode; align?: 'left' | 'right'}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
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
  // Keep the open menu inside the viewport (card BLG-450, round 3): align left/right only picks
  // which edge it opens from — at a narrow width that can still run the menu off the opposite
  // edge (the collapsed header's "Open" menu, near the right edge of a 390px phone, opens
  // leftward from align="left" and overflowed). Clamp it back in with an 8px margin instead of
  // adding a second alignment mode: measure after layout, shift with a transform (doesn't
  // disturb the CSS-driven left/right anchor), and re-measure on resize while open.
  useLayoutEffect(() => {
    if (!open) return
    const el = list.current
    if (!el) return
    const margin = 8
    const place = () => {
      el.style.transform = ''
      const rect = el.getBoundingClientRect()
      let dx = 0
      if (rect.right > window.innerWidth - margin) dx = (window.innerWidth - margin) - rect.right
      if (rect.left + dx < margin) dx = margin - rect.left
      if (dx) el.style.transform = `translateX(${dx}px)`
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
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
        {icon ?? <MoreIcon />}
      </button>
      {/* Clicking an item closes the menu (its own handler runs first). Right-aligned by default (a
          ⋯ menu near the end of a row); left-aligned for a trigger near the row's start (the
          header's "Open" menu, BLG-450), so it opens into the screen instead of off its edge. */}
      <div ref={list} className={align === 'left' ? 'icon-menu-list icon-menu-list-left' : 'icon-menu-list'} role="menu" aria-label={label} hidden={!open} onClick={() => setOpen(false)}>
        {children}
      </div>
    </div>
  )
}

/** One plain row (card BLG-450, review round 2): an icon, then the label with its quiet secondary
 *  text underneath, if any — the same "primary label + quiet secondary text" shape as other rows in
 *  this menu (SessionBoardMenuItem) and elsewhere in the app (the palette's option rows). Never a
 *  link style, never a separate caption line of its own. */
export function IconMenuItem({label, hint, icon, onSelect, danger = false, disabled = false, testId}: {label: string; hint?: string; icon?: ReactNode; onSelect(): void; danger?: boolean; disabled?: boolean; testId?: string}) {
  return (
    <button type="button" role="menuitem" className={danger ? 'icon-menu-item danger' : 'icon-menu-item'} data-testid={testId} disabled={disabled} title={disabled ? hint : undefined} onClick={onSelect}>
      {icon && <span className="icon-menu-icon" aria-hidden="true">{icon}</span>}
      <span className="icon-menu-text">
        <span className="icon-menu-label">{label}</span>
        {hint && <span className="icon-menu-hint">{hint}</span>}
      </span>
    </button>
  )
}
