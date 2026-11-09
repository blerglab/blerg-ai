import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import SessionDockWindows from './SessionDockWindows'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { MemoryRouter } from 'react-router-dom'
vi.mock('../apiFetch', async original => { const actual = await original<typeof import('../apiFetch')>(); return { ...actual, apiFetch: (url: string, ...args: Parameters<typeof actual.apiFetch> extends [unknown, ...infer Rest] ? Rest : never) => url.startsWith('/api/inbox') ? Promise.resolve(new Response('{"items":[],"unread":0,"next":""}')) : url.endsWith('/categories') ? Promise.resolve(new Response('{"categories":[]}')) : url.endsWith('/live') ? Promise.resolve(new Response('{}')) : actual.apiFetch(url, ...args) } })
vi.mock('../ws', () => import('../test/wsMock'))
// Git status and delivery reads; the membership read has its own cadence.
const gitReads = (fetcher: { mock: { calls: unknown[][] } }) => fetcher.mock.calls.filter(([url]) => !String(url).endsWith('/workspace/repositories') && !String(url).startsWith('/api/inbox')).length
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => false }))

afterEach(() => vi.restoreAllMocks())
beforeEach(() => {
  localStorage.clear()
  useMessageStore.setState({ messages: [] })
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Coordinate' }), makeSession({ id: 'child', title: 'Review', parent_session_id: 'main' }), makeSession({ id: 'deep', title: 'Deep review', parent_session_id: 'child' }), makeSession({ id: 'done', title: 'Old review', parent_session_id: 'main', archived_at: '2026-10-30T12:00:00Z' })] })
})
function mount(mobile = false, open = vi.fn()) { return render(<MemoryRouter><SessionDockWindows sessionId="main" mobile={mobile} onOpen={open} files={<p>Workspace files</p>} /></MemoryRouter>) }
it('keeps live descendants of archived parents in their original order', () => {
  useSessionStore.setState(s => ({ sessions: [...s.sessions,
    makeSession({ id: 'nested-a', title: 'Nested first', parent_session_id: 'done' }),
    makeSession({ id: 'nested-b', title: 'Nested second', parent_session_id: 'done' }),
  ] }))
  mount()
  expect(screen.getAllByRole('treeitem')).toEqual(['Main', 'Review', 'Nested first', 'Nested second', 'Finished'].map(name => screen.getByRole('treeitem', { name })))
})
it('starts with Sessions, collapses descendants and Finished, and switches to Files', () => {
  const open = vi.fn(); mount(false, open)
  expect(screen.getAllByRole('tab').map(t => t.getAttribute('aria-label'))).toEqual(['Channels', 'Files', 'Repositories', 'Questions', 'Inbox'])
  expect(screen.getByText('Main')).toBeVisible()
  expect(screen.queryByText('Deep review')).toBeNull()
  expect(screen.queryByText('Old review')).toBeNull()
  fireEvent.keyDown(screen.getByRole('treeitem', { name: 'Review' }), { key: 'ArrowRight' })
  fireEvent.click(screen.getByText('Deep review')); expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: 'deep' }))
  fireEvent.click(screen.getByRole('tab', { name: 'Files' })); expect(screen.getByText('Workspace files')).toBeVisible()
})
it('stacks by dropping in the body, persists the divider, and merges on the tab row', () => {
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(300)
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(700)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 300, height: 700, right: 300, bottom: 700, x: 0, y: 0, toJSON() {} })
  const data = new Map<string, string>()
  const dataTransfer = { setData: (key: string, value: string) => data.set(key, value), getData: (key: string) => data.get(key) ?? '', types: ['application/x-blerg-session-tab'] }
  const view = mount()
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer })
  fireEvent.drop(document.querySelector('.session-group')!, { dataTransfer, clientX: 150, clientY: 690 })
  const divider = screen.getByRole('separator', { name: 'Resize tab groups 1' })
  fireEvent.keyDown(divider, { key: 'ArrowDown' }); expect(divider).toHaveAttribute('aria-valuenow', '55')
  view.unmount(); mount()
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '55')
  fireEvent.dragStart(screen.getByRole('tab', { name: 'Files' }), { dataTransfer })
  fireEvent.drop(screen.getAllByRole('tablist')[0], { dataTransfer })
  expect(screen.queryByRole('separator')).toBeNull()
  expect(screen.getByText('Workspace files')).toBeVisible()
})
it('uses switch-only tabs on phones even with a saved stacked layout', () => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'sessions', panels: ['sessions', 'files'], ratio: .6 }))
  mount(true)
  expect(screen.queryByRole('separator')).toBeNull()
  expect(screen.getByRole('tab', { name: 'Files' })).toHaveAttribute('draggable', 'false')
  fireEvent.click(screen.getByRole('tab', { name: 'Files' })); expect(screen.getByText('Workspace files')).toBeVisible()
  expect(JSON.parse(localStorage.getItem('blerg-runner.session-dock.windows..main')!).layout).toMatchObject({ type: 'split', first: { active: 'sessions' }, second: { active: 'files' } })
})
it('uses the Explorer keyboard order, roving focus and a collapsed Finished tree', () => {
  mount()
  const main = screen.getByRole('treeitem', { name: 'Main' })
  main.focus(); fireEvent.keyDown(main, { key: 'ArrowDown' })
  const child = screen.getByRole('treeitem', { name: 'Review' })
  expect(child).toHaveFocus()
  fireEvent.keyDown(child, { key: 'ArrowRight' })
  fireEvent.keyDown(child, { key: 'ArrowRight' })
  const deep = screen.getByRole('treeitem', { name: 'Deep review' })
  expect(deep).toHaveFocus()
  fireEvent.keyDown(deep, { key: 'ArrowLeft' }); expect(child).toHaveFocus()
  fireEvent.keyDown(child, { key: 'End' })
  const finished = screen.getByRole('treeitem', { name: 'Finished' })
  expect(finished).toHaveFocus()
  fireEvent.keyDown(finished, { key: 'ArrowRight' })
  expect(screen.getByRole('treeitem', { name: 'Old review' })).toBeVisible()
  expect(screen.getByRole('link', { name: 'History' })).toHaveAttribute('href', '/sessions/done?panel=history')
  expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([finished])
})
it('keeps the selected session when switching tool windows', () => {
  mount()
  fireEvent.click(screen.getByRole('treeitem', { name: 'Review' }))
  fireEvent.click(screen.getByRole('tab', { name: 'Files' }))
  fireEvent.click(screen.getByRole('tab', { name: 'Channels' }))
  expect(screen.getByRole('treeitem', { name: 'Review' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('treeitem', { name: 'Main' })).toHaveAttribute('aria-selected', 'false')
})
it('leaves a keyboard entry point when a focused chat moves into Finished', () => {
  mount()
  const child = screen.getByRole('treeitem', { name: 'Review' })
  act(() => child.focus())
  act(() => useSessionStore.setState({ sessions: useSessionStore.getState().sessions.map(s => s.id === 'child' ? { ...s, archived_at: '2026-10-30T12:00:00Z' } : s) }))
  expect(screen.queryByRole('treeitem', { name: 'Review' })).toBeNull()
  expect(screen.getByRole('treeitem', { name: 'Main' })).toHaveAttribute('tabIndex', '0')
  expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toHaveLength(1)
})
it('review: keeps expanded descendants, Finished and scroll across merged tool switches', () => {
  mount()
  fireEvent.keyDown(screen.getByRole('treeitem', { name: 'Review' }), { key: 'ArrowRight' })
  fireEvent.click(screen.getByRole('treeitem', { name: 'Finished' }))
  const panel = screen.getByRole('tabpanel', { name: 'Channels' }); panel.scrollTop = 27
  fireEvent.click(screen.getByRole('tab', { name: 'Files' }))
  fireEvent.click(screen.getByRole('tab', { name: 'Channels' }))
  expect(screen.getByRole('treeitem', { name: 'Deep review' })).toBeVisible()
  expect(screen.getByRole('treeitem', { name: 'Old review' })).toBeVisible()
  expect(screen.getByRole('tabpanel', { name: 'Channels' }).scrollTop).toBe(27)
})
it('review: a leaf chevron slot does not swallow the row activation', () => {
  const open = vi.fn(); mount(false, open)
  fireEvent.click(screen.getByRole('treeitem', { name: 'Main' }).querySelector('.workspace-chevron')!)
  expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: 'main' }))
})

it('shows a calm read-only empty repository window', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', repo: '', workspace: { members: [] } })] })
  mount()
  fireEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
  expect(screen.getByText('No repositories in this workspace.')).toBeVisible()
  expect(screen.queryByRole('button', { name: /add|remove/i })).toBeNull()
})
it('lists names without reading Git or showing delivery details', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', workspace: { members: [{ id: 'app', name: 'app', path: 'app' }] }, workspace_results: [{ member_id: 'app', head: 'abc', branch: 'wip/app', pending: true }] })] })
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{}'))
  mount()
  fireEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
  expect(screen.getByRole('button', { name: 'app' })).toBeVisible()
  expect(screen.queryByText('wip/app')).toBeNull()
  expect(screen.queryByRole('img', { name: 'Commits not delivered' })).toBeNull()
  expect(gitReads(fetcher)).toBe(0)
  fetcher.mockRestore()
})
it('reaches the third tool by keyboard while two windows are stacked', () => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'sessions', panels: ['sessions', 'files'], ratio: .5 }))
  mount()
  const tab = screen.getByRole('tab', { name: 'Channels' })
  tab.focus(); fireEvent.keyDown(tab, { key: 'ArrowRight' })
  expect(screen.getByRole('tab', { name: 'Repositories' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('tab', { name: 'Repositories' })).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Files' })).toBeVisible()
  expect(screen.getByRole('separator')).toBeVisible()
})

it('keeps an opened worker in Workers and leaves an empty channel section unlabeled', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'worker', started_by: { kind: 'session' }, title: 'Review', parent_session_id: 'main', runtime: 'docker' })] })
  render(<MemoryRouter><SessionDockWindows sessionId="worker" mobile={false} files={null} /></MemoryRouter>)
  expect(screen.getByRole('treeitem', { name: 'Main' })).toBeVisible()
  expect(document.querySelector('.dock-section-label')).toBeNull()
  const workers = screen.getByRole('treeitem', { name: 'Workers, 1, Sandbox' })
  expect(workers).toHaveAttribute('aria-expanded', 'true')
  expect(workers.querySelector('[data-runtime]')).not.toBeNull()
  expect(screen.getByRole('treeitem', { name: 'Review' })).toHaveAccessibleDescription('Worker, Sandbox')
})

it('BLG-501: shows each worker row its own runtime glyph when the Workers folder mixes runtimes', () => {
  useSessionStore.setState({
    sessions: [
      makeSession({ id: 'main' }),
      makeSession({ id: 'host-worker', started_by: { kind: 'session' }, title: 'Host worker', parent_session_id: 'main', runtime: 'daemon' }),
      makeSession({ id: 'cluster-worker', started_by: { kind: 'session' }, title: 'Cluster worker', parent_session_id: 'main', runtime: 'cluster' }),
    ],
  })
  render(<MemoryRouter><SessionDockWindows sessionId="host-worker" mobile={false} files={null} /></MemoryRouter>)
  expect(screen.getByRole('treeitem', { name: 'Host worker' }).querySelectorAll('[data-runtime="host"]')).toHaveLength(1)
  expect(screen.getByRole('treeitem', { name: 'Cluster worker' }).querySelectorAll('[data-runtime="cluster"]')).toHaveLength(1)
})

it('keeps a person-started channel under a worker in the keyboard order', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'worker', title: 'Review', parent_session_id: 'main', started_by: { kind: 'session' } }), makeSession({ id: 'discussion', title: 'Follow-up discussion', parent_session_id: 'worker' })] })
  mount()
  const channel = screen.getByRole('treeitem', { name: 'Follow-up discussion' })
  act(() => channel.focus())
  expect(channel).toHaveAttribute('tabIndex', '0')
  expect(screen.getAllByRole('treeitem').filter(row => row.tabIndex === 0)).toEqual([channel])
})
it('keeps parent-handled waits quiet but surfaces person questions and failures while collapsed', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'worker', title: 'Review', status: 'waiting', parent_session_id: 'main', started_by: { kind: 'session' } })] })
  mount()
  const workers = screen.getByRole('treeitem', { name: 'Workers, 1' })
  expect(workers).not.toHaveTextContent('Review')
  act(() => useMessageStore.setState({ messages: [{ id: 'question', session_id: 'worker', kind: 'ask', status: 'open', answerable: true, owner_only: true, body: 'Choose a direction', created_at: '2026-10-31T12:00:00Z' }] }))
  expect(workers).toHaveAttribute('aria-expanded', 'false')
  expect(workers).toHaveTextContent('Review')
  expect(workers).toHaveAccessibleName('Workers, 1, Review: needs you')
  act(() => useMessageStore.setState({ messages: [] }))
  expect(workers).not.toHaveTextContent('Review')
  act(() => useSessionStore.getState().updateSessionStatus('worker', 'error'))
  expect(workers).toHaveTextContent('Review')
  expect(workers).toHaveAccessibleName('Workers, 1, Review: failed')
})
it('shows the runtime when a standalone worker is opened without a listed parent', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Automated review', started_by: { kind: 'agent' }, runtime: 'daemon' })] })
  mount()
  const worker = screen.getByRole('treeitem', { name: 'Automated review' })
  expect(screen.queryByRole('treeitem', { name: 'Main' })).toBeNull()
  expect(worker).toHaveAccessibleDescription('Workspace, Host')
  expect(worker.querySelectorAll('[data-runtime="host"]')).toHaveLength(1)
})

it.each([false, true])('reveals a directly opened worker and keeps route selection current (phone=%s)', mobile => {
  const worker = makeSession({ id: 'worker', title: 'Worker location', parent_session_id: 'main', started_by: { kind: 'session' }, status: 'error' })
  useSessionStore.setState(s => ({ sessions: [...s.sessions, worker] }))
  const renderDock = (id: string) => <MemoryRouter><SessionDockWindows sessionId={id} mobile={mobile} files={null} /></MemoryRouter>
  const view = render(renderDock('main'))
  view.rerender(renderDock('worker'))
  expect(screen.getByRole('treeitem', { name: /^Worker location, failed$/ })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('treeitem', { name: /^Workers/ })).toHaveAttribute('aria-expanded', 'true')
  view.unmount()
  render(renderDock('worker'))
  expect(screen.getByRole('treeitem', { name: /^Worker location, failed$/ })).toHaveAttribute('aria-selected', 'true')
})
it('shows channel unread in dock rows and makes an agent-started root a top row', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Automated workspace', started_by: { kind: 'agent' } }), makeSession({ id: 'channel', title: 'Unread channel', parent_session_id: 'main', unread: true })] })
  mount()
  expect(screen.getByRole('treeitem', { name: 'Automated workspace' }).closest('li')).toHaveClass('dock-main')
  expect(screen.getByLabelText('Unread channel output')).toBeVisible()
})

it('switches tools without redundant window collapse controls', () => {
  mount()
  expect(document.querySelector('.session-dock-collapse')).toBeNull()
  fireEvent.click(screen.getByRole('tab', { name: 'Files' }))
  expect(screen.getByRole('tabpanel', { name: 'Files' })).toBeVisible()
})

it('uses no icon for workers and a bubble for Main and channels', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'main' }), makeSession({ id: 'channel', title: 'Discuss', parent_session_id: 'main' }), makeSession({ id: 'worker', title: 'Review', parent_session_id: 'main', started_by: { kind: 'session' } })] })
  mount()
  expect(screen.getByRole('treeitem', { name: 'Review' }).querySelector('[data-chat-role]')).toBeNull()
  expect(screen.getByRole('treeitem', { name: 'Main' }).querySelector('path')?.getAttribute('d')).toBe('M3 2h10v8H6l-3 3z')
  expect(screen.getByRole('treeitem', { name: 'Discuss' }).querySelector('path')?.getAttribute('d')).toBe('M3 2h10v8H6l-3 3z')
})

it('keeps stacked tool tabs in their fixed order after mouse and keyboard selection', () => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'sessions', panels: ['sessions', 'files'], ratio: .5 }))
  mount()
  const order = () => Array.from(screen.getAllByRole('tablist')[0].querySelectorAll('[role="tab"]')).map(tab => tab.getAttribute('aria-label'))
  expect(order()).toEqual(['Channels', 'Repositories', 'Questions', 'Inbox'])
  fireEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
  expect(order()).toEqual(['Channels', 'Repositories', 'Questions', 'Inbox'])
  fireEvent.keyDown(screen.getByRole('tab', { name: 'Repositories' }), { key: 'ArrowLeft' })
  expect(screen.getByRole('tab', { name: 'Channels' })).toHaveAttribute('aria-selected', 'true')
  expect(order()).toEqual(['Channels', 'Repositories', 'Questions', 'Inbox'])
})


it('uses the panel group layout and the shared tab strip for dock tools', () => {
  mount()
  expect(document.querySelector('.session-groups .session-group .session-tabstrip')).not.toBeNull()
  expect(document.querySelector('.session-dock-drop-hint')).toBeNull()
})
it('restores dragged tab order independently of the active tool', () => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'files', layout: { type: 'group', id: 'dock-main', tabs: ['repositories', 'sessions', 'files'], active: 'files' } }))
  mount()
  const order = () => screen.getAllByRole('tab').map(t => t.getAttribute('aria-label'))
  expect(order()).toEqual(['Repositories', 'Channels', 'Files', 'Questions', 'Inbox'])
  fireEvent.click(screen.getByRole('tab', { name: 'Channels' }))
  expect(order()).toEqual(['Repositories', 'Channels', 'Files', 'Questions', 'Inbox'])
})

it('migrates all three saved tool windows and retains selection state on a phone round trip', () => {
  localStorage.setItem('blerg-runner.session-dock.windows', JSON.stringify({ active: 'files', panels: ['sessions', 'files', 'repositories'], ratio: .3, lower: .65 }))
  const view = mount()
  expect(screen.getAllByRole('tabpanel')).toHaveLength(3)
  const saved = JSON.parse(localStorage.getItem('blerg-runner.session-dock.windows..main')!).layout
  expect(saved).toMatchObject({ type: 'split', ratio: .3, second: { type: 'split', ratio: expect.closeTo(.5) } })
  view.unmount()
  const phone = mount(true)
  fireEvent.click(screen.getByRole('tab', { name: 'Repositories' }))
  expect(screen.queryByRole('separator')).toBeNull()
  expect(JSON.parse(localStorage.getItem('blerg-runner.session-dock.windows..main')!).layout).toEqual(saved)
  phone.unmount(); mount()
  expect(screen.getAllByRole('tabpanel')).toHaveLength(3)
})

it('offers an inline channel name from the dock', () => {
 mount()
 fireEvent.click(screen.getByRole('button', { name: 'New channel' }))
 expect(screen.getByRole('textbox', { name: 'Channel name' })).toHaveFocus()
 expect(screen.getByText('Change repositories and permissions')).toBeVisible()
})
