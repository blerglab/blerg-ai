import { useEffect, useRef, useState, type ButtonHTMLAttributes } from 'react'
export type DockRowMenu<T> = { data: T; x: number; y: number; anchor: HTMLButtonElement }
export function useDockRowMenu<T>() {
  const [menu, setMenu] = useState<DockRowMenu<T> | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const origin = useRef({ x: 0, y: 0 })
  const suppressAnchor = useRef<HTMLButtonElement | null>(null)
  const suppressClickUntil = useRef(0)
  const cancel = () => clearTimeout(timer.current)
  useEffect(() => () => clearTimeout(timer.current), [])
  const close = (restore = true) => { if (restore) menu?.anchor.focus({ preventScroll: true }); setMenu(null) }
  const handlers = (data: T): ButtonHTMLAttributes<HTMLButtonElement> => ({
    onContextMenu: event => { event.preventDefault(); event.stopPropagation(); cancel(); setMenu({ data, x: event.clientX, y: event.clientY, anchor: event.currentTarget }) },
    onKeyDown: event => {
      if (event.key !== 'ContextMenu' && !(event.key === 'F10' && event.shiftKey)) return
      event.preventDefault(); event.stopPropagation()
      const box = event.currentTarget.getBoundingClientRect()
      setMenu({ data, x: box.left + 8, y: box.bottom, anchor: event.currentTarget })
    },
    onPointerDown: event => {
      cancel()
      if (event.pointerType !== 'touch') return
      const anchor = event.currentTarget, x = event.clientX, y = event.clientY
      origin.current = { x, y }
      timer.current = setTimeout(() => { suppressAnchor.current = anchor; suppressClickUntil.current = performance.now() + 1000; setMenu({ data, x, y, anchor }) }, 500)
    },
    onPointerMove: event => { if (Math.hypot(event.clientX - origin.current.x, event.clientY - origin.current.y) > 8) cancel() },
    onPointerUp: cancel,
    onPointerCancel: cancel,
    onClickCapture: event => { if (event.currentTarget === suppressAnchor.current && performance.now() < suppressClickUntil.current) { event.preventDefault(); event.stopPropagation(); suppressClickUntil.current = 0 } },
  })
  return { menu, handlers, close }
}
