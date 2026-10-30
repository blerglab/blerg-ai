import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import WorkspacePicker from './WorkspacePicker'

vi.mock('../lib/launchCache', () => ({ currentAccount: () => 'person-a' }))
vi.mock('../apiFetch', () => ({ apiFetch: (url: string, init?: RequestInit) => fetch(url, init) }))
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
it('warns that an empty host folder cannot start host children', async () => {
  const folder = { path: 'notes', name: 'notes', kind: 'directory', source_identity: 'notes-source' }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => new Response(JSON.stringify(url.startsWith('/api/workspace/discovery?')
    ? { ok: true, page: { root_identity: 'root', root: { ...folder, path: '.', name: 'Repositories' }, entries: [folder], truncated: false } }
    : { ok: true, selection: { ...JSON.parse(String(init?.body)).selection, members: [], membership_snapshot: 'empty' }, members: [] }), { headers: { 'Content-Type': 'application/json' } })))
  render(<WorkspacePicker daemon={{ id: 'desktop-a', name: 'Desktop', mode: 'local', repos_root: '/repos', status: 'connected', discovery_version: 1, sandbox_available: true, available_engines: ['claude'] }} runtime="daemon" repos={[]} onChange={() => {}} />)
  fireEvent.click(await screen.findByRole('checkbox', { name: /notes.*Folder/ }))
  expect(await screen.findByRole('note', { name: 'Folder without repositories' })).toHaveTextContent('Host children need a Git repository.')
})
