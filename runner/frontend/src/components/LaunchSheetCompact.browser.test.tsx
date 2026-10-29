import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { clearLaunchCache } from '../lib/launchCache'
import { BUILTIN_MODELS } from '../lib/engineModels'
import { useSessionStore } from '../hooks/useSessionStore'
import LaunchSheet from './LaunchSheet'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'test-token', redirectToRefresh: vi.fn() }))
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
const entry = (name: string, kind = 'repository', path = name) => ({ name, kind, path, source_identity: `source:${path}` })
const launchBodies: Record<string, unknown>[] = []
const repositories = ['atlas', 'beacon', 'canopy', 'drift', 'ember', 'fern', 'grove', 'harbor', 'island', 'juniper', 'kite', 'lantern']
afterEach(() => { delete document.documentElement.dataset.theme; launchBodies.length = 0; cleanup(); vi.unstubAllGlobals(); clearLaunchCache(); useSessionStore.setState({ sessions: [] }); localStorage.clear() })
async function open(runtime: 'daemon' | 'cluster', width = 1440, height = 900, theme = 'light') {
  document.documentElement.dataset.theme = theme
  clearLaunchCache(); localStorage.clear()
  useSessionStore.setState({ sessions: [{ id: 'previous', repo: 'example-org/harbor', daemon_id: 'desktop', project_path: '/repos/harbor', title: 'Earlier work', status: 'idle', started_at: '2026-01-01T00:00:00Z', runtime }] })
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    if (url === '/api/sessions' && init?.method === 'POST') { launchBodies.push(JSON.parse(String(init.body))); return json({ session_id: 'launched' }) }
    if (url === '/api/repos') return json({ repos: repositories.map(name => ({ name, full_name: `example-org/${name}`, provider: 'github', checked_out: ['Desktop'] })) })
    if (url === '/api/daemons') return json([{ id: 'desktop', name: 'Desktop', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true, available_engines: ['claude', 'codex'], discovery_version: 1, ownership_revision: 1, connection_generation: 'current', workspace_capabilities: { manifest_version: 2, linux_host: true, sandbox: true } }])
    if (url === '/api/cluster/status') return json({ configured: true, available_engines: ['claude', 'codex'], git_configured: true, workspace_capabilities: { manifest_version: 2, cluster: true } })
    if (url === '/api/me/credentials') return json({ engines: ['claude', 'codex'], git: true })
    if (url.startsWith('/api/runner/launch-options?')) return json({ state: 'known', engines: [{ id: 'claude', name: 'Claude', restricted: true }, { id: 'codex', name: 'Codex', restricted: true }] })
    if (url.startsWith('/api/models/claude')) return json({ models: BUILTIN_MODELS.claude })
    if (url.startsWith('/api/models/codex')) return json({ models: BUILTIN_MODELS.codex })
    if (url === '/api/session-boards') return json({ configured: true, boards: [{ id: 'board', name: 'Product' }] })
    if (url === '/api/mcp/connections') return json({ connections: [] })
    if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: { root_identity: 'root', root: entry('Repositories', 'directory', '.'), entries: new URL(url, location.origin).searchParams.get('path') === '.' ? [entry('apps', 'directory'), ...repositories.map(name => entry(name))] : [entry('web', 'repository', 'apps/web')], truncated: false } })
    if (url === '/api/workspace/preview') {
      const body = JSON.parse(String(init?.body))
      return json({ ok: true, selection: body.selection, members: body.selection.members.map((m: { path: string }) => ({ name: m.path.split('/').at(-1) })) })
    }
    return json({})
  }))
  await page.viewport(width, height)
  render(<MemoryRouter><LaunchSheet open onClose={() => {}} /></MemoryRouter>)
  fireEvent.click(await screen.findByTestId(`runtime-${runtime}`))
  await screen.findByRole('region', { name: 'Workspace repositories' })
  await screen.findByRole('checkbox', { name: /harbor/ })
}


async function capture(path: string, width: number, height: number) {
  const { base64 } = await page.screenshot({ path, base64: true })
  const bytes = Uint8Array.from(atob(base64), char => char.charCodeAt(0))
  const png = new DataView(bytes.buffer)
  expect(png.getUint32(16)).toBe(width)
  expect(png.getUint32(20)).toBe(height)
}

it.each(['daemon', 'cluster'] as const)('puts search, actions, selection and compact recent rows first for %s', async runtime => {
  await open(runtime)
  const picker = screen.getByRole('region', { name: 'Workspace repositories' })
  const search = within(picker).getByRole('textbox', { name: 'Search repositories' })
  const create = within(picker).getByRole('button', { name: 'New repository' })
  const empty = within(picker).getByRole('button', { name: 'Empty workspace' })
  const tree = within(picker).getByRole(runtime === 'daemon' ? 'tree' : 'group', { name: runtime === 'daemon' ? 'Configured folder tree' : 'Hosted repositories' })
  expect(search.compareDocumentPosition(create) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(create.getBoundingClientRect().top).toBe(empty.getBoundingClientRect().top)
  expect(empty.getBoundingClientRect().bottom).toBeLessThan(tree.getBoundingClientRect().top)
  const checkboxes = within(tree).getAllByRole('checkbox')
  expect(checkboxes[runtime === 'daemon' ? 1 : 0].closest('label')!.textContent).toContain('harbor')
  expect(checkboxes[runtime === 'daemon' ? 1 : 0].closest('label')!.getBoundingClientRect().height).toBeLessThanOrEqual(32)
  expect(within(tree).queryByText('Git')).toBeNull()
  fireEvent.click(within(tree).getByRole('checkbox', { name: /harbor/ }))
  await screen.findByRole('button', { name: runtime === 'daemon' ? 'Remove harbor' : 'Remove example-org/harbor' })
  expect((screen.getByRole('textbox', { name: 'Session name' }) as HTMLInputElement).value).toBe('harbor')
  fireEvent.click(empty)
  await waitFor(() => expect(screen.queryByRole('button', { name: /Remove / })).toBeNull())
  expect(screen.getByText('0 / 20')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Empty workspace' }).getAttribute('aria-pressed')).toBe('true')
  fireEvent.click(screen.getByRole('button', { name: 'New repository' }))
  expect(await screen.findByPlaceholderText(runtime === 'daemon' ? 'Search repos, or owner/name or a URL...' : 'org/name')).toBeVisible()
})

it('searches loaded folders without fetching the entire filesystem and navigates with arrow keys', async () => {
  await open('daemon')
  const search = screen.getByRole('textbox', { name: 'Search repositories' })
  fireEvent.click(screen.getByRole('button', { name: 'Expand apps' }))
  await screen.findByRole('checkbox', { name: /web/ })
  fireEvent.change(search, { target: { value: 'apps/web' } })
  const tree = screen.getByRole('tree', { name: 'Configured folder tree' })
  expect(within(tree).queryByRole('checkbox', { name: /atlas/ })).toBeNull()
  fireEvent.keyDown(search, { key: 'ArrowDown' })
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' })
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' })
  expect(document.activeElement).toBe(within(tree).getByRole('treeitem', { name: /web/ }))
  await userEvent.keyboard(' ')
  await screen.findByRole('button', { name: 'Remove web' })
  fireEvent.change(search, { target: { value: '' } })
  const checkbox = within(tree).getByRole('treeitem', { name: /apps.*Folder/ })
  checkbox.focus(); fireEvent.keyDown(checkbox, { key: 'ArrowLeft' })
  expect(within(tree).queryByRole('checkbox', { name: /web/ })).toBeNull()
  fireEvent.keyDown(checkbox, { key: 'ArrowRight' })
  expect(within(tree).getByRole('checkbox', { name: /web/ })).toBeTruthy()
})

it('allows collapse and expansion during search and restores the earlier expansion on clearing', async () => {
  await open('daemon')
  const search = screen.getByRole('textbox', { name: 'Search repositories' })
  fireEvent.click(screen.getByRole('button', { name: 'Expand apps' }))
  await screen.findByRole('checkbox', { name: /web/ })
  fireEvent.change(search, { target: { value: 'apps/web' } })
  fireEvent.click(screen.getByRole('button', { name: 'Collapse apps' }))
  expect(screen.queryByRole('checkbox', { name: /web/ })).toBeNull()
  expect(screen.getByRole('button', { name: 'Expand apps' })).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(screen.getByRole('button', { name: 'Expand apps' }))
  expect(screen.getByRole('checkbox', { name: /web/ })).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Collapse apps' }))
  fireEvent.change(search, { target: { value: '' } })
  expect(screen.getByRole('checkbox', { name: /web/ })).toBeVisible()

  // A previously collapsed, loaded branch is opened by search, then restored.
  fireEvent.click(screen.getByRole('button', { name: 'Collapse apps' }))
  fireEvent.change(search, { target: { value: 'apps/web' } })
  expect(screen.getByRole('checkbox', { name: /web/ })).toBeVisible()
  fireEvent.change(search, { target: { value: '' } })
  expect(screen.queryByRole('checkbox', { name: /web/ })).toBeNull()
})

it('exposes a checked tree with levels, nested groups and one keyboard tab stop', async () => {
  await open('daemon')
  const tree = screen.getByRole('tree', { name: 'Configured folder tree' })
  expect(tree).toHaveAttribute('aria-multiselectable', 'true')
  const root = within(tree).getByRole('treeitem', { name: 'Repositories Folder' })
  const apps = within(tree).getByRole('treeitem', { name: 'apps Folder' })
  const harbor = within(tree).getByRole('treeitem', { name: 'harbor Git repository' })
  expect(root).toHaveAttribute('aria-level', '1')
  expect(apps).toHaveAttribute('aria-level', '2')
  expect(root).toHaveAttribute('aria-expanded', 'true')
  expect(apps).toHaveAttribute('aria-expanded', 'false')
  expect(root.querySelector(':scope > [role="group"]')).toContainElement(apps)
  const tabStops = () => [...tree.querySelectorAll('[tabindex="0"]')]
  expect(tabStops()).toEqual([root])
  fireEvent.keyDown(screen.getByRole('textbox', { name: 'Search repositories' }), { key: 'ArrowDown' })
  expect(root).toHaveFocus()
  fireEvent.keyDown(root, { key: 'ArrowDown' })
  expect(harbor).toHaveFocus()
  expect(tabStops()).toEqual([harbor])
  await userEvent.keyboard(' ')
  await screen.findByRole('button', { name: 'Remove harbor' })
  expect(harbor).toHaveAttribute('aria-checked', 'true')
  expect(root).toHaveAttribute('aria-checked', 'mixed')
  await userEvent.keyboard(' ')
  await waitFor(() => expect(harbor).toHaveAttribute('aria-checked', 'false'))

  apps.focus()
  fireEvent.keyDown(apps, { key: 'ArrowRight' })
  const web = await within(tree).findByRole('treeitem', { name: 'web Git repository' })
  expect(web).toHaveAttribute('aria-level', '3')
  fireEvent.change(screen.getByRole('textbox', { name: 'Search repositories' }), { target: { value: 'apps/web' } })
  fireEvent.keyDown(apps, { key: 'ArrowRight' })
  expect(web).toHaveFocus()
  fireEvent.keyDown(web, { key: 'ArrowLeft' })
  expect(apps).toHaveFocus()
  fireEvent.keyDown(apps, { key: 'ArrowLeft' })
  expect(apps).toHaveAttribute('aria-expanded', 'false')
  expect(within(tree).queryByRole('treeitem', { name: 'web Git repository' })).toBeNull()
  expect(tabStops()).toEqual([apps])
  fireEvent.keyDown(apps, { key: 'ArrowRight' })
  fireEvent.keyDown(apps, { key: 'End' })
  expect(within(tree).getByRole('treeitem', { name: 'web Git repository' })).toHaveFocus()
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowUp' })
  expect(apps).toHaveFocus()
  fireEvent.keyDown(apps, { key: 'Home' })
  expect(root).toHaveFocus()
  await userEvent.keyboard('{Tab}')
  expect(screen.getByRole('textbox', { name: 'Session name' })).toHaveFocus()
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(root).toHaveFocus()
})

it.each(['daemon', 'cluster'] as const)('keeps %s layout within the viewport with reachable controls', async runtime => {
  for (const [width, height] of [[390, 844], [1440, 900], [3840, 2160]]) {
    await open(runtime, width, height, 'dark')
    const picker = screen.getByRole('region', { name: 'Workspace repositories' })
    fireEvent.click(within(picker).getByRole('checkbox', { name: /harbor/ }))
    await screen.findByText('1 / 20')
    const panel = document.querySelector('.launch-panel')!
    expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
    expect(panel.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    const launch = screen.getByRole('button', { name: /Launch/ })
    expect(launch.getBoundingClientRect().bottom).toBeLessThanOrEqual(height)
    if (width > 900) expect(panel.scrollHeight).toBeLessThanOrEqual(panel.clientHeight + 1)
    const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
    if (dir) await capture(`${dir}/${runtime}-${width}x${height}.png`, width, height)
    if (width === 390) {
      screen.getByRole('textbox', { name: 'Session name' }).scrollIntoView({ block: 'center' })
      if (dir) await capture(`${dir}/${runtime}-${width}x${height}-options.png`, width, height)
    }
    cleanup(); clearLaunchCache()
  }
})

it('keeps explanations short with details available and the empty-folder note beside its choice', async () => {
  await open('daemon')
  const card = screen.getByTestId('runtime-docker')
  expect(card.textContent).toContain('Works in a separate clone on this desktop.')
  expect(card.textContent).not.toContain('uncommitted work')
  const details = screen.getByText('Sandbox details').closest('details')!
  expect(details.open).toBe(false)
  fireEvent.click(screen.getByText('Sandbox details'))
  expect(details.open).toBe(true)
  expect(details.textContent).toContain('wip/<session-id>')
  expect(screen.getByText('About unattended sessions').closest('details')!.open).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: 'Empty workspace' }))
  const plan = screen.getByTestId('no-repo-plan')
  expect(screen.getByTestId('launch-col-repo').contains(plan)).toBe(true)
  expect(plan.textContent).toContain('Keeps your files in /repos/.scratch-')
  expect(screen.getByText('About this folder').closest('details')!.open).toBe(false)
  fireEvent.click(screen.getByText('Sandbox details'))
  const panel = document.querySelector('.launch-panel')!
  expect(panel.scrollHeight).toBeLessThanOrEqual(panel.clientHeight + 1)
  const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
  if (dir) await capture(`${dir}/daemon-empty-1440x900.png`, 1440, 900)
})

it.each(['daemon', 'cluster'] as const)('launches the explicit %s multi-selection and keeps host consent mandatory', async runtime => {
  await open(runtime)
  const picker = screen.getByRole('region', { name: 'Workspace repositories' })
  fireEvent.click(within(picker).getByRole('checkbox', { name: /harbor/ }))
  await screen.findByText('1 / 20')
  fireEvent.click(within(picker).getByRole('checkbox', { name: /beacon/ }))
  await screen.findByText('2 / 20')
  const launch = screen.getByRole('button', { name: 'Launch Session' })
  if (runtime === 'daemon') {
    expect(launch).toBeDisabled()
    fireEvent.click(screen.getByTestId('agent-ack'))
  } else expect(screen.queryByTestId('agent-ack')).toBeNull()
  await waitFor(() => expect(launch).not.toBeDisabled())
  fireEvent.click(launch)
  await waitFor(() => expect(launchBodies).toHaveLength(1))
  const body = launchBodies[0]
  expect(body.title).toBe('harbor')
  expect(body.kind).toBe('agent')
  expect(body.runtime).toBe(runtime)
  expect(body.no_repo).toBeUndefined()
  expect(body.selection).toEqual(runtime === 'cluster' ? { members: [], hosted: [
    { provider: 'github', full_name: 'example-org/harbor' }, { provider: 'github', full_name: 'example-org/beacon' },
  ] } : { daemon_id: 'desktop', ownership_revision: 1, connection_generation: 'current', root_identity: 'root', layout: 'names', members: [
    { path: 'harbor', source_identity: 'source:harbor' }, { path: 'beacon', source_identity: 'source:beacon' },
  ] })
})
