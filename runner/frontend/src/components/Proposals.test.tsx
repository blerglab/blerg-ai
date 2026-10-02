import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Proposals from './Proposals'
import { usePendingProposals, isAwaitingYou } from '../lib/proposals'
import type { ProposalInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function prop(over: Partial<ProposalInfo> = {}): ProposalInfo {
  const args = over.arguments ?? { to: 'a@example.com', body: '<img src=x onerror=alert(1)>' }
  return {
    id: 'p1', state: 'pending', connection_id: 'c1', connection_name: 'calendar', tool: 'send_invite',
    arguments: args, arguments_raw: JSON.stringify(args), agent_summary: 'Invite Alice',
    session_id: 's1', cron_id: null, created_at: '2026-09-29T10:00:00Z', expires_at: '2026-10-06T10:00:00Z',
    decided_at: null, decided_by: null, result: null, ...over,
  }
}

function res(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) })
}

let items: ProposalInfo[]
let calls: { url: string; method: string; body?: unknown }[]
let action: (url: string) => Promise<unknown> | null
let pageSize = 50

beforeEach(() => {
  items = []
  calls = []
  action = () => null
  pageSize = 50
  usePendingProposals.setState({ count: 0 })
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const custom = action(url)
    if (custom) return custom
    const u = new URL(url, 'http://x')
    const pendingCount = items.filter(i => i.state === 'pending').length
    if (u.pathname === '/api/proposals/count') return res({ pending_count: pendingCount })
    if (u.pathname === '/api/proposals') {
      const scope = u.searchParams.get('scope') ?? 'open'
      if (scope === 'open') return res({ proposals: items.filter(isAwaitingYou), decided_next: null, pending_count: pendingCount })
      const decided = items.filter(i => !isAwaitingYou(i))
      const from = Number(u.searchParams.get('before') ?? '0')
      const page = decided.slice(from, from + pageSize)
      const next = from + pageSize < decided.length ? String(from + pageSize) : null
      return res({ proposals: page, decided_next: next, pending_count: pendingCount })
    }
    const one = /^\/api\/proposals\/([^/]+)$/.exec(u.pathname)
    if (one && method === 'GET') {
      const found = items.find(i => i.id === decodeURIComponent(one[1]))
      return found ? res(found) : res({ error: 'proposal not found' }, 404)
    }
    return res({ error: 'unexpected ' + url }, 404)
  })
})

const renderPage = (entry = '/proposals') => render(<MemoryRouter initialEntries={[entry]}><Proposals /></MemoryRouter>)
const posts = () => calls.filter(c => c.method === 'POST')
const APPROVE = '/api/proposals/p1/approve'
const OPEN = '/api/proposals?scope=open'

describe('Proposals list', () => {
  it('explains proposals in an empty state', async () => {
    renderPage()
    expect(await screen.findByTestId('proposals-empty')).toBeTruthy()
    expect(screen.getByTestId('proposals-empty').textContent).toMatch(/queued for your approval/i)
  })

  it('shows pending by default and decided under its tab', async () => {
    items = [prop(), prop({ id: 'p2', state: 'done', tool: 'older_tool', decided_at: '2026-09-29T11:00:00Z' })]
    renderPage()
    expect(await screen.findByTestId('proposal-p1')).toBeTruthy()
    expect(screen.queryByTestId('proposal-p2')).toBeNull()
    fireEvent.click(screen.getByRole('tab', { name: /Decided/ }))
    expect(await screen.findByTestId('proposal-p2')).toBeTruthy()
    expect(screen.queryByTestId('proposal-p1')).toBeNull()
  })

  it('shows tool, connection, session link, cron badge, unverified summary label and state', async () => {
    items = [prop({ cron_id: 'cr1' })]
    renderPage()
    const card = await screen.findByTestId('proposal-p1')
    expect(card.textContent).toContain('send_invite')
    expect(card.textContent).toContain('calendar')
    expect(within(card).getByRole('link', { name: /session/i }).getAttribute('href')).toBe('/sessions/s1')
    expect(within(card).getByTestId('proposal-cron-badge')).toBeTruthy()
    expect(within(card).getByTestId('proposal-summary').textContent).toBe('Summary (unverified): Invite Alice')
    expect(within(card).getByTestId('proposal-state').textContent).toBe('pending')
  })

  it('renders the frozen arguments exactly, markup as text', async () => {
    items = [prop()]
    renderPage()
    const args = await screen.findByTestId('proposal-args-p1')
    expect(args.textContent).toBe(JSON.stringify({ to: 'a@example.com', body: '<img src=x onerror=alert(1)>' }, null, 2))
    expect(args.querySelector('img')).toBeNull()
  })

  it('offers to expand long argument values in the card instead of truncating silently', async () => {
    const long = 'x'.repeat(2000)
    items = [prop({ arguments: { text: long } })]
    renderPage()
    const args = await screen.findByTestId('proposal-args-p1')
    expect(args.textContent).toContain('text')
    expect(args.textContent!.length).toBeLessThan(2000)
    fireEvent.click(screen.getByRole('button', { name: /show all/i }))
    expect(screen.getByTestId('proposal-args-p1').textContent).toContain(long)
  })

  it('shows the stored JSON text: big integers, 1.10 and key order are not re-serialised', async () => {
    items = [prop({ arguments: { b: 12345678901234567000, a: 1.1 }, arguments_raw: '{"b": 12345678901234567890, "a": 1.10}' })]
    renderPage()
    const args = await screen.findByTestId('proposal-args-p1')
    expect(args.textContent).toBe('{\n  "b": 12345678901234567890,\n  "a": 1.10\n}')
  })

  it('updates the shared pending count from the list', async () => {
    items = [prop(), prop({ id: 'p3' })]
    renderPage()
    await screen.findByTestId('proposal-p1')
    expect(usePendingProposals.getState().count).toBe(2)
  })

  it('polls the open list every 30 seconds and on focus, but not while the tab is hidden', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    let hidden = false
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
    try {
      renderPage()
      await act(async () => { await vi.advanceTimersByTimeAsync(0) })
      const n = () => calls.filter(c => c.url === OPEN).length
      const before = n()
      await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
      expect(n()).toBe(before + 1)
      await act(async () => { window.dispatchEvent(new Event('focus')); await vi.advanceTimersByTimeAsync(0) })
      expect(n()).toBe(before + 2)
      hidden = true
      await act(async () => { await vi.advanceTimersByTimeAsync(90_000) })
      expect(n()).toBe(before + 2)
    } finally {
      Reflect.deleteProperty(document, 'hidden')
      vi.useRealTimers()
    }
  })

  it('never lets a slow older list response overwrite a newer one', async () => {
    items = [prop()]
    let slow: (v: unknown) => void = () => {}
    let first = true
    action = url => {
      if (url === OPEN && first) { first = false; return new Promise(r => { slow = r }) }
      return null
    }
    renderPage()
    await waitFor(() => expect(calls.filter(c => c.url === OPEN).length).toBe(1))
    items = [prop({ id: 'p9', tool: 'newer_tool' })]
    await act(async () => { window.dispatchEvent(new Event('focus')) })
    expect(await screen.findByTestId('proposal-p9')).toBeTruthy()
    await act(async () => { slow(res({ proposals: [prop()], decided_next: null, pending_count: 1 })) })
    expect(screen.queryByTestId('proposal-p1')).toBeNull()
    expect(screen.getByTestId('proposal-p9')).toBeTruthy()
  })
})

describe('Scopes', () => {
  it('the Pending tab uses scope=open and never the unscoped list', async () => {
    items = [prop()]
    renderPage()
    await screen.findByTestId('proposal-p1')
    expect(calls.map(c => c.url)).toContain(OPEN)
    expect(calls.map(c => c.url)).not.toContain('/api/proposals')
  })

  it('open proposals are not hidden by many newer decided ones', async () => {
    items = [prop({ id: 'old-open', created_at: '2020-01-01T00:00:00Z' }),
      ...Array.from({ length: 120 }, (_, i) => prop({ id: `d${i}`, state: 'done', decided_at: '2026-09-29T11:00:00Z' }))]
    renderPage()
    expect(await screen.findByTestId('proposal-old-open')).toBeTruthy()
  })

  it('the Decided tab fetches scope=decided and loads more by following decided_next', async () => {
    pageSize = 2
    items = [prop(), ...['d1', 'd2', 'd3', 'd4', 'd5'].map(id => prop({ id, state: 'done', tool: `t_${id}` }))]
    renderPage()
    await screen.findByTestId('proposal-p1')
    fireEvent.click(screen.getByRole('tab', { name: /Decided/ }))
    expect(await screen.findByTestId('proposal-d2')).toBeTruthy()
    expect(screen.queryByTestId('proposal-d3')).toBeNull()
    expect(calls.map(c => c.url)).toContain('/api/proposals?scope=decided')
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))
    expect(await screen.findByTestId('proposal-d4')).toBeTruthy()
    expect(calls.map(c => c.url)).toContain('/api/proposals?scope=decided&before=2')
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))
    expect(await screen.findByTestId('proposal-d5')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /load more/i })).toBeNull()
  })

  it('URL-encodes an opaque decided_next cursor', async () => {
    const cursor = '2026-09-29T11:00:00+02:00|a b&c'
    action = url => {
      if (url === '/api/proposals?scope=decided') return res({ proposals: [prop({ id: 'd1', state: 'done' })], decided_next: cursor, pending_count: 0 })
      if (url === `/api/proposals?scope=decided&before=${encodeURIComponent(cursor)}`) return res({ proposals: [prop({ id: 'd2', state: 'done' })], decided_next: null, pending_count: 0 })
      return null
    }
    renderPage()
    fireEvent.click(await screen.findByRole('tab', { name: /Decided/ }))
    fireEvent.click(await screen.findByRole('button', { name: /load more/i }))
    expect(await screen.findByTestId('proposal-d2')).toBeTruthy()
  })
})

describe('Deep link ?id=', () => {
  let scrolled: string[]
  beforeEach(() => {
    scrolled = []
    Element.prototype.scrollIntoView = function scroll(this: Element) { scrolled.push(this.getAttribute('data-testid') ?? '') }
  })

  it('scrolls to and highlights a proposal in the open list', async () => {
    items = [prop({ id: 'p0' }), prop()]
    renderPage('/proposals?id=p1')
    const card = await screen.findByTestId('proposal-p1')
    await waitFor(() => expect(scrolled).toContain('proposal-p1'))
    expect(card.getAttribute('data-focused')).toBe('true')
    expect(screen.getByTestId('proposal-p0').getAttribute('data-focused')).toBeNull()
  })

  it('fetches a decided proposal not in the list and switches to the Decided tab', async () => {
    pageSize = 1
    items = [prop({ id: 'p0' }), prop({ id: 'dnew', state: 'done' }), prop({ id: 'dold', state: 'rejected' })]
    renderPage('/proposals?id=dold')
    const card = await screen.findByTestId('proposal-dold')
    expect(card.getAttribute('data-focused')).toBe('true')
    expect(screen.getByRole('tab', { name: /Decided/ }).getAttribute('aria-selected')).toBe('true')
    expect(calls.map(c => c.url)).toContain('/api/proposals/dold')
    await waitFor(() => expect(scrolled).toContain('proposal-dold'))
  })

  it('says so clearly for an unknown id', async () => {
    items = [prop()]
    renderPage('/proposals?id=nope')
    const msg = await screen.findByTestId('proposal-not-found')
    expect(msg.textContent).toMatch(/nope/)
    expect(msg.textContent).toMatch(/not found/i)
    expect(screen.getByTestId('proposal-p1')).toBeTruthy()
  })
})

describe('Approve', () => {
  it('asks for confirmation naming the tool and connection before sending anything', async () => {
    items = [prop()]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    expect(screen.getByTestId('approve-confirm-p1').textContent).toContain('This will run send_invite on calendar with exactly these arguments')
    expect(posts()).toEqual([])
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('approve-confirm-p1')).toBeNull()
    expect(posts()).toEqual([])
  })

  it('the confirm step shows the complete arguments, untruncated, in a scrollable box', async () => {
    const long = 'y'.repeat(3000)
    items = [prop({ arguments: { text: long, n: 2 } })]
    renderPage()
    const before = await screen.findByTestId('proposal-args-p1')
    expect(before.textContent).not.toContain(long)
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    const after = screen.getByTestId('proposal-args-p1')
    expect(after.textContent).toContain(long)
    expect(after.textContent).not.toMatch(/more characters/)
    expect(screen.queryByRole('button', { name: /show all|collapse long values/i })).toBeNull()
    expect(after.style.maxHeight).not.toBe('')
    expect(after.style.overflowY).toBe('auto')
  })

  it('sends one approve request, shows busy, ignores double clicks, then shows the result as plain text', async () => {
    items = [prop()]
    let finish: (v: unknown) => void = () => {}
    action = url => (url === APPROVE ? new Promise(r => { finish = r }) : null)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    const confirm = screen.getByRole('button', { name: 'Run it' })
    fireEvent.click(confirm)
    fireEvent.click(confirm)
    const busy = await screen.findByRole('button', { name: /Running/ })
    expect((busy as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(busy)
    expect(posts()).toEqual([{ url: APPROVE, method: 'POST', body: undefined }])
    items = [prop({ state: 'done', result: { content: [{ type: 'text', text: '<b>Sent</b> **ok**' }] } })]
    await act(async () => { finish(res(items[0])) })
    fireEvent.click(await screen.findByRole('tab', { name: /Decided/ }))
    const r = await screen.findByTestId('proposal-result-p1')
    expect(r.textContent).toBe('<b>Sent</b> **ok**')
    expect(r.querySelector('b')).toBeNull()
  })

  async function approveRefused(status: number, error: string) {
    items = [prop()]
    action = url => (url === APPROVE ? res({ error }, status) : null)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    fireEvent.click(screen.getByRole('button', { name: 'Run it' }))
    return (await screen.findByTestId('proposal-error-p1')).textContent ?? ''
  }

  it('explains a changed tool and keeps the proposal listed', async () => {
    const msg = await approveRefused(409, "the tool's definition changed (or the server no longer offers it) since the agent proposed this, so it was not run")
    expect(msg).toMatch(/changed/i)
    expect(msg).toMatch(/propose again/i)
    expect(screen.getByRole('button', { name: 'Approve' })).toBeTruthy()
  })

  it('explains an expired proposal', async () => {
    expect(await approveRefused(409, 'this proposal has expired (it is older than 7 days) and can no longer be approved: let the agent propose again')).toMatch(/expired.*propose it again/i)
  })

  it('explains an already decided proposal', async () => {
    expect(await approveRefused(409, 'this proposal is no longer pending (done)')).toMatch(/already decided or is running/i)
  })

  it('explains a connection that is gone', async () => {
    expect(await approveRefused(409, 'the connection no longer exists, so this proposal can no longer be run')).toMatch(/no longer exists/i)
  })

  it('on 502 says nothing was sent and it is back in pending', async () => {
    const msg = await approveRefused(502, 'the MCP server was unreachable')
    expect(msg).toMatch(/nothing was sent/i)
    expect(msg).toMatch(/back in your pending list/i)
  })

  it('tells the person to sign in again on 401', async () => {
    expect(await approveRefused(401, 'x')).toMatch(/sign in again/i)
  })

  it('reports 503 (gateway not configured)', async () => {
    expect(await approveRefused(503, 'x')).toMatch(/not configured/i)
  })
})

describe('Reject', () => {
  it('posts a reject and refreshes', async () => {
    items = [prop()]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    items = [prop({ state: 'rejected' })]
    await waitFor(() => expect(posts()).toEqual([{ url: '/api/proposals/p1/reject', method: 'POST', body: undefined }]))
    await waitFor(() => expect(screen.queryByTestId('proposal-p1')).toBeNull())
  })
})

describe('Unknown outcome', () => {
  it('asks the person to check the target, and resolves with the chosen outcome', async () => {
    items = [prop({ state: 'unknown', result: { error: 'the connection dropped' } })]
    renderPage()
    const card = await screen.findByTestId('proposal-p1')
    expect(card.textContent).toContain('Outcome unknown — check the target service, then mark what happened')
    expect(within(card).queryByRole('button', { name: 'Approve' })).toBeNull()
    fireEvent.click(within(card).getByRole('button', { name: 'Mark failed' }))
    await waitFor(() => expect(posts()).toEqual([{ url: '/api/proposals/p1/resolve', method: 'POST', body: { outcome: 'failed' } }]))
  })

  it('Mark done sends outcome done', async () => {
    items = [prop({ state: 'unknown' })]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Mark done' }))
    await waitFor(() => expect(posts()[0]?.body).toEqual({ outcome: 'done' }))
  })
})

describe('Decided results', () => {
  it('shows a failed error as text', async () => {
    items = [prop({ state: 'failed', result: { error: 'refused <script>' } })]
    renderPage()
    fireEvent.click(await screen.findByRole('tab', { name: /Decided/ }))
    expect((await screen.findByTestId('proposal-result-p1')).textContent).toBe('refused <script>')
  })
})
