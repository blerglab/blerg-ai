// Card #144: "+ Add" opens a plugin browser: the engine's marketplaces and plugins with a search and
// an Install per plugin (the existing install path), a fallback to the add form for an engine with no
// catalog, and "Add marketplace" as a secondary action. MCP rows that need a sign-in get Connect
// (claude.ai connectors), Sign in (host sessions) or a note (sandboxed sessions).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import PluginBrowser from './PluginBrowser'
import { DisallowedPlugins } from './SessionPlugins'
import { useSessionPlugins } from '../hooks/useSessionPlugins'
import McpSignIn from './McpSignIn'
import type { SessionPluginSettings } from '../lib/sessionPlugins'
import { parseCatalog, safeAuthURL, validMarketplaceInput } from '../lib/pluginCatalog'
import { decodeLabel, hostToUnicode } from '../lib/punycode'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const reply = (body: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => body })

const settings: SessionPluginSettings = {
  engine: 'claude', supported: true, session_apply: true,
  allowed_marketplaces: ['example-org/plugins'], any_marketplace: false, plugins: [], requests: [],
}

const CATALOG = {
  engine: 'claude',
  marketplaces: [
    { name: 'fixture-market', source: 'example-org/plugins' },
    { name: 'local-market' },
    { name: 'other-market', source: 'someone/else' },
  ],
  plugins: [
    { name: 'release-notes', marketplace: 'fixture-market', description: 'Writes release notes <img src=x onerror=alert(1)>.', version: '1.2.0', author: 'Example Org' },
    { name: 'lint-helper', marketplace: 'fixture-market', description: 'Lints the diff.', installed: true },
    { name: 'already-on', marketplace: 'fixture-market', installed: true, enabled: true },
    { name: 'local-one', marketplace: 'local-market' },
    { name: 'elsewhere', marketplace: 'other-market' },
    { name: 'orphan', marketplace: 'not-listed' },
  ],
  truncated: false,
}

function model(overrides: Partial<SessionPluginSettings> = {}) {
  return { sessionId: 's1', data: { ...settings, ...overrides }, error: null, refresh: vi.fn(async () => {}) }
}

function show(m = model()) {
  const onClose = vi.fn()
  render(<MemoryRouter><PluginBrowser model={m} onClose={onClose} /></MemoryRouter>)
  return { onClose, m }
}

beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (url === '/api/sessions/s1/plugins/catalog') return reply(CATALOG)
    if (url === '/api/sessions/s1/plugins' && init?.method === 'POST') return reply({ id: 'p1', state: 'executing', result: { state: 'queued' } }, 202)
    return reply({}, 404)
  })
})

describe('the plugin browser', () => {
  it('lists every plugin of the known marketplaces, as text, with search', async () => {
    show()
    const list = await screen.findByTestId('plugin-browser-list')
    const rows = within(list).getAllByTestId('plugin-browser-row')
    expect(rows.map(r => r.querySelector('.pb-name')?.textContent)).toEqual(['release-notes', 'lint-helper', 'already-on', 'local-one', 'elsewhere'])
    expect(screen.getByText(/Writes release notes <img/)).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByTestId('plugin-browser-count')).toHaveTextContent('5')
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search plugins' }), { target: { value: 'lints' } })
    expect(within(list).getAllByTestId('plugin-browser-row')).toHaveLength(1)
    expect(screen.getByTestId('plugin-browser-count')).toHaveTextContent('1 of 5')
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search plugins' }), { target: { value: 'nothing-like-this' } })
    expect(screen.getByTestId('plugin-browser-no-match')).toBeInTheDocument()
  })

  it('installs through the existing path, scoped to the session, and says how it applies', async () => {
    const { m } = show()
    await screen.findByTestId('plugin-browser-list')
    fireEvent.click(screen.getByRole('button', { name: 'Install release-notes' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'release-notes', scope: 'session' }),
    })))
    expect(await screen.findByText('Queued until foreground and background work are idle.')).toBeInTheDocument()
    expect(m.refresh).toHaveBeenCalled()
    // Always on, by choice.
    fireEvent.change(screen.getByLabelText('Install for'), { target: { value: 'always' } })
    fireEvent.click(screen.getByRole('button', { name: 'Install lint-helper' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins', expect.objectContaining({
      body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'lint-helper', scope: 'always' }),
    })))
  })

  it('cannot install what this install does not allow, nor what is already on', async () => {
    show()
    await screen.findByTestId('plugin-browser-list')
    const local = screen.getByRole('button', { name: 'Install local-one' })
    const other = screen.getByRole('button', { name: 'Install elsewhere' })
    const on = screen.getByRole('button', { name: 'Install already-on' })
    expect(local).toBeDisabled()
    expect(local).toHaveAccessibleDescription('This local marketplace cannot be installed here.')
    expect(within(local.parentElement!).getByText('This local marketplace cannot be installed here.')).toBeVisible()
    expect(within(other.parentElement!).getByText('This marketplace is not allowed by this install.')).toBeVisible()
    expect(within(on.parentElement!).getByText('Already enabled in this session.')).toBeVisible()
    expect(other).toBeDisabled()
    expect(other.getAttribute('title')).toContain('not allowed')
    expect(on).toBeDisabled()
    fireEvent.click(other)
    expect(apiFetch.mock.calls.filter(c => c[1]?.method === 'POST')).toHaveLength(0)
    // State glyphs, with words for a screen reader.
    const states = screen.getAllByTestId('plugin-state').map(s => s.getAttribute('title'))
    expect(states).toEqual(['Installed, not enabled here', 'Enabled in this session'])
  })

  it.each(['session', 'always'])('installs an allowed native Codex marketplace for %s', async scope => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => url.endsWith('/catalog')
      ? reply({ engine: 'codex', marketplaces: [{ name: 'openai-curated', source: 'codex:openai-curated' }], plugins: [{ name: 'selected', marketplace: 'openai-curated' }] })
      : init?.method === 'POST' ? reply({ id: 'p1', result: { state: 'queued' } }, 202) : reply({}))
    show(model({ engine: 'codex', allowed_marketplaces: ['codex:openai-curated'] }))
    const install = await screen.findByRole('button', { name: 'Install selected' })
    fireEvent.change(screen.getByLabelText('Install for'), { target: { value: scope } })
    expect(install).toBeEnabled()
    fireEvent.click(install)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins', expect.objectContaining({
      body: JSON.stringify({ marketplace: 'codex:openai-curated', plugin: 'selected', scope }),
    })))
  })

  it('shows a native account refusal below its disabled action', async () => {
    apiFetch.mockImplementation(() => reply({ engine: 'codex', marketplaces: [{ name: 'openai-curated', source: 'codex:openai-curated' }], plugins: [{ name: 'selected', marketplace: 'openai-curated', install_reason: 'Codex does not allow this plugin for your account.' }] }))
    show(model({ engine: 'codex', any_marketplace: true }))
    const install = await screen.findByRole('button', { name: 'Install selected' })
    expect(install).toBeDisabled()
    expect(install).toHaveAccessibleDescription('Codex does not allow this plugin for your account.')
    expect(within(install.parentElement!).getByText('Codex does not allow this plugin for your account.')).toBeVisible()
  })

  it('allows removing a managed plugin while Codex refuses future installs', async () => {
    apiFetch.mockImplementation(() => reply({ engine: 'codex', marketplaces: [{ name: 'openai-curated', source: 'codex:openai-curated' }], plugins: [{ name: 'selected', marketplace: 'openai-curated', managed: true, enabled: true, install_reason: 'Codex does not allow this plugin for your account.' }] }))
    show(model({ engine: 'codex', any_marketplace: true }))
    expect(await screen.findByRole('button', { name: 'Remove selected' })).toBeEnabled()
    fireEvent.change(screen.getByLabelText('Install for'), { target: { value: 'always' } })
    expect(screen.getByRole('button', { name: 'Install selected' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Install selected' })).toHaveAccessibleDescription('Codex does not allow this plugin for your account.')
  })

  it('offers only always-on when the session cannot load plugins live', async () => {
    show(model({ session_apply: false, reason: 'This engine loads plugin changes in new sessions.' }))
    await screen.findByTestId('plugin-browser-list')
    expect(screen.getByRole('option', { name: 'This session only' })).toBeDisabled()
    expect(screen.getByLabelText('Install for')).toHaveValue('always')
    expect(screen.getByText('This engine loads plugin changes in new sessions.')).toBeInTheDocument()
  })

  it('can save an enabled Codex plugin as always on without live support', async () => {
    show(model({ engine: 'codex', session_apply: false }))
    await screen.findByTestId('plugin-browser-list')
    const install = screen.getByRole('button', { name: 'Install already-on' })
    expect(install).toBeEnabled()
    fireEvent.click(install)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins', expect.objectContaining({
      body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'already-on', scope: 'always' }),
    })))
  })

  it('removes a managed Codex plugin after its marketplace is revoked', async () => {
    apiFetch.mockImplementation((url: string, init?: RequestInit) => url.endsWith('/catalog')
      ? reply({ ...CATALOG, plugins: [{ name: 'selected', marketplace: 'fixture-market', installed: true, enabled: true, managed: true }] })
      : init?.method === 'POST' ? reply({ id: 'p1', result: { state: 'queued' } }, 202) : reply({}))
    show(model({ engine: 'codex', allowed_marketplaces: [] }))
    expect(await screen.findByText('Marketplace no longer allowed. Existing plugins are kept.')).toBeVisible()
    expect(await screen.findByRole('button', { name: 'Remove selected' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: 'Remove selected' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins', expect.objectContaining({
      body: JSON.stringify({ marketplace: 'example-org/plugins', plugin: 'selected', scope: 'session', action: 'remove' }),
    })))
  })

  it('refreshes completed add → remove → add without closing, including delayed application', async () => {
    let enabled = false
    let pending: { id: string; kind: string; state: string; result: { state: string } } | null = null
    let request = 0
    apiFetch.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith('/catalog')) return reply({ ...CATALOG, engine: 'codex', plugins: [{ name: 'selected', marketplace: 'fixture-market', installed: true, enabled, managed: enabled }] })
      if (init?.method === 'POST') {
        pending = { id: `p${++request}`, kind: 'plugin_request', state: 'executing', result: { state: 'queued' } }
        return reply(pending, 202)
      }
      return reply({ ...settings, engine: 'codex', plugins: enabled ? [{ marketplace: 'example-org/plugins', plugin: 'selected' }] : [], requests: pending ? [pending] : [] })
    })
    let refresh: () => Promise<void> = async () => {}
    function LiveBrowser() {
      const m = useSessionPlugins('s1', true)
      refresh = m.refresh
      return <PluginBrowser model={m} onClose={() => {}} />
    }
    render(<MemoryRouter><LiveBrowser /></MemoryRouter>)
    for (const action of ['add', 'remove', 'add']) {
      const label = action === 'add' ? 'Install selected' : 'Remove selected'
      fireEvent.click(await screen.findByRole('button', { name: label }))
      await waitFor(() => expect(apiFetch.mock.calls.filter(c => c[1]?.method === 'POST')).toHaveLength(request))
      // The server accepts while a foreground turn is still active.
      await screen.findByText('Queued until foreground and background work are idle.')
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument()
      enabled = action === 'add'
      pending = { id: `p${request}`, kind: 'plugin_request', state: 'done', result: { state: 'applied' } }
      await refresh()
      await screen.findByRole('button', { name: enabled ? 'Remove selected' : 'Install selected' })
      expect(apiFetch.mock.calls.filter(c => c[0].endsWith('/catalog')).length).toBeGreaterThan(request)
    }
  })

  it('offers the launch sheet after saving a plugin for the next session', async () => {
    apiFetch.mockImplementation((url: string) => url.endsWith('/catalog') ? reply(CATALOG)
      : reply({ id: 'p1', result: { state: 'new_sessions', message: 'This plugin loads when a session starts — added for your next session.' } }, 202))
    const opened = vi.fn()
    window.addEventListener('blerg:open-launch-sheet', opened)
    try {
      const { onClose } = show(model({ engine: 'codex', session_apply: false }))
      fireEvent.click(await screen.findByRole('button', { name: 'Install release-notes' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Start a new session with it' }))
      expect(onClose).toHaveBeenCalled()
      expect(opened).toHaveBeenCalledOnce()
    } finally { window.removeEventListener('blerg:open-launch-sheet', opened) }
  })

  it('falls back to the add form for an engine without a catalog', async () => {
    apiFetch.mockImplementation(() => reply({ code: 'unsupported', error: 'none' }, 409))
    show()
    expect(await screen.findByTestId('plugin-browser-fallback')).toHaveTextContent('This engine has no plugin catalog here')
    expect(screen.getByLabelText('Plugin name')).toBeInTheDocument()
    expect(screen.queryByTestId('plugin-browser-list')).toBeNull()
  })

  it('adds a marketplace from owner/repo or a GitHub https URL only', async () => {
    show()
    await screen.findByTestId('plugin-browser-list')
    const input = screen.getByLabelText('Add marketplace')
    const add = screen.getByRole('button', { name: 'Add' })
    for (const bad of ['http://github.com/a/b', 'git@github.com:a/b.git', 'https://gitlab.com/a/b', '-x/y', 'just-words']) {
      fireEvent.change(input, { target: { value: bad } })
      expect(add).toBeDisabled()
    }
    apiFetch.mockImplementation((url: string) => url.endsWith('/plugins/marketplaces')
      ? reply({ ...CATALOG, marketplaces: [...CATALOG.marketplaces, { name: 'added', source: 'example-org/added' }], plugins: [...CATALOG.plugins, { name: 'new-one', marketplace: 'added' }] })
      : reply(CATALOG))
    fireEvent.change(input, { target: { value: 'https://github.com/example-org/added.git' } })
    fireEvent.click(add)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/plugins/marketplaces', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ source: 'https://github.com/example-org/added.git' }),
    })))
    expect(await screen.findByText('new-one')).toBeInTheDocument()
  })

  it('says when the catalog is truncated, and that marketplaces are shared', async () => {
    apiFetch.mockImplementation(() => reply({ ...CATALOG, truncated: true }))
    show()
    expect(await screen.findByTestId('plugin-browser-truncated')).toHaveTextContent('Not every plugin is listed')
    expect(screen.getByText("Marketplaces are shared by everyone using this machine's engine.")).toBeInTheDocument()
  })

  it('closes on Escape', async () => {
    const { onClose } = show()
    await screen.findByTestId('plugin-browser-list')
    fireEvent.keyDown(screen.getByTestId('plugin-browser'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})

describe('catalog and URL validation', () => {
  it('drops malformed catalog entries', () => {
    const c = parseCatalog({ marketplaces: [{ name: 'm' }, { name: 5 }], plugins: [{ name: 'a', marketplace: 'm' }, { name: 'b', marketplace: 'x' }, { marketplace: 'm' }] })
    expect(c?.plugins.map(p => p.name)).toEqual(['a'])
    expect(parseCatalog({ plugins: 'no' })).toBeNull()
  })
  it('opens only https sign-in links', () => {
    expect(safeAuthURL('https://auth.example/a?b=c')).toBe('https://auth.example/a?b=c')
    for (const bad of ['http://auth.example/a', 'javascript:alert(1)', 'https://u:p@auth.example/', 'data:text/html,x', 7]) expect(safeAuthURL(bad)).toBeNull()
  })
  it('decodes punycode hosts', () => {
    expect(decodeLabel('mnchen-3ya')).toBe('münchen')
    expect(hostToUnicode('xn--80ak6aa92e.com')).toBe('\u0430\u0440\u0440\u04cf\u0435.com')
    expect(hostToUnicode('auth.example')).toBe('auth.example')
    expect(hostToUnicode('xn--!!.example')).toBe('xn--!!.example')
  })
  it('accepts owner/repo and GitHub https URLs as marketplaces', () => {
    expect(validMarketplaceInput('example-org/plugins')).toBe(true)
    expect(validMarketplaceInput('https://github.com/example-org/plugins')).toBe(true)
    expect(validMarketplaceInput('https://github.com/example-org/plugins?x=1')).toBe(false)
  })
})

describe('MCP sign-in rows', () => {
  const row = (source: string) => ({ name: source === 'claude.ai' ? 'claude.ai Gmail' : 'tracker', status: 'needs-auth' as const, source })

  it('connects a claude.ai connector on claude.ai, in a new tab, with the restart note', () => {
    render(<McpSignIn item={row('claude.ai')} ctx={{ sessionId: 's1', runtime: 'daemon' }} />)
    const link = screen.getByRole('link', { name: /Connect claude.ai Gmail/ })
    expect(link).toHaveAttribute('href', 'https://claude.ai/customize/connectors')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link.getAttribute('rel')).toContain('noopener')
    expect(screen.getByText('After connecting, restart the session to load it.')).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it('signs in to a server from the user settings: shows the host, then confirms server, source and host', async () => {
    apiFetch.mockImplementation(() => reply({ auth_url: 'https://auth.example/authorize?state=s', requires_user_action: true, callback_expected: true, source: 'user' }))
    render(<McpSignIn item={row('user')} ctx={{ sessionId: 's1', machine: 'build-box' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/mcp/sign-in', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ server: 'tracker' }),
    })))
    const host = await screen.findByTestId('caps-mcp-host')
    expect(host).toHaveTextContent('Opens auth.example')
    expect(host.querySelector('strong')?.textContent).toBe('auth.example')
    expect(screen.queryByTestId('caps-mcp-idn')).toBeNull()
    // No link leaves Blerg before the confirm.
    expect(screen.queryByRole('link')).toBeNull()
    const opener = screen.getByRole('button', { name: 'Open the sign-in for tracker at auth.example' })
    fireEvent.click(opener)
    const confirm = screen.getByRole('alertdialog', { name: 'Sign in to tracker at auth.example?' })
    expect(confirm).toBe(screen.getByTestId('caps-mcp-confirm'))
    expect(document.activeElement).toBe(confirm)
    expect(confirm).toHaveTextContent('Defined in your user config on build-box.')
    const open = within(confirm).getByTestId('caps-mcp-open-signin')
    expect(open).toHaveAttribute('href', 'https://auth.example/authorize?state=s')
    expect(open).toHaveAttribute('target', '_blank')
    expect(open.getAttribute('aria-label')).toContain('auth.example')
    expect(screen.getByRole('status')).toHaveTextContent('on the machine that runs this session')
    fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByTestId('caps-mcp-confirm')).toBeNull()
    // Focus goes back to where it was.
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open the sign-in for tracker at auth.example' })))
  })

  it('shows a non-default port as part of the host', async () => {
    apiFetch.mockImplementation(() => reply({ auth_url: 'https://auth.example:8443/a', requires_user_action: true, source: 'user' }))
    render(<McpSignIn item={row('user')} ctx={{ sessionId: 's1' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    expect((await screen.findByTestId('caps-mcp-host')).querySelector('strong')?.textContent).toBe('auth.example:8443')
    fireEvent.click(screen.getByRole('button', { name: 'Open the sign-in for tracker at auth.example:8443' }))
    expect(screen.getByRole('alertdialog')).toHaveTextContent('on this machine')
  })

  it('shows a Cyrillic look-alike host as the ASCII the browser visits, with a warning', async () => {
    // "аррӏе.com" in Cyrillic: it would read as apple.com if shown decoded.
    apiFetch.mockImplementation(() => reply({ auth_url: 'https://xn--80ak6aa92e.com/oauth', requires_user_action: true, callback_expected: true, source: 'user' }))
    render(<McpSignIn item={row('codex')} ctx={{ sessionId: 's1' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    const host = await screen.findByTestId('caps-mcp-host')
    expect(host.querySelector('strong')?.textContent).toBe('xn--80ak6aa92e.com')
    expect(host.textContent).not.toContain('\u0430\u0440\u0440')
    const warn = screen.getByTestId('caps-mcp-idn')
    expect(warn).toHaveTextContent('This address uses international characters; check it carefully.')
    expect(warn.querySelector('small')?.textContent).toBe('\u0430\u0440\u0440\u04cf\u0435.com')
    fireEvent.click(screen.getByRole('button', { name: 'Open the sign-in for tracker at xn--80ak6aa92e.com' }))
    const confirm = screen.getByRole('alertdialog')
    expect(confirm).toHaveTextContent('Sign in to tracker at xn--80ak6aa92e.com?')
    expect(confirm).toHaveTextContent('Defined in your global Codex config on this machine')
    expect(confirm).toHaveTextContent('its address uses international characters')
  })

  it.each([
    ['project', 'it was defined by the project'],
    ['local', 'it was defined by the project'],
    ['dynamic', 'it was defined by the project'],
    ['agent', 'it was defined by the project'],
    ['unknown', 'it was defined by the project'],
    ['something-new', 'it was defined by the project'],
    ['plugin', 'it came with a plugin'],
    ['managed', 'it is managed by your organization'],
  ])('offers no sign-in link for a %s server', (source, why) => {
    render(<McpSignIn item={row(source)} ctx={{ sessionId: 's1' }} />)
    expect(screen.getByTestId('caps-mcp-terminal')).toHaveTextContent(`Sign in to this server from the terminal (${why})`)
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('turns a server the daemon finds untrusted into the terminal note', async () => {
    apiFetch.mockImplementation(() => reply({ error: 'Sign in to this server from the terminal.', code: 'untrusted' }, 403))
    render(<McpSignIn item={row('codex')} ctx={{ sessionId: 's1' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    expect(await screen.findByTestId('caps-mcp-terminal')).toHaveTextContent('Sign in to this server from the terminal')
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('never opens a URL that is not https', async () => {
    apiFetch.mockImplementation(() => reply({ auth_url: 'javascript:alert(1)', requires_user_action: true }))
    render(<McpSignIn item={row('user')} ctx={{ sessionId: 's1' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('did not return a usable sign-in link')
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('says the server error, and that sandboxed sign-in is coming', async () => {
    apiFetch.mockImplementation(() => reply({ error: 'This server does not need a sign-in now.', code: 'not_found' }, 404))
    const view = render(<McpSignIn item={row('user')} ctx={{ sessionId: 's1' }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in to tracker' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('does not need a sign-in now')
    view.unmount()
    render(<McpSignIn item={row('user')} ctx={{ sessionId: 's1', runtime: 'docker' }} />)
    expect(screen.getByTestId('caps-mcp-sandboxed')).toHaveTextContent('Sign-in for sandboxed sessions is coming.')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows nothing for a server that does not need a sign-in', () => {
    const { container } = render(<McpSignIn item={{ name: 'ok', status: 'connected', source: 'user' }} ctx={{ sessionId: 's1' }} />)
    expect(container).toBeEmptyDOMElement()
  })
})

it('flags installed session plugins after their marketplace is disabled without removing them', async () => {
  show(model({ allowed_marketplaces: [] }));
  const list = await screen.findByTestId('plugin-browser-list');
  const row = within(list).getAllByTestId('plugin-browser-row').find(r => r.textContent?.includes('already-on'))!;
  expect(within(row).getByText('Marketplace no longer allowed. Existing plugins are kept.')).toBeVisible();
  expect(within(row).getByRole('button', { name: 'Install already-on' })).toBeDisabled();
  expect(apiFetch.mock.calls.filter(([,init]) => init?.method === 'POST')).toHaveLength(0);
});

it('flags disallowed saved entries in the session list even without a catalog', () => {
  render(<DisallowedPlugins model={model({ plugins: [{ marketplace: 'codex:old', plugin: 'retained' }], allowed_marketplaces: [] })} />)
  expect(screen.getByRole('region', { name: 'Disallowed saved plugins' })).toHaveTextContent('retained: marketplace no longer allowed')
  expect(apiFetch).not.toHaveBeenCalled()
})
