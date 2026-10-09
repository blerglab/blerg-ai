// The PDF side of the review mode: every page as a canvas that fits the pane, pdf.js's text layer
// over it so a selection is real text, and the requests' quotes highlighted where they sit on the
// page. pdf.js comes in on demand (src/pdf/loader.ts); pages render as they scroll near.
import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import type { PDFDocumentLoadingTask, PDFDocumentProxy, PageViewport, RenderTask, TextLayer } from 'pdfjs-dist'
import type { Anchor, ReviewRequest } from '../../model/review'
import { anchorSelection, findQuote } from '../../model/anchor'
import { loadPdfJs, type PdfJs } from '../../pdf/loader'
import './pdf.css'

export interface PdfPagesProps {
  blob: Blob
  requests: ReviewRequest[]
  focusedId?: string | null
  onHover(id: string | null): void
  onSelection(anchor: Anchor | null): void
}

/** How far outside the viewport a page starts rendering. */
const NEAR = '600px'
/** Resizes come in bursts; the pages re-render once the burst settles. */
const RESIZE_SETTLE_MS = 100

const NO_PAGES: ReadonlySet<number> = new Set()

interface PageInfo {
  n: number
  /** The page's size at scale 1, in CSS px. */
  width: number
  height: number
}

interface Loaded {
  pdfjs: PdfJs
  doc: PDFDocumentProxy
  pages: PageInfo[]
}

export default function PdfPages({ blob, requests, focusedId, onHover, onSelection }: PdfPagesProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const pageEls = useRef(new Map<number, HTMLDivElement>())
  // What was opened and which pages are near, each kept with the blob it belongs to: another blob
  // starts from nothing without an effect having to reset anything.
  const [opened, setOpened] = useState<{ blob: Blob; loaded: Loaded | null; failed: boolean } | null>(null)
  const [width, setWidth] = useState(0)
  const [nearOf, setNearOf] = useState<{ blob: Blob; pages: ReadonlySet<number> } | null>(null)
  const loaded = opened?.blob === blob ? opened.loaded : null
  const failed = opened?.blob === blob && opened.failed
  const near = nearOf?.blob === blob ? nearOf.pages : NO_PAGES
  // Without IntersectionObserver every page renders.
  const noObserver = typeof IntersectionObserver === 'undefined'
  const onSelectionRef = useRef(onSelection)
  useLayoutEffect(() => {
    onSelectionRef.current = onSelection
  }, [onSelection])

  // The document: opened once per blob, closed when the blob changes or the pages unmount.
  useEffect(() => {
    let alive = true
    let task: PDFDocumentLoadingTask | null = null
    ;(async () => {
      const [pdfjs, data] = await Promise.all([loadPdfJs(), blob.arrayBuffer()])
      if (!alive) return
      task = pdfjs.getDocument({ data })
      const doc = await task.promise
      const pages: PageInfo[] = []
      for (let n = 1; n <= doc.numPages; n++) {
        const page = await doc.getPage(n)
        const { width, height } = page.getViewport({ scale: 1 })
        pages.push({ n, width, height })
      }
      if (alive) setOpened({ blob, loaded: { pdfjs, doc, pages }, failed: false })
    })().catch(() => {
      if (alive) setOpened({ blob, loaded: null, failed: true })
    })
    return () => {
      alive = false
      task?.destroy()
    }
  }, [blob])

  // The width the pages fit: the container's content box, re-measured as it resizes.
  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    const measure = () => {
      const cs = getComputedStyle(el)
      setWidth(Math.max(0, el.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0)))
    }
    measure()
    let timer: ReturnType<typeof setTimeout> | null = null
    const ro = new ResizeObserver(() => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(measure, RESIZE_SETTLE_MS)
    })
    ro.observe(el)
    return () => {
      ro.disconnect()
      if (timer) clearTimeout(timer)
    }
  }, [])

  // Which pages are near the viewport: those render; the rest are placeholders.
  useEffect(() => {
    if (!loaded || noObserver) return
    const io = new IntersectionObserver(
      entries => {
        setNearOf(prev => {
          const next = new Set(prev?.blob === blob ? prev.pages : NO_PAGES)
          for (const e of entries) {
            const n = Number((e.target as HTMLElement).dataset.page)
            if (e.isIntersecting) next.add(n)
            else next.delete(n)
          }
          return { blob, pages: next }
        })
      },
      { rootMargin: NEAR },
    )
    for (const el of pageEls.current.values()) io.observe(el)
    return () => io.disconnect()
  }, [loaded, noObserver, blob])

  // A selection inside one page's text layer is reported as an anchor on that page.
  useEffect(() => {
    const onChange = () => {
      const sel = document.getSelection()
      const root = containerRef.current
      if (!sel || !root) return
      if (sel.rangeCount === 0 || sel.isCollapsed) {
        onSelectionRef.current(null)
        return
      }
      const range = sel.getRangeAt(0)
      const start = elementOf(range.startContainer)
      const end = elementOf(range.endContainer)
      const layer = start?.closest<HTMLElement>('.textLayer') ?? null
      if (!layer || !root.contains(layer)) {
        if (root.contains(start) || root.contains(end)) onSelectionRef.current(null)
        return
      }
      if (!end || !layer.contains(end)) {
        onSelectionRef.current(null)
        return
      }
      const page = Number(layer.closest<HTMLElement>('.pdf-page')?.dataset.page)
      onSelectionRef.current(anchorSelection(layer, sel, { page }))
    }
    document.addEventListener('selectionchange', onChange)
    return () => document.removeEventListener('selectionchange', onChange)
  }, [])

  if (failed) {
    return (
      <div className="pdf-pages" ref={containerRef}>
        <p className="pdf-error">This PDF could not be opened.</p>
      </div>
    )
  }

  return (
    <div className="pdf-pages" ref={containerRef}>
      {loaded?.pages.map(info => (
        <PdfPage
          key={info.n}
          loaded={loaded}
          info={info}
          scale={width > 0 ? width / info.width : 0}
          visible={noObserver || near.has(info.n)}
          requests={requests}
          focusedId={focusedId ?? null}
          onHover={onHover}
          pageEls={pageEls}
        />
      ))}
    </div>
  )
}

function elementOf(node: Node | null): HTMLElement | null {
  if (!node) return null
  return node.nodeType === Node.ELEMENT_NODE ? (node as HTMLElement) : node.parentElement
}

// ── one page ──────────────────────────────────────────────────────────────────

interface Box {
  id: string
  status: ReviewRequest['status']
  left: number
  top: number
  width: number
  height: number
}

interface PdfPageProps {
  loaded: Loaded
  info: PageInfo
  scale: number
  visible: boolean
  requests: ReviewRequest[]
  focusedId: string | null
  onHover(id: string | null): void
  pageEls: RefObject<Map<number, HTMLDivElement>>
}

function PdfPage({ loaded, info, scale, visible, requests, focusedId, onHover, pageEls }: PdfPageProps) {
  const { n } = info
  const pageRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const layerRef = useRef<HTMLDivElement>(null)
  // 0 while the text layer is empty; bumped each time it is (re)rendered.
  const [layerVersion, setLayerVersion] = useState(0)
  const [boxes, setBoxes] = useState<Box[]>([])

  useLayoutEffect(() => {
    const el = pageRef.current
    const els = pageEls.current
    if (!el) return
    els.set(n, el)
    return () => {
      els.delete(n)
    }
  }, [n, pageEls])

  // The canvas and the text layer, drawn when the page is near and at the width it fits.
  useEffect(() => {
    const canvas = canvasRef.current
    const layer = layerRef.current
    if (!visible || scale <= 0 || !canvas || !layer) return
    const { pdfjs, doc } = loaded
    let cancelled = false
    let render: RenderTask | null = null
    let text: TextLayer | null = null
    ;(async () => {
      const page = await doc.getPage(n)
      if (cancelled) return
      const viewport = page.getViewport({ scale })
      const dpr = window.devicePixelRatio || 1
      const deviceViewport = dpr === 1 ? viewport : page.getViewport({ scale: scale * dpr })
      canvas.width = Math.floor(deviceViewport.width)
      canvas.height = Math.floor(deviceViewport.height)
      render = page.render({ canvas, viewport: deviceViewport })
      await render.promise
      if (cancelled) return
      const textContent = await page.getTextContent()
      if (cancelled) return
      layer.replaceChildren()
      text = new pdfjs.TextLayer({ textContentSource: textContent, container: layer, viewport })
      await text.render()
      if (cancelled) return
      layoutTextLayer(layer, viewport)
      setLayerVersion(v => v + 1)
    })().catch(err => {
      if (!cancelled && !(err instanceof loaded.pdfjs.RenderingCancelledException)) {
        console.warn(`PDF page ${n} did not render`, err)
      }
    })
    return () => {
      cancelled = true
      render?.cancel()
      text?.cancel()
      layer.replaceChildren()
      canvas.width = 0
      canvas.height = 0
      setLayerVersion(0)
    }
  }, [loaded, n, scale, visible])

  // The highlight boxes: a box per client rect of each request's quote, once the text is there.
  useEffect(() => {
    const layer = layerRef.current
    const pageEl = pageRef.current
    if (!layerVersion || !layer || !pageEl) {
      setBoxes([])
      return
    }
    const layOut = () => {
      const base = pageEl.getBoundingClientRect()
      const out: Box[] = []
      for (const r of requests) {
        if (r.anchor.page !== n) continue
        const range = findQuote(layer, r.anchor)
        if (!range) continue
        for (const rect of Array.from(range.getClientRects())) {
          if (rect.width === 0 && rect.height === 0) continue
          out.push({
            id: r.id,
            status: r.status,
            left: rect.left - base.left,
            top: rect.top - base.top,
            width: rect.width,
            height: rect.height,
          })
        }
      }
      setBoxes(out)
    }
    layOut()
    window.addEventListener('resize', layOut)
    return () => window.removeEventListener('resize', layOut)
  }, [layerVersion, requests, n])

  return (
    <div
      className="pdf-page"
      ref={pageRef}
      data-page={n}
      data-rendered={layerVersion > 0 ? 'true' : undefined}
      style={{ aspectRatio: `${info.width} / ${info.height}` }}
      aria-label={`Page ${n}`}
    >
      {!layerVersion && <div className="pdf-page-placeholder" />}
      <canvas ref={canvasRef} />
      <div className="pdf-hl-layer">
        {boxes.map((b, i) => (
          <div
            key={`${b.id}:${i}`}
            className={`pdf-hl ${b.status}${b.id === focusedId ? ' focus' : ''}`}
            data-request={b.id}
            style={{ left: b.left, top: b.top, width: b.width, height: b.height }}
            onMouseEnter={() => onHover(b.id)}
            onMouseLeave={() => onHover(null)}
          />
        ))}
      </div>
      <div className="textLayer" ref={layerRef} />
    </div>
  )
}

/**
 * pdf.js lays its text runs out through custom properties its own stylesheet reads
 * (--font-height, --scale-x, --rotate against --total-scale-factor). The package's stylesheets
 * style against --chat-* tokens only, so the same geometry is written inline here instead.
 */
function layoutTextLayer(layer: HTMLElement, viewport: PageViewport) {
  // pdf.js sizes the layer by --total-scale-factor; it fills the page box instead.
  layer.style.width = ''
  layer.style.height = ''
  for (const span of Array.from(layer.querySelectorAll<HTMLElement>('span'))) {
    const fontHeight = parseFloat(span.style.getPropertyValue('--font-height'))
    if (fontHeight > 0) span.style.fontSize = `${fontHeight * viewport.scale}px`
    const rotate = span.style.getPropertyValue('--rotate').trim()
    const scaleX = span.style.getPropertyValue('--scale-x').trim()
    const transform: string[] = []
    if (rotate) transform.push(`rotate(${rotate})`)
    if (scaleX) transform.push(`scaleX(${scaleX})`)
    if (transform.length) span.style.transform = transform.join(' ')
  }
}
