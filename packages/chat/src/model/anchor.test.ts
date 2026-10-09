// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { anchorSelection, applyHighlights, describePlace, findQuote } from './anchor'
import type { ReviewRequest } from './review'

function mount(html: string): HTMLElement {
  const root = document.createElement('div')
  root.innerHTML = html
  document.body.appendChild(root)
  return root
}

/** Selects the text from `start` to `end` inside `node` (a text node). */
function select(node: Node, start: number, end: number, endNode: Node = node): Selection {
  const range = document.createRange()
  range.setStart(node, start)
  range.setEnd(endNode, end)
  const sel = window.getSelection()!
  sel.removeAllRanges()
  sel.addRange(range)
  return sel
}

const textNode = (el: Element | null) => el!.firstChild as Text

afterEach(() => {
  document.body.innerHTML = ''
  window.getSelection()?.removeAllRanges()
})

describe('anchorSelection', () => {
  it('records the quote, the nearest heading above, and the occurrence', () => {
    const root = mount('<h1>Title</h1><p>the mean rose</p><h2>Results</h2><p>the mean rose by 12%</p>')
    const p = root.querySelectorAll('p')[1]
    const sel = select(textNode(p), 0, 8)
    expect(anchorSelection(root, sel)).toEqual({ kind: 'text', quote: 'the mean', heading: 'Results', occurrence: 2 })
  })

  it('takes the heading above even when the selection starts inside it', () => {
    const root = mount('<h2>Method</h2><p>we sampled</p>')
    const sel = select(textNode(root.querySelector('h2')), 0, 6)
    expect(anchorSelection(root, sel)?.heading).toBe('Method')
  })

  it('has no heading before the first one', () => {
    const root = mount('<p>intro text</p><h2>Later</h2>')
    const sel = select(textNode(root.querySelector('p')), 0, 5)
    expect(anchorSelection(root, sel)).toEqual({ kind: 'text', quote: 'intro', occurrence: 1 })
  })

  it('collapses whitespace in the quote and counts occurrences across markup', () => {
    const root = mount('<p>a <em>b</em> c</p><p>a\n  b c</p><p>a b   c</p>')
    const ps = root.querySelectorAll('p')
    const sel = select(textNode(ps[2]), 0, 7)
    expect(anchorSelection(root, sel)).toMatchObject({ quote: 'a b c', occurrence: 3 })
  })

  it('adds the page and the source line when given', () => {
    const root = mount('<p>hello world</p>')
    const sel = select(textNode(root.querySelector('p')), 0, 5)
    const a = anchorSelection(root, sel, { page: 2, sourceLineOf: q => (q === 'hello' ? 14 : undefined) })
    expect(a).toEqual({ kind: 'text', quote: 'hello', occurrence: 1, page: 2, line: 14 })
  })

  it('is null for a collapsed selection or one outside the root', () => {
    const root = mount('<p>inside</p>')
    const outside = mount('<p>outside</p>')
    expect(anchorSelection(root, select(textNode(root.querySelector('p')), 2, 2))).toBeNull()
    expect(anchorSelection(root, select(textNode(outside.querySelector('p')), 0, 3))).toBeNull()
    window.getSelection()!.removeAllRanges()
    expect(anchorSelection(root, window.getSelection()!)).toBeNull()
  })
})

describe('findQuote', () => {
  it('finds the nth occurrence across text nodes, whatever the whitespace', () => {
    const root = mount('<p>the <b>mean</b> rose</p><p>the\n   mean   rose</p>')
    const r1 = findQuote(root, { kind: 'text', quote: 'the mean rose', occurrence: 1 })!
    expect(r1.toString()).toBe('the mean rose')
    expect(root.querySelectorAll('p')[0].contains(r1.commonAncestorContainer)).toBe(true)
    const r2 = findQuote(root, { kind: 'text', quote: 'the mean rose', occurrence: 2 })!
    expect(r2.toString().replace(/\s+/g, ' ')).toBe('the mean rose')
    expect(root.querySelectorAll('p')[1].contains(r2.commonAncestorContainer)).toBe(true)
  })

  it('covers exactly the quote, not the whitespace around it', () => {
    const root = mount('<p>  padded  quote  here</p>')
    expect(findQuote(root, { kind: 'text', quote: 'quote' })!.toString()).toBe('quote')
  })

  it('is null when the quote is not there, or the occurrence is past the last', () => {
    const root = mount('<p>once</p>')
    expect(findQuote(root, { kind: 'text', quote: 'twice' })).toBeNull()
    expect(findQuote(root, { kind: 'text', quote: 'once', occurrence: 2 })).toBeNull()
    expect(findQuote(root, { kind: 'region', rect: { x: 0, y: 0, w: 1, h: 1 } })).toBeNull()
    expect(findQuote(root, { kind: 'text', quote: '' })).toBeNull()
  })

  it('round-trips an anchor made from a selection', () => {
    const root = mount('<p>x y</p><p>x y</p><p>x y</p>')
    const sel = select(textNode(root.querySelectorAll('p')[1]), 0, 3)
    const a = anchorSelection(root, sel)!
    const r = findQuote(root, a)!
    expect(r.startContainer).toBe(textNode(root.querySelectorAll('p')[1]))
    expect(r.toString()).toBe('x y')
  })
})

describe('applyHighlights', () => {
  const req = (id: string, quote: string, status: ReviewRequest['status'] = 'open'): ReviewRequest =>
    ({ id, anchor: { kind: 'text', quote }, text: '', status, createdAt: '' })

  it('is a no-op without the Custom Highlight API', () => {
    const root = mount('<p>abc</p>')
    const g = globalThis as { CSS?: unknown }
    const saved = g.CSS
    g.CSS = undefined
    try {
      const cleanup = applyHighlights(root, [req('a', 'abc')])
      expect(typeof cleanup).toBe('function')
      cleanup()
    } finally {
      g.CSS = saved
    }
  })

  it('registers one highlight per status and one for the focused request, and clears them', () => {
    const root = mount('<p>open done declined</p>')
    class FakeHighlight { ranges: Range[]; constructor(...r: Range[]) { this.ranges = r } }
    const g = globalThis as { CSS?: unknown; Highlight?: unknown }
    const savedCSS = g.CSS
    const savedHighlight = g.Highlight
    const registry = new Map<string, FakeHighlight>()
    g.CSS = { highlights: registry }
    g.Highlight = FakeHighlight
    try {
      const cleanup = applyHighlights(root, [req('a', 'open'), req('b', 'done', 'done'), req('c', 'declined', 'declined'), req('d', 'missing')], 'b')
      expect([...registry.keys()].sort()).toEqual(['chat-review-declined', 'chat-review-done', 'chat-review-focus', 'chat-review-open'])
      expect(registry.get('chat-review-open')!.ranges.map(r => r.toString())).toEqual(['open'])
      expect(registry.get('chat-review-done')!.ranges.map(r => r.toString())).toEqual(['done'])
      expect(registry.get('chat-review-focus')!.ranges.map(r => r.toString())).toEqual(['done'])
      cleanup()
      expect(registry.size).toBe(0)
    } finally {
      g.CSS = savedCSS
      g.Highlight = savedHighlight
    }
  })
})

describe('describePlace', () => {
  it('names the third the rect’s centre falls in', () => {
    const at = (x: number, y: number) => describePlace({ x, y, w: 0, h: 0 })
    expect(at(0.1, 0.1)).toBe('top left')
    expect(at(0.5, 0.1)).toBe('top')
    expect(at(0.9, 0.1)).toBe('top right')
    expect(at(0.1, 0.5)).toBe('left')
    expect(at(0.5, 0.5)).toBe('centre')
    expect(at(0.9, 0.5)).toBe('right')
    expect(at(0.1, 0.9)).toBe('bottom left')
    expect(at(0.5, 0.9)).toBe('bottom')
    expect(at(0.9, 0.9)).toBe('bottom right')
  })

  it('uses the centre of a box, not its corner', () => {
    expect(describePlace({ x: 0.3, y: 0.3, w: 0.6, h: 0.6 })).toBe('centre')
    expect(describePlace({ x: 0.2, y: 0, w: 0.6, h: 0.2 })).toBe('top')
  })
})
