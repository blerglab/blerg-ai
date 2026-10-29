import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { sessionTabDragType } from './SessionGroups'
import { tabDragSpeed, tabScrollDelta, tabWheelDelta } from '../lib/tabStrip'

type Item = { id: string; label: string; preview?: boolean; icon: ReactNode }
type Props = Pick<HTMLAttributes<HTMLDivElement>, 'onDrop' | 'onDragLeave' | 'onKeyDown'> & {
  active: string; items: Item[]; label: string; children: ReactNode
  onSelect(id: string): void
  onDragPosition(x: number, strip: HTMLElement): void
}
function reveal(element: HTMLElement) {
  const active = element.querySelector<HTMLElement>('.session-tab.active')
  if (active && element.clientWidth) element.scrollLeft += tabScrollDelta(element.getBoundingClientRect(), active.getBoundingClientRect())
}
export default function SessionTabStrip({ active, items, label, children, onSelect, onDragPosition, ...handlers }: Props) {
  const strip = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const [overflow, setOverflow] = useState(false)
  const [menu, setMenu] = useState(false)
  const dragPosition = useRef(onDragPosition)
  useLayoutEffect(() => { dragPosition.current = onDragPosition })
  const signature = items.map(item => item.id).join('\0')
  useLayoutEffect(() => {
    const element = strip.current!
    const measure = () => {
      if (!element.clientWidth) { setMenu(false); return }
      const next = element.scrollWidth > element.clientWidth + 2
      setOverflow(next)
      if (!next) setMenu(false)
    }
    let width = element.clientWidth
    const observer = new ResizeObserver(entries => {
      measure()
      // Only a viewport resize may override manual scrolling. Badges, labels
      // and preview styling can change tab widths while the person is browsing.
      if (entries.some(entry => entry.target === element) && element.clientWidth !== width) {
        width = element.clientWidth
        reveal(element)
      }
    })
    observer.observe(element)
    for (const child of element.children) if (child.classList.contains('session-tab')) observer.observe(child)
    measure()
    reveal(element)
    return () => observer.disconnect()
  }, [active, signature])
  useEffect(() => {
    const element = strip.current!
    let frame = 0, pointer = 0, last = 0
    const stop = () => {
      cancelAnimationFrame(frame); frame = 0; last = 0
    }
    const step = (now: number) => {
      const box = element.getBoundingClientRect(), speed = tabDragSpeed(pointer, box.left, box.right)
      const previous = element.scrollLeft
      element.scrollLeft += speed * (last ? Math.min(32, now - last) : 16) / 1000
      last = now
      dragPosition.current(pointer, element)
      frame = speed && element.scrollLeft !== previous ? requestAnimationFrame(step) : 0
    }
    const over = (event: DragEvent) => {
      if (!event.dataTransfer?.types.includes(sessionTabDragType)) return
      event.preventDefault(); event.dataTransfer.dropEffect = 'move'
      pointer = event.clientX
      dragPosition.current(pointer, element)
      if (!frame) frame = requestAnimationFrame(step)
    }
    const leave = (event: DragEvent) => { if (!element.contains(event.relatedTarget as Node | null)) stop() }
    const end = () => stop()
    const wheel = (event: WheelEvent) => {
      if (element.scrollWidth <= element.clientWidth + 2 || event.ctrlKey) return
      const delta = tabWheelDelta(event, element.clientWidth)
      if (delta) { event.preventDefault(); element.scrollLeft += delta }
    }
    element.addEventListener('wheel', wheel, { passive: false })
    element.addEventListener('dragover', over); element.addEventListener('dragleave', leave)
    document.addEventListener('drop', end, true); document.addEventListener('dragend', end, true)
    return () => {
      stop()
      element.removeEventListener('wheel', wheel); element.removeEventListener('dragover', over); element.removeEventListener('dragleave', leave)
      document.removeEventListener('drop', end, true); document.removeEventListener('dragend', end, true)
    }
  }, [])
  return <div className="session-tabstrip">
    <div {...handlers} ref={strip} className="session-tabs" role="tablist" aria-label={label}>{children}</div>
    {overflow && <button type="button" className="icon-btn session-tab-overflow" ref={trigger} aria-label="All tabs" title="Show all tabs" aria-haspopup="menu" aria-expanded={menu} onClick={() => setMenu(value => !value)}><svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" aria-hidden="true"><path d="m3 6 5 5 5-5" /></svg></button>}
    {overflow && menu && <TabMenu anchor={trigger} items={items} active={active} onSelect={id => { setMenu(false); onSelect(id) }} onClose={back => { setMenu(false); if (back) trigger.current?.focus() }} />}
  </div>
}
function TabMenu({ anchor, items, active, onSelect, onClose }: { anchor: React.RefObject<HTMLButtonElement | null>; items: Item[]; active: string; onSelect(id: string): void; onClose(back: boolean): void }) {
  const root = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  useLayoutEffect(() => {
    const place = () => {
      const button = anchor.current!, menu = root.current!, box = button.getBoundingClientRect()
      setPosition({ top: Math.max(8, box.bottom + menu.offsetHeight + 4 <= innerHeight - 8 ? box.bottom + 4 : box.top - menu.offsetHeight - 4), left: Math.max(8, Math.min(box.right - menu.offsetWidth, innerWidth - menu.offsetWidth - 8)) })
    }
    place(); window.addEventListener('resize', place); window.addEventListener('scroll', place, true)
    return () => { window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true) }
  }, [anchor])
  useEffect(() => {
    const current = root.current?.querySelector<HTMLElement>('[aria-checked=true]') ?? root.current?.querySelector<HTMLElement>('[role=menuitemradio]')
    current?.focus(); current?.scrollIntoView({ block: 'nearest' })
  }, [])
  useEffect(() => {
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node) && !anchor.current?.contains(event.target as Node)) onClose(false) }
    document.addEventListener('pointerdown', outside, true)
    return () => document.removeEventListener('pointerdown', outside, true)
  }, [anchor, onClose])
  return createPortal(<div ref={root} className="session-tab-menu" role="menu" aria-label="All tabs" style={position} onKeyDown={event => {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); onClose(true) }
    if (event.key === 'Tab') onClose(true)
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault()
      const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role=menuitemradio]')], current = buttons.indexOf(document.activeElement as HTMLButtonElement)
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
      buttons[next]?.focus(); buttons[next]?.scrollIntoView({ block: 'nearest' })
    }
  }}>{items.map(item => <button key={item.id} type="button" className="session-tab-menu-item" role="menuitemradio" aria-checked={item.id === active} onClick={() => onSelect(item.id)}><span className="session-tab-menu-icon" aria-hidden="true">{item.icon}</span><span>{item.label}</span>{item.preview && <em>Preview</em>}</button>)}</div>, document.body)
}
