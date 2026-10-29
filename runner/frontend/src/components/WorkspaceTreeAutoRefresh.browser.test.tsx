// Card #270 in a real browser: the explorer reads again by itself after the agent's turn ends,
// in place (no Loading, expansion and scroll kept), its Git status badges move, and a hidden
// explorer only marks itself stale until it is shown again.
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import WorkspaceTree from './WorkspaceTree'
import '../index.css'
import '@blerglab/chat/blerg.css'

const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))

const server = { added: [] as string[], changed: 1 }
function respond(url: string) {
  const query = new URL(url, 'https://example.com').searchParams
  const op = query.get('op'), path = query.get('path') ?? ''
  const files = Array.from({ length: 60 }, (_, i) => ({ name: `file-${String(i).padStart(2, '0')}.ts`, path: `app/file-${String(i).padStart(2, '0')}.ts`, directory: false }))
  const entries = path === 'app'
    ? [...server.added.map(name => ({ name, path: `app/${name}`, directory: false })), ...files]
    : [{ name: 'app', path: 'app', directory: true, repository: true, member_id: 'app' }, { name: 'README.md', path: 'README.md', directory: false }]
  return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, truncated: false, ...(op === 'status' ? { status: { branch: 'main', changed: server.changed, untracked: 0, files: server.added.map(name => ({ path: name, code: 'U' })) } } : {}) }, members: [{ id: 'app', name: 'app', path: 'app' }], runtime: 'host' }))
}
const workspaceReads = () => read.mock.calls.filter(([url]) => String(url).includes('/workspace?')).length

let seq = 100
const ev = (kind: AgentEvent['kind'], payload: unknown): AgentEvent => ({ type: 'agent_event', session_id: 'session', client_event_id: `b${++seq}`, seq, ts: '2026-10-27T12:00:00Z', kind, payload })
const turnEnd = () => act(() => { useTranscriptStore.getState().ingest(ev('turn_done', {})) })

beforeEach(() => {
  Object.assign(server, { added: [], changed: 1 })
  read.mockReset().mockImplementation(async (url: string) => respond(url))
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestMany('session', [ev('user_message', { text: 'go', source: 'chat' })])
  useTranscriptStore.getState().ingestReplayDone('session', { lastSeq: seq, hasMore: false })
})
afterEach(async () => { await page.viewport(1280, 800) })

function Host({ hidden }: { hidden: boolean }) {
  return <div hidden={hidden} style={{ width: 'min(100%, 340px)', height: 300, overflowY: 'auto' }}><WorkspaceTree sessionId="session" active /></div>
}

it.each([[390, 844], [3840, 2160]])('shows the agent\'s new files and Git status after its turn, in place, at %i px', async (width, height) => {
  await page.viewport(width, height)
  render(<Host hidden={false} />)
  ;(await screen.findByRole('treeitem', { name: 'app' })).click()
  await screen.findByText('file-59.ts')
  await screen.findByText(/main · 1 changed/)
  const body = document.querySelector<HTMLElement>('.workspace-tree')!.parentElement!
  body.scrollTop = 400
  const scrolled = body.scrollTop
  expect(scrolled).toBeGreaterThan(0)
  // What the person is looking at: a row in view, and where it sits.
  const anchor = () => screen.getByText('file-30.ts').getBoundingClientRect().top - body.getBoundingClientRect().top
  const before = anchor()
  const reads = workspaceReads()
  const loading: string[] = []
  const watch = new MutationObserver(() => { for (const t of ['Loading…', 'Refreshing workspace…']) if (document.body.textContent?.includes(t)) loading.push(t) })
  watch.observe(document.body, { subtree: true, childList: true, characterData: true })
  server.added = ['NEW.ts']; server.changed = 2
  turnEnd()
  await screen.findByText('NEW.ts')
  await screen.findByText(/main · 2 changed/)
  watch.disconnect()
  expect(loading).toEqual([])
  expect(workspaceReads() - reads).toBe(3)
  expect(screen.getByRole('treeitem', { name: 'app, untracked in this folder' })).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('treeitem', { name: 'NEW.ts, untracked' })).toBeInTheDocument()
  // A row added above it does not move it (the browser's scroll anchoring keeps it in place).
  expect(Math.abs(anchor() - before)).toBeLessThanOrEqual(1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it('a hidden explorer only marks itself stale, and reads when it is shown', async () => {
  const view = render(<Host hidden={false} />)
  await screen.findByRole('treeitem', { name: 'app' })
  await new Promise(r => setTimeout(r, 1100))
  view.rerender(<Host hidden />)
  const reads = workspaceReads()
  server.added = ['LATER.md']
  turnEnd()
  await new Promise(r => setTimeout(r, 1500))
  expect(workspaceReads()).toBe(reads)
  view.rerender(<Host hidden={false} />)
  await expect.poll(workspaceReads).toBe(reads + 1)
  await new Promise(r => setTimeout(r, 1200))
  expect(workspaceReads()).toBe(reads + 1)
})
