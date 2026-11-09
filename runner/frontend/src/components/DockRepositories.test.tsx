import { Profiler } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import DockRepositories from './DockRepositories'
import { useSessionStore } from '../hooks/useSessionStore'
import { flushOpen, setSocketOpen } from '../test/wsMock'
import { makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => false }))
const member = { id: 'app', name: 'app', path: 'app', source_identity: 'app-source' }
const session = (id: string, title: string, parent?: string) => makeSession({ id, title, parent_session_id: parent, started_by: parent ? { kind: 'session' } : undefined, workspace: { members: [member] } })
beforeEach(() => {
	localStorage.clear()
  vi.useFakeTimers()
  // Deliberately put a grandchild before its parent in the inventory.
  useSessionStore.setState({ sessions: [session('deep', 'Nested worker', 'worker'), session('main', 'Coordinate work'), session('other', 'Other workspace'), session('foreign', 'Foreign worker', 'other'), session('orphan', 'Orphan worker', 'missing'), session('worker', 'Local worker', 'main')] })
  vi.spyOn(globalThis, 'fetch').mockImplementation(async url => new Response(JSON.stringify(String(url).endsWith('/result') ? { workspace_results: [] } : { result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0, partial: false } } })))
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
// Git status, graph and delivery reads; the membership poll has its own cadence.
const reads = () => vi.mocked(fetch).mock.calls.filter(([url]) => !String(url).endsWith('/workspace/repositories')).length
async function flush() { await act(async () => { await vi.advanceTimersByTimeAsync(400) }) }

it('ignores other workspaces for refreshes but watches nested workers', async () => {
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(reads()).toBe(2)
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'foreign' ? { ...s, status: 'idle' } : s) }))
  await flush()
  expect(reads()).toBe(2)
  act(() => useSessionStore.setState({ sessions: [...useSessionStore.getState().sessions, session('unrelated', 'Another workspace')] }))
  await flush()
  expect(reads()).toBe(2)
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'deep' ? { ...s, status: 'idle' } : s) }))
  await flush()
  expect(reads()).toBe(4)
})

type Membership = { revision: number; unsupported?: string; add_blocked?: string; blockers?: Record<string, string>; operation?: Record<string, unknown>; last?: Record<string, unknown> }
function mockMembership(view: Membership, status = { branch: 'main', changed: 0, untracked: 0, partial: false }) {
  const posts: { url: string; body: Record<string, unknown> }[] = []
  vi.mocked(fetch).mockImplementation(async (url, init) => {
    const target = String(url)
    if (init?.method === 'POST') {
      posts.push({ url: target, body: JSON.parse(String(init.body)) })
      return new Response(JSON.stringify({ id: 'op' }), { status: 202 })
    }
    if (target.endsWith('/workspace/repositories')) return new Response(JSON.stringify({ blockers: {}, ...view }))
    if (target.endsWith('/result')) return new Response(JSON.stringify({ workspace_results: [] }))
    return new Response(JSON.stringify({ result: { ok: true, status } }))
  })
  return posts
}
const pending = { id: 'op', kind: 'add', phase: 'authorized', requested_by: 'person', member_id: 'new', member_name: 'web', member_path: 'web', proposed_digest: 'digest' }
function single() { useSessionStore.setState({ sessions: [session('main', 'Coordinate work')] }) }

it('removes a clean repository with the revision the person saw', async () => {
  single()
  const posts = mockMembership({ revision: 3 })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  fireEvent.click(screen.getByRole('button', { name: 'Actions for app' }))
  fireEvent.click(screen.getByRole('button', { name: 'Remove app' }))
  await flush()
  expect(posts).toHaveLength(1)
  expect(posts[0].url).toContain('/api/sessions/main/workspace/repositories')
  expect(posts[0].body).toMatchObject({ kind: 'remove', member_id: 'app', expected_revision: 3 })
})
it('states on the row why a repository cannot be removed', async () => {
  single()
  mockMembership({ revision: 0 }, { branch: 'main', changed: 2, untracked: 0, partial: false })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  fireEvent.click(screen.getByRole('button', { name: 'Actions for app' }))
  expect(screen.getByRole('button', { name: 'Remove app' })).toBeDisabled()
  expect(screen.getByTitle('Commit or discard the changes first.')).toBeVisible()
})
it('states the server blocker on the row', async () => {
  single()
  mockMembership({ revision: 0, blockers: { app: "This is the workspace's last repository." } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  fireEvent.click(screen.getByRole('button', { name: 'Actions for app' }))
  expect(screen.getByRole('button', { name: 'Remove app' })).toBeDisabled()
  expect(screen.getByTitle("This is the workspace's last repository.")).toBeVisible()
})
it('shows a pending change as a row state and blocks other changes', async () => {
  single()
  mockMembership({ revision: 0, operation: { ...pending, waiting: 'Waiting for the current turn to finish.' } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByText('web')).toBeVisible()
  expect(screen.getByText('Adding. Waiting for the current turn to finish.')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Add repository' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: 'Actions for app' }))
  expect(screen.getByRole('button', { name: 'Remove app' })).toBeDisabled()
})
it('shows a pending removal on the member row', async () => {
  single()
  mockMembership({ revision: 0, operation: { ...pending, kind: 'remove', member_id: 'app', member_name: 'app', waiting: 'Waiting for the workers using this repository to finish.' } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByText('Removing. Waiting for the workers using this repository to finish.')).toBeVisible()
})
it('lets the person approve exactly what Main requested', async () => {
  single()
  const posts = mockMembership({ revision: 0, operation: { ...pending, phase: 'requested', requested_by: 'main' } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByText('Main asks to add web')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
  await flush()
  expect(posts[0].url).toContain('/workspace/repositories/op/decide')
  expect(posts[0].body).toEqual({ allow: true, proposed_digest: 'digest' })
})
it('explains the last refusal', async () => {
  single()
  mockMembership({ revision: 0, last: { ...pending, phase: 'refused', refusal: 'A folder with that name already exists in the workspace.' } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByRole('status')).toHaveTextContent("Couldn't add web. A folder with that name already exists in the workspace.")
})
it('hides denied operations and persists a refusal dismissal across reloads by operation id', async () => {
  single()
  mockMembership({ revision: 0, last: { ...pending, phase: 'denied' } })
  let view = render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.queryByText(/You declined/)).toBeNull()
  view.unmount()
  const refusal = { ...pending, phase: 'refused', refusal: 'Could not clone this repository. Check your Git access.' }
  mockMembership({ revision: 0, last: refusal })
  view = render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByRole('status')).toHaveTextContent(refusal.refusal)
  const dismiss = screen.getByRole('button', { name: 'Dismiss repository notice' })
  expect(dismiss).toHaveAttribute('title', 'Dismiss repository notice')
  fireEvent.click(dismiss)
  expect(screen.queryByRole('status')).toBeNull()
  view.unmount()
  view = render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.queryByRole('status')).toBeNull()
  view.unmount()
  mockMembership({ revision: 0, last: { ...refusal, id: 'new-operation' } })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByRole('status')).toHaveTextContent(refusal.refusal)
})
it('says why changes are unavailable and offers none', async () => {
  single()
  mockMembership({ revision: 0, unsupported: "Cluster workspaces can't change repositories yet." })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByTitle("Cluster workspaces can't change repositories yet.")).toBeVisible()
  expect(screen.queryByRole('button', { name: 'Add repository' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Remove app' })).toBeNull()
})
it('still offers Add in an empty workspace', async () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', workspace: { members: [] } })] })
  mockMembership({ revision: 0 })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByText('No repositories in this workspace.')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Add repository' })).toBeEnabled()
})
it('retries a busy root status read instead of calling a healthy repository unavailable', async () => {
  const rootMember = { ...member, path: '.' }
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', workspace: { members: [rootMember] } })] })
  let statusReads = 0
  vi.mocked(fetch).mockImplementation(async url => {
    if (String(url).endsWith('/result')) return new Response('{}')
    expect(new URL(String(url), 'http://example.test').searchParams.get('path')).toBe('')
    statusReads++
    return statusReads === 1 ? new Response(JSON.stringify({ code: 'busy' }), { status: 409 }) : new Response(JSON.stringify({ result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0, partial: false } } }))
  })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(statusReads).toBe(2)
  expect(screen.getByRole('listitem', { name: 'app, main, Clean' })).toHaveAccessibleName('app, main, Clean')
})
it('explains failed or partial state reads and allows a deliberate retry', async () => {
  useSessionStore.setState({ sessions: [session('main', 'Coordinate')] })
  let partial = true
  vi.mocked(fetch).mockImplementation(async url => new Response(JSON.stringify(String(url).endsWith('/result') ? {} : { result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0, partial } } })))
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  expect(screen.getByRole("img", { name: "Git status unavailable" })).toBeVisible()
  partial = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry state for app' }))
  await flush()
  expect(screen.getByRole('listitem', { name: 'app, main, Clean' })).toHaveAccessibleName('app, main, Clean')
  expect(screen.queryByText("Git status unavailable")).toBeNull()
})

it('shows only repository names in the dock without starting Git reads', async () => {
  render(<DockRepositories sessionId="main" active />)
  await flush()
  const list = screen.getByRole('list', { name: 'Repositories' })
  expect(list.textContent).toBe('app')
  expect(within(list).getAllByRole('button')).toHaveLength(1)
  expect(within(list).queryByRole('img')).toBeNull()
  expect(reads()).toBe(0)
})

it('opens known active branch owners and previews their title, engine, runtime and state without extra reads', async () => {
  const worker = { ...session('worker', 'Review changes', 'main'), engine: 'codex', runtime: 'docker' as const, workspace_results: [{ member_id: 'app', head: 'abc', branch: 'wip/12345678-1234-1234-1234-123456789abc' }] }
  const channel = { ...session('channel', 'Discuss changes', 'main'), started_by: undefined, workspace_results: [{ member_id: 'app', head: 'abc', branch: 'discussion' }] }
  useSessionStore.setState({ sessions: [session('main', 'Coordinate'), worker, channel, { ...worker, id: 'finished', status: 'stopped' }, { ...worker, id: 'foreign', parent_session_id: 'elsewhere' }, session('unknown', 'Unknown branch', 'main')] })
  const open = vi.fn()
  render(<DockRepositories sessionId="main" focusedMember="app" active onOpen={open} />)
  await flush()
  const branches = screen.getByRole('list', { name: 'Current branches for app' })
  expect(within(branches).getAllByRole('button')).toHaveLength(3)
  expect(within(branches).getByRole('button', { name: 'main' })).toBeVisible()
  expect(within(branches).getByRole('button', { name: 'discussion' })).toBeVisible()
  const branch = within(branches).getByRole('button', { name: worker.workspace_results[0].branch })
  expect(branch.textContent).toContain('…')
  fireEvent.pointerEnter(branch, { pointerType: 'mouse' })
  await act(async () => { await vi.advanceTimersByTimeAsync(600) })
  const card = screen.getByRole('tooltip')
  expect(card).toHaveTextContent('Review changes')
  expect(card).toHaveTextContent('Codex')
  expect(within(card).getByRole('img', { name: /Sandbox/ })).toBeVisible()
  expect(card).toHaveTextContent(worker.status)
  fireEvent.click(branch)
  expect(open).toHaveBeenCalledWith(worker)
  expect(reads()).toBe(2)
  expect(fetch).toHaveBeenCalledTimes(3)
})

it.each(['server', 'daemon'])('bounds retries when the %s reader stays busy', async source => {
  useSessionStore.setState({ sessions: [session('main', 'Coordinate')] })
  vi.mocked(fetch).mockImplementation(async url => String(url).endsWith('/result') ? new Response('{}') : source === 'server' ? new Response('{"code":"busy"}', { status: 409 }) : new Response('{"result":{"ok":false,"error_code":"busy"}}'))
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await act(async () => { await vi.advanceTimersByTimeAsync(15000) })
  expect(screen.getByRole("img", { name: "Git status unavailable" })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Retry state for app' })).toBeVisible()
  expect(reads()).toBe(9)
  expect(fetch).toHaveBeenCalledTimes(10)
})
it('cancels a pending busy retry when the repository tool closes', async () => {
  vi.mocked(fetch).mockImplementation(async () => new Response('{"code":"busy"}', { status: 409 }))
  const view = render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await act(async () => { await vi.advanceTimersByTimeAsync(400) })
  view.unmount()
  await act(async () => { await vi.advanceTimersByTimeAsync(15000) })
  expect(reads()).toBe(1)
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('can dismiss a branch preview on its first visible commit', async () => {
  let dismissed = false
  render(<Profiler id="branch-preview" onRender={() => {
    if (!dismissed && document.querySelector('[role="tooltip"]')) {
      dismissed = true
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    }
  }}><DockRepositories sessionId="main" focusedMember="app" active /></Profiler>)
  await flush()
  fireEvent.pointerEnter(screen.getByRole('button', { name: 'main' }), { pointerType: 'mouse' })
  await act(async () => { await vi.advanceTimersByTimeAsync(250) })
  expect(dismissed).toBe(true)
  expect(screen.queryByRole('tooltip')).toBeNull()
})

it.each([true, false])('reads membership once on mount (socket open: %s) and once on reconnect', async connected => {
  setSocketOpen(connected)
  mockMembership({ revision: 0 })
  const view = render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  const membershipReads = () => vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith('/workspace/repositories')).length
  expect(membershipReads()).toBe(1)
  act(() => flushOpen())
  await flush()
  expect(membershipReads()).toBe(2)
  view.unmount()
  setSocketOpen(true)
})

it('closes the row actions with Escape or an outside pointer', async () => {
  single()
  mockMembership({ revision: 0 })
  render(<DockRepositories sessionId="main" focusedMember="app" active />)
  await flush()
  const button = screen.getByRole('button', { name: 'Actions for app' })
  fireEvent.click(button)
  expect(screen.getByRole('button', { name: 'Remove app' })).toBeVisible()
  fireEvent.keyDown(document, { key: 'Escape' })
  expect(screen.queryByRole('button', { name: 'Remove app' })).toBeNull()
  fireEvent.click(button)
  fireEvent.pointerDown(document.body)
  expect(screen.queryByRole('button', { name: 'Remove app' })).toBeNull()
})

it('loads dismissal for the new session when the dock is reused', async () => {
 const refusal = { ...pending, phase: 'refused', refusal: 'Git could not complete the clone.' }
 mockMembership({ revision: 0, last: refusal })
 localStorage.setItem('blerg:repository-notice-dismissed:other', pending.id)
 const view = render(<DockRepositories sessionId="main" active />)
 await flush()
 expect(screen.getByRole('status')).toHaveTextContent(refusal.refusal)
 view.rerender(<DockRepositories sessionId="other" active />)
 await flush()
 expect(screen.queryByRole('status')).toBeNull()
 view.rerender(<DockRepositories sessionId="main" active />)
 await flush()
 expect(screen.getByRole('status')).toHaveTextContent(refusal.refusal)
})

it('ends only this workspace host consents and waits for confirmed cleanup', async () => {
  single()
  const posted: { url: string; body: unknown }[] = []
  vi.mocked(fetch).mockImplementation(async (url, init) => {
    const target = String(url)
    if (init?.method === 'POST') { posted.push({ url: target, body: JSON.parse(String(init.body)) }); return new Response(JSON.stringify({ withdrawn: true, cleanup_pending: true })) }
    if (target.endsWith('/workspace/repositories')) return new Response(JSON.stringify({ revision: 3, blockers: {}, operation: { ...pending, waiting_code: 'keeper', waiting: 'Waiting for the shell keeper to reconnect.' } }))
    if (target.includes('/shell-consents')) return new Response(JSON.stringify(target.includes('?after=next') ? { consents: [{ id: 'second', withdrawn: false }] } : { consents: [{ id: 'first', withdrawn: false }, { id: 'old', withdrawn: true }], next: 'next' }))
    return new Response(JSON.stringify({ workspace_results: [], result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0 } } }))
  })
  render(<DockRepositories sessionId="main" active />)
  await flush()
  fireEvent.click(screen.getByRole('button', { name: 'End host shells and continue' }))
  await flush()
  expect(posted).toEqual([])
  fireEvent.click(screen.getByRole('button', { name: 'End host shells' }))
  await flush()
  expect(posted).toEqual([
    { url: '/api/sessions/main/shell-consent/withdraw', body: { consent_id: 'first' } },
    { url: '/api/sessions/main/shell-consent/withdraw', body: { consent_id: 'second' } },
  ])
  expect(screen.getByText('Host shell consent withdrawn. The repository change will continue after the keeper confirms cleanup.')).toBeTruthy()
  expect(screen.queryByText('Waiting for the open shell to close.')).toBeNull()
})

it.each([403, 200])('does not promise cleanup when ending host shells cannot be confirmed (%i)', async status => {
  single()
  vi.mocked(fetch).mockImplementation(async (url, init) => {
    const target = String(url)
    if (target.endsWith('/workspace/repositories')) return new Response(JSON.stringify({ revision: 3, blockers: {}, operation: { ...pending, waiting_code: 'keeper' } }))
    if (target.includes('/shell-consents')) return new Response(JSON.stringify({ consents: [{ id: 'first' }] }))
    if (init?.method === 'POST') return new Response('{}', { status })
    return new Response('{}')
  })
  render(<DockRepositories sessionId="main" active />)
  await flush()
  fireEvent.click(screen.getByRole('button', { name: 'End host shells and continue' }))
  fireEvent.click(screen.getByRole('button', { name: 'End host shells' }))
  await flush()
  expect(screen.getByText('Host shell cleanup could not be requested. Try again or restore the keeper.')).toBeTruthy()
})

it('gives an incompatible keeper its remedy and offers no cleanup it cannot finish', async () => {
  single()
  const waiting = 'The shell keeper is from an incompatible Blerg version. Restart the keeper service on this desktop to replace it (this ends its open shells); the repository change continues afterward.'
  vi.mocked(fetch).mockImplementation(async url => {
    const target = String(url)
    if (target.endsWith('/workspace/repositories')) return new Response(JSON.stringify({ revision: 3, blockers: {}, operation: { ...pending, waiting_code: 'keeper_incompatible', waiting } }))
    return new Response(JSON.stringify({ workspace_results: [], result: { ok: true, status: { branch: 'main', changed: 0, untracked: 0 } } }))
  })
  render(<DockRepositories sessionId="main" active />)
  await flush()
  expect(screen.getByText(new RegExp('incompatible Blerg version'))).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'End host shells and continue' })).toBeNull()
})
