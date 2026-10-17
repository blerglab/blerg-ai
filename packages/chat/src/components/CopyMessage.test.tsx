import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import { handleChatCopy, writeRich } from '../model/richCopy'

const SOURCE = '## Plan\n\nSee [the doc](https://e.test/x) and **this**.\n\n```go\nfunc main() {}\n```\n\n```card:nope\n{"a":1}\n```'
const show = () => render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />)

describe('Copy on a message', () => {
  let written: Record<string, string>[]
  beforeEach(() => {
    resetChatState()
    written = []
    class Item { data: Record<string, Blob>; constructor(d: Record<string, Blob>) { this.data = d } }
    vi.stubGlobal('ClipboardItem', Item)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { write: async (items: Item[]) => { for (const i of items) written.push(Object.fromEntries(await Promise.all(Object.entries(i.data).map(async ([k, b]) => [k, await b.text()])))) } },
    })
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('puts the Markdown source and the rendered HTML on the clipboard, on assistant and own messages', async () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'make a **plan**', source: 'human' } }), ev({ seq: 2, kind: 'assistant_text', payload: { text: SOURCE } }))
    show()
    const buttons = screen.getAllByRole('button', { name: 'Copy message' })
    expect(buttons).toHaveLength(2)
    fireEvent.click(buttons[1])
    await waitFor(() => expect(written).toHaveLength(1))
    expect(written[0]['text/plain']).toBe(SOURCE)
    expect(written[0]['text/html']).toContain('<h2>Plan</h2>')
    expect(written[0]['text/html']).toContain('<a href="https://e.test/x">the doc</a>')
    expect(written[0]['text/html']).not.toMatch(/<button|<time|Copy/)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Copied' })).toBeInTheDocument())
  })

  it('is a real button, reachable by keyboard', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'hi' } }))
    show()
    const b = screen.getByRole('button', { name: 'Copy message' })
    expect(b.tagName).toBe('BUTTON')
    expect(b).toHaveProperty('tabIndex', 0)
  })
})

describe('Selection copy', () => {
  const select = (from: Node, fo: number, to: Node, to_o: number) => {
    const r = document.createRange()
    r.setStart(from, fo)
    r.setEnd(to, to_o)
    const s = window.getSelection()!
    s.removeAllRanges()
    s.addRange(r)
  }
  const copy = (root: Element) => {
    const store: Record<string, string> = {}
    const e = new Event('copy', { bubbles: true, cancelable: true }) as ClipboardEvent
    Object.defineProperty(e, 'clipboardData', { value: { setData: (t: string, v: string) => { store[t] = v } } })
    handleChatCopy(e, root)
    return { store, prevented: e.defaultPrevented }
  }
  const mount = (body: string) => {
    document.body.innerHTML = `<div id="root">${body}</div>`
    return document.getElementById('root')!
  }
  afterEach(() => { document.body.innerHTML = ''; window.getSelection()?.removeAllRanges() })

  const card = '<div class="agent-card assistant"><span class="card-author">You</span><div class="md"><p id="p1">First <strong>bold</strong> line</p><ul><li id="l1">one</li><li id="l2">two <a href="https://e.test/a">link</a></li></ul>'
    + '<div class="md-codeblock"><button class="md-copy">Copy</button><pre data-lang="js"><code id="code"><span>let a = 1</span>\n<span>let b = 2</span></code></pre></div></div><div class="card-foot"><time id="t">2m ago</time></div></div>'

  it('keeps paragraphs, lists, links and code, and drops the card chrome', () => {
    const root = mount(card)
    select(document.getElementById('p1')!.firstChild!, 0, document.getElementById('t')!.firstChild!, 3)
    const { store, prevented } = copy(root)
    expect(prevented).toBe(true)
    expect(store['text/plain']).toBe('First **bold** line\n\n- one\n- two [link](https://e.test/a)\n\n```js\nlet a = 1\nlet b = 2\n```')
    expect(store['text/html']).toContain('<ul><li>one</li>')
    expect(store['text/html']).not.toMatch(/2m ago|<button|Copy/)
  })

  it('copies a selection inside a code block as exactly that text', () => {
    const root = mount(card)
    const code = document.getElementById('code')!
    select(code.firstChild!.firstChild!, 4, code.lastChild!.firstChild!, 5)
    const { store } = copy(root)
    expect(store['text/plain']).toBe('a = 1\nlet b')
    expect(store['text/html']).toBe('<pre><code class="language-js">a = 1\nlet b</code></pre>')
  })

  it('leaves a selection outside the markdown, or an empty one, to the browser', () => {
    const root = mount(card + '<div id="x">tool output</div>')
    select(document.getElementById('x')!.firstChild!, 0, document.getElementById('x')!.firstChild!, 4)
    expect(copy(root).prevented).toBe(false)
    window.getSelection()!.removeAllRanges()
    expect(copy(root).prevented).toBe(false)
  })
})

describe('writeRich fallbacks', () => {
  afterEach(() => { vi.unstubAllGlobals(); delete (document as { execCommand?: unknown }).execCommand })

  it('omits an empty HTML flavour', async () => {
    const seen: string[][] = []
    class Item { constructor(d: Record<string, Blob>) { seen.push(Object.keys(d)) } }
    vi.stubGlobal('ClipboardItem', Item)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { write: async () => {} } })
    expect(await writeRich('x', '')).toBe(true)
    expect(seen).toEqual([['text/plain']])
  })

  it('falls back to writeText when write rejects', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('ClipboardItem', class { constructor(_d: unknown) {} })
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { write: async () => { throw new Error('no') }, writeText } })
    expect(await writeRich('x', '<p>x</p>')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('x')
  })

  it('falls back to a hidden textarea with no clipboard API', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
    const exec = vi.fn(() => true)
    document.execCommand = exec as unknown as typeof document.execCommand
    expect(await writeRich('x', '<p>x</p>')).toBe(true)
    expect(exec).toHaveBeenCalledWith('copy')
    expect(document.querySelector('textarea')).toBeNull()
  })
})
