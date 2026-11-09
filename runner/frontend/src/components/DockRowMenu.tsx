import './DockRowMenu.css'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
export type DockRowAction = { label: string; title?: string; disabled?: boolean; run(): void }
export default function DockRowMenu({ x, y, actions, close, label = 'Channel actions' }: { label?: string; x: number; y: number; actions: DockRowAction[]; close(restore?: boolean): void }) {
  const root = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ top: y, left: x })
  useLayoutEffect(() => {
    const element = root.current!
    setPosition({ top: Math.max(8, Math.min(y, innerHeight - element.offsetHeight - 8)), left: Math.max(8, Math.min(x, innerWidth - element.offsetWidth - 8)) })
    element.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
  }, [x, y])
  useEffect(() => {
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) close(false) }
    document.addEventListener('pointerdown', outside, true)
    return () => document.removeEventListener('pointerdown', outside, true)
  }, [close])
  return createPortal(<div ref={root} className="dock-row-menu" role="menu" aria-label={label} style={position} onKeyDown={event => {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close() }
    else if (event.key === 'Tab') close()
    else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault()
      const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')], index = buttons.indexOf(document.activeElement as HTMLButtonElement)
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
      buttons[next]?.focus()
    }
  }}>{actions.map(action => <button type="button" className="btn small" role="menuitem" tabIndex={-1} key={action.label} disabled={action.disabled} title={action.title} onClick={() => { close(); action.run() }}>{action.label}</button>)}</div>, document.body)
}
