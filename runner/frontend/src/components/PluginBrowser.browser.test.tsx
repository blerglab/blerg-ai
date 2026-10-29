// Card #144 in real Chromium, from the session panel's Capabilities tab: at 3840 px (fixed panel)
// and 390 px (phone drawer). "+ Add" opens a plugin browser that uses the space (a large panel on a
// 4K screen, the whole screen on a phone) with the engine's plugins, a search and an Install per
// plugin; the MCP rows that need a sign-in get Connect (claude.ai connector) and Sign in (host).
// With VITE_CAPS_SHOT_DIR set it also saves screenshots.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '../index.css'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  redirectToRefresh: vi.fn(),
}))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_CAPS_SHOT_DIR

const CAPS = {
  seq: 3,
  ts: new Date().toISOString(),
  capabilities: {
    engine: 'claude', engine_name: 'Claude Code', version: '2.1.308', model: 'claude-opus-5-5', cwd: '/home/dev/repos/widget',
    groups: [
      { id: 'plugins', label: 'Plugins', items: [{ name: 'frontend-design', status: 'enabled', source: 'user' }] },
      { id: 'mcp', label: 'MCP servers', items: [
        { name: 'issues', status: 'connected', detail: '18 tools', source: 'user' },
        { name: 'tracker', status: 'needs-auth', source: 'user' },
        { name: 'claude.ai Gmail', status: 'needs-auth', source: 'claude.ai' },
        { name: 'repo-server', status: 'needs-auth', source: 'project' },
      ] },
    ],
  },
}

const PLUGINS = { engine: 'claude', supported: true, session_apply: true, allowed_marketplaces: ['example-org/plugins'], any_marketplace: false, plugins: [], requests: [] }

const CATALOG = {
  engine: 'claude',
  marketplaces: [{ name: 'fixture-market', source: 'example-org/plugins', description: 'Fixture plugins' }],
  plugins: Array.from({ length: 40 }, (_, i) => ({
    name: i === 0 ? 'release-notes' : `plugin-${String(i).padStart(2, '0')}`,
    marketplace: 'fixture-market',
    description: i === 0 ? 'Writes release notes from the merged pull requests since the last tag, grouped by area, with links to each change.' : `Does task ${i} for the team.`,
    version: '1.0.' + i, author: 'Example Org', category: i % 2 ? 'productivity' : 'development',
    installed: i === 3, enabled: i === 3,
  })),
  truncated: false,
}

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.clear()
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (url.endsWith('/capabilities')) return json(CAPS)
    if (url.endsWith('/plugins/catalog')) return json(CATALOG)
    if (url.endsWith('/mcp/sign-in')) return json({ auth_url: 'https://auth.example/authorize?state=fixture', requires_user_action: true, callback_expected: true, source: 'user' })
    if (url.endsWith('/plugins') && init?.method === 'POST') return json({ id: 'p1', state: 'executing', result: { state: 'queued' } }, 202)
    if (url.endsWith('/plugins')) return json(PLUGINS)
    return json({})
  })
})

const box = (el: Element) => el.getBoundingClientRect()

describe.each([
  { width: 390, height: 800, mode: 'drawer' as const },
  { width: 3840, height: 1600, mode: 'panel' as const },
])('plugin browser and MCP sign-in at $width px ($mode)', ({ width, height, mode }) => {
  beforeEach(async () => { await page.viewport(width, height) })

  async function openTab() {
    render(
      <div style={{ height: '100vh', display: 'flex', justifyContent: 'flex-end' }}>
        <MemoryRouter>
          <SessionRail id="rail" session={makeSession({ runtime: 'daemon' })} live={makeLive({ canReleasePrompt: true })} toolChanges={0}
            mode={mode} open onClose={() => {}} onOpenArtifact={() => {}} />
        </MemoryRouter>
      </div>,
    )
    await userEvent.click(screen.getByRole('tab', { name: /^Capabilities/ }))
    await screen.findByText('frontend-design')
    return mode === 'drawer' ? screen.getByRole('dialog', { name: 'Session panel' }) : screen.getByTestId('session-rail')
  }

  it.each(['session', 'always'])('installs a native Codex plugin for %s and displays disabled reasons', async scope => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/capabilities')) return json(CAPS)
      if (url.endsWith('/plugins/catalog')) return json({ engine: 'codex', marketplaces: [{ name: 'openai-curated', source: 'codex:openai-curated' }, { name: 'blocked', source: 'codex:blocked' }], plugins: [
        { name: 'native-plugin', marketplace: 'openai-curated' },
        { name: 'account-blocked', marketplace: 'openai-curated', install_reason: 'Codex does not allow this plugin for your account.' },
        { name: 'policy-blocked', marketplace: 'blocked' },
      ] })
      if (url.endsWith('/plugins') && init?.method === 'POST') return json({ id: 'native', result: { state: 'queued' } }, 202)
      if (url.endsWith('/plugins')) return json({ ...PLUGINS, engine: 'codex', allowed_marketplaces: ['codex:openai-curated'] })
      return json({})
    })
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    const install = await within(dialog).findByRole('button', { name: 'Install native-plugin' })
    const select = within(dialog).getByRole('combobox', { name: 'Install for' }) as HTMLSelectElement
    select.value = scope
    select.dispatchEvent(new Event('change', { bubbles: true }))
    for (const [name, reason] of [
      ['account-blocked', 'Codex does not allow this plugin for your account.'],
      ['policy-blocked', 'This marketplace is not allowed by this install.'],
    ]) {
      const button = within(dialog).getByRole('button', { name: `Install ${name}` })
      expect(button).toBeDisabled()
      const line = within(button.parentElement!).getByText(reason)
      expect(line).toBeVisible()
      expect(box(line).top).toBeGreaterThanOrEqual(box(button).bottom)
      expect(box(line).right).toBeLessThanOrEqual(box(dialog).right)
    }
    expect(install).toBeEnabled()
    await userEvent.click(install)
    expect(apiFetch).toHaveBeenCalledWith(expect.stringMatching(/\/plugins$/), expect.objectContaining({ body: JSON.stringify({ marketplace: 'codex:openai-curated', plugin: 'native-plugin', scope }) }))
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('keeps revoked managed plugins removable while blocking new installs', async () => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/capabilities')) return json(CAPS)
      if (url.endsWith('/plugins/catalog')) return json({ engine: 'codex', marketplaces: [{ name: 'revoked', source: 'codex:revoked' }], plugins: [
        { name: 'retained', marketplace: 'revoked', managed: true, installed: true, enabled: true },
        { name: 'new-plugin', marketplace: 'revoked' },
      ] })
      if (url.endsWith('/plugins') && init?.method === 'POST') return json({ id: 'remove', result: { state: 'queued' } }, 202)
      if (url.endsWith('/plugins')) return json({ ...PLUGINS, engine: 'codex', allowed_marketplaces: [] })
      return json({})
    })
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    const remove = await within(dialog).findByRole('button', { name: 'Remove retained' })
    expect(remove).toBeEnabled()
    expect(within(dialog).getByText('Marketplace no longer allowed. Existing plugins are kept.')).toBeVisible()
    expect(within(dialog).getByRole('button', { name: 'Install new-plugin' })).toBeDisabled()
    await userEvent.click(remove)
    expect(apiFetch).toHaveBeenCalledWith(expect.stringMatching(/\/plugins$/), expect.objectContaining({ body: JSON.stringify({ marketplace: 'codex:revoked', plugin: 'retained', scope: 'session', action: 'remove' }) }))
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it.each([
    { code: '', sessionApply: true, scope: 'session' },
    { code: '', sessionApply: true, scope: 'always' },
    { code: 'sandboxed', sessionApply: false, scope: 'always' },
    { code: 'unsupported', sessionApply: true, scope: 'session' },
  ])('adds native Codex by name for $scope (catalog fallback: $code)', async ({ code, sessionApply, scope }) => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/capabilities')) return json(CAPS)
      if (url.endsWith('/plugins/catalog')) return code ? json({ code }, 409) : json(CATALOG)
      if (url.endsWith('/plugins') && init?.method === 'POST') return json({ id: 'native', result: { state: sessionApply ? 'queued' : 'new_sessions' } }, 202)
      if (url.endsWith('/plugins')) return json({ ...PLUGINS, engine: 'codex', session_apply: sessionApply, allowed_marketplaces: ['codex:openai-curated'] })
      return json({})
    })
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    if (!code) await userEvent.click(await within(dialog).findByRole('button', { name: 'Enter a plugin by name' }))
    const name = await within(dialog).findByLabelText('Plugin name')
    await userEvent.type(name, 'helper')
    const select = within(dialog).getByLabelText('Apply plugin') as HTMLSelectElement
    select.value = scope
    select.dispatchEvent(new Event('change', { bubbles: true }))
    const add = within(dialog).getByRole('button', { name: scope === 'session' ? 'Add to session' : 'Save always on' })
    expect(add).toBeEnabled()
    await userEvent.click(add)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(expect.stringMatching(/\/plugins$/), expect.objectContaining({
      body: JSON.stringify({ marketplace: 'codex:openai-curated', plugin: 'helper', scope }),
    })))
    expect(await within(dialog).findByRole('status')).toHaveTextContent(sessionApply ? 'Queued until' : 'Saved for new sessions')
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('explains native source rejection for Claude next to the by-name action', async () => {
    apiFetch.mockImplementation((url: string) => {
      if (url.endsWith('/capabilities')) return json(CAPS)
      if (url.endsWith('/plugins/catalog')) return json({ code: 'unsupported' }, 409)
      if (url.endsWith('/plugins')) return json({ ...PLUGINS, allowed_marketplaces: ['codex:openai-curated'] })
      return json({})
    })
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    await userEvent.type(await within(dialog).findByLabelText('Plugin name'), 'helper')
    const add = within(dialog).getByRole('button', { name: 'Add to session' })
    expect(add).toBeDisabled()
    expect(add).toHaveAccessibleDescription('This marketplace requires Codex.')
    const reason = within(dialog).getByText('This marketplace requires Codex.')
    expect(reason).toBeVisible()
    expect(box(reason).top).toBeGreaterThanOrEqual(box(add).bottom)
    expect(box(reason).right).toBeLessThanOrEqual(box(dialog).right)
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
  })

  it('saves an enabled Codex plugin as always on when live loading is unavailable', async () => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/capabilities')) return json(CAPS)
      if (url.endsWith('/plugins/catalog')) return json({ ...CATALOG, engine: 'codex' })
      if (url.endsWith('/plugins') && init?.method === 'POST') return json({ id: 'p1', result: { state: 'new_sessions', message: 'This plugin loads when a session starts — added for your next session.' } }, 202)
      if (url.endsWith('/plugins')) return json({ ...PLUGINS, engine: 'codex', session_apply: false, reason: 'This engine loads plugins when a session starts — added for your next session.' })
      return json({})
    })
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    const install = await within(dialog).findByRole('button', { name: 'Install plugin-03' })
    expect(install).toBeEnabled()
    expect(within(dialog).getByRole('option', { name: 'This session only' })).toBeDisabled()
    await userEvent.click(install)
    expect(await within(dialog).findByRole('button', { name: 'Start a new session with it' })).toBeVisible()
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    expect(apiFetch).toHaveBeenCalledWith(expect.stringMatching(/\/plugins$/), expect.objectContaining({ body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'plugin-03', scope: 'always' }) }))
  })

  it('"+ Add" opens a browser that uses the space, searches, and installs through the existing path', async () => {
    await openTab()
    await userEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a plugin' })
    const list = await within(dialog).findByTestId('plugin-browser-list')
    expect(within(list).getAllByTestId('plugin-browser-row')).toHaveLength(40)
    const d = box(dialog)
    if (width === 390) {
      expect(Math.round(d.width)).toBe(390)
      expect(Math.round(d.height)).toBe(height)
    } else {
      // Much wider than the 340 px panel, within the screen.
      expect(d.width).toBeGreaterThan(2000)
      expect(d.right).toBeLessThanOrEqual(width)
      // The rows flow into columns rather than stretching one line across 4K.
      const rows = within(list).getAllByTestId('plugin-browser-row')
      expect(new Set(rows.slice(0, 6).map(r => Math.round(box(r).left))).size).toBeGreaterThan(1)
    }
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
    if (shotDir) await page.screenshot({ path: `${shotDir}/plugin-browser-${width}.png`, element: dialog })

    const search = within(dialog).getByRole('searchbox', { name: 'Search plugins' })
    expect(document.activeElement).toBe(search)
    await userEvent.type(search, 'release')
    await waitFor(() => expect(within(list).getAllByTestId('plugin-browser-row')).toHaveLength(1))
    const install = within(dialog).getByRole('button', { name: 'Install release-notes' })
    expect(box(install).width).toBeGreaterThanOrEqual(24)
    expect(box(install).height).toBeGreaterThanOrEqual(24)
    await userEvent.click(install)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/plugins', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'release-notes', scope: 'session' }),
    })))
    expect(await within(dialog).findByText('Queued until foreground and background work are idle.')).toBeTruthy()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add a plugin' })).toBeNull())
  })

  it('gives "!" MCP rows Connect and Sign in that fit the panel', async () => {
    const rail = await openTab()
    const connect = screen.getByRole('link', { name: /Connect claude.ai Gmail/ })
    expect(connect.getAttribute('href')).toBe('https://claude.ai/customize/connectors')
    expect(connect.getAttribute('target')).toBe('_blank')
    expect(screen.getByText('After connecting, restart the session to load it.')).toBeTruthy()
    const signIn = screen.getByRole('button', { name: 'Sign in to tracker' })
    await userEvent.click(signIn)
    expect((await screen.findByTestId('caps-mcp-host')).textContent).toContain('auth.example')
    await userEvent.click(screen.getByRole('button', { name: 'Open the sign-in for tracker at auth.example' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(document.activeElement).toBe(confirm)
    const open = await screen.findByTestId('caps-mcp-open-signin')
    expect(open.getAttribute('href')).toBe('https://auth.example/authorize?state=fixture')
    expect(open.getAttribute('rel')).toContain('noopener')
    // A server the project defined: no link, the terminal note.
    expect(within(rail).getAllByTestId('caps-mcp-terminal')[0].textContent).toContain('defined by the project')
    const r = box(rail)
    for (const el of [connect, open]) {
      expect(box(el).right).toBeLessThanOrEqual(r.right)
      expect(box(el).height).toBeGreaterThanOrEqual(24)
    }
    if (shotDir) await page.screenshot({ path: `${shotDir}/mcp-signin-${width}.png`, element: rail })
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
})
