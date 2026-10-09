// MarkupMode: draw over an image before sending it back to the session. The image sits in the
// stage as an <img> (layout, natural size); a <canvas> over it takes the pointer and shows the
// marks. Every mark is a vector shape in image pixels, redrawn from scratch on each change, so
// undo, a resize of the stage and the flattened copy all come from one drawing routine. Send
// composes image + marks + pins at the image's natural size, as <stem>.annotated.png.
import { useCallback, useEffect, useRef, useState } from 'react'
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

type Tool = 'pen' | 'arrow' | 'box' | 'pin'
type Swatch = 'accent' | 'danger' | 'info'
type Point = { x: number; y: number }

type Shape =
  | { kind: 'pen'; points: Point[]; color: Swatch; width: number }
  | { kind: 'arrow'; from: Point; to: Point; color: Swatch; width: number }
  | { kind: 'box'; from: Point; to: Point; color: Swatch; width: number }

// What undo takes back: the last mark or the last pin, whichever came later.
type Entry = 'shape' | 'pin'

const TOOLS: Array<{ id: Tool; label: string; key: string }> = [
  { id: 'pen', label: 'Pen', key: 'P' },
  { id: 'arrow', label: 'Arrow', key: 'A' },
  { id: 'box', label: 'Box', key: 'B' },
  { id: 'pin', label: 'Pin', key: 'N' },
]
const SWATCHES: Array<{ id: Swatch; label: string }> = [
  { id: 'accent', label: 'Accent colour' },
  { id: 'danger', label: 'Danger colour' },
  { id: 'info', label: 'Info colour' },
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
  const [shapes, setShapes] = useState<Shape[]>([])
  const [pins, setPins] = useState<Pin[]>([])
  const [history, setHistory] = useState<Entry[]>([])
  const [draft, setDraft] = useState<Shape | null>(null)
  const pendingFocus = useRef<number | null>(null) // a pin just placed, whose note box gets the focus
  const [layout, setLayout] = useState(0) // bumped when the stage changes size
  const [sending, setSending] = useState(false)
  const [status, setStatus] = useState<{ kind: 'sent' } | { kind: 'failed'; text: string } | null>(null)

  const nextPin = pins.reduce((m, p) => Math.max(m, p.n), 0) + 1
  const empty = shapes.length === 0 && pins.length === 0

  useEffect(() => rootRef.current?.focus(), [])

  // Redraw the overlay from the vector state whenever anything that shows on it changes.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || !natural) return
    const rect = canvas.getBoundingClientRect()
    const dpr = window.devicePixelRatio || 1
    const w = Math.max(1, Math.round((rect.width || natural.w) * dpr))
    const h = Math.max(1, Math.round((rect.height || natural.h) * dpr))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(1, 0, 0, 1, 0, 0)
    ctx.clearRect(0, 0, w, h)
    ctx.scale(w / natural.w, h / natural.h)
    drawMarks(ctx, natural, draft ? [...shapes, draft] : shapes, pins, colorsOf(rootRef.current))
  }, [natural, shapes, pins, draft, layout])

  // Follow the stage's size (the dialog resizes, the phone rotates).
  useEffect(() => {
    const img = imgRef.current
    if (!img) return
    const bump = () => setLayout(n => n + 1)
    const ro = new ResizeObserver(bump)
    ro.observe(img)
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

  // Pointer position in image pixels.
  const toImage = useCallback((e: React.PointerEvent): Point | null => {
    const canvas = canvasRef.current
    if (!canvas || !natural) return null
    const rect = canvas.getBoundingClientRect()
    if (!rect.width || !rect.height) return null
    const x = ((e.clientX - rect.left) / rect.width) * natural.w
    const y = ((e.clientY - rect.top) / rect.height) * natural.h
    return { x: clamp(x, 0, natural.w), y: clamp(y, 0, natural.h) }
  }, [natural])

  function addPin(p: Point) {
    if (!natural) return
    const pin: Pin = { n: nextPin, note: '', x: round4(p.x / natural.w), y: round4(p.y / natural.h) }
    setPins(ps => [...ps, pin])
    setHistory(h => [...h, 'pin'])
    pendingFocus.current = pin.n
    setStatus(null)
  }

  function onPointerDown(e: React.PointerEvent<HTMLCanvasElement>) {
    if (e.button !== 0 && e.pointerType === 'mouse') return
    const p = toImage(e)
    if (!p) return
    e.preventDefault()
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
    if (!draft) return
    const p = toImage(e)
    if (!p) return
    e.preventDefault()
    setDraft(draft.kind === 'pen' ? { ...draft, points: [...draft.points, p] } : { ...draft, to: p })
  }

  function onPointerUp(e: React.PointerEvent<HTMLCanvasElement>) {
    if (!draft) return
    const p = toImage(e)
    const done: Shape = !p ? draft : draft.kind === 'pen' ? { ...draft, points: [...draft.points, p] } : { ...draft, to: p }
    setDraft(null)
    if (isDegenerate(done)) return
    setShapes(s => [...s, done])
    setHistory(h => [...h, 'shape'])
    setStatus(null)
  }

  function undo() {
    const last = history[history.length - 1]
    if (!last) return
    setHistory(h => h.slice(0, -1))
    if (last === 'shape') setShapes(s => s.slice(0, -1))
    else setPins(ps => ps.slice(0, -1))
  }

  function clear() {
    setShapes([])
    setPins([])
    setHistory([])
    setDraft(null)
  }

  function removePin(n: number) {
    setPins(ps => ps.filter(p => p.n !== n))
    // The history entry of a pin removed by hand is spent: drop the newest 'pin' entry.
    setHistory(h => {
      const i = h.lastIndexOf('pin')
      return i < 0 ? h : [...h.slice(0, i), ...h.slice(i + 1)]
    })
  }

  function setNote(n: number, note: string) {
    setPins(ps => ps.map(p => (p.n === n ? { ...p, note } : p)))
  }

  async function send() {
    const img = imgRef.current
    if (!img || !natural || empty || sending) return
    setSending(true)
    setStatus(null)
    try {
      const file = await flatten(img, natural, shapes, pins, colorsOf(rootRef.current), annotatedName(artifact.name))
      const ok = await onSend(file, pins)
      if (ok) {
        setStatus({ kind: 'sent' })
        window.setTimeout(onClose, 700)
      } else {
        setStatus({ kind: 'failed', text: 'Not sent — not connected' })
      }
    } catch (e) {
      setStatus({ kind: 'failed', text: `Not sent — ${e instanceof Error ? e.message : 'something went wrong'}` })
    } finally {
      setSending(false)
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      e.preventDefault()
      onClose()
      return
    }
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
        <div className="markup-group" role="group" aria-label="Colour">
          {SWATCHES.map(s => (
            <button
              key={s.id}
              type="button"
              className={`markup-swatch is-${s.id}`}
              aria-label={s.label}
              aria-pressed={color === s.id}
              onClick={() => setColor(s.id)}
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
              onClick={() => setWidth(w)}
            >
              <span className="markup-width-dot" style={{ width: w + 4, height: w + 4 }} />
            </button>
          ))}
        </div>
      </div>

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

      {pins.length > 0 && (
        <ol className="markup-pins" aria-label="Pins">
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

      <div className="markup-actions">
        <span className="markup-status" role="status" aria-live="polite">
          {status?.kind === 'sent' ? 'Sent' : status?.kind === 'failed' ? status.text : ''}
        </span>
        <button type="button" className="markup-tool" onClick={onClose}>Cancel</button>
        <button type="button" className="markup-send" disabled={empty || sending || !natural} onClick={() => void send()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </div>
    </div>
  )
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
