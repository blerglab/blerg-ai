// The tab bar's board button: shown only when the session is associated with a board (its launch
// board, or a board it has access to). One board: a link that opens it in a new tab, as the
// palette's "Open board" does. Several: a small menu on the shared popover surface, launch board
// first, each opening in a new tab.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBoardOrigin } from '../lib/boardLink'
import { boardPageUrl, useSessionBoardLinks, type BoardLink } from '../lib/sessionBoardLinks'

export function BoardIcon() {
  return <svg className="icon-btn-svg" width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
    <rect x="3" y="4" width="18" height="16" rx="2" /><path d="M9 4v16M15 4v16" /><path d="M5.5 8h1.5M11.5 8h1.5M11.5 11h1.5M17.5 8h1" />
  </svg>
}

export default function SessionBoardButton({ sessionId, refreshKey }: { sessionId: string; refreshKey?: string }) {
  const links = useSessionBoardLinks(sessionId, refreshKey)
  const origin = useBoardOrigin()
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  if (links.length === 0) return null
  if (links.length === 1) {
    const label = `Open board: ${links[0].name}`
    return <a className="icon-btn session-board-toggle" data-testid="session-board-button" href={boardPageUrl(origin, links[0].id)} target="_blank" rel="noopener" aria-label={label} title={label}><BoardIcon /></a>
  }
  return <>
    <button ref={trigger} type="button" className="icon-btn session-board-toggle" data-testid="session-board-button" aria-label="Open a board" title="Open a board" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(o => !o)}><BoardIcon /></button>
    {open && <BoardMenu anchor={trigger} links={links} origin={origin} onClose={() => setOpen(false)} />}
  </>
}

const GAP = 6
const EDGE = 8

function BoardMenu({ anchor, links, origin, onClose }: { anchor: React.RefObject<HTMLButtonElement | null>; links: BoardLink[]; origin: string; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  // Under the button, right edges aligned, kept inside the window (the phone's bottom sheet ignores it).
  useLayoutEffect(() => {
    function place() {
      const a = anchor.current?.getBoundingClientRect()
      const el = box.current
      if (!a || !el) return
      const vw = document.documentElement.clientWidth || window.innerWidth
      const left = Math.max(EDGE, Math.min(a.right - el.offsetWidth, vw - EDGE - el.offsetWidth))
      let top = a.bottom + GAP
      if (top + el.offsetHeight > window.innerHeight - EDGE && a.top - GAP - el.offsetHeight >= EDGE) top = a.top - GAP - el.offsetHeight
      setPos(p => (p && p.top === top && p.left === left ? p : { top, left }))
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => { window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true) }
  }, [anchor])
  const focused = useRef(false)
  useEffect(() => {
    if (!pos || focused.current) return
    focused.current = true
    box.current?.querySelector<HTMLElement>('[role=menuitem]')?.focus()
  }, [pos])
  const onCloseRef = useRef(onClose)
  useEffect(() => { onCloseRef.current = onClose })
  useEffect(() => {
    const away = (e: PointerEvent) => {
      const t = e.target as Node
      if (box.current?.contains(t) || anchor.current?.contains(t)) return
      onCloseRef.current()
    }
    document.addEventListener('pointerdown', away, true)
    return () => document.removeEventListener('pointerdown', away, true)
  }, [anchor])
  const close = (focusBack: boolean) => { onClose(); if (focusBack) anchor.current?.focus() }
  return createPortal(<>
    <div className="confirm-popover-scrim" aria-hidden="true" />
    <div ref={box} className="confirm-popover session-board-menu" data-testid="session-board-menu" style={pos ? { top: pos.top, left: pos.left } : { visibility: 'hidden' }}
      onKeyDown={e => {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(true); return }
        if (e.key === 'Tab') { close(false); return }
        if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
          e.preventDefault()
          const items = [...e.currentTarget.querySelectorAll<HTMLElement>('[role=menuitem]')]
          const i = items.indexOf(document.activeElement as HTMLElement)
          const next = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
          items[next]?.focus()
        }
      }}>
      <h2 className="confirm-popover-title">Open a board</h2>
      <div role="menu" aria-label="Open a board">
        {links.map(l => <a key={l.id} role="menuitem" href={boardPageUrl(origin, l.id)} target="_blank" rel="noopener" title={`Open board: ${l.name}`} onClick={() => close(false)}>
          <span className="session-board-name">{l.name}</span>{l.launch && <span className="session-board-launch">launch board</span>}
        </a>)}
      </div>
    </div>
  </>, document.body)
}
