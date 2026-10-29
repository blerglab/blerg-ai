import { useState } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ViewerHostContext, type ViewerHostValue } from '@blerglab/chat'
import WorkspaceTree from './WorkspaceTree'
import SettingsDialog from './SettingsDialog'
import { signedInAccount } from '../lib/liveBoardCard'
import { revealSessionFile } from '../lib/openSessionFile'
import { syncViewCacheSessions } from '../lib/sessionViewCache'
import '../index.css'
import '@blerglab/chat/blerg.css'
const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => 'person-a' }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))
vi.mock('./DelegationRulesSection', () => ({ default: () => null }))
vi.mock('./EngineStatusSection', () => ({ default: () => null }))
vi.mock('./ShellLimitsSection', () => ({ default: () => null }))
vi.mock('./UsageNoticeSettings', () => ({ default: () => null }))
const dir = (path: string) => ({ path, name: path.split('/').at(-1)!, directory: true })
let prefix = 'dev'
let listings: Record<string, ReturnType<typeof dir>[]>
function Harness() {
  const [path, setPath] = useState('')
  const [shown, setShown] = useState(true)
  const [settings, setSettings] = useState(false)
  const host: ViewerHostValue = { views: path ? [{ id: 'file', view: { kind: 'workspace', path } }] : [], active: path ? 'file' : 'chat', target: null, open: vi.fn(), close: vi.fn(), keep: vi.fn() }
  return <ViewerHostContext.Provider value={host}>
    <button className="btn" onClick={() => { setPath(`${prefix}/ll/api/deep.txt`); revealSessionFile({ session: 'compact', account: signedInAccount(), path: `${prefix}/ll/api/deep.txt` }) }}>Reveal file</button>
    <button className="btn" onClick={() => { setPath(`${prefix}/ll`); revealSessionFile({ session: 'compact', account: signedInAccount(), path: `${prefix}/ll` }) }}>Reveal folder</button>
    <button className="btn" onClick={() => setSettings(true)}>Preferences</button>
    <button className="btn" onClick={() => setShown(!shown)}>Toggle tree</button>
    <div style={{ width: '100%', maxWidth: 340 }}><WorkspaceTree sessionId="compact" active={shown} /></div>
    {settings && <SettingsDialog onClose={() => setSettings(false)} />}
  </ViewerHostContext.Provider>
}
beforeEach(() => {
  prefix = 'dev'
  syncViewCacheSessions([{ id: 'compact', status: 'idle' }])
  read.mockReset().mockImplementation(async (url: string) => {
    if (url === '/api/me/preferences') return new Response('{}')
    const q = new URL(url, location.origin).searchParams
    return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: listings[q.get('path') ?? ''] ?? [], truncated: false }, members: [], runtime: 'host' }))
  })
})
afterEach(async () => { await page.viewport(1280, 800) })
function fixture(head = 'dev') {
  prefix = head
  listings = { '': [dir(head)], [head]: [dir(`${head}/ll`)], [`${head}/ll`]: [dir(`${head}/ll/api`)], [`${head}/ll/api`]: [{ ...dir(`${head}/ll/api/deep.txt`), directory: false }] }
  return render(<Harness />)
}
async function loadedChain() {
  fireEvent.click(await screen.findByRole('treeitem', { name: prefix }))
  await screen.findByRole('treeitem', { name: 'deep.txt' })
  await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull())
  return screen.getByRole('treeitem', { name: `${prefix}/ll/api` })
}
it('grows one lazy row and expands the deepest folder once', async () => {
  fixture(); const row = await loadedChain()
  expect(row.textContent).toContain('dev / ll / api')
  expect(read.mock.calls.filter(([url]) => String(url).includes('op=list'))).toHaveLength(4)
  expect(row.getAttribute('aria-expanded')).toBe('true')
  expect(screen.getAllByRole('treeitem')).toHaveLength(2)
})
it('selects a middle segment and its folder path without expanding the tail', async () => {
  fixture(); const row = await loadedChain()
  fireEvent.click(screen.getByText('ll', { selector: '.workspace-segment' }))
  expect(row.getAttribute('aria-selected')).toBe('true')
  expect(row.getAttribute('data-selected-path')).toBe('dev/ll')
  expect(row.getAttribute('aria-expanded')).toBe('true')
})
it('reveals a deep file and a folder inside the compact chain', async () => {
  fixture(); await screen.findByRole('treeitem', { name: 'dev' })
  fireEvent.click(screen.getByText('Reveal file'))
  const file = await screen.findByRole('treeitem', { name: 'deep.txt' })
  expect(file.getAttribute('aria-selected')).toBe('true')
  expect(screen.getByRole('treeitem', { name: 'dev/ll/api' }).getAttribute('aria-level')).toBe('1')
  expect(file.getAttribute('aria-level')).toBe('2')
  fireEvent.click(screen.getByText('Reveal folder'))
  await waitFor(() => expect(screen.getByRole('treeitem', { name: 'dev/ll/api' }).getAttribute('data-selected-path')).toBe('dev/ll'))
})
it('navigates a compact row as one keyboard item', async () => {
  fixture(); const row = await loadedChain(); row.focus()
  const file = screen.getByRole('treeitem', { name: 'deep.txt' })
  await userEvent.keyboard('{ArrowRight}')
  expect(document.activeElement).toBe(file)
  await userEvent.keyboard('{ArrowLeft}')
  expect(document.activeElement).toBe(row)
  await userEvent.keyboard('{ArrowLeft}')
  expect(row.getAttribute('aria-expanded')).toBe('false')
  await userEvent.keyboard('{Enter}')
  expect(row.getAttribute('aria-expanded')).toBe('true')
})
it('turns compaction off in Settings immediately while retaining expansion', async () => {
  fixture(); await loadedChain()
  fireEvent.click(screen.getByText('Preferences'))
  const setting = screen.getByRole('checkbox', { name: 'Compact folders' })
  expect((setting as HTMLInputElement).checked).toBe(true)
  fireEvent.click(setting)
  await screen.findByRole('treeitem', { name: 'll' })
  expect(screen.getByRole('treeitem', { name: 'dev' }).getAttribute('aria-expanded')).toBe('true')
  expect(read.mock.calls.some(([url, init]) => url === '/api/me/preferences' && JSON.parse(init.body).compact_folders === false)).toBe(true)
  fireEvent.click(setting)
  await screen.findByRole('treeitem', { name: 'dev/ll/api' })
})
it.each([390, 3840])('truncates the start with a full-path tooltip at %i px', async width => {
  await page.viewport(width, 844)
  fixture('a-long-folder-'.repeat(20))
  const row = await loadedChain()
  const label = row.querySelector<HTMLElement>('.workspace-compact-name')!
  expect(label).not.toBeNull()
  expect(label.title).toBe(`${prefix}/ll/api`)
  expect(getComputedStyle(label).direction).toBe('rtl')
  expect(label.scrollWidth).toBeGreaterThan(label.clientWidth)
  expect(row.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

it('reveals the same file again after its compact parent was collapsed', async () => {
  fixture(); await screen.findByRole('treeitem', { name: 'dev' })
  fireEvent.click(screen.getByText('Reveal file'))
  await screen.findByRole('treeitem', { name: 'deep.txt' })
  await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull())
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll/api' }))
  expect(screen.queryByRole('treeitem', { name: 'deep.txt' })).toBeNull()
  fireEvent.click(screen.getByText('Reveal file'))
  const file = await screen.findByRole('treeitem', { name: 'deep.txt' })
  expect(file.getAttribute('aria-selected')).toBe('true')
})

it('reveals a repeated reference received while the cached explorer was hidden', async () => {
  fixture(); await screen.findByRole('treeitem', { name: 'dev' })
  fireEvent.click(screen.getByText('Reveal file'))
  await screen.findByRole('treeitem', { name: 'deep.txt' })
  await waitFor(() => expect(screen.queryByText('Loading…')).toBeNull())
  fireEvent.click(screen.getByRole('treeitem', { name: 'dev/ll/api' }))
  fireEvent.click(screen.getByText('Toggle tree'))
  expect(screen.queryByRole('tree')).toBeNull()
  fireEvent.click(screen.getByText('Reveal file'))
  fireEvent.click(screen.getByText('Toggle tree'))
  const file = await screen.findByRole('treeitem', { name: 'deep.txt' })
  expect(file.getAttribute('aria-selected')).toBe('true')
})
