import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useViewerHost, type HostedView, type Ticket } from '@blerglab/chat'
import SessionChatTabs from './SessionChatTabs'
import { SessionViewTargets, viewId, useSessionTabs } from '../hooks/useSessionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'

const phone = vi.hoisted(() => ({ value: false }))
vi.mock('../hooks/useIsMobile', () => ({ useIsMobile: () => phone.value }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', () => ({ getAccessToken: () => null }))
vi.mock('../hooks/useSessionLive', () => ({ useSessionLive: () => ({ source: 'server', live: {} }) }))
vi.mock('./SessionBoardTabs', () => ({ SessionBoardHost: () => null }))
const views: HostedView[] = [
  ...['html', 'image', 'pdf', 'markdown', 'chart', 'diagram', 'diff'].map(kind => ({ kind: 'artifact' as const, artifact: { id: kind, name: kind, view: kind === 'chart' ? 'json' : kind === 'diagram' ? 'text' : kind, size: 10, content_type: 'text/plain' } })),
  { kind: 'artifact', artifact: { id: 'review', name: 'review.md', view: 'markdown', size: 10, content_type: 'text/markdown' }, review: true },
  { kind: 'workspace', path: 'app.ts' },
  { kind: 'commit', sha: 'abc12345' },
  { kind: 'message', messageId: 'message', text: 'hello', label: 'Message' },
  { kind: 'visual', id: 'visual', visualKind: 'mermaid', source: 'graph TD; A-->B', label: 'Diagram' },
]
vi.mock('./AgentChatView', () => ({ default: function MockAgentChatView({ onOpenTicket }: { onOpenTicket?: (ticket: Ticket) => void }) {
  const host = useViewerHost()
  return <>{views.map((view, i) => <button key={i} onClick={() => host?.open(view, true)}>Open {i}</button>)}<button onClick={() => onOpenTicket?.({ source: 'blerg-board', board_id: 'board', key: '#743', title: 'Campaign' })}>Ticket</button><span data-testid="destination">{host ? 'tabs' : 'modal'}</span></>
} }))
function Harness() {
  const tabs = useSessionTabs('person', 'main', false)
  return <><button onClick={() => tabs.open({ kind: 'session-chat', sessionId: 'channel', label: 'Marketing' })}>Channel</button><SessionChatTabs tabs={tabs} /><div ref={node => { if (node) node.append(tabs.targetFor('session-chat:channel')) }} /><output>{JSON.stringify(tabs.tabs)}</output></>
}
beforeEach(() => { phone.value = false; localStorage.clear(); useSessionStore.setState({ sessions: [makeSession({ id: 'channel' })] }) })
it.each(views.map((view, i) => [view.kind === 'artifact' ? view.artifact.id : view.kind, i]))('opens channel %s in desktop tabs', (_kind, index) => {
  render(<Harness />)
  fireEvent.click(screen.getByText('Channel'))
  fireEvent.click(screen.getByText(`Open ${index}`))
  expect(screen.getByTestId('destination')).toHaveTextContent('tabs')
  expect(screen.getByRole('status').textContent).toContain('"ownerSessionId":"channel"')
  expect(screen.getByRole('status').textContent).toContain('"kind":"session-chat"')
})

it('opens channel tickets in the shared strip', () => {
  render(<Harness />)
  fireEvent.click(screen.getByText('Channel'))
  fireEvent.click(screen.getByText('Ticket'))
  expect(screen.getByRole('status').textContent).toContain('"kind":"ticket"')
  expect(screen.getByRole('status').textContent).toContain('"sessionId":"channel"')
  expect(screen.getByRole('status').textContent).toContain('"kind":"session-chat"')
})
it('keeps the phone modal fallback', () => {
  phone.value = true
  render(<Harness />)
  fireEvent.click(screen.getByText('Channel'))
  expect(screen.getByTestId('destination')).toHaveTextContent('modal')
})

it('keeps identical file paths and snapshots separate across chats', () => {
  for (const view of views) {
    expect(viewId({ ...view, ownerSessionId: 'channel' })).not.toBe(viewId({ ...view, ownerSessionId: 'worker' }))
    expect(viewId({ ...view, ownerSessionId: 'channel' })).not.toBe(viewId(view))
  }
})

it('retains a closed chat target while its viewers still need their providers', () => {
  const targets = new SessionViewTargets('test')
  const chat = targets.get('session-chat:channel')
  targets.prune([{ id: 'file', preview: false, view: { kind: 'workspace', path: 'app.ts', ownerSessionId: 'channel' } }])
  expect(targets.get('session-chat:channel')).toBe(chat)
  targets.prune([])
  expect(targets.get('session-chat:channel')).not.toBe(chat)
})
