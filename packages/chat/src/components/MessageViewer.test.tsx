// MessageViewer (BLG-449): the tab a popped-out message opens in reuses the chat's own Markdown
// component — same renderer, same support for code blocks, tables, Mermaid and Vega-Lite as the
// transcript itself, not a second one built for this card.
import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import MessageViewer from './MessageViewer'
import { HostedViewers, ViewerHostContext, type ViewerHostValue } from './ViewerHost'
import { createFakeFiles } from '../test/fakeTransport'

function host(over: Partial<ViewerHostValue> = {}): ViewerHostValue {
  return { open: vi.fn(), close: vi.fn(), keep: vi.fn(), views: [], active: 'chat', target: null, ...over }
}

it('draws a heading, a code block and a table through the ordinary Markdown renderer', () => {
  const text = '## Plan\n\nSee below.\n\n```js\nconst x = 1\n```\n\n| a | b |\n| - | - |\n| 1 | 2 |\n'
  render(<MessageViewer text={text} label="Plan" onClose={() => {}} embedded />)
  const body = document.querySelector<HTMLElement>('.artifact-markdown.md-doc')!
  expect(body).toBeTruthy()
  expect(within(body).getByRole('heading', { name: 'Plan' })).toBeInTheDocument()
  expect(document.querySelector('pre code')).toHaveTextContent('const x = 1')
  expect(screen.getByRole('table')).toBeInTheDocument()
  expect(screen.getByRole('cell', { name: '2' })).toBeInTheDocument()
})

// A tab already carries its own name on the tab strip; the embedded viewer must not say it again
// visibly (the Review feedback this test guards: "Just over" was shown both as the tab header and
// as the message's own heading).
it('says the name once in an embedded tab: no visible toolbar title beside the message\'s own heading', () => {
  render(<MessageViewer text={'## Plan\n\nDetails.'} label="Plan" onClose={() => {}} embedded />)
  expect(screen.getAllByRole('heading', { name: 'Plan' })).toHaveLength(1)
  // The accessible name survives for aria-labelledby, just not drawn on screen.
  expect(screen.getByText('Plan', { selector: '.chat-sr-only' })).toBeInTheDocument()
  expect(screen.getByRole('toolbar', { name: 'File actions' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Copy title' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
})

it('names the tab from the given label, with a Close action when not embedded', () => {
  const onClose = vi.fn()
  render(<MessageViewer text="hi" label="General · Jul 26, 8:00 PM" onClose={onClose} />)
  expect(screen.getByRole('heading', { name: 'General · Jul 26, 8:00 PM' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(onClose).toHaveBeenCalledOnce()
})

it('is what HostedViewers renders for a message tab, with no file read involved', async () => {
  const files = createFakeFiles()
  const target = document.createElement('div')
  document.body.appendChild(target)
  const value = host({ target, active: 'message:e1', views: [{ id: 'message:e1', view: { kind: 'message', messageId: 'e1', text: '## Snapshot\n\nFrozen text.', label: 'Snapshot' } }] })
  render(<ViewerHostContext.Provider value={value}><HostedViewers sessionId="s1" files={files} /></ViewerHostContext.Provider>)
  try {
    const body = await screen.findByText('Frozen text.')
    expect(within(body.closest('.artifact-markdown')!).getByRole('heading', { name: 'Snapshot' })).toBeInTheDocument()
    expect(files.rawCalls).toEqual([])
  } finally { target.remove() }
})
