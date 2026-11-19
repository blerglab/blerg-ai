// A popped-out message must render exactly as the transcript renders it (BLG-449 review M1):
// EventCards never passes `html` to Markdown, so raw HTML stays literal text everywhere; it does
// pass `cards` and, for an assistant reply only, `workspaceLinks`. This exercises the whole path —
// PopOutMessage's click, the real ViewerHostValue.open, HostedViewers, MessageViewer — not just
// MessageViewer alone, since the gap the review found was in what reached MessageViewer, not in
// MessageViewer's own rendering.
import { useState } from 'react'
import { expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ChatView from './ChatView'
import { defineCard, createCardRegistry } from '../cards/registry'
import { HostedViewers, ViewerHostContext, type HostedView, type ViewerHostValue } from './ViewerHost'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  elements: Element[] = []
  cb: () => void
  constructor(cb: () => void) { this.cb = cb; FakeResizeObserver.instances.push(this) }
  observe(el: Element) { this.elements.push(el) }
  unobserve(el: Element) { this.elements = this.elements.filter(e => e !== el) }
  disconnect() { this.elements = [] }
}

const cards = createCardRegistry([defineCard('demo', ({ data }) => <div data-testid="demo-card">{String(data.label)}</div>)])

const TEXT = [
  '<b>raw html stays literal</b>',
  '',
  '```card:demo',
  '{"label":"Demo card"}',
  '```',
  '',
  'Open `src/app.ts` to see more.',
].join('\n')

function HostedChat({ canOpenWorkspaceFiles = true, seedKind = 'assistant_text' as 'assistant_text' | 'user_message' }) {
  const [view, setView] = useState<HostedView | null>(null)
  const t = createFakeTransport()
  Object.assign(t.files!, { workspaceFile: async (_s: string, path: string) => ({ path, size: 2, view: 'text', content_type: 'text/plain', blob: new Blob(['hi']) }) })
  const host: ViewerHostValue = { open: v => setView(v), close: () => setView(null), keep: () => {}, active: view ? 'tab' : 'chat', views: view ? [{ id: 'tab', view }] : [], target: document.body }
  seed(ev({ seq: 1, kind: seedKind, payload: seedKind === 'assistant_text' ? { text: TEXT } : { text: TEXT, source: 'human' } }))
  return (
    <ViewerHostContext.Provider value={host}>
      <ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} cards={cards} canOpenWorkspaceFiles={canOpenWorkspaceFiles} />
      <HostedViewers sessionId={SESSION} files={t.files!} cards={cards} />
    </ViewerHostContext.Provider>
  )
}

async function popOut() {
  const pane = document.querySelector('.agent-timeline')!
  const card = document.querySelector<HTMLElement>('.agent-card.user, .agent-card.assistant')!
  Object.defineProperty(pane, 'clientHeight', { configurable: true, value: 100 })
  // jsdom lays nothing out: make the message's text ten 20px lines, past the 4-line default.
  const body = [...card.children].find(el => !el.classList.contains('card-author')) as HTMLElement
  body.style.lineHeight = '20px'
  Object.defineProperty(body, 'offsetHeight', { configurable: true, value: 200 })
  await act(async () => {
    // Fire every registered observer: the shared pane observer and Transcript's own.
    for (const inst of FakeResizeObserver.instances) inst.cb()
  })
  fireEvent.click(screen.getByTestId('popout-message'))
}

it('renders raw HTML as literal text, a card fence through the registry, and a workspace link, exactly as an assistant reply does in the chat', async () => {
  resetChatState()
  FakeResizeObserver.instances = []
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)
  render(<HostedChat />)
  await popOut()

  await waitFor(() => expect(document.querySelector('.artifact-body [data-testid="demo-card"]')).toBeInTheDocument())
  // Raw HTML: literal text, never an actual <b> element from this source.
  expect(document.querySelector('.artifact-body p')?.textContent).toBe('<b>raw html stays literal</b>')
  expect(document.querySelector('.artifact-body b')).toBeNull()
  // The card fence: rendered through the registry, not a fallback code block.
  expect(document.querySelector('.artifact-body [data-testid="demo-card"]')).toHaveTextContent('Demo card')
  expect(document.querySelector('.artifact-body pre code')).toBeNull()
  // The file path: a clickable workspace-file link, same as the chat gives an assistant reply.
  const link = document.querySelector('.artifact-body .md-wsfile')
  expect(link).not.toBeNull()
  expect(link).toHaveAttribute('data-workspace-path', 'src/app.ts')

  vi.unstubAllGlobals()
})

it('never offers a workspace link for a popped-out message that was a person\'s own (EventCards parity)', async () => {
  resetChatState()
  FakeResizeObserver.instances = []
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)
  render(<HostedChat seedKind="user_message" />)
  await popOut()

  await waitFor(() => expect(document.querySelector('.artifact-body [data-testid="demo-card"]')).toBeInTheDocument()) // cards still render for a person's message
  expect(document.querySelector('.artifact-body .md-wsfile')).toBeNull() // but never a workspace link

  vi.unstubAllGlobals()
})
