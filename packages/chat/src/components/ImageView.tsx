import { useCallback, useEffect, useLayoutEffect, useRef, useState, type PointerEvent } from 'react'
import './image-view.css'

type Point = { x: number; y: number }
type View = Point & { scale: number; fit: boolean }
const clamp = (n: number, min: number, max: number) => Math.min(max, Math.max(min, n))
function boundedOffset(value: number, size: number, available: number, fit: boolean) {
  const gap = available - size
  return fit && gap >= 0 ? gap / 2 : clamp(value, Math.min(0, gap), Math.max(0, gap))
}

/** Owns only the preview's gestures. MarkupMode has its own image/canvas coordinate system. */
export default function ImageView({ src, name }: { src: string; name: string }) {
  const viewport = useRef<HTMLDivElement>(null)
  const natural = useRef({ width: 0, height: 0 })
  const current = useRef<View>({ x: 0, y: 0, scale: 1, fit: true })
  const [view, setView] = useState<View>({ x: 0, y: 0, scale: 1, fit: true })
  const [dimensions, setDimensions] = useState({ width: 0, height: 0 })
  const ready = dimensions.width > 0
  const [pannable, setPannable] = useState(false)
  const pannableRef = useRef(false)
  const [floor, setFloor] = useState(.1)
  const [dragging, setDragging] = useState(false)
  const pointers = useRef(new Map<number, Point>())

  const fitScale = useCallback(() => {
    const el = viewport.current!
    // Fit may go below the 10% zoom floor: a tall image in a short group must still fit entirely.
    return clamp(Math.min(1, el.clientWidth / natural.current.width, el.clientHeight / natural.current.height), .001, 16)
  }, [])
  const minScale = useCallback(() => Math.min(.1, fitScale()), [fitScale])

  const update = useCallback((next: View) => {
    const el = viewport.current!
    const width = natural.current.width * next.scale, height = natural.current.height * next.scale
    // Fit centers the image. Manual zoom can move within the available space on either
    // side, preserving pointer anchors even while an axis still fits in a large viewport.
    const bounded = {
      ...next,
      x: boundedOffset(next.x, width, el.clientWidth, next.fit),
      y: boundedOffset(next.y, height, el.clientHeight, next.fit),
    }
    const overflow = width > el.clientWidth || height > el.clientHeight
    const movable = !next.fit && (width < el.clientWidth || height < el.clientHeight)
    pannableRef.current = overflow || movable
    setFloor(Math.min(.1, fitScale()))
    setPannable(pannableRef.current)
    current.current = bounded
    setView(bounded)
  }, [fitScale])

  const fit = useCallback(() => {
    if (!natural.current.width) return
    update({ x: 0, y: 0, scale: fitScale(), fit: true })
  }, [fitScale, update])

  // Translate the old image point at `anchor` to `destination` (also used by moving pinches).
  const zoom = useCallback((scale: number, anchor?: Point, destination = anchor) => {
    if (!natural.current.width) return
    const el = viewport.current!, old = current.current
    const at = anchor ?? { x: el.clientWidth / 2, y: el.clientHeight / 2 }
    const to = destination ?? at
    const next = clamp(scale, minScale(), 16), ratio = next / old.scale
    update({ scale: next, fit: false, x: to.x - (at.x - old.x) * ratio, y: to.y - (at.y - old.y) * ratio })
  }, [update, minScale])

  const pan = useCallback((dx: number, dy: number) => {
    if (!natural.current.width) return
    update({ ...current.current, x: current.current.x + dx, y: current.current.y + dy })
  }, [update])

  // Non-passive: browser page zoom must not compete with image zoom. Geometry lives in
  // refs so successive events before a render still anchor against the latest image position.
  useEffect(() => {
    const el = viewport.current!
    function wheel(e: WheelEvent) {
      if (!natural.current.width) return
      const zooming = e.ctrlKey || e.metaKey
      // A fitted image has nothing to pan: let the viewer or page scroll normally.
      if (!zooming && !pannableRef.current) return
      e.stopPropagation()
      e.preventDefault()
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? el.clientHeight : 1
      if (zooming) {
        const rect = el.getBoundingClientRect()
        zoom(current.current.scale * Math.exp(-e.deltaY * unit * .01), { x: e.clientX - rect.left, y: e.clientY - rect.top })
      } else {
        pan(-(e.shiftKey ? e.deltaY : e.deltaX) * unit, -(e.shiftKey ? e.deltaX : e.deltaY) * unit)
      }
    }
    // touch-action only allows native scrolling while fitted; claim two-finger pinches here.
    function touch(e: TouchEvent) { if (e.touches.length > 1 && e.cancelable) e.preventDefault() }
    el.addEventListener('wheel', wheel, { passive: false })
    el.addEventListener('touchstart', touch, { passive: false })
    el.addEventListener('touchmove', touch, { passive: false })
    return () => { el.removeEventListener('wheel', wheel); el.removeEventListener('touchstart', touch); el.removeEventListener('touchmove', touch) }
  }, [pan, zoom])

  useLayoutEffect(() => {
    const el = viewport.current!
    const observer = new ResizeObserver(() => {
      if (!natural.current.width || !el.clientWidth || !el.clientHeight) return
      if (current.current.fit) fit()
      else update(current.current)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [fit, update])

  function local(e: { clientX: number; clientY: number }): Point {
    const rect = viewport.current!.getBoundingClientRect()
    return { x: e.clientX - rect.left, y: e.clientY - rect.top }
  }
  function pointerDown(e: PointerEvent<HTMLDivElement>) {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    e.currentTarget.focus({ preventScroll: true })
    // A fitted image has nothing to drag; leave native gestures alone until a second finger joins.
    if (!pannableRef.current && pointers.current.size === 0 && e.pointerType === 'touch') { pointers.current.set(e.pointerId, local(e)); return }
    if (!pannableRef.current && e.pointerType !== 'touch') return
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    pointers.current.set(e.pointerId, local(e))
    setDragging(true)
  }
  function pointerMove(e: PointerEvent) {
    const old = pointers.current.get(e.pointerId)
    if (!old) return
    const next = local(e)
    const other = [...pointers.current].find(([id]) => id !== e.pointerId)?.[1]
    pointers.current.set(e.pointerId, next)
    if (other) {
      const distance = Math.hypot(old.x - other.x, old.y - other.y)
      if (distance > 0) zoom(current.current.scale * Math.hypot(next.x - other.x, next.y - other.y) / distance,
        { x: (old.x + other.x) / 2, y: (old.y + other.y) / 2 },
        { x: (next.x + other.x) / 2, y: (next.y + other.y) / 2 })
    } else pan(next.x - old.x, next.y - old.y)
  }
  function pointerEnd(e: PointerEvent) {
    pointers.current.delete(e.pointerId)
    setDragging(pointers.current.size > 0)
  }
  const keyDown = useCallback((e: KeyboardEvent) => {
    const target = e.target as HTMLElement
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || target.closest('input,textarea,select,[contenteditable]:not([contenteditable="false"]),.xterm')) return
    switch (e.key) {
      case '+': case '=': zoom(current.current.scale * 1.25); break
      case '-': case '−': zoom(current.current.scale / 1.25); break
      case '0': fit(); break
      case '1': zoom(1); break
      case 'ArrowLeft': pan(40, 0); break
      case 'ArrowRight': pan(-40, 0); break
      case 'ArrowUp': pan(0, 40); break
      case 'ArrowDown': pan(0, -40); break
      default: return
    }
    e.preventDefault()
    e.stopPropagation()
  }, [fit, pan, zoom])
  // Tab activation focuses the enclosing viewer region; canvas clicks focus the viewport.
  // Listen only in this viewer and accept its root or our own controls, never sibling fields.
  useLayoutEffect(() => {
    const root = viewport.current!.parentElement!
    const host = root.closest('.artifact-viewer') ?? root
    const key = (e: KeyboardEvent) => { if (e.target === host || root.contains(e.target as Node)) keyDown(e) }
    host.addEventListener('keydown', key as EventListener)
    return () => host.removeEventListener('keydown', key as EventListener)
  }, [keyDown])

  return <div className="image-view">
    <div className="image-view-toolbar" role="toolbar" aria-label="Image zoom">
      <button type="button" className="artifact-btn" aria-label="Zoom out" title="Zoom out (−)" disabled={!ready || view.scale <= floor + 1e-9} onClick={() => zoom(current.current.scale / 1.25)}>−</button>
      <button type="button" className="artifact-btn" aria-label="Zoom in" title="Zoom in (+)" disabled={!ready || view.scale >= 16} onClick={() => zoom(current.current.scale * 1.25)}>+</button>
      <button type="button" className="artifact-btn" aria-pressed={view.fit} title="Fit (0)" disabled={!ready} onClick={fit}>Fit</button>
      <button type="button" className="artifact-btn" aria-pressed={!view.fit && view.scale === 1} title="Actual size (1)" disabled={!ready} onClick={() => zoom(1)}>100%</button>
      <output aria-label="Zoom percent">{Math.round(view.scale * 100)}%</output>
    </div>
    <div ref={viewport} className={`image-view-viewport${pannable ? ' can-pan' : ''}${dragging && pannable ? ' is-dragging' : ''}`}
      role="region" aria-label={`${name} image viewport`} tabIndex={0}
      onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={pointerEnd} onPointerCancel={pointerEnd} onLostPointerCapture={pointerEnd}
      onDoubleClick={e => current.current.fit ? zoom(1, local(e)) : fit()}>
      <img className="artifact-image image-view-image" src={src} alt={name} draggable={false}
        onLoad={e => { natural.current = { width: e.currentTarget.naturalWidth, height: e.currentTarget.naturalHeight }; setDimensions(natural.current); fit() }}
        style={{ width: dimensions.width || undefined, height: dimensions.height || undefined, transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }} />
    </div>
  </div>
}
