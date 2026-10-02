import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
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

  it('saves the pod limits in seconds and shows a refusal reason', async () => {
    const calls: { url: string; body?: string }[] = []
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      calls.push({ url, body: init?.body as string | undefined })
      if (url === '/api/cluster/settings') {
        return Promise.resolve({ ok: false, status: 403, json: () => Promise.resolve({ error: 'missing capability: account.manage' }) })
      }
      if (url === '/api/cluster/status') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({
          configured: true, active_sessions: 0, available_engines: [], pod_idle_timeout_seconds: 86400, pod_ttl_seconds: 604800,
        }) })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }))
    render(<ClusterStatus />)
    const idle = await screen.findByLabelText(/End a pod after idle/)
    fireEvent.change(idle, { target: { value: '12' } })
    fireEvent.click(screen.getByText('Save'))
    await screen.findByText('missing capability: account.manage')
    await waitFor(() => {
      const put = calls.find(c => c.url === '/api/cluster/settings')
      expect(JSON.parse(put!.body!)).toEqual({ pod_idle_timeout_seconds: 43200, pod_ttl_seconds: 604800 })
    })
  })

  function capFetch(status: Record<string, unknown>, put: { ok: boolean; status: number; body: Record<string, unknown> }) {
    const calls: { url: string; body?: string }[] = []
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      calls.push({ url, body: init?.body as string | undefined })
      if (url === '/api/cluster/settings') {
        return Promise.resolve({ ok: put.ok, status: put.status, json: () => Promise.resolve(put.body) })
      }
      if (url === '/api/cluster/status') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(status) })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }))
    return () => calls.filter(c => c.url === '/api/cluster/settings').map(c => JSON.parse(c.body!))
  }
  const capStatus = {
    configured: true, active_sessions: 3, available_engines: [], max_sessions: 4, max_sessions_default: 4,
    cpu_request: '500m', mem_request: '1Gi', pod_idle_timeout_seconds: 86400, pod_ttl_seconds: 604800,
  }

  it('shows the effective session cap, its default, live usage and what a pod costs', async () => {
    capFetch({ ...capStatus, max_sessions: 6 }, { ok: true, status: 200, body: {} })
    render(<ClusterStatus />)
    const field = await screen.findByLabelText('Max concurrent session pods')
    expect(field).toHaveValue(6)
    expect(screen.getByText('default 4')).toBeInTheDocument()
    expect(screen.getByText('3 in use')).toBeInTheDocument()
    expect(screen.getByText(/about 0\.5 CPU and 1 GiB/)).toBeInTheDocument()
    expect(screen.getByText('Use default')).toBeInTheDocument()
  })

  it('offers no Use default while the cap is the default', async () => {
    capFetch(capStatus, { ok: true, status: 200, body: {} })
    render(<ClusterStatus />)
    expect(await screen.findByLabelText('Max concurrent session pods')).toHaveValue(4)
    expect(screen.queryByText('Use default')).not.toBeInTheDocument()
  })

  it('saves a changed session cap with the other limits', async () => {
    const puts = capFetch(capStatus, { ok: true, status: 200, body: { ...capStatus, max_sessions: 8 } })
    render(<ClusterStatus />)
    fireEvent.change(await screen.findByLabelText('Max concurrent session pods'), { target: { value: '8' } })
    fireEvent.click(screen.getByText('Save'))
    await screen.findByLabelText('Max concurrent session pods')
    await waitFor(() => expect(puts()).toEqual([{ pod_idle_timeout_seconds: 86400, pod_ttl_seconds: 604800, max_sessions: 8 }]))
  })

  it('announces the saved settings so the sidebar pill can follow without a reload', async () => {
    capFetch(capStatus, { ok: true, status: 200, body: { ...capStatus, max_sessions: 12 } })
    const heard: unknown[] = []
    const listen = (e: Event) => heard.push((e as CustomEvent).detail)
    window.addEventListener('blerg:cluster-settings-saved', listen)
    try {
      render(<ClusterStatus />)
      fireEvent.change(await screen.findByLabelText('Max concurrent session pods'), { target: { value: '12' } })
      fireEvent.click(screen.getByText('Save'))
      await waitFor(() => expect(heard).toHaveLength(1))
      expect(heard[0]).toMatchObject({ max_sessions: 12 })
    } finally {
      window.removeEventListener('blerg:cluster-settings-saved', listen)
    }
  })

  it('does not send max_sessions when it was not changed', async () => {
    const puts = capFetch(capStatus, { ok: true, status: 200, body: capStatus })
    render(<ClusterStatus />)
    await screen.findByLabelText('Max concurrent session pods')
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(puts()).toEqual([{ pod_idle_timeout_seconds: 86400, pod_ttl_seconds: 604800 }]))
  })

  it('Use default clears the override on Save', async () => {
    const puts = capFetch({ ...capStatus, max_sessions: 9 }, { ok: true, status: 200, body: capStatus })
    render(<ClusterStatus />)
    const field = await screen.findByLabelText('Max concurrent session pods')
    fireEvent.click(screen.getByText('Use default'))
    expect(field).toHaveValue(4)
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(puts()).toEqual([{ pod_idle_timeout_seconds: 86400, pod_ttl_seconds: 604800, max_sessions: 0 }]))
  })

  it.each(['0', '65', '2.5', ''])('refuses the session cap %j without calling the server', async v => {
    const puts = capFetch(capStatus, { ok: true, status: 200, body: capStatus })
    render(<ClusterStatus />)
    fireEvent.change(await screen.findByLabelText('Max concurrent session pods'), { target: { value: v } })
    fireEvent.click(screen.getByText('Save'))
    expect(await screen.findByText(/whole number from 1 to 64/)).toBeInTheDocument()
    expect(puts()).toEqual([])
  })

  it('shows the refusal when a non-administrator saves the session cap', async () => {
    capFetch(capStatus, { ok: false, status: 403, body: { error: 'missing capability: account.manage' } })
    render(<ClusterStatus />)
    fireEvent.change(await screen.findByLabelText('Max concurrent session pods'), { target: { value: '8' } })
    fireEvent.click(screen.getByText('Save'))
    await screen.findByText('missing capability: account.manage')
  })

  it('draws the page when the server reports no engines as null (a cluster with no operator credentials)', async () => {
    vi.stubGlobal('fetch', mockFetch({
      configured: true,
      namespace: 'blerg-runner-sessions',
      image: 'reg/blerg-runner-agent:latest',
      max_sessions: 4,
      active_sessions: 0,
      available_engines: null,
    }))
    render(<ClusterStatus />)
    await screen.findByText('blerg-runner-sessions')
    expect(screen.getByText('Claude —')).toBeInTheDocument()
    expect(screen.getByText('Codex —')).toBeInTheDocument()
  })
})
