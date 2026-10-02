import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import CronRuns from './CronRuns'
import type { CronRunInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function run(over: Partial<CronRunInfo>): CronRunInfo {
  return {
    id: 'r1', cron_id: 'c1', scheduled_for: '2026-09-01T08:00:00Z', claimed_at: '2026-09-01T08:00:01Z',
    started_at: '2026-09-01T08:00:05Z', session_id: 's1', status: 'started', reason: null, late: false, manual: false, ...over,
  }
}

function serve(runs: CronRunInfo[] | { status: number; error: string }) {
  apiFetch.mockImplementation((url: string) => {
    expect(url).toBe('/api/crons/c1/runs')
    if (Array.isArray(runs)) return Promise.resolve({ ok: true, json: () => Promise.resolve({ runs }) })
    return Promise.resolve({ ok: false, status: runs.status, json: () => Promise.resolve({ error: runs.error }) })
  })
}

const renderRuns = () => render(<MemoryRouter><CronRuns cronId="c1" /></MemoryRouter>)

beforeEach(() => { apiFetch.mockReset() })

describe('CronRuns', () => {
  it('says so when the cron has never run', async () => {
    serve([])
    renderRuns()
    expect(await screen.findByTestId('cron-runs-empty')).toHaveTextContent(/no runs yet/i)
  })

  it('marks a run that started late', async () => {
    serve([run({ id: 'r1', late: true }), run({ id: 'r2', late: false, scheduled_for: '2026-08-31T08:00:00Z' })])
    renderRuns()
    await screen.findByTestId('cron-run-r1')
    expect(screen.getByTestId('cron-run-r1')).toHaveTextContent(/ran late/i)
    expect(screen.getByTestId('cron-run-r2')).not.toHaveTextContent(/ran late/i)
  })

  it('links a started run to its session', async () => {
    serve([run({ id: 'r1', session_id: 'sess-9' })])
    renderRuns()
    const link = await screen.findByRole('link', { name: /open session/i })
    expect(link).toHaveAttribute('href', '/sessions/sess-9')
  })

  it('shows why a run was skipped, held or failed, and marks manual runs', async () => {
    serve([
      run({ id: 'a', status: 'skipped', reason: 'previous run still active', session_id: null, started_at: null }),
      run({ id: 'b', status: 'held', reason: 'no daemon connected', session_id: null, started_at: null }),
      run({ id: 'c', status: 'failed', reason: 'connection needs to be signed in again', session_id: null, manual: true }),
    ])
    renderRuns()
    await screen.findByTestId('cron-run-a')
    expect(screen.getByTestId('cron-run-a')).toHaveTextContent('skipped')
    expect(screen.getByTestId('cron-run-a')).toHaveTextContent('previous run still active')
    expect(screen.getByTestId('cron-run-b')).toHaveTextContent('held')
    expect(screen.getByTestId('cron-run-b')).toHaveTextContent('no daemon connected')
    expect(screen.getByTestId('cron-run-c')).toHaveTextContent('failed')
    expect(screen.getByTestId('cron-run-c')).toHaveTextContent(/manual/i)
    expect(screen.queryByRole('link', { name: /open session/i })).toBeNull()
  })

  it('shows the server error', async () => {
    serve({ status: 503, error: 'crons are not enabled on this server' })
    renderRuns()
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('crons are not enabled'))
  })
})
