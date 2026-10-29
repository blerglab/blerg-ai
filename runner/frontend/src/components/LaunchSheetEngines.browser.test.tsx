import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { clearLaunchCache } from '../lib/launchCache'
import { BUILTIN_MODELS } from '../lib/engineModels'
import { useSessionStore } from '../hooks/useSessionStore'
import LaunchSheet from './LaunchSheet'
import { clearEngineNotes } from '../lib/engineStatus'
import '../index.css'

// Engine discovery after install: the launch sheet lists every known engine, the unavailable ones
// disabled with the server's reason and exact fix, and Check again / Re-detect PATH act at once.

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'test-token', redirectToRefresh: vi.fn() }))
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
const entry = (name: string, kind = 'repository', path = name) => ({ name, kind, path, source_identity: `source:${path}` })
const checks: { url: string; body: Record<string, unknown> }[] = []
let codexLoggedIn = false
afterEach(() => { clearEngineNotes(); checks.length = 0; codexLoggedIn = false; cleanup(); vi.unstubAllGlobals(); clearLaunchCache(); useSessionStore.setState({ sessions: [] }); localStorage.clear() })

const UNAVAILABLE = () => [
  ...(codexLoggedIn ? [] : [{ id: 'codex', name: 'Codex', status: 'not_logged_in', reason: 'Codex is installed on Desktop but not logged in.', fix: 'Run `codex login` on Desktop, then Check again.', action: 'check_again', daemon_id: 'desktop' }]),
  { id: 'hermes', name: 'Hermes', status: 'not_installed', reason: "Hermes is installed at ~/.nvm/versions/node/v22.11.0/bin/hermes on Desktop, but that folder isn't on the daemon's PATH.",
    fix: "Re-detect PATH to give the daemon your login shell's PATH. If it's still missing, add that folder to PATH in your shell profile, then Re-detect PATH.", action: 'redetect_path', daemon_id: 'desktop',
    found_at: '~/.nvm/versions/node/v22.11.0/bin/hermes', searched_path: ['/usr/local/bin', '/usr/bin', '/bin', '~/.local/bin'] },
  { id: 'openclaw', name: 'OpenClaw', status: 'misconfigured', reason: "OpenClaw on Desktop isn't set up yet.", fix: 'Run `openclaw onboard` on Desktop, then Check again.', action: 'check_again', daemon_id: 'desktop' },
]

async function open(width: number, height: number) {
  document.documentElement.dataset.theme = 'dark'
  useSessionStore.setState({ sessions: [] })
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    if (url.startsWith('/api/daemons/') && url.endsWith('/engine-check')) {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>
      checks.push({ url, body })
      if (!body.redetect_path) codexLoggedIn = true
      return json({ available_engines: ['claude', ...(codexLoggedIn ? ['codex'] : [])], path_added: body.redetect_path ? ['~/.nvm/versions/node/v22.11.0/bin'] : [],
        engine_statuses: [{ engine: 'codex', status: codexLoggedIn ? 'available' : 'not_logged_in', reason: '' }, { engine: 'hermes', status: 'not_logged_in', reason: 'Hermes is installed on Desktop but not logged in.' }] })
    }
    if (url === '/api/repos') return json({ repos: [] })
    if (url === '/api/daemons') return json([{ id: 'desktop', name: 'Desktop', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true, available_engines: ['claude'], discovery_version: 1, ownership_revision: 1, connection_generation: 'current', workspace_capabilities: { manifest_version: 2, linux_host: true, sandbox: true } }])
    if (url === '/api/cluster/status') return json({ configured: false })
    if (url === '/api/me/credentials') return json({ engines: [], git: true })
    if (url.startsWith('/api/runner/launch-options?')) return json({ state: 'known', engines: [{ id: 'claude', name: 'Claude', restricted: true }, ...(codexLoggedIn ? [{ id: 'codex', name: 'Codex', restricted: true }] : [])], unavailable: UNAVAILABLE() })
    if (url.startsWith('/api/models/claude')) return json({ models: BUILTIN_MODELS.claude })
    if (url.startsWith('/api/models/codex')) return json({ models: BUILTIN_MODELS.codex })
    if (url === '/api/session-boards') return json({ configured: false, boards: [] })
    if (url === '/api/mcp/connections') return json({ connections: [] })
    if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: { root_identity: 'root', root: entry('Repositories', 'directory', '.'), entries: [entry('harbor')], truncated: false } })
    return json({})
  }))
  await page.viewport(width, height)
  render(<MemoryRouter><LaunchSheet open onClose={() => {}} /></MemoryRouter>)
  fireEvent.click(await screen.findByTestId('runtime-daemon'))
  return screen.findByTestId('engines-unavailable')
}

async function capture(path: string, width: number, height: number) {
  const { base64 } = await page.screenshot({ path, base64: true })
  const bytes = Uint8Array.from(atob(base64), char => char.charCodeAt(0))
  const png = new DataView(bytes.buffer)
  expect(png.getUint32(16)).toBe(width)
  expect(png.getUint32(20)).toBe(height)
}

it('lists unavailable engines disabled, with their reason and exact fix', async () => {
  const list = await open(1440, 900)
  for (const id of ['codex', 'hermes', 'openclaw']) {
    const pill = within(list).getByTestId(`engine-unavailable-${id}`)
    expect(pill).toBeDisabled()
  }
  const codex = within(list).getByTestId('engine-status-codex')
  expect(codex.textContent).toContain('Codex is installed on Desktop but not logged in.')
  expect(within(codex).getByText('codex login').tagName).toBe('CODE')
  expect(within(codex).queryByRole('button', { name: 'Re-detect PATH' })).toBeNull()
  const hermes = within(list).getByTestId('engine-status-hermes')
  expect(within(hermes).getByRole('button', { name: 'Re-detect PATH' })).toBeEnabled()
  expect(hermes.querySelector('details')!.textContent).toContain('/usr/local/bin:/usr/bin:/bin:~/.local/bin')
  // An unavailable engine is never selectable as the launch engine.
  expect(screen.queryByTestId('engine-codex')).toBeNull()
})

it('Check again re-detects at once and the logged-in engine becomes selectable', async () => {
  const list = await open(1440, 900)
  fireEvent.click(within(list).getByTestId('engine-check-codex'))
  await screen.findByTestId('engine-codex')
  expect(checks).toEqual([{ url: '/api/daemons/desktop/engine-check', body: { redetect_path: false } }])
  await waitFor(() => expect(screen.queryByTestId('engine-unavailable-codex')).toBeNull())
  expect(screen.getByTestId('engine-unavailable-hermes')).toBeDisabled()
})

it('Re-detect PATH reports the folders it added and what still blocks the engine', async () => {
  const list = await open(1440, 900)
  fireEvent.click(within(list).getByTestId('engine-redetect-hermes'))
  // The sheet re-reads what is offered after the check; the outcome stays on the row.
  await waitFor(() => expect(screen.getByTestId('engine-status-hermes').textContent).toContain("Added ~/.nvm/versions/node/v22.11.0/bin to the daemon’s PATH."))
  expect(screen.getByTestId('engine-status-hermes').textContent).toContain('Still unavailable: Hermes is installed on Desktop but not logged in.')
  expect(checks[0].body).toEqual({ redetect_path: true })
})

it.each([[390, 844], [3840, 2160]])('fits the unavailable engines at %ix%i', async (width, height) => {
  const list = await open(width, height)
  const panel = document.querySelector('.launch-panel')!
  expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
  for (const row of within(list).getAllByRole('listitem')) expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  if (width > 900) expect(panel.scrollHeight).toBeLessThanOrEqual(panel.clientHeight + 1)
  const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
  if (dir) {
    list.scrollIntoView({ block: 'center' })
    await capture(`${dir}/engines-unavailable-${width}x${height}.png`, width, height)
  }
})
