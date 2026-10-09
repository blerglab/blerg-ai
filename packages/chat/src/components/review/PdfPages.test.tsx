// PdfPages over a fake pdf.js (jsdom cannot render a PDF): the pages and their placeholders, the
// text layer of the pages near the viewport, a selection reported with its page, and the
// highlights of the requests on a rendered page. The anchor model is the real one.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import PdfPages from './PdfPages'
import type { Anchor, ReviewRequest } from '../../model/review'

// ── a fake pdf.js ─────────────────────────────────────────────────────────────

const fake = vi.hoisted(() => {
  const PAGE_TEXT: Record<number, string[]> = {
    1: ['Hello world'],
    2: ['The mean of ', 'the sample', ' and the sample again'],
    3: ['Last page'],
  }
  const WIDTH = 600
  const HEIGHT = 800

  class FakeTextLayer {
    static rendered: number[] = []
    private items: Array<{ str: string }>
    private container: HTMLElement
    constructor(opts: { textContentSource: { items: Array<{ str: string }> }; container: HTMLElement; viewport: unknown }) {
      this.items = opts.textContentSource.items
      this.container = opts.container
    }
    async render() {
      for (const item of this.items) {
        const span = document.createElement('span')
        span.textContent = item.str
        span.style.setProperty('--font-height', '12px')
        span.style.setProperty('--scale-x', '1.5')
        this.container.append(span)
      }
    }
    cancel() {}
  }

  class RenderingCancelledException extends Error {}

  const viewport = (scale: number) => ({ width: WIDTH * scale, height: HEIGHT * scale, scale })
  const page = (n: number) => ({
    getViewport: ({ scale }: { scale: number }) => viewport(scale),
    render: vi.fn(() => ({ promise: Promise.resolve(), cancel: vi.fn() })),
    getTextContent: async () => ({ items: PAGE_TEXT[n].map(str => ({ str })) }),
  })
  const destroy = vi.fn()
  const doc = {
    numPages: 3,
    getPage: async (n: number) => page(n),
  }
  const pdfjs = {
    TextLayer: FakeTextLayer,
    RenderingCancelledException,
    GlobalWorkerOptions: { workerSrc: '' },
    getDocument: vi.fn(() => ({ promise: Promise.resolve(doc), destroy })),
  }
  return { pdfjs, destroy, PAGE_TEXT }
})

vi.mock('../../pdf/loader', () => ({
  loadPdfJs: () => Promise.resolve(fake.pdfjs),
}))

// ── browser bits jsdom lacks ──────────────────────────────────────────────────

class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = []
  targets = new Set<Element>()
  private cb: IntersectionObserverCallback
  options?: IntersectionObserverInit
  constructor(cb: IntersectionObserverCallback, options?: IntersectionObserverInit) {
    this.cb = cb
    this.options = options
    FakeIntersectionObserver.instances.push(this)
  }
  observe(el: Element) {
    this.targets.add(el)
  }
  unobserve(el: Element) {
    this.targets.delete(el)
  }
  disconnect() {
    this.targets.clear()
  }
  /** Marks the given pages as intersecting (the rest untouched). */
  fire(pages: number[], isIntersecting = true) {
    const entries = [...this.targets]
      .filter(t => pages.includes(Number((t as HTMLElement).dataset.page)))
      .map(target => ({ target, isIntersecting }) as IntersectionObserverEntry)
    this.cb(entries, this as unknown as IntersectionObserver)
  }
}

const RECT = { left: 10, top: 20, width: 100, height: 12, right: 110, bottom: 32, x: 10, y: 20 }
const rects = () => [RECT] as unknown as DOMRectList

function req(over: Partial<ReviewRequest> = {}): ReviewRequest {
  return {
    id: 'r1',
    anchor: { kind: 'text', quote: 'the sample', page: 2, occurrence: 1 },
    text: 'median, not mean',
    status: 'open',
    createdAt: '2026-10-06T10:00:00Z',
    ...over,
  }
}

const blob = new Blob(['%PDF-1.4 fake'], { type: 'application/pdf' })
let onHover: ReturnType<typeof vi.fn<(id: string | null) => void>>
let onSelection: ReturnType<typeof vi.fn<(anchor: Anchor | null) => void>>

const io = () => FakeIntersectionObserver.instances.at(-1)!
const pageEl = (root: HTMLElement, n: number) => root.querySelector<HTMLElement>(`.pdf-page[data-page="${n}"]`)!

/** Selects `text` inside page n's text layer and lets the document know. */
function select(root: HTMLElement, n: number, text: string) {
  const layer = pageEl(root, n).querySelector('.textLayer')!
  const span = [...layer.querySelectorAll('span')].find(s => s.textContent!.includes(text))!
  const node = span.firstChild as Text
  const at = node.data.indexOf(text)
  const range = document.createRange()
  range.setStart(node, at)
  range.setEnd(node, at + text.length)
  const sel = document.getSelection()!
  sel.removeAllRanges()
  sel.addRange(range)
  document.dispatchEvent(new Event('selectionchange'))
}

async function show(props: Partial<React.ComponentProps<typeof PdfPages>> = {}) {
  const r = render(<PdfPages blob={blob} requests={[]} onHover={onHover} onSelection={onSelection} {...props} />)
  await waitFor(() => expect(r.container.querySelectorAll('.pdf-page')).toHaveLength(3))
  return r
}

beforeEach(() => {
  onHover = vi.fn<(id: string | null) => void>()
  onSelection = vi.fn<(anchor: Anchor | null) => void>()
  FakeIntersectionObserver.instances = []
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, get: () => 600 })
  Object.defineProperty(Range.prototype, 'getClientRects', { configurable: true, value: rects })
  fake.destroy.mockClear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientWidth
  delete (Range.prototype as unknown as Record<string, unknown>).getClientRects
  document.getSelection()?.removeAllRanges()
})

describe('PdfPages: pages', () => {
  it('lays out one placeholder per page at its aspect ratio, and watches them scroll near', async () => {
    const { container } = await show()
    const pages = container.querySelectorAll<HTMLElement>('.pdf-page')
    expect(pages).toHaveLength(3)
    expect(container.querySelectorAll('.pdf-page-placeholder')).toHaveLength(3)
    expect(pages[0].style.aspectRatio).toBe('600 / 800')
    expect(pages[1].getAttribute('aria-label')).toBe('Page 2')
    expect(io().targets.size).toBe(3)
    expect(io().options?.rootMargin).toBe('600px')
    expect(container.querySelector('.textLayer span')).toBeNull()
  })

  it('renders the text layer of the pages near the viewport, the rest stay placeholders', async () => {
    const { container } = await show()
    act(() => io().fire([1, 2]))
    await waitFor(() => expect(pageEl(container, 2).querySelectorAll('.textLayer span')).toHaveLength(3))
    expect(pageEl(container, 1).querySelector('.textLayer span')!.textContent).toBe('Hello world')
    expect(pageEl(container, 1).querySelector('.pdf-page-placeholder')).toBeNull()
    expect(pageEl(container, 3).querySelector('.textLayer span')).toBeNull()
    expect(pageEl(container, 3).querySelector('.pdf-page-placeholder')).not.toBeNull()
    // pdf.js's custom properties became inline geometry at the page's scale (600 / 600 = 1).
    const span = pageEl(container, 2).querySelector<HTMLElement>('.textLayer span')!
    expect(span.style.fontSize).toBe('12px')
    expect(span.style.transform).toBe('scaleX(1.5)')
  })

  it('closes the document on unmount', async () => {
    const { unmount } = await show()
    unmount()
    expect(fake.destroy).toHaveBeenCalled()
  })
})

describe('PdfPages: selection', () => {
  it('reports a selection inside page 2 as an anchor on page 2', async () => {
    const { container } = await show()
    act(() => io().fire([2]))
    await waitFor(() => expect(pageEl(container, 2).querySelector('.textLayer span')).not.toBeNull())
    select(container, 2, 'sample')
    expect(onSelection).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'text', quote: 'sample', page: 2, occurrence: 1 }))
    select(container, 2, 'sample again')
    expect(onSelection).toHaveBeenLastCalledWith(expect.objectContaining({ quote: 'sample again', page: 2, occurrence: 1 }))
  })

  it('reports a collapsed selection as none', async () => {
    const { container } = await show()
    act(() => io().fire([2]))
    await waitFor(() => expect(pageEl(container, 2).querySelector('.textLayer span')).not.toBeNull())
    select(container, 2, 'mean')
    expect(onSelection).toHaveBeenLastCalledWith(expect.objectContaining({ quote: 'mean' }))
    document.getSelection()!.removeAllRanges()
    document.dispatchEvent(new Event('selectionchange'))
    expect(onSelection).toHaveBeenLastCalledWith(null)
  })
})

describe('PdfPages: highlights', () => {
  it('draws a box per request on a rendered page, none on a page not yet rendered', async () => {
    const requests = [req(), req({ id: 'r2', status: 'done', anchor: { kind: 'text', quote: 'Last page', page: 3 } })]
    const { container } = await show({ requests })
    expect(container.querySelector('.pdf-hl')).toBeNull()
    act(() => io().fire([2]))
    await waitFor(() => expect(pageEl(container, 2).querySelector('.pdf-hl')).not.toBeNull())
    const box = pageEl(container, 2).querySelector<HTMLElement>('.pdf-hl')!
    expect(box.className).toBe('pdf-hl open')
    expect(box.dataset.request).toBe('r1')
    expect(box.style.left).toBe('10px')
    expect(box.style.top).toBe('20px')
    expect(box.style.width).toBe('100px')
    expect(pageEl(container, 3).querySelector('.pdf-hl')).toBeNull()
    // Page 3 scrolls near: its request gets a box with its status.
    act(() => io().fire([3]))
    await waitFor(() => expect(pageEl(container, 3).querySelector('.pdf-hl')).not.toBeNull())
    expect(pageEl(container, 3).querySelector('.pdf-hl')!.className).toBe('pdf-hl done')
  })

  it('marks the focused request and reports hover', async () => {
    const requests = [req(), req({ id: 'r2', status: 'declined', anchor: { kind: 'text', quote: 'Hello', page: 1 } })]
    const { container, rerender } = await show({ requests, focusedId: 'r1' })
    act(() => io().fire([1, 2]))
    await waitFor(() => expect(container.querySelectorAll('.pdf-hl')).toHaveLength(2))
    expect(pageEl(container, 2).querySelector('.pdf-hl')!.className).toBe('pdf-hl open focus')
    expect(pageEl(container, 1).querySelector('.pdf-hl')!.className).toBe('pdf-hl declined')
    rerender(<PdfPages blob={blob} requests={requests} focusedId="r2" onHover={onHover} onSelection={onSelection} />)
    expect(pageEl(container, 2).querySelector('.pdf-hl')!.className).toBe('pdf-hl open')
    expect(pageEl(container, 1).querySelector('.pdf-hl')!.className).toBe('pdf-hl declined focus')
    const box = pageEl(container, 2).querySelector<HTMLElement>('.pdf-hl')!
    fireEvent.mouseEnter(box)
    expect(onHover).toHaveBeenLastCalledWith('r1')
    fireEvent.mouseLeave(box)
    expect(onHover).toHaveBeenLastCalledWith(null)
  })

  it('drops the boxes of a page that scrolls away', async () => {
    const { container } = await show({ requests: [req()] })
    act(() => io().fire([2]))
    await waitFor(() => expect(pageEl(container, 2).querySelector('.pdf-hl')).not.toBeNull())
    act(() => io().fire([2], false))
    await waitFor(() => expect(pageEl(container, 2).querySelector('.pdf-hl')).toBeNull())
    expect(pageEl(container, 2).querySelector('.textLayer span')).toBeNull()
    expect(pageEl(container, 2).querySelector('.pdf-page-placeholder')).not.toBeNull()
  })
})
