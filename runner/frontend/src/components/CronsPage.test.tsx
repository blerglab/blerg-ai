import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import CronsPage from './CronsPage'
import type { CronInfo, CronRunInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function cron(over: Partial<CronInfo> = {}): CronInfo {
  return {
    id: 'c1', name: 'Morning digest', status: 'active', enabled: true, schedule: '30 8 * * *', timezone: 'UTC', prompt: 'Summarise', engine: 'claude',
    model: null, effort: null, runtime: 'auto', daemon_id: null, board_id: null, mcp: [],
    token_expires_at: '2027-06-01T00:00:00Z', grace_seconds: 3600, max_runtime_seconds: 1800,
    next_run_at: '2026-10-02T08:30:00Z', last_run_at: '2026-10-01T08:30:00Z', consecutive_failures: 0, paused_reason: null,
    last_run: { id: 'r1', cron_id: 'c1', scheduled_for: '2026-10-01T08:30:00Z', claimed_at: '', started_at: null, session_id: 's1', status: 'started', reason: null, late: false, manual: false },
    connection_problems: [], created_at: '', updated_at: '', ...over,
  }
}

function ok(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) })
}

let crons: CronInfo[]
let calls: { url: string; method: string; body?: unknown }[]
let runs: CronRunInfo[]
let actionResult: (url: string, method: string) => Promise<unknown> | null

beforeEach(() => {
  crons = []
  runs = []
  calls = []
  actionResult = () => null
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const custom = actionResult(url, method)
    if (custom) return custom
    if (url === '/api/crons' && method === 'GET') return ok({ crons })
    if (/^\/api\/crons\/[^/]+\/runs$/.test(url)) return ok({ runs })
    if (/^\/api\/crons\/[^/]+\/run$/.test(url)) return ok({ id: 'r9', status: 'started', session_id: 's9', manual: true, late: false }, 202)
    if (/^\/api\/crons\/[^/]+(\/(pause|resume|renew))?$/.test(url) && method !== 'GET' && method !== 'DELETE') return ok(crons[0])
    if (method === 'DELETE') return Promise.resolve({ ok: true, status: 204, json: () => Promise.reject(new Error('no body')) })
    if (url === '/api/mcp/connections') return ok({ connections: [] })
    if (url === '/api/daemons') return ok([])
    if (url === '/api/boards') return ok([])
    if (url.startsWith('/api/models/')) return ok({ models: [], source: 'none' })
    return ok({ error: 'unexpected ' + url }, 404)
  })
})

const renderPage = () => render(<MemoryRouter><CronsPage /></MemoryRouter>)
const mutating = () => calls.filter(c => c.method !== 'GET')

describe('CronsPage', () => {
  it('shows an empty state with a way to create the first cron', async () => {
    renderPage()
    expect(await screen.findByTestId('crons-empty')).toHaveTextContent(/no crons yet/i)
    expect(screen.getByRole('button', { name: /new cron/i })).toBeInTheDocument()
  })

  it('lists a cron with its schedule in words, next and last run, status and token expiry', async () => {
    crons = [cron()]
    renderPage()
    const row = await screen.findByTestId('cron-c1')
    expect(within(row).getByText('Morning digest')).toBeInTheDocument()
    expect(row).toHaveTextContent('Every day at 08:30')
    expect(row).toHaveTextContent(/next run/i)
    expect(row).toHaveTextContent(/last run/i)
    expect(within(row).getByTestId('cron-status-c1')).toHaveTextContent('active')
    expect(row).toHaveTextContent(/access expires/i)
    for (const name of [/run now/i, /edit/i, /delete/i, /renew/i]) {
      expect(within(row).getByRole('button', { name })).toBeInTheDocument()
    }
    expect(within(row).getByRole('switch', { name: /enabled/i })).toHaveAttribute('aria-checked', 'true')
  })

  it('shows a paused cron with its reason and resumes it', async () => {
    crons = [cron({ status: 'paused', paused_reason: 'access revoked' })]
    renderPage()
    const row = await screen.findByTestId('cron-c1')
    expect(within(row).getByTestId('cron-status-c1')).toHaveTextContent('paused')
    expect(within(row).getByTestId('cron-paused-reason-c1')).toHaveTextContent('access revoked')
    expect(within(row).queryByRole('button', { name: /run now/i })).toBeNull()
    // A paused cron has no switch: getting out of a pause issues a new token, so it is its own action.
    expect(within(row).queryByRole('switch')).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: /^resume/i }))
    await waitFor(() => expect(mutating()).toEqual([{ url: '/api/crons/c1/resume', method: 'POST' }]))
  })

  it('the enabled switch is a plain toggle: a PATCH, never pause or resume (which revoke and mint tokens)', async () => {
    crons = [cron()]
    renderPage()
    const toggle = await screen.findByRole('switch', { name: /enabled/i })
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(toggle)
    await waitFor(() => expect(mutating()).toEqual([{ url: '/api/crons/c1', method: 'PATCH', body: { enabled: false } }]))
  })

  it('shows a disabled cron as off, not scheduled, and switches it back on with a PATCH', async () => {
    crons = [cron({ status: 'disabled', enabled: false })]
    renderPage()
    const row = await screen.findByTestId('cron-c1')
    expect(within(row).getByTestId('cron-status-c1')).toHaveTextContent('disabled')
    expect(row).toHaveTextContent('Next run: not scheduled')
    const toggle = within(row).getByRole('switch', { name: /enabled/i })
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle)
    await waitFor(() => expect(mutating()).toEqual([{ url: '/api/crons/c1', method: 'PATCH', body: { enabled: true } }]))
  })

  it('renews the token and runs now', async () => {
    crons = [cron()]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /renew/i }))
    await waitFor(() => expect(mutating()).toContainEqual({ url: '/api/crons/c1/renew', method: 'POST' }))
    fireEvent.click(screen.getByRole('button', { name: /run now/i }))
    await waitFor(() => expect(mutating()).toContainEqual({ url: '/api/crons/c1/run', method: 'POST' }))
    expect(await screen.findByTestId('cron-notice')).toHaveTextContent(/started/i)
  })

  it('asks in the page before deleting, and only then deletes', async () => {
    crons = [cron()]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /^delete/i }))
    const confirm = await screen.findByTestId('cron-delete-confirm-c1')
    expect(confirm).toHaveTextContent(/revokes/i)
    expect(mutating()).toEqual([])
    fireEvent.click(within(confirm).getByRole('button', { name: /cancel/i }))
    expect(screen.queryByTestId('cron-delete-confirm-c1')).toBeNull()
    expect(mutating()).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: /^delete/i }))
    crons = []
    fireEvent.click(within(await screen.findByTestId('cron-delete-confirm-c1')).getByRole('button', { name: /confirm delete/i }))
    await waitFor(() => expect(mutating()).toEqual([{ url: '/api/crons/c1', method: 'DELETE' }]))
    await screen.findByTestId('crons-empty')
  })

  it('shows the server refusal of an action', async () => {
    crons = [cron()]
    actionResult = (url) => (url.endsWith('/renew') ? ok({ error: 'a run of this cron is still held' }, 409) : null)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /renew/i }))
    expect(await screen.findByTestId('crons-error')).toHaveTextContent('a run of this cron is still held')
  })

  it('warns about an expired token and a connection that needs attention', async () => {
    crons = [cron({ status: 'expired', connection_problems: ['calendar needs to be signed in again'] })]
    renderPage()
    const row = await screen.findByTestId('cron-c1')
    expect(within(row).getByTestId('cron-status-c1')).toHaveTextContent('expired')
    expect(within(row).getByTestId('cron-token-c1')).toHaveTextContent(/expired/i)
    expect(within(row).getByTestId('cron-problems-c1')).toHaveTextContent('calendar needs to be signed in again')
  })

  it('shows the run history with the ran-late marker', async () => {
    crons = [cron()]
    runs = [{ id: 'r1', cron_id: 'c1', scheduled_for: '2026-10-01T08:30:00Z', claimed_at: '', started_at: '2026-10-01T20:00:00Z', session_id: 's1', status: 'started', reason: null, late: true, manual: false }]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /history/i }))
    expect(await screen.findByTestId('cron-run-r1')).toHaveTextContent(/ran late/i)
  })

  it('opens the form for a new cron and returns to the list after saving', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /new cron/i }))
    expect(await screen.findByTestId('cron-form')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(screen.queryByTestId('cron-form')).toBeNull()
  })

  it('opens the form prefilled for an edit', async () => {
    crons = [cron()]
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /^edit/i }))
    await screen.findByTestId('cron-form')
    expect((document.getElementById('cron-name') as HTMLInputElement).value).toBe('Morning digest')
  })

  it('says when crons are not enabled', async () => {
    actionResult = (url, method) => (url === '/api/crons' && method === 'GET' ? ok({ error: 'crons are not enabled on this server' }, 503) : null)
    renderPage()
    expect(await screen.findByTestId('crons-error')).toHaveTextContent('crons are not enabled')
  })
})
