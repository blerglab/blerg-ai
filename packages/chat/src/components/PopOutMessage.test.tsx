// Pop out a long message into its own tab (BLG-449): the icon beside Copy, shown only once the
// message's text runs past the chosen number of rendered lines (more than 4 by default),
// re-measured live as the card or the pane resizes. Runs through the real chat timeline (ChatView -> Transcript -> EventCards), with
// a controllable ResizeObserver standing in for layout jsdom does not compute.
import { createRef, type RefObject } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import PopOutMessage, { popOutLabel } from './PopOutMessage'
import { ChatPaneContext } from './chatPaneContext'
import { ViewerHostContext, type HostedView, type ViewerHostValue } from './ViewerHost'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import { POP_OUT_KEY, writePopOutChoice } from '../model/popOutPref'

function host(over: Partial<ViewerHostValue> = {}): ViewerHostValue {
  return { open: vi.fn(), close: vi.fn(), keep: vi.fn(), views: [], active: 'chat', target: null, ...over }
}

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  elements: Element[] = []
  cb: () => void
  constructor(cb: () => void) { this.cb = cb; FakeResizeObserver.instances.push(this) }
  observe(el: Element) { this.elements.push(el) }
  unobserve(el: Element) { this.elements = this.elements.filter(e => e !== el) }
  disconnect() { this.elements = [] }
}

function setHeight(el: Element, prop: 'clientHeight' | 'scrollHeight', value: number) {
  Object.defineProperty(el, prop, { configurable: true, value })
}

// jsdom lays nothing out: give the message's text (the card's first child after its author line)
// a 20px line height and a height of `lines` such lines.
function setLines(card: Element, lines: number) {
  const body = [...card.children].find(el => !el.classList.contains('card-author')) as HTMLElement
  body.style.lineHeight = '20px'
  Object.defineProperty(body, 'offsetHeight', { configurable: true, value: lines * 20 })
}

// The pane is watched by more than one ResizeObserver (Transcript's own scroll-anchoring, as well
// as PopOutMessage's): fire every one of them, since a real resize would too.
function triggerFor(el: Element) {
  const insts = FakeResizeObserver.instances.filter(i => i.elements.includes(el))
  if (!insts.length) throw new Error('no observer registered for this element')
  act(() => { for (const inst of insts) inst.cb() })
}

function showChat(over: Partial<ViewerHostValue> = {}) {
  const value = host(over)
  render(
    <ViewerHostContext.Provider value={value}>
      <ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />
    </ViewerHostContext.Provider>,
  )
  return value
}

describe('Pop out a long message into a tab (BLG-449)', () => {
  beforeEach(() => {
    resetChatState()
    FakeResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    // These cases use the default, more than 4 lines; the setting is tested below.
    localStorage.removeItem(POP_OUT_KEY)
  })
  afterEach(() => { vi.unstubAllGlobals(); localStorage.removeItem(POP_OUT_KEY) })

  it('is hidden at 4 lines or fewer', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'a short reply' } }))
    showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByText('a short reply').closest('.agent-card')!
    setHeight(pane, 'clientHeight', 300)
    setLines(card, 4) // exactly 4 lines: not MORE than 4
    triggerFor(pane)
    expect(screen.queryByTestId('popout-message')).toBeNull()
  })

  it('shows once the text runs past 4 lines, as a reachable button with the right tooltip', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'a longer reply' } }))
    showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByText('a longer reply').closest('.agent-card')!
    setHeight(pane, 'clientHeight', 300)
    setLines(card, 4.1) // just over 4 lines
    triggerFor(pane)
    const btn = screen.getByTestId('popout-message')
    expect(btn.tagName).toBe('BUTTON')
    expect(btn).toHaveAttribute('aria-label', 'Open in a tab')
    expect(btn).toHaveAttribute('title', 'Open in a tab')
    expect(btn).toHaveProperty('tabIndex', 0)
  })

  it('re-measures live as the card resizes (a narrower pane wraps the text onto more lines), in either direction', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'a middling reply' } }))
    showChat()
    const card = screen.getByText('a middling reply').closest('.agent-card')!
    setLines(card, 3) // wide: three lines
    triggerFor(card)
    expect(screen.queryByTestId('popout-message')).toBeNull()
    setLines(card, 6) // narrower: it wraps onto six
    triggerFor(card)
    expect(screen.getByTestId('popout-message')).toBeInTheDocument()
    setLines(card, 3) // and back
    triggerFor(card)
    expect(screen.queryByTestId('popout-message')).toBeNull()
  })

  it('counts only the message text, never the card\'s author line or footer', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'two lines of text' } }))
    showChat()
    const card = screen.getByText('two lines of text').closest('.agent-card')!
    setHeight(card, 'scrollHeight', 2000) // a tall card: feet, footers, whatever else
    setLines(card, 2)
    triggerFor(card)
    expect(screen.queryByTestId('popout-message')).toBeNull()
  })

  it('opens a tab named from the message\'s first heading, with its Markdown text, on click', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: '## Plan\n\nSome details.' } }))
    const value = showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByRole('heading', { name: 'Plan' }).closest('.agent-card')!
    setLines(card, 10)
    triggerFor(pane)
    fireEvent.click(screen.getByTestId('popout-message'))
    expect(value.open).toHaveBeenCalledWith({
      kind: 'message',
      messageId: expect.any(String),
      text: '## Plan\n\nSome details.',
      label: 'Plan',
      workspaceLinks: true,
    })
  })

  it('never offers workspace links for a person\'s own popped-out message (EventCards parity)', () => {
    seed(ev({ seq: 1, kind: 'user_message', payload: { text: '## Plan\n\nSome details.', source: 'human' } }))
    const value = showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByRole('heading', { name: 'Plan' }).closest('.agent-card')!
    setLines(card, 10)
    triggerFor(pane)
    fireEvent.click(screen.getByTestId('popout-message'))
    expect(value.open).toHaveBeenCalledWith(expect.objectContaining({ workspaceLinks: undefined }))
  })

  it('falls back to the chat name and time when the message has no heading', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'no heading in this one' }, ts: '2026-07-26T12:00:00Z' }))
    const value = showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByText('no heading in this one').closest('.agent-card')!
    setLines(card, 10)
    triggerFor(pane)
    fireEvent.click(screen.getByTestId('popout-message'))
    expect(value.open).toHaveBeenCalledWith(expect.objectContaining({ label: expect.stringMatching(/^Workspace · /) }))
  })

  it('reopening the same message focuses the one tab instead of opening a second', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'pop me twice' } }))
    const opened: { id: string }[] = []
    const value = showChat({ open: vi.fn((v: HostedView) => { if (v.kind === 'message') opened.push({ id: v.messageId }) }) })
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByText('pop me twice').closest('.agent-card')!
    setLines(card, 10)
    triggerFor(pane)
    fireEvent.click(screen.getByTestId('popout-message'))
    fireEvent.click(screen.getByTestId('popout-message'))
    expect(value.open).toHaveBeenCalledTimes(2)
    const [first, second] = (value.open as ReturnType<typeof vi.fn>).mock.calls.map(c => (c[0] as Extract<HostedView, { kind: 'message' }>).messageId)
    expect(first).toBe(second)
  })

  it('shares one ResizeObserver across every message in the transcript, not one per message', () => {
    seed(
      ev({ seq: 1, kind: 'assistant_text', payload: { text: 'first reply' } }),
      ev({ seq: 2, kind: 'assistant_text', payload: { text: 'second reply' } }),
      ev({ seq: 3, kind: 'assistant_text', payload: { text: 'third reply' } }),
    )
    showChat()
    const pane = document.querySelector('.agent-timeline')!
    const cards = ['first reply', 'second reply', 'third reply'].map(t => screen.getByText(t).closest('.agent-card')!)
    // More than one observer watches the pane (Transcript's own scroll-anchoring observes it plus
    // every row, for a much larger set): the pop-out check is identified by watching exactly the
    // pane and these three cards, nothing more.
    const shared = FakeResizeObserver.instances.filter(i => i.elements.length === 1 + cards.length && i.elements.includes(pane) && cards.every(c => i.elements.includes(c)))
    expect(shared).toHaveLength(1)
  })

  it('does not tear down and recreate its observer as the message text changes (a streaming reply growing by the chunk)', () => {
    const paneRef: RefObject<HTMLDivElement | null> = createRef()
    const pane = document.createElement('div')
    paneRef.current = pane
    const value = host()
    const { rerender } = render(
      <ChatPaneContext.Provider value={paneRef}>
        <ViewerHostContext.Provider value={value}>
          <div className="agent-card assistant"><PopOutMessage text="chunk one" fallbackLabel="Chat" messageId="e1" /></div>
        </ViewerHostContext.Provider>
      </ChatPaneContext.Provider>,
    )
    expect(FakeResizeObserver.instances).toHaveLength(1)
    for (const text of ['chunk one two', 'chunk one two three', 'chunk one two three four']) {
      rerender(
        <ChatPaneContext.Provider value={paneRef}>
          <ViewerHostContext.Provider value={value}>
            <div className="agent-card assistant"><PopOutMessage text={text} fallbackLabel="Chat" messageId="e1" /></div>
          </ViewerHostContext.Provider>
        </ChatPaneContext.Provider>,
      )
    }
    expect(FakeResizeObserver.instances).toHaveLength(1) // still the one observer, never recreated
  })

  it('has nowhere to open a tab (no host) and renders no button at all', () => {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'standalone, no host' } }))
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({ status: 'idle' })} />)
    const pane = document.querySelector('.agent-timeline')
    expect(pane).toBeTruthy()
    // No ViewerHostContext: PopOutMessage never shows, whatever the heights say.
    expect(screen.queryByTestId('popout-message')).toBeNull()
  })
})

describe('the pop-out threshold setting', () => {
  beforeEach(() => {
    resetChatState()
    FakeResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    localStorage.removeItem(POP_OUT_KEY)
  })
  afterEach(() => { vi.unstubAllGlobals(); localStorage.removeItem(POP_OUT_KEY) })

  function measured(lines: number) {
    seed(ev({ seq: 1, kind: 'assistant_text', payload: { text: 'a reply' } }))
    showChat()
    const pane = document.querySelector('.agent-timeline')!
    const card = screen.getByText('a reply').closest('.agent-card')!
    setLines(card, lines)
    triggerFor(pane)
  }

  it('offers the pop-out past 4 lines by default', () => {
    measured(5)
    expect(screen.getByTestId('popout-message')).toBeTruthy()
  })

  it('follows a changed setting at once, without a reload', () => {
    measured(5)
    act(() => writePopOutChoice('20'))
    triggerFor(document.querySelector('.agent-timeline')!)
    expect(screen.queryByTestId('popout-message')).toBeNull()
    act(() => writePopOutChoice('always'))
    triggerFor(document.querySelector('.agent-timeline')!)
    expect(screen.getByTestId('popout-message')).toBeTruthy()
  })

  it('ignores the old pane-share setting and uses the default', () => {
    localStorage.setItem('blerg.chat.popOutAt', 'half')
    measured(5)
    expect(screen.getByTestId('popout-message')).toBeTruthy()
  })
})

describe('popOutLabel (BLG-449 review L4)', () => {
  it('ignores a heading-looking line inside a fenced code block, using the real first heading instead', () => {
    const text = '```md\n# Not a real heading\n```\n\n## Real heading\n\nBody.'
    expect(popOutLabel(text, 'Fallback')).toBe('Real heading')
  })

  it('falls back when every heading-looking line is inside a fence', () => {
    const text = 'Some text.\n\n```md\n# Not a real heading\n```\n'
    expect(popOutLabel(text, 'Fallback')).toBe('Fallback')
  })

  it('handles a tilde fence the same way as a backtick one', () => {
    const text = '~~~md\n# Not a real heading\n~~~\n\n## Real heading\n'
    expect(popOutLabel(text, 'Fallback')).toBe('Real heading')
  })

  it('strips inline markdown from the heading: bold, code, a link and italics', () => {
    expect(popOutLabel('## **Bold** plan', 'x')).toBe('Bold plan')
    expect(popOutLabel('## Plan for `main.go`', 'x')).toBe('Plan for main.go')
    expect(popOutLabel('## See [the doc](https://e.test/x)', 'x')).toBe('See the doc')
    expect(popOutLabel('## _Italic_ plan', 'x')).toBe('Italic plan')
  })

  it('still uses a plain heading verbatim', () => {
    expect(popOutLabel('## Plan\n\nDetails.', 'x')).toBe('Plan')
  })
})
