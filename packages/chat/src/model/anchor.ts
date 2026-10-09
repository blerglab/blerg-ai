// Anchors in a rendered document: a selection becomes a quote with its heading and occurrence,
// and an anchor finds its Range again (whitespace-insensitive, across text nodes) so it can be
// highlighted. Runs in the browser; nothing here touches React.
import type { Anchor, ReviewRequest } from './review'

export interface AnchorOptions {
  /** The PDF page the root shows. */
  page?: number
  /** The markdown source line a quote comes from, when the pane keeps a map. */
  sourceLineOf?: (quote: string) => number | undefined
}

const collapse = (s: string) => s.replace(/\s+/g, ' ').trim()

/** The root's text, raw and whitespace-collapsed, with the maps between the two and the text
 *  nodes each raw index belongs to. */
interface TextMap {
  nodes: Text[]
  /** Raw offset where each node's data starts. */
  starts: number[]
  norm: string
  /** Raw index → index in `norm` of where that character landed (or would have). */
  normAt: number[]
  /** Index in `norm` → raw index of that character; `rawAt[norm.length]` is the raw length. */
  rawAt: number[]
}

function mapText(root: HTMLElement): TextMap {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  const starts: number[] = []
  let raw = ''
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    nodes.push(n as Text)
    starts.push(raw.length)
    raw += (n as Text).data
  }
  let norm = ''
  const normAt: number[] = new Array(raw.length + 1)
  const rawAt: number[] = []
  for (let i = 0; i < raw.length; i++) {
    normAt[i] = norm.length
    const c = raw[i]
    if (/\s/.test(c)) {
      if (norm.length === 0 || norm.endsWith(' ')) continue
      rawAt[norm.length] = i
      norm += ' '
    } else {
      rawAt[norm.length] = i
      norm += c
    }
  }
  if (norm.endsWith(' ')) norm = norm.slice(0, -1)
  normAt[raw.length] = norm.length
  rawAt[norm.length] = raw.length
  return { nodes, starts, norm, normAt, rawAt }
}

/** The index in `norm` where the nth (1-based) non-overlapping occurrence of `quote` starts; -1 when
 *  there is none. */
function nthIndex(norm: string, quote: string, n: number): number {
  let from = 0
  for (let i = 1; ; i++) {
    const at = norm.indexOf(quote, from)
    if (at < 0) return -1
    if (i === n) return at
    from = at + quote.length
  }
}

/** How many non-overlapping occurrences of `quote` start before `before`, plus one. */
function occurrenceAt(norm: string, quote: string, before: number): number {
  let n = 1
  let from = 0
  for (;;) {
    const at = norm.indexOf(quote, from)
    if (at < 0 || at >= before) return n
    n++
    from = at + quote.length
  }
}

/** The raw text length from the root's start to a boundary — what the walker's nodes hold up to
 *  there. */
function rawOffset(root: HTMLElement, container: Node, offset: number): number {
  const r = document.createRange()
  r.setStart(root, 0)
  r.setEnd(container, offset)
  return r.toString().length
}

/** The text node and offset a raw index falls in; `end` asks for the position just after the
 *  character at `raw - 1`, so the end stays inside the node that holds the quote's last character. */
function place(map: TextMap, raw: number, end = false): [Text, number] | null {
  const want = end ? raw - 1 : raw
  let idx = -1
  for (let i = 0; i < map.starts.length; i++) {
    if (map.starts[i] <= want) idx = i
    else break
  }
  if (idx < 0) return null
  return [map.nodes[idx], want - map.starts[idx] + (end ? 1 : 0)]
}

/** The selection's text (trimmed, whitespace collapsed), the nearest heading at or above its
 *  start within root, the occurrence of that quote in root's text, and the page and line when
 *  given. null for an empty or collapsed selection, or one not wholly inside root. */
export function anchorSelection(root: HTMLElement, sel: Selection, opts: AnchorOptions = {}): Anchor | null {
  if (sel.rangeCount === 0 || sel.isCollapsed) return null
  const range = sel.getRangeAt(0)
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null
  const quote = collapse(range.toString())
  if (!quote) return null
  const map = mapText(root)
  const start = map.normAt[rawOffset(root, range.startContainer, range.startOffset)]
  const anchor: Anchor = { kind: 'text', quote }
  const heading = headingAbove(root, range)
  if (heading) anchor.heading = heading
  anchor.occurrence = occurrenceAt(map.norm, quote, start)
  if (opts.page !== undefined) anchor.page = opts.page
  const line = opts.sourceLineOf?.(quote)
  if (line !== undefined) anchor.line = line
  return anchor
}

function headingAbove(root: HTMLElement, range: Range): string | undefined {
  let found: Element | undefined
  for (const h of root.querySelectorAll('h1,h2,h3,h4,h5,h6')) {
    const hr = document.createRange()
    hr.selectNodeContents(h)
    if (hr.compareBoundaryPoints(Range.START_TO_START, range) <= 0) found = h
    else break
  }
  const text = found ? collapse(found.textContent ?? '') : ''
  return text || undefined
}

/** The Range in root that the anchor's quote (its nth occurrence) covers, across text nodes,
 *  matched with whitespace collapsed; null when it is not there. */
export function findQuote(root: HTMLElement, anchor: Anchor): Range | null {
  const quote = anchor.quote ? collapse(anchor.quote) : ''
  if (anchor.kind !== 'text' || !quote) return null
  const map = mapText(root)
  const at = nthIndex(map.norm, quote, anchor.occurrence ?? 1)
  if (at < 0) return null
  const from = place(map, map.rawAt[at])
  const to = place(map, map.rawAt[at + quote.length - 1] + 1, true)
  if (!from || !to) return null
  const range = document.createRange()
  range.setStart(from[0], from[1])
  range.setEnd(to[0], to[1])
  return range
}

// ─── highlights ───────────────────────────────────────────────────────────────

type HighlightCtor = new (...ranges: Range[]) => object
interface HighlightRegistry { set(name: string, h: object): unknown; delete(name: string): unknown }

/** The CSS Custom Highlight API, when the browser has it. */
function highlightApi(): { registry: HighlightRegistry; Highlight: HighlightCtor } | null {
  const g = globalThis as unknown as { CSS?: { highlights?: HighlightRegistry }; Highlight?: HighlightCtor }
  const registry = g.CSS?.highlights
  if (!registry || typeof registry.set !== 'function' || typeof g.Highlight !== 'function') return null
  return { registry, Highlight: g.Highlight }
}

/** Highlights every request's quote under the names `chat-review-open|done|declined`, and the
 *  focused one under `chat-review-focus`, through the CSS Custom Highlight API; a no-op without
 *  it. Returns a cleanup that clears what it set. */
export function applyHighlights(root: HTMLElement, requests: ReviewRequest[], focusedId?: string | null): () => void {
  const api = highlightApi()
  if (!api) return () => {}
  const byStatus: Record<ReviewRequest['status'], Range[]> = { open: [], done: [], declined: [] }
  const focus: Range[] = []
  for (const r of requests) {
    const range = findQuote(root, r.anchor)
    if (!range) continue
    byStatus[r.status].push(range)
    if (focusedId && r.id === focusedId) focus.push(range)
  }
  const names: string[] = []
  const set = (name: string, ranges: Range[]) => {
    if (ranges.length === 0) return
    api.registry.set(name, new api.Highlight(...ranges))
    names.push(name)
  }
  set('chat-review-open', byStatus.open)
  set('chat-review-done', byStatus.done)
  set('chat-review-declined', byStatus.declined)
  set('chat-review-focus', focus)
  return () => { for (const name of names) api.registry.delete(name) }
}

// ─── places ───────────────────────────────────────────────────────────────────

/** Where a rect's centre falls, in thirds: 'top left' … 'centre' … 'bottom right'. */
export function describePlace(rect: { x: number; y: number; w: number; h: number }): string {
  const cx = rect.x + rect.w / 2
  const cy = rect.y + rect.h / 2
  const col = cx < 1 / 3 ? 'left' : cx < 2 / 3 ? '' : 'right'
  const row = cy < 1 / 3 ? 'top' : cy < 2 / 3 ? '' : 'bottom'
  if (row && col) return `${row} ${col}`
  return row || col || 'centre'
}
