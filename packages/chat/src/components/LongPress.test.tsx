import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

const SOURCE = 'See [the doc](https://e.test/x) and **this**.\n\n```go\nfunc main() {}\n```'
let written: Record<string, string>[]

function touch(on: boolean) {
  window.matchMedia = ((q: string) => ({ matches: on && q.includes('hover: none'), media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as unknown as typeof window.matchMedia
}
const show = () => render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />)
const hold = (el: Element, x = 10, y = 10) => fireEvent.touchStart(el, { touches: [{ clientX: x, clientY: y }] })

beforeEach(() => {
  vi.useFakeTimers()
  resetChatState()
  touch(true)
  written = []
  class Item { data: Record<string, Blob>; constructor(d: Record<string, Blob>) { this.data = d } }
  vi.stubGlobal('ClipboardItem', Item)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { write: async (items: Item[]) => { for (const i of items) written.push(Object.fromEntries(await Promise.all(Object.entries(i.data).map(async ([k, b]) => [k, await b.text()])))) } },
  })
  Element.prototype.scrollIntoView = vi.fn() as unknown as Element['scrollIntoView']
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

const wait = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('long-press to copy', () => {
  it('opens the sheet after the hold and copies the Markdown and HTML', async () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: SOURCE } }))
    show()
    const card = document.querySelector('.agent-card.assistant')!
    hold(card.querySelector('p')!)
    await wait(499)
    expect(screen.queryByRole('dialog')).toBeNull()
    await wait(2)
    const dlg = screen.getByRole('dialog', { name: 'Message actions' })
    expect(dlg).toContainElement(document.activeElement as HTMLElement)
    fireEvent.click(within(dlg).getByRole('button', { name: 'Copy message' }))
    await wait(0)
    expect(written[0]['text/plain']).toBe(SOURCE)
    expect(written[0]['text/html']).toContain('<a href="https://e.test/x">the doc</a>')
    expect(screen.getByRole('status')).toHaveTextContent('Copied')
    await wait(1000)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('is cancelled by movement or by lifting the finger', async () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'hello', source: 'human' } }))
    show()
    const card = document.querySelector('.agent-card.user')!
    hold(card)
    fireEvent.touchMove(card, { touches: [{ clientX: 10, clientY: 25 }] })
    await wait(600)
    expect(screen.queryByRole('dialog')).toBeNull()
    hold(card)
    fireEvent.touchEnd(card)
    await wait(600)
    expect(screen.queryByRole('dialog')).toBeNull()
    hold(card)
    fireEvent.touchMove(card, { touches: [{ clientX: 14, clientY: 12 }] })
    await wait(600)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('leaves a press on a link or a button alone', async () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: SOURCE } }))
    show()
    hold(screen.getByRole('link', { name: 'the doc' }))
    await wait(600)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('copies just the code from a code block', async () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: SOURCE } }))
    show()
    hold(document.querySelector('.md-codeblock pre')!)
    await wait(600)
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Copy message' }))
    await wait(0)
    expect(written[0]['text/plain']).toBe('func main() {}')
    expect(written[0]['text/html']).toBeUndefined()
  })

  it('copies a ticket as KEY Title and its link, and a checklist as Markdown', async () => {
    seed(
      ev({ seq: 1, kind: 'ticket', payload: { action: 'pin', ticket: { source: 'jira', key: 'ABC-1', url: 'https://t.test/ABC-1', title: 'Crash on save' } } }),
      ev({ seq: 2, kind: 'checklist', payload: { items: [{ text: 'Read', done: true }, { text: 'Write', done: false }], op: 'set' } }),
    )
    show()
    hold(screen.getByTestId('ticket-card').querySelector('.ticket-row-title')!)
    await wait(600)
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Copy message' }))
    await wait(0)
    expect(written[0]['text/plain']).toBe('ABC-1 Crash on save\nhttps://t.test/ABC-1')
    await wait(1000)
    const row = screen.getByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button'))
    hold(row.querySelector('.checklist-items')!)
    await wait(600)
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Copy message' }))
    await wait(0)
    expect(written[1]['text/plain']).toBe('- [x] Read\n- [ ] Write')
  })

  it('Cancel dismisses the sheet', async () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'hello', source: 'human' } }))
    show()
    hold(document.querySelector('.agent-card.user')!)
    await wait(600)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(written).toHaveLength(0)
  })

  it('Select text opens the text as read, all selected, and copies the part still selected', async () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'Hello **bold** world' } }))
    show()
    hold(document.querySelector('.agent-card.assistant p')!)
    await wait(600)
    fireEvent.click(screen.getByRole('button', { name: 'Select text' }))
    expect(screen.queryByRole('dialog', { name: 'Message actions' })).toBeNull()
    const view = screen.getByRole('dialog', { name: 'Select text' })
    const box = within(view).getByRole('textbox', { name: 'Text to select' }) as HTMLTextAreaElement
    expect(box.value).toBe('Hello bold world') // no Markdown marks
    expect(box).toHaveAttribute('readonly')
    expect([box.selectionStart, box.selectionEnd]).toEqual([0, box.value.length])
    box.setSelectionRange(6, 10)
    fireEvent.click(within(view).getByRole('button', { name: 'Copy' }))
    await wait(0)
    expect(written[0]['text/plain']).toBe('bold')
    await wait(1000)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('Select text copies everything when nothing is selected, and Done closes it', async () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'hello there', source: 'human' } }))
    show()
    hold(document.querySelector('.agent-card.user')!)
    await wait(600)
    fireEvent.click(screen.getByRole('button', { name: 'Select text' }))
    const box = screen.getByRole('textbox', { name: 'Text to select' }) as HTMLTextAreaElement
    box.setSelectionRange(3, 3)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await wait(0)
    expect(written[0]['text/plain']).toBe('hello there')
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('keeps the press unselectable until the finger lifts, and clears what the browser selected', async () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: 'hello', source: 'human' } }))
    show()
    const card = document.querySelector('.agent-card.user')!
    expect(card.closest('[data-pressing]')).toBeNull()
    hold(card)
    await wait(600)
    const sheet = screen.getByRole('dialog', { name: 'Message actions' })
    expect(card.closest('[data-pressing]')).not.toBeNull() // still held
    // The browser's own long-press selects the button now under the finger…
    const label = within(sheet).getByRole('button', { name: 'Copy message' })
    window.getSelection()!.selectAllChildren(label)
    fireEvent.touchEnd(card)
    // …and lifting the finger clears it.
    expect(window.getSelection()!.toString()).toBe('')
    expect(card.closest('[data-pressing]')).toBeNull()
  })

  it('does nothing with a mouse (desktop keeps the hover button)', async () => {
    touch(false)
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'hi' } }))
    show()
    hold(document.querySelector('.agent-card.assistant')!)
    await wait(600)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('button', { name: 'Copy message' })).toBeInTheDocument()
  })
})
