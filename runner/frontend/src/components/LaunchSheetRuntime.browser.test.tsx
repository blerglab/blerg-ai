import { browserWidths } from '../test/browserWidths'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import LaunchSheet from './LaunchSheet'
import { clearLaunchCache, fetchCluster, fetchDaemons } from '../lib/launchCache'
import { coreOrigin } from '../authClient'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...await orig<typeof import('../authClient')>(), getAccessToken: () => 'test-token' }))
// Browser files share sessionStorage, including another file's warm desktop inventory.
beforeEach(() => { clearLaunchCache(); localStorage.clear() })
afterEach(() => { cleanup(); vi.unstubAllGlobals(); clearLaunchCache(); localStorage.clear() })
const repository = { name: 'project', path: 'project', source_identity: 'project-source', kind: 'repository' }
const otherRepository = { ...repository, name: 'other-project', path: 'other-project', source_identity: 'other-project-source' }
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }))

async function open(width: number, rejectHost: boolean | 'partial' = false, holdHost?: () => Promise<void>, cached?: 'cluster' | 'daemons') {
  await page.viewport(width, 900)
  const previews: string[] = []
  const launches: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    if (url === '/api/daemons') return json([{ id: 'desktop', name: 'Desktop', status: 'connected', mode: 'local', repos_root: '/repos', discovery_version: 1, sandbox_available: true, available_engines: ['claude'], workspace_capabilities: { manifest_version: 2, linux_host: true, sandbox: true } }])
    if (url === '/api/repos') return json({ repos: [] })
    if (url === '/api/cluster/status') return json({ configured: false })
    if (url === '/api/me/credentials') return json({ engines: [], git: false })
    if (url === '/api/session-boards') return json({ configured: false, boards: [] })
    if (url === '/api/mcp/connections') return json({ connections: [] })
    if (url.startsWith('/api/runner/launch-options?')) return json({ state: 'known', engines: [{ id: 'claude', name: 'Claude', restricted: true }] })
    if (url.startsWith('/api/models/')) return json({ models: [{ id: 'default', name: 'Default' }] })
    if (url.startsWith('/api/workspace/discovery?')) return json({ ok: true, page: { root_identity: 'root', root: { ...repository, path: '.', name: 'Repositories', kind: 'directory' }, entries: [repository, otherRepository] } })
    if (url === '/api/workspace/preview') {
      const body = JSON.parse(String(init?.body))
      previews.push(body.runtime)
      if (holdHost && body.runtime === 'host') return holdHost().then(() => new Response(JSON.stringify({ ok: true, selection: body.selection, members: body.selection.members.map((member: { path: string }) => ({ name: member.path })) })))
      if (rejectHost && body.runtime === 'host' && (rejectHost !== 'partial' || body.selection.members.length > 1 || body.selection.members[0]?.path === 'other-project')) return json({ ok: false, code: 'invalid_selection' })
      return json({ ok: true, selection: body.selection, members: body.selection.members.map((member: { path: string }) => ({ name: member.path })) })
    }
    if (url === '/api/sessions') {
      launches.push(JSON.parse(String(init?.body)))
      return json({ error: 'cluster Codex needs a dedicated login to renew its access.' }, 400)
    }
    return json({})
  }))
  if (cached === 'cluster') await fetchCluster()
  if (cached === 'daemons') await fetchDaemons()
  render(<MemoryRouter><LaunchSheet open onClose={() => {}} /></MemoryRouter>)
  fireEvent.click(await screen.findByRole('checkbox', { name: /^project.*Git repository/ }))
  await screen.findByRole('button', { name: 'Remove project' })
  fireEvent.click(screen.getByRole('checkbox', { name: /^other-project.*Git repository/ }))
  await screen.findByRole('button', { name: 'Remove other-project' })
  return { previews, launches }
}

it.each(browserWidths([390, 3840]))('keeps checked repositories across desktop runtimes and shows linked error sentences at %s px', async width => {
  const { previews, launches } = await open(width)
  fireEvent.click(screen.getByTestId('runtime-daemon'))
  await waitFor(() => expect(previews).toEqual(['sandbox', 'sandbox', 'host']))
  expect(await screen.findByRole('checkbox', { name: /^project.*Git repository/ })).toBeChecked()
  fireEvent.click(screen.getByTestId('runtime-docker'))
  await waitFor(() => expect(previews).toEqual(['sandbox', 'sandbox', 'host', 'sandbox']))
  const launch = screen.getByRole('button', { name: /Launch/ })
  await waitFor(() => expect(launch).toBeEnabled())
  fireEvent.click(launch)
  const error = await screen.findByText('Failed to launch workspace: cluster Codex needs a dedicated login to renew its access.')
  expect(error.textContent).not.toContain('{')
  expect(screen.getByRole('link', { name: 'Reconnect in Settings' }).getAttribute('href')).toBe(`${coreOrigin()}/settings#managed-connections`)
  expect(launches[0]).toMatchObject({ runtime: 'docker', selection: { members: [{ path: 'project' }, { path: 'other-project' }] } })
  const sheet = document.querySelector('.launch-panel')!
  expect(sheet.scrollWidth).toBeLessThanOrEqual(sheet.clientWidth + 1)
})

it('explains when the new runtime rejects the repository selection', async () => {
  await open(390, true)
  fireEvent.click(screen.getByTestId('runtime-daemon'))
  expect(await screen.findByText('The selected repositories were cleared because they are incompatible with this runtime. Select repositories again.')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Remove project' })).toBeNull()
  expect(screen.getByRole('button', { name: /Launch/ })).toBeDisabled()
})

it('withholds launch while the preserved selection is being checked for the new runtime', async () => {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await open(390, false, () => gate)
  fireEvent.click(screen.getByTestId('runtime-daemon'))
  fireEvent.click(screen.getByTestId('agent-ack'))
  const launch = screen.getByRole('button', { name: /Launch/ })
  expect(launch).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Remove project' })).toBeTruthy()
  release()
  await waitFor(() => expect(launch).toBeEnabled())
  expect(await screen.findByRole('checkbox', { name: /^project.*Git repository/ })).toBeChecked()
})

it('keeps compatible repositories when only part of a selection is rejected', async () => {
  await open(390, 'partial')
  fireEvent.click(screen.getByTestId('runtime-daemon'))
  await screen.findByText('Some selected repositories were cleared because they are incompatible with this runtime.')
  expect(await screen.findByRole('checkbox', { name: /^project.*Git repository/ })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: /^other-project.*Git repository/ })).not.toBeChecked()
  fireEvent.click(screen.getByTestId('agent-ack'))
  await waitFor(() => expect(screen.getByRole('button', { name: /Launch/ })).toBeEnabled())
})

it('keeps the selection through a second runtime change while the first check is pending', async () => {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await open(390, false, () => gate)
  fireEvent.click(screen.getByTestId('runtime-daemon'))
  fireEvent.click(screen.getByTestId('runtime-docker'))
  release()
  await waitFor(() => expect(screen.getByRole('button', { name: /Launch/ })).toBeEnabled())
  expect(screen.getByRole('button', { name: 'Remove project' })).toBeTruthy()
  expect(await screen.findByRole('checkbox', { name: /^other-project.*Git repository/ })).toBeChecked()
})

it.each(['cluster', 'daemons'] as const)('waits for both inventories before choosing a runtime with only %s cached', async cached => {
  const { previews } = await open(390, false, undefined, cached)
  await waitFor(() => expect(previews).toEqual(['sandbox', 'sandbox']))
  expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
})
