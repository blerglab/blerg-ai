// #250 review: replies that land after the person moved on (another account, another desktop scope,
// a newer visit of the same session, a save that outlived its view) never reach the cache or the
// board button; the explorer keeps its page budget and never leaves an open folder empty.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'

const who = vi.hoisted(() => ({ subject: 'person-a' }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: who.subject, exp: 9999999999 }))}.x` }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('./chatTransport', () => ({ blergTransport: () => ({ files: undefined }), invalidateChatTransport: () => {} }))

import { readView, resetViewCacheForTest, syncViewCacheSessions, writeView, pageTotal, EXPLORER_PAGE_CAP } from './sessionViewCache'
import { resetSessionBoardLinksForTest, useSessionBoardLinks } from './sessionBoardLinks'
import SessionPermissions from '../components/SessionPermissions'
import PlanNudges from '../components/PlanNudges'
import WorkspaceTree, { type ExplorerSnapshot } from '../components/WorkspaceTree'
import { StrictMode } from 'react'
import { useEngineModels } from './engineModels'
import { clearLaunchCache, launchModelsCache } from './launchCache'
import '../hooks/useSessionStore'
import { emit } from '../test/wsMock'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const permissions = (board: string) => ({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: board, board_name: `Board ${board}`, level: 'full' }] })
/** A reply the test answers when it chooses; records the signal the request was made with. */
function deferred() {
  let resolve!: (r: Response) => void
  const promise = new Promise<Response>(r => { resolve = r })
  return { promise, resolve, signal: undefined as AbortSignal | undefined }
}
const flush = () => act(async () => { for (let i = 0; i < 10; i++) await Promise.resolve() })

beforeEach(() => {
  vi.useRealTimers()
  who.subject = 'person-a'
  apiFetch.mockReset()
  syncViewCacheSessions([{ id: 's', status: 'idle' }])
})
afterEach(() => { resetViewCacheForTest(); resetSessionBoardLinksForTest() })

describe('board links (P1)', () => {
  it("a late reply of the previous account never replaces the current account's boards", async () => {
    const a = deferred(), b = deferred()
    apiFetch.mockImplementationOnce((_: string, init?: RequestInit) => { a.signal = init?.signal ?? undefined; return a.promise })
    const first = renderHook(() => useSessionBoardLinks('s'))
    first.unmount()
    who.subject = 'person-b'
    window.dispatchEvent(new Event('blerg-identity-changed'))
    apiFetch.mockImplementationOnce(() => b.promise)
    const second = renderHook(() => useSessionBoardLinks('s'))
    b.resolve(json(permissions('b-current')))
    await waitFor(() => expect(second.result.current.map(l => l.id)).toEqual(['b-current']))
    a.resolve(json(permissions('a-private')))
    await flush()
    expect(second.result.current.map(l => l.id)).toEqual(['b-current'])
    expect(a.signal?.aborted).toBe(true)
  })
})

describe('panel reads', () => {
  it('a permissions reply asked for before a desktop scope change is not cached (P1)', async () => {
    const old = deferred()
    apiFetch.mockImplementation((url: string) => url.endsWith('/permissions') ? old.promise : Promise.resolve(json({})))
    const view = render(<SessionPermissions sessionId="s" active delegationGrants={[]} refreshKey="" />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s/permissions', expect.anything()))
    window.dispatchEvent(new Event('blerg-desktop-scope-changed'))
    view.unmount()
    old.resolve(json(permissions('removed-scope')))
    await flush()
    expect(readView('person-a', 's', 'permissions')).toBeUndefined()
  })

  it('an older visit of the same session never overwrites the newer cached permissions, and is aborted (P2)', async () => {
    const first = deferred(), second = deferred()
    const replies = [first, second]
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (!url.endsWith('/permissions')) return Promise.resolve(json({}))
      const next = replies.shift()!
      next.signal = init?.signal ?? undefined
      return next.promise
    })
    const visit1 = render(<SessionPermissions sessionId="s" active delegationGrants={[]} refreshKey="" />)
    await waitFor(() => expect(first.signal).toBeDefined())
    visit1.unmount()
    expect(first.signal?.aborted).toBe(true)
    render(<SessionPermissions sessionId="s" active delegationGrants={[]} refreshKey="" />)
    await waitFor(() => expect(second.signal).toBeDefined())
    second.resolve(json(permissions('new-permissions')))
    await screen.findByText(/new-permissions/)
    first.resolve(json(permissions('old-permissions')))
    await flush()
    const cached = readView<{ view: { boards: { board_id: string }[] } }>('person-a', 's', 'permissions')
    expect(cached?.view.boards[0].board_id).toBe('new-permissions')
  })

  it("a plan-nudge save is cached under the account that made it, not the one signed in when it lands (P2)", async () => {
    const save = deferred()
    apiFetch.mockImplementation((_: string, init?: RequestInit) => init?.method === 'PUT' ? save.promise : Promise.resolve(json({ enabled: true })))
    const view = render(<PlanNudges sessionId="s" />)
    const box = await screen.findByRole('checkbox', { name: 'Nudge when the plan drifts' })
    fireEvent.click(box)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s/plan-nudges', expect.objectContaining({ method: 'PUT' })))
    view.unmount()
    who.subject = 'person-b'
    window.dispatchEvent(new Event('blerg-identity-changed'))
    save.resolve(json({ enabled: false }))
    await flush()
    expect(readView('person-b', 's', 'plan-nudges')).toBeUndefined()
  })
})

describe('explorer page budget (P2)', () => {
  const dir = (name: string) => ({ name, path: name, directory: true })
  function server(root: object[], cursorFor: (path: string) => boolean = () => false, extra: Record<string, object[]> = {}) {
    apiFetch.mockImplementation(async (url: string) => {
      const q = new URL(url, 'https://x').searchParams
      const path = q.get('path') ?? ''
      const file = { name: `${path}-${q.get('cursor') ?? 0}.txt`, path: `${path}/${q.get('cursor') ?? 0}.txt`, directory: false }
      const entries = path ? [...(q.get('cursor') ? [] : extra[path] ?? []), file] : root
      const cursor = path && cursorFor(path) ? String(Number(q.get('cursor') ?? 0) + 1) : undefined
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, cursor, truncated: !!cursor }, members: [], runtime: 'host' })
    })
  }
  const listCalls = () => apiFetch.mock.calls.map(c => String(c[0])).filter(u => u.includes('op=list'))
  const snapshot = () => readView<ExplorerSnapshot>('person-a', 's', 'explorer')

  const loadPages = async (name: string, n: number) => {
    fireEvent.click(await screen.findByRole('treeitem', { name }))
    await screen.findByText(`${name}-0.txt`)
    for (let i = 1; i < n; i++) {
      fireEvent.click(screen.getAllByRole('button', { name: 'Load more' }).at(-1)!)
      await screen.findByText(`${name}-${i}.txt`)
    }
  }
  const snapshotOf = (pages: Record<string, { entries: object[]; loaded?: number; firstSize?: number }>, expanded: string[], statuses: ExplorerSnapshot['statuses'] = {}): ExplorerSnapshot => ({
    binding: 'launch:1', expanded, statuses, statusErrors: {}, statusTimes: {}, members: [], runtime: 'host', unavailable: [],
    filter: '', hits: null, focusRoot: '', focusedPath: '', scrollTop: 0, pages: pages as ExplorerSnapshot['pages'],
  })

  it(`live browsing is unlimited (R4, R2); only the snapshot keeps at most ${EXPLORER_PAGE_CAP} pages, the folders on screen first, and a return re-reads the rest`, async () => {
    server([dir('one'), dir('two')], path => path === 'one' || path === 'two')
    const view = render(<WorkspaceTree sessionId="s" active />)
    await loadPages('one', 120)
    // Past 100 pages Load more still offers the rest; nothing the person opened is closed.
    expect(screen.queryByText('Partial listing: this directory reached the read limit.')).toBeNull()
    await loadPages('two', 90)
    expect(screen.getByRole('treeitem', { name: 'one' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('treeitem', { name: 'two' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('one-119.txt')).toBeTruthy()
    const kept = snapshot()!
    expect(pageTotal(kept.pages)).toBeLessThanOrEqual(EXPLORER_PAGE_CAP)
    expect(Object.keys(kept.pages).sort()).toEqual(['', 'one'])
    view.unmount()
    apiFetch.mockClear()
    render(<WorkspaceTree sessionId="s" active />)
    // two's listing was not kept: read again (one page) and shown open, never empty.
    await screen.findByText('two-0.txt', {}, { timeout: 20_000 })
    expect(screen.getByRole('treeitem', { name: 'two' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByText('Expand again to retry.')).toBeNull()
    expect(listCalls().filter(u => u.includes('path=two'))).toHaveLength(1)
  }, 60_000)

  it('a folder opened under ancestors that fill the budget opens and stays open (R2)', async () => {
    server([dir('a')], path => path === 'a' || path === 'a/b', { a: [dir('a/b')], 'a/b': [dir('a/b/c')] })
    writeView<ExplorerSnapshot>('person-a', 's', 'explorer', snapshotOf({
      '': { entries: [dir('a')] }, a: { entries: [dir('a/b')], loaded: 100 }, 'a/b': { entries: [dir('a/b/c')], loaded: 99 },
    }, ['', 'a', 'a/b']))
    render(<WorkspaceTree sessionId="s" active />)
    // The return re-reads the first page of each open folder only: root, a, a/b.
    await waitFor(() => expect(listCalls()).toHaveLength(3), { timeout: 20_000 })
    fireEvent.click(await screen.findByRole('treeitem', { name: 'a/b/c' }))
    await screen.findByText('a/b/c-0.txt')
    await flush()
    expect(screen.getByRole('treeitem', { name: 'a/b/c' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('a/b/c-0.txt')).toBeTruthy()
  }, 60_000)

  it('the snapshot keeps Git status only for paths its kept listings show (R5)', async () => {
    server([dir('big'), dir('outer')], path => path === 'big')
    const status = { branch: 'main', changed: 0, untracked: 0 } as NonNullable<ExplorerSnapshot['statuses'][string]>
    writeView<ExplorerSnapshot>('person-a', 's', 'explorer', snapshotOf({
      '': { entries: [dir('big'), dir('outer')] }, big: { entries: [], loaded: 199 },
      outer: { entries: [{ ...dir('outer/repo'), repository: true }] }, 'outer/repo': { entries: [] },
    }, ['', 'big'], { 'outer/repo': status, big: status }))
    render(<WorkspaceTree sessionId="s" active />)
    await screen.findByRole('treeitem', { name: 'big' })
    const kept = snapshot()!
    expect(Object.keys(kept.pages).sort()).toEqual(['', 'big'])
    expect(Object.keys(kept.statuses)).toEqual(['big'])
  }, 60_000)

  it('reads again an open folder whose cached listing was dropped, instead of leaving it empty', async () => {
    server([dir('kept'), dir('evicted')])
    writeView<ExplorerSnapshot>('person-a', 's', 'explorer', {
      binding: 'launch:1', expanded: ['', 'kept', 'evicted'], statuses: {}, statusErrors: {}, statusTimes: {}, members: [], runtime: 'host', unavailable: [],
      filter: '', hits: null, focusRoot: '', focusedPath: '', scrollTop: 0,
      pages: { '': { entries: [dir('kept'), dir('evicted')], truncated: false }, kept: { entries: [{ name: 'kept-0.txt', path: 'kept/0.txt', directory: false }], truncated: false } },
    })
    render(<WorkspaceTree sessionId="s" active />)
    await screen.findByText('evicted-0.txt')
    expect(screen.queryByText('Expand again to retry.')).toBeNull()
    expect(listCalls().map(u => new URL(u, 'https://x').searchParams.get('path'))).toEqual(['', 'kept', 'evicted'])
  })

  it('a return re-reads the first page of every open folder and closes none, however many there are', async () => {
    const names = Array.from({ length: EXPLORER_PAGE_CAP + 5 }, (_, i) => `d${String(i).padStart(3, '0')}`)
    server(names.map(dir))
    writeView<ExplorerSnapshot>('person-a', 's', 'explorer', snapshotOf({ '': { entries: names.map(dir) } }, ['', ...names]))
    render(<WorkspaceTree sessionId="s" active />)
    await screen.findByText(`${names.at(-1)}-0.txt`, {}, { timeout: 30_000 })
    expect(listCalls()).toHaveLength(names.length + 1)
    for (const name of names) expect(screen.getByRole('treeitem', { name })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByText('Expand again to retry.')).toBeNull()
  }, 60_000)

  it('a 199-page parent kept in the snapshot does not close its open child whose listing was not kept (N1)', async () => {
    server([dir('big')], path => path === 'big', { big: [dir('big/child')] })
    writeView<ExplorerSnapshot>('person-a', 's', 'explorer', snapshotOf({
      '': { entries: [dir('big')] },
      big: { entries: [dir('big/child'), ...Array.from({ length: 198 }, (_, i) => ({ name: `big-${i + 1}.txt`, path: `big/${i + 1}.txt`, directory: false }))], loaded: 199, firstSize: 1 },
    }, ['', 'big', 'big/child']))
    render(<WorkspaceTree sessionId="s" active />)
    await screen.findByText('big/child-0.txt', {}, { timeout: 20_000 })
    expect(screen.getByRole('treeitem', { name: 'big/child' })).toHaveAttribute('aria-expanded', 'true')
    expect(listCalls().map(u => new URL(u, 'https://x').searchParams.get('path'))).toEqual(['', 'big', 'big/child'])
    // The later pages of the parent stay as cached until the person loads more.
    expect(screen.getByText('big-198.txt')).toBeTruthy()
  }, 60_000)
})

describe('StrictMode and refreshes', () => {
  it('PlanNudges reads (and caches) its setting under StrictMode effect replay (R1)', async () => {
    apiFetch.mockImplementation((_: string, init?: RequestInit) => new Promise((resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('Cancelled', 'AbortError')))
      setTimeout(() => resolve(json({ enabled: true })), 5)
    }))
    render(<StrictMode><PlanNudges sessionId="s" /></StrictMode>)
    expect(await screen.findByRole('checkbox', { name: 'Nudge when the plan drifts' })).toBeChecked()
    expect(screen.queryByText('Could not load the plan nudge setting.')).toBeNull()
    expect(readView('person-a', 's', 'plan-nudges')).toEqual({ enabled: true })
  })

  it('a same-account inventory refresh during a model read reads the list again instead of leaving it loading (R3)', async () => {
    clearLaunchCache()
    const first = deferred()
    let calls = 0
    apiFetch.mockImplementation(() => ++calls === 1 ? first.promise : Promise.resolve(json({ models: [{ id: 'gpt-new', name: 'GPT new' }] })))
    const hook = renderHook(() => useEngineModels('codex', 'd1', { cache: launchModelsCache }))
    await waitFor(() => expect(calls).toBe(1))
    act(() => emit({ type: 'desktop_sources_changed', desktop_id: 'd1' } as Parameters<typeof emit>[0]))
    first.resolve(json({ models: [{ id: 'gpt-old', name: 'GPT old' }] }))
    await waitFor(() => expect(hook.result.current.models.map(m => m.id)).toEqual(['gpt-new']))
    expect(hook.result.current.loading).toBe(false)
    clearLaunchCache()
  })
})
