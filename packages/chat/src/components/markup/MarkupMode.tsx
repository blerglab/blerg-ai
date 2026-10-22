// MarkupMode: draw over an image before sending it back to the session. The image sits in the
// stage as an <img> (layout, natural size); a <canvas> over it takes the pointer and shows the
// marks. Every mark is a vector shape in image pixels, redrawn from scratch on each change, so
// undo, a resize of the stage and the flattened copy all come from one drawing routine. Send
// composes image + marks + pins at the image's natural size, as <stem>.annotated.png.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ArtifactInfo } from '../../transport/types'
import type { Pin } from '../../model/feedback'
import './markup.css'

export interface MarkupModeProps {
  artifact: ArtifactInfo
  /** A blob: URL of the image bytes (already fetched by the viewer). */
  src: string
  /** Uploads the flattened PNG and sends the message; resolves false when nothing was sent. */
  onSend(file: File, pins: Pin[]): Promise<boolean>
  onClose(): void
}

type Tool = 'select' | 'pen' | 'arrow' | 'box' | 'pin'
type Swatch = 'accent' | 'danger' | 'info'
type Point = { x: number; y: number }

type Shape =
  | { kind: 'pen'; points: Point[]; color: Swatch; width: number }
  | { kind: 'arrow'; from: Point; to: Point; color: Swatch; width: number }
  | { kind: 'box'; from: Point; to: Point; color: Swatch; width: number }

// What undo takes back: the last mark or the last pin, whichever came later.
type Layer = { n: number; shape: Shape; note: string; hidden: boolean }
type Entry = { kind: 'shape' | 'pin'; n: number }

const TOOLS: Array<{ id: Tool; label: string; key: string }> = [
  { id: 'select', label: 'Select', key: 'V' },
  { id: 'pen', label: 'Pen', key: 'P' },
  { id: 'arrow', label: 'Arrow', key: 'A' },
  { id: 'box', label: 'Box', key: 'B' },
  { id: 'pin', label: 'Comment', key: 'N' },
]
const SWATCHES: Array<{ id: Swatch; label: string }> = [
  { id: 'accent', label: 'Accent color' },
  { id: 'danger', label: 'Danger color' },
  { id: 'info', label: 'Info color' },
]
const WIDTHS = [2, 4, 6]

// Where the token colours fall back to when the stylesheet is not there (tests, a bare mount).
const FALLBACK: Record<Swatch | 'accentFg', string> = {
  accent: '#3b82f6',
  danger: '#ef4444',
  info: '#0ea5e9',
  accentFg: '#ffffff',
}

/** The name the flattened copy is sent under: the artifact's stem plus `.annotated.png`. */
export function annotatedName(name: string): string {
  const dot = name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name
  return `${stem}.annotated.png`
}

export default function MarkupMode({ artifact, src, onSend, onClose }: MarkupModeProps) {
  const rootRef = useRef<HTMLDivElement | null>(null)
  const imgRef = useRef<HTMLImageElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const noteRefs = useRef(new Map<number, HTMLTextAreaElement>())

  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)
  const [tool, setTool] = useState<Tool>('pen')
  const [color, setColor] = useState<Swatch>('accent')
  const [width, setWidth] = useState(4)
  const [layers, setLayers] = useState<Layer[]>([])
  const [selected, setSelected] = useState<number | null>(null)
  const nextNumber = useRef(1)
  const mounted = useRef(false)
  const closeTimer = useRef<number | undefined>(undefined)
  const [pins, setPins] = useState<Pin[]>([])
  const [history, setHistory] = useState<Entry[]>([])
  const [draft, setDraft] = useState<Shape | null>(null)
  const pendingFocus = useRef<number | null>(null) // a pin just placed, whose note box gets the focus
  const [layout, setLayout] = useState(0) // bumped when the stage changes size
  const [sending, setSending] = useState(false)
  const [status, setStatus] = useState<{ kind: 'sent' } | { kind: 'failed'; text: string } | null>(null)

  const visible = useMemo(() => layers.filter(layer => !layer.hidden), [layers])
  const shapes = useMemo(() => visible.map(layer => layer.shape), [visible])
  const exportPins = useMemo(() => [...pins, ...visible.filter(layer => layer.note.trim()).map(layer => layerPin(layer, natural))].sort((a, b) => a.n - b.n), [pins, visible, natural])
  const empty = layers.length === 0 && pins.length === 0
  const exportEmpty = visible.length === 0 && pins.length === 0
  const selectedLayer = layers.find(layer => layer.n === selected)
  const frozen = sending || status?.kind === 'sent'

  useEffect(() => {
    mounted.current = true
    rootRef.current?.focus()
    return () => {
      mounted.current = false
      window.clearTimeout(closeTimer.current)
    }
  }, [])

  // Redraw the overlay from the vector state whenever anything that shows on it changes. The
  // canvas first takes the image's exact box inside the stage (the stage can be wider than a
  // height-capped image), then draws in image pixels at one uniform scale.
  useEffect(() => {
    const canvas = canvasRef.current
    const img = imgRef.current
    if (!canvas || !img || !natural) return
    const box = imageBox(img, natural)
    placeCanvas(canvas, img, box)
    const dpr = window.devicePixelRatio || 1
    const w = Math.max(1, Math.round((box.width || natural.w) * dpr))
    const h = Math.max(1, Math.round((box.height || natural.h) * dpr))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(1, 0, 0, 1, 0, 0)
    ctx.clearRect(0, 0, w, h)
    const k = box.width ? (box.scale * dpr) : Math.min(w / natural.w, h / natural.h)
    ctx.scale(k, k)
    drawMarks(ctx, natural, draft ? [...shapes, draft] : shapes, exportPins, colorsOf(rootRef.current))
    if (selectedLayer && !selectedLayer.hidden) drawSelection(ctx, selectedLayer.shape, natural, colorsOf(rootRef.current).accent)
  }, [natural, shapes, exportPins, draft, layout, selectedLayer])

  // Follow the stage's size (the dialog resizes, the phone rotates).
  useEffect(() => {
    const img = imgRef.current
    if (!img) return
    const bump = () => setLayout(n => n + 1)
    const ro = new ResizeObserver(bump)
    ro.observe(img)
    // The image can move inside the stage without changing size (the stage grows around it).
    if (img.parentElement) ro.observe(img.parentElement)
    window.addEventListener('resize', bump)
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', bump)
    }
  }, [])

  // A new pin's note box gets the focus once it is in the list.
  useEffect(() => {
    const n = pendingFocus.current
    if (n === null) return
    pendingFocus.current = null
    noteRefs.current.get(n)?.focus()
  }, [pins])

  function onImageLoad() {
    const img = imgRef.current
    if (!img) return
    const rect = img.getBoundingClientRect()
    setNatural({ w: img.naturalWidth || rect.width || 1, h: img.naturalHeight || rect.height || 1 })
  }

  // Pointer position in image pixels, measured against the image's own box (not the stage's),
  // with one scale factor for both axes.
  const toImage = useCallback((e: React.PointerEvent): Point | null => {
    const img = imgRef.current
    if (!img || !natural) return null
    const box = imageBox(img, natural)
    if (!box.scale) return null
    const x = (e.clientX - box.left) / box.scale
    const y = (e.clientY - box.top) / box.scale
    return { x: clamp(x, 0, natural.w), y: clamp(y, 0, natural.h) }
  }, [natural])

  function addPin(p: Point) {
    if (frozen) return
    if (!natural) return
    const pin: Pin = { n: nextNumber.current++, note: '', x: round4(p.x / natural.w), y: round4(p.y / natural.h) }
    setPins(ps => [...ps, pin])
    setHistory(h => [...h, {kind: 'pin', n: pin.n}])
    setSelected(null)
    pendingFocus.current = pin.n
    setStatus(null)
  }

  function onPointerDown(e: React.PointerEvent<HTMLCanvasElement>) {
    if (frozen) return
    if (e.button !== 0 && e.pointerType === 'mouse') return
    const p = toImage(e)
    if (!p) return
    e.preventDefault()
    if (tool === 'select') {
      const layer = [...layers].reverse().find(layer => !layer.hidden && hitShape(layer.shape, p, 10 / (imageBox(imgRef.current!, natural!).scale || 1)))
      selectLayer(layer?.n ?? null)
      return
    }
    if (tool === 'pin') {
      addPin(p)
      return
    }
    e.currentTarget.setPointerCapture?.(e.pointerId)
    setDraft(
      tool === 'pen'
        ? { kind: 'pen', points: [p], color, width }
        : { kind: tool, from: p, to: p, color, width },
    )
  }

  function onPointerMove(e: React.PointerEvent<HTMLCanvasElement>) {
    if (frozen) return
    if (!draft) return
    const p = toImage(e)
    if (!p) return
    e.preventDefault()
    setDraft(draft.kind === 'pen' ? { ...draft, points: [...draft.points, p] } : { ...draft, to: p })
  }

  function onPointerUp(e: React.PointerEvent<HTMLCanvasElement>) {
    if (frozen) return
    if (!draft) return
    const p = toImage(e)
    const done: Shape = !p ? draft : draft.kind === 'pen' ? { ...draft, points: [...draft.points, p] } : { ...draft, to: p }
    setDraft(null)
    if (isDegenerate(done)) return
    const n = nextNumber.current++
    setLayers(layers => [...layers, {n, shape: done, note: '', hidden: false}])
    setSelected(n)
    setHistory(h => [...h, {kind: 'shape', n}])
    setStatus(null)
  }

  function undo() {
    if (frozen) return
    const last = history[history.length - 1]
    if (!last) return
    setHistory(h => h.slice(0, -1))
    if (last.kind === 'shape') setLayers(layers => layers.filter(layer => layer.n !== last.n))
    else setPins(ps => ps.filter(pin => pin.n !== last.n))
    if (selected === last.n) setSelected(null)
  }

  function clear() {
    if (frozen) return
    setLayers([])
    setSelected(null)
    setPins([])
    setHistory([])
    setDraft(null)
  }

  function removePin(n: number) {
    if (frozen) return
    setPins(ps => ps.filter(p => p.n !== n))
    setHistory(h => h.filter(entry => entry.n !== n))
  }

  function setNote(n: number, note: string) {
    if (frozen) return
    setPins(ps => ps.map(p => (p.n === n ? { ...p, note } : p)))
  }

  function selectLayer(n: number | null) {
    if (frozen) return
    setSelected(n)
    setTool('select')
    const layer = layers.find(layer => layer.n === n)
    if (layer) { setColor(layer.shape.color); setWidth(layer.shape.width) }
  }

  function styleLayer(change: {color?: Swatch; width?: number}) {
    if (frozen) return
    if (change.color) setColor(change.color)
    if (change.width) setWidth(change.width)
    setLayers(layers => layers.map(layer => layer.n === selected ? {...layer, shape: {...layer.shape, ...change}} : layer))
  }

  function removeLayer(n: number) {
    if (frozen) return
    setLayers(layers => layers.filter(layer => layer.n !== n))
    setHistory(h => h.filter(entry => entry.n !== n))
    if (selected === n) setSelected(null)
  }

  async function send() {
    const img = imgRef.current
    if (!img || !natural || exportEmpty || frozen) return
    setSending(true)
    setStatus(null)
    try {
      const file = await flatten(img, natural, shapes, exportPins, colorsOf(rootRef.current), annotatedName(artifact.name))
      if (!mounted.current) return
      const ok = await onSend(file, exportPins)
      if (!mounted.current) return
      if (ok) {
        setStatus({ kind: 'sent' })
        closeTimer.current = window.setTimeout(onClose, 700)
      } else {
        setStatus({ kind: 'failed', text: 'Not sent — not connected' })
      }
    } catch (e) {
      if (!mounted.current) return
      setStatus({ kind: 'failed', text: `Not sent — ${e instanceof Error ? e.message : 'something went wrong'}` })
    } finally {
      if (mounted.current) setSending(false)
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      e.preventDefault()
      onClose()
      return
    }
    if (frozen) return
    const t = e.target as HTMLElement
    if (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || e.ctrlKey || e.metaKey || e.altKey) return
    const key = e.key.toLowerCase()
    const hit = TOOLS.find(x => x.key.toLowerCase() === key)
    if (hit) {
      setTool(hit.id)
      e.preventDefault()
    } else if (key === 'z') {
      undo()
      e.preventDefault()
    }
  }

  return (
    <div ref={rootRef} className="markup" data-testid="markup-mode" tabIndex={-1} onKeyDown={onKeyDown}>
      <fieldset className="markup-editing" disabled={frozen} aria-label="Image annotations">
      <div className="markup-toolbar" role="toolbar" aria-label="Mark up tools">
        <div className="markup-group">
          {TOOLS.map(t => (
            <button
              key={t.id}
              type="button"
              className="markup-tool"
              aria-pressed={tool === t.id}
              title={`${t.label} (${t.key})`}
              onClick={() => setTool(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div className="markup-group">
          <button type="button" className="markup-tool" onClick={undo} disabled={history.length === 0} title="Undo (Z)">Undo</button>
          <button type="button" className="markup-tool" onClick={clear} disabled={empty}>Clear</button>
        </div>
        <div className="markup-group" role="group" aria-label="Color">
          {SWATCHES.map(s => (
            <button
              key={s.id}
              type="button"
              className={`markup-swatch is-${s.id}`}
              aria-label={s.label}
              aria-pressed={color === s.id}
              onClick={() => styleLayer({color: s.id})}
            />
          ))}
        </div>
        <div className="markup-group" role="group" aria-label="Stroke width">
          {WIDTHS.map(w => (
            <button
              key={w}
              type="button"
              className="markup-width"
              aria-label={`Stroke ${w}`}
              aria-pressed={width === w}
              onClick={() => styleLayer({width: w})}
            >
              <span className="markup-width-dot" style={{ width: w + 4, height: w + 4 }} />
            </button>
          ))}
        </div>
      </div>

      <p className="markup-help">{selectedLayer ? `Editing ${selectedLayer.shape.kind} annotation ${selectedLayer.n}: color, stroke and comment.` : 'Draw a mark, or use Comment for a point comment. Select a drawing on the image or in Annotations to edit it.'}</p>
      <div className="markup-workspace">
      <div className="markup-stage" data-tool={tool}>
        <img ref={imgRef} className="markup-image" src={src} alt={artifact.name} onLoad={onImageLoad} draggable={false} />
        <canvas
          ref={canvasRef}
          className="markup-canvas"
          data-testid="markup-canvas"
          aria-hidden="true"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={() => setDraft(null)}
        />
      </div>

      <aside className="markup-sidebar" aria-label="Annotations">
        <h3>Annotations</h3>
        <p className="markup-help">Hidden annotations stay here and are excluded from Send. The original image is unchanged.</p>
        {layers.length === 0 && pins.length === 0 && <p className="markup-help">No annotations yet.</p>}
        <ol className="markup-layers">
          {layers.map(layer => <li key={layer.n} className={`markup-layer${selected === layer.n ? ' is-selected' : ''}${layer.hidden ? ' is-hidden' : ''}`}>
            <div className="markup-layer-heading">
              <button type="button" className="markup-tool" aria-label={`Select ${layer.shape.kind[0].toUpperCase() + layer.shape.kind.slice(1)} annotation ${layer.n}`} aria-pressed={selected === layer.n} onClick={() => selectLayer(layer.n)}>{layer.n}. {layer.shape.kind}</button>
              <button type="button" className="markup-tool" aria-label={`${layer.hidden ? 'Show' : 'Hide'} annotation ${layer.n}`} onClick={() => setLayers(layers => layers.map(item => item.n === layer.n ? {...item, hidden: !item.hidden} : item))}>{layer.hidden ? 'Show' : 'Hide'}</button>
              <button type="button" className="markup-tool" aria-label={`Delete annotation ${layer.n}`} onClick={() => removeLayer(layer.n)}>Delete</button>
            </div>
            <textarea className="markup-note" rows={2} aria-label={`Comment for annotation ${layer.n}`} placeholder="Comment on this drawing (optional)" value={layer.note} onFocus={() => selectLayer(layer.n)} onChange={event => setLayers(layers => layers.map(item => item.n === layer.n ? {...item, note: event.target.value} : item))} />
          </li>)}
        </ol>
      {pins.length > 0 && (
        <ol className="markup-pins" aria-label="Point comments">
          {pins.map(p => (
            <li key={p.n} className="markup-pin" data-testid={`markup-pin-${p.n}`} data-x={p.x} data-y={p.y}>
              <span className="markup-pin-badge" aria-hidden="true">{p.n}</span>
              <textarea
                ref={el => {
                  if (el) noteRefs.current.set(p.n, el)
                  else noteRefs.current.delete(p.n)
                }}
                className="markup-note"
                aria-label={`Note for pin ${p.n}`}
                placeholder="What should change here?"
                rows={1}
                value={p.note}
                onChange={e => setNote(p.n, e.target.value)}
              />
              <button type="button" className="markup-tool" aria-label={`Remove pin ${p.n}`} onClick={() => removePin(p.n)}>✕</button>
            </li>
          ))}
        </ol>
      )}

      </aside>
      </div>
      </fieldset>
      <div className="markup-actions">
        <span className="markup-status" role="status" aria-live="polite">
          {status?.kind === 'sent' ? 'Sent' : status?.kind === 'failed' ? status.text : ''}
        </span>
        <button type="button" className="markup-tool" onClick={onClose}>Cancel</button>
        <button type="button" className="markup-send" disabled={exportEmpty || frozen || !natural} onClick={() => void send()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </div>
    </div>
  )
}

type ImageBox = { left: number; top: number; width: number; height: number; scale: number }

/** Where the image's pixels are on screen: the <img> box, with the natural size fitted into it
 *  at one uniform scale (centered if the box's aspect ever differs from the image's). */
function imageBox(img: HTMLImageElement, natural: { w: number; h: number }): ImageBox {
  const rect = img.getBoundingClientRect()
  if (!rect.width || !rect.height) return { left: rect.left, top: rect.top, width: 0, height: 0, scale: 0 }
  const scale = Math.min(rect.width / natural.w, rect.height / natural.h)
  const width = natural.w * scale
  const height = natural.h * scale
  return { left: rect.left + (rect.width - width) / 2, top: rect.top + (rect.height - height) / 2, width, height, scale }
}

/** Puts the overlay canvas exactly over the image's pixels inside the (positioned) stage. */
function placeCanvas(canvas: HTMLCanvasElement, img: HTMLImageElement, box: ImageBox) {
  const stage = img.offsetParent instanceof HTMLElement ? img.offsetParent : img.parentElement
  if (!stage || !box.width) return
  const s = stage.getBoundingClientRect()
  canvas.style.left = `${box.left - s.left - stage.clientLeft}px`
  canvas.style.top = `${box.top - s.top - stage.clientTop}px`
  canvas.style.width = `${box.width}px`
  canvas.style.height = `${box.height}px`
}

function layerPin(layer: Layer, natural: {w: number; h: number} | null): Pin {
  const shape = layer.shape
  const point = shape.kind === 'pen' ? shape.points[Math.floor(shape.points.length / 2)] : shape.kind === 'arrow' ? shape.to : {x: (shape.from.x + shape.to.x) / 2, y: (shape.from.y + shape.to.y) / 2}
  return {n: layer.n, note: layer.note, x: round4(point.x / (natural?.w || 1)), y: round4(point.y / (natural?.h || 1))}
}

function segmentDistance(point: Point, a: Point, b: Point) {
  const dx = b.x - a.x, dy = b.y - a.y
  const t = clamp(((point.x - a.x) * dx + (point.y - a.y) * dy) / (dx * dx + dy * dy || 1), 0, 1)
  return Math.hypot(point.x - a.x - t * dx, point.y - a.y - t * dy)
}

function hitShape(shape: Shape, point: Point, tolerance: number) {
  if (shape.kind === 'pen') return shape.points.some((p, i) => i > 0 && segmentDistance(point, shape.points[i - 1], p) <= tolerance)
  if (shape.kind === 'arrow') return segmentDistance(point, shape.from, shape.to) <= tolerance
  const {from, to} = shape
  return [[from, {x: to.x, y: from.y}], [{x: to.x, y: from.y}, to], [to, {x: from.x, y: to.y}], [{x: from.x, y: to.y}, from]].some(([a, b]) => segmentDistance(point, a, b) <= tolerance)
}

function drawSelection(ctx: CanvasRenderingContext2D, shape: Shape, natural: {w: number; h: number}, color: string) {
  const points = shape.kind === 'pen' ? shape.points : [shape.from, shape.to]
  const bounds = points.reduce((box, point) => ({left: Math.min(box.left, point.x), right: Math.max(box.right, point.x), top: Math.min(box.top, point.y), bottom: Math.max(box.bottom, point.y)}), {left: Infinity, right: -Infinity, top: Infinity, bottom: -Infinity})
  const pad = 7 * unitFor(natural)
  ctx.save()
  ctx.setLineDash([4 * unitFor(natural), 4 * unitFor(natural)])
  for (const [stroke, width] of [[haloFor(color), 4], [color, 2]] as const) {
    ctx.strokeStyle = stroke
    ctx.lineWidth = width * unitFor(natural)
    ctx.strokeRect(bounds.left - pad, bounds.top - pad, bounds.right - bounds.left + 2 * pad, bounds.bottom - bounds.top + 2 * pad)
  }
  ctx.restore()
}

// ─── drawing ──────────────────────────────────────────────────────────────────

type Colors = Record<Swatch | 'accentFg', string>

/** The token colours as the page resolves them, falling back when there is no stylesheet. */
function colorsOf(el: HTMLElement | null): Colors {
  const out = { ...FALLBACK }
  if (!el) return out
  const style = window.getComputedStyle(el)
  const read = (name: string) => style.getPropertyValue(name).trim()
  out.accent = read('--chat-accent') || out.accent
  out.danger = read('--chat-danger') || out.danger
  out.info = read('--chat-info') || out.info
  out.accentFg = read('--chat-accent-fg') || out.accentFg
  return out
}

/** Marks are specified in screen-ish pixels (a stroke of 4); a large image scales them up so
 *  they still read when it is shown fitted. */
function unitFor(natural: { w: number; h: number }): number {
  return Math.max(1, Math.min(natural.w, natural.h) / 800)
}

/** Draws the marks and the pins in image pixels; the caller has scaled the context so that the
 *  image's natural size fills the canvas. Pins keep fractions and are placed from `natural`. */
function drawMarks(ctx: CanvasRenderingContext2D, natural: { w: number; h: number }, shapes: Shape[], pins: Pin[], colors: Colors) {
  const unit = unitFor(natural)
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  for (const s of shapes) {
    const stroke = colors[s.color]
    const halo = haloFor(stroke)
    const w = s.width * unit
    // The halo first, wider, then the mark on top: an outline that reads on any image.
    strokeShape(ctx, s, halo, w + 3 * unit)
    strokeShape(ctx, s, stroke, w)
  }
  const r = 13 * unit
  for (const p of pins) {
    const cx = p.x * natural.w
    const cy = p.y * natural.h
    ctx.beginPath()
    ctx.arc(cx, cy, r + 2 * unit, 0, Math.PI * 2)
    ctx.fillStyle = haloFor(colors.accent)
    ctx.fill()
    ctx.beginPath()
    ctx.arc(cx, cy, r, 0, Math.PI * 2)
    ctx.fillStyle = colors.accent
    ctx.fill()
    ctx.fillStyle = colors.accentFg
    ctx.font = `bold ${Math.round(14 * unit)}px sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(String(p.n), cx, cy + unit * 0.5)
  }
}

function strokeShape(ctx: CanvasRenderingContext2D, s: Shape, color: string, width: number) {
  ctx.strokeStyle = color
  ctx.fillStyle = color
  ctx.lineWidth = width
  if (s.kind === 'pen') {
    ctx.beginPath()
    s.points.forEach((p, i) => (i === 0 ? ctx.moveTo(p.x, p.y) : ctx.lineTo(p.x, p.y)))
    if (s.points.length === 1) ctx.lineTo(s.points[0].x + 0.01, s.points[0].y)
    ctx.stroke()
  } else if (s.kind === 'box') {
    const x = Math.min(s.from.x, s.to.x)
    const y = Math.min(s.from.y, s.to.y)
    ctx.strokeRect(x, y, Math.abs(s.to.x - s.from.x), Math.abs(s.to.y - s.from.y))
  } else {
    const { from, to } = s
    const angle = Math.atan2(to.y - from.y, to.x - from.x)
    const head = Math.max(10, width * 3)
    const base = { x: to.x - head * Math.cos(angle), y: to.y - head * Math.sin(angle) }
    ctx.beginPath()
    ctx.moveTo(from.x, from.y)
    ctx.lineTo(base.x, base.y)
    ctx.stroke()
    const spread = Math.PI / 7
    ctx.beginPath()
    ctx.moveTo(to.x, to.y)
    ctx.lineTo(to.x - head * Math.cos(angle - spread), to.y - head * Math.sin(angle - spread))
    ctx.lineTo(to.x - head * Math.cos(angle + spread), to.y - head * Math.sin(angle + spread))
    ctx.closePath()
    ctx.fill()
  }
}

/** White under a dark colour, black under a light one. */
export function haloFor(color: string): string {
  const rgb = parseColor(color)
  if (!rgb) return '#ffffff'
  const [r, g, b] = rgb.map(c => c / 255)
  const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
  return lum > 0.6 ? '#000000' : '#ffffff'
}

function parseColor(c: string): [number, number, number] | null {
  const s = c.trim()
  const hex = /^#([0-9a-f]{3,8})$/i.exec(s)?.[1]
  if (hex) {
    if (hex.length === 3 || hex.length === 4) return [0, 1, 2].map(i => parseInt(hex[i] + hex[i], 16)) as [number, number, number]
    if (hex.length === 6 || hex.length === 8) return [0, 2, 4].map(i => parseInt(hex.slice(i, i + 2), 16)) as [number, number, number]
    return null
  }
  const fn = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/i.exec(s)
  if (fn) return [Number(fn[1]), Number(fn[2]), Number(fn[3])]
  return null
}

/** The image with the marks drawn over it at its natural size, as a PNG File. */
async function flatten(
  img: HTMLImageElement,
  natural: { w: number; h: number },
  shapes: Shape[],
  pins: Pin[],
  colors: Colors,
  name: string,
): Promise<File> {
  const off = document.createElement('canvas')
  off.width = natural.w
  off.height = natural.h
  const ctx = off.getContext('2d')
  if (!ctx) throw new Error('no canvas')
  ctx.drawImage(img, 0, 0, natural.w, natural.h)
  drawMarks(ctx, natural, shapes, pins, colors)
  const blob = await new Promise<Blob | null>(resolve => off.toBlob(resolve, 'image/png'))
  if (!blob) throw new Error('could not encode the image')
  return new File([blob], name, { type: 'image/png' })
}

function isDegenerate(s: Shape): boolean {
  if (s.kind === 'pen') return s.points.length < 2
  return Math.abs(s.to.x - s.from.x) < 2 && Math.abs(s.to.y - s.from.y) < 2
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, n))
}

function round4(n: number): number {
  return Math.round(n * 10000) / 10000
}
