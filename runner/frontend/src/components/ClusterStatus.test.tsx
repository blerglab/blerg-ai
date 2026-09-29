import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import ClusterStatus from './ClusterStatus'

function mockFetch(clusterStatus: Record<string, unknown>, sessions: Record<string, unknown>[] = []) {
  return vi.fn((url: string) => {
    if (url === '/api/cluster/status') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(clusterStatus) })
    }
    if (url.startsWith('/api/sessions')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
  })
}

describe('ClusterStatus', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows the not-configured empty state when cluster runtime is off', async () => {
    vi.stubGlobal('fetch', mockFetch({ configured: false, active_sessions: 0, available_engines: [] }))
    render(<ClusterStatus />)
    await screen.findByText('Not configured')
  })

  it('shows configuration, engine availability, and running sessions when configured', async () => {
    vi.stubGlobal('fetch', mockFetch(
      {
        configured: true,
        namespace: 'blerg-runner-sessions',
        image: 'reg/blerg-runner-agent:latest',
        daemon_id: '00000000-0000-4000-8000-00000000f00d',
        max_sessions: 4,
        active_sessions: 1,
        available_engines: ['claude', 'codex'],
        secret_name: 'blerg-runner-agent',
      },
      [
        { id: 's1', status: 'running', repo: 'org/repo', title: 'Fix bug', engine: 'codex', started_at: '2026-01-01T00:00:00Z' },
      ],
    ))
    render(<ClusterStatus />)
    await screen.findByText('blerg-runner-sessions')
    expect(screen.getByText('reg/blerg-runner-agent:latest')).toBeInTheDocument()
    expect(screen.getByText('Claude ✓')).toBeInTheDocument()
    expect(screen.getByText('Codex ✓')).toBeInTheDocument()
    expect(screen.getByText('Hermes —')).toBeInTheDocument()
    expect(screen.getByText('Fix bug')).toBeInTheDocument()
    expect(screen.getByText(/org\/repo · Codex/)).toBeInTheDocument()
    expect(screen.getByText('1 active Job of 4 max')).toBeInTheDocument()
  })

  it('shows the git chip as configured when the operator Secret carries a git token', async () => {
    vi.stubGlobal('fetch', mockFetch({
      configured: true, active_sessions: 0, available_engines: ['claude'], git_configured: true,
    }))
    render(<ClusterStatus />)
    expect(await screen.findByText('Git: configured')).toBeInTheDocument()
  })

  it('shows the git chip as not configured when no shared git token is present', async () => {
    vi.stubGlobal('fetch', mockFetch({
      configured: true, active_sessions: 0, available_engines: ['claude'], git_configured: false,
    }))
    render(<ClusterStatus />)
    expect(await screen.findByText('Git: not configured')).toBeInTheDocument()
  })

  it('shows an RBAC hint when active_sessions is -1', async () => {
    vi.stubGlobal('fetch', mockFetch({
      configured: true, active_sessions: -1, available_engines: [],
    }))
    render(<ClusterStatus />)
    await screen.findByText(/Active Job count unavailable/)
  })
})
