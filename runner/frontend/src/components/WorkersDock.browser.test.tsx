import { browserWidths } from '../test/browserWidths'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within, fireEvent } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionDockWindows from './SessionDockWindows'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => false }))

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string) => {
  if (url.endsWith('/categories')) return Response.json({ categories: [] })
  const live = url.match(/^\/api\/sessions\/([^/]+)\/live$/)
  if (live) return Response.json({ session_id: decodeURIComponent(live[1]) })
  throw new Error(`Unexpected dock fixture request: ${url}`)
}) }))

beforeEach(() => { vi.spyOn(console, 'warn') })
afterEach(() => {
  cleanup()
  try { expect(console.warn).not.toHaveBeenCalled() } finally { vi.restoreAllMocks() }
})

for (const width of browserWidths([390, 1024, 3840])) for (const count of [0, 3, 40]) it(`separates Main, channels and ${count} workers at ${width}px`, async () => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  await page.viewport(width, 900)
  useMessageStore.setState({ messages: count ? [{ id: 'question', session_id: 'worker-0', kind: 'ask', status: 'open', answerable: true, owner_only: true, body: 'Choose a direction', created_at: '2026-10-31T12:00:00Z' }] : [] })
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Coordinate', status: 'idle' }), makeSession({ id: 'channel', title: 'Design discussion', parent_session_id: 'main', runtime: 'docker', status: 'idle' }), ...Array.from({ length: count }, (_, i) => makeSession({ id: `worker-${i}`, title: i === 0 ? 'Review needs a decision' : i === 1 ? 'Build failed' : `Quiet worker ${i}`, parent_session_id: 'main', started_by: { kind: 'session' }, runtime: i === 0 ? 'daemon' : i % 3 === 2 ? 'cluster' : 'docker', status: i === 0 ? 'waiting' : i === 1 ? 'error' : i === 2 ? 'idle' : 'running' }))] })
  render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 300, height: 700, display: 'flex' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Repository files</p>} /></div></MemoryRouter>)
  const checkIdleGlyphs = () => {
    const glyphs = [...document.querySelectorAll<HTMLElement>('.session-dock-content [data-testid="status-dot"][title="idle"]')]
    expect(glyphs.length).toBeGreaterThan(0)
    for (const glyph of glyphs) {
      const range = document.createRange()
      range.selectNodeContents(glyph)
      expect([...range.getClientRects()]).toHaveLength(1)
      expect(getComputedStyle(glyph).whiteSpace).toBe('nowrap')
      expect(glyph.getBoundingClientRect().width).toBe(16)
      expect(glyph.getBoundingClientRect().height).toBeLessThanOrEqual(16)
    }
  }
  checkIdleGlyphs()
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/dock-${width}-${count}.png` })
  expect(screen.getByRole('tab', { name: 'Channels' })).toBeVisible()
  expect(screen.getByRole('treeitem', { name: 'Main' })).toBeVisible()
  expect(screen.getByRole('heading', { name: /channels$/i })).toBeVisible()
  if (!count) expect(screen.queryByRole('treeitem', { name: /^Workers/ })).toBeNull()
  else {
    const workers = screen.getByRole('treeitem', { name: /^Workers/ })
    expect(workers).toHaveAttribute('aria-expanded', count > 3 ? 'true' : 'false')
    expect(workers).toHaveTextContent('Review needs a decision')
    expect(workers).toHaveTextContent('Build failed')
    if (count <= 3) expect(screen.queryByRole('treeitem', { name: /Quiet worker/ })).toBeNull()
    expect(within(workers).getByRole('img', { name: 'Mixed runtimes' })).toBeVisible()
    fireEvent.keyDown(workers, { key: 'ArrowRight' })
    expect(screen.getAllByRole('treeitem')).toHaveLength(count + 3)
    expect(within(screen.getByRole('treeitem', { name: 'Review needs a decision' })).getByRole('img', { name: 'Needs you' })).toBeVisible()
    for (const row of screen.getAllByRole('treeitem')) expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)
    checkIdleGlyphs()
    expect(document.querySelectorAll('.dock-worker')).toHaveLength(count)
    for (const row of document.querySelectorAll('.dock-worker')) expect(row.querySelectorAll('.runtime-icon').length).toBeLessThanOrEqual(1)
    if (shots) await page.screenshot({ path: `${shots}/dock-${width}-${count}-expanded.png` })
  }
})

for (const width of [390, 3840]) for (const theme of ['dark', 'colorblind-dark']) it(`aligns worker columns at ${width}px in ${theme}`, async () => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', theme)
  await page.viewport(width, 900)
  useMessageStore.setState({ messages: [{ id: 'ask', session_id: 'attention', kind: 'ask', status: 'open', answerable: true, owner_only: true, body: 'Choose a direction', created_at: '2026-10-31T12:00:00Z' }] })
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Coordinate', status: 'idle' }),
    ...(['short', 'long', 'role', 'attention'] as const).map((id, i) => makeSession({ id, title: ['Build', 'Review the complete implementation and all regression coverage before release', 'Gates', 'Choose a release direction'][i], runtime: i % 2 ? 'daemon' : 'cluster', status: i === 1 ? 'idle' : i === 3 ? 'waiting' : 'running', worker_kind: i === 2 ? 'role' : 'card', parent_session_id: 'main', started_by: { kind: 'session' } }))] })
  render(<MemoryRouter><div style={{ width: width === 390 ? '100%' : 300, height: 700, display: 'flex' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Repository files</p>} /></div></MemoryRouter>)
  const rows = [...document.querySelectorAll<HTMLElement>('.dock-worker > .workspace-row')]
  expect(rows).toHaveLength(4)
  const x = (row: HTMLElement, selector: string) => row.querySelector(selector)!.getBoundingClientRect().x
  for (const row of rows) {
    for (const selector of ['.runtime-icon', '.workspace-codes', '.workspace-name']) expect(x(row, selector)).toBeCloseTo(x(rows[0], selector), 1)
    expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)
  }
  expect(within(rows[2]).getByRole('img', { name: 'Role worker' })).toBeVisible()
  const title = rows[1].querySelector('.workspace-name')!
  expect(title.scrollWidth).toBeGreaterThan(title.clientWidth)
  fireEvent.click(rows[0])
  await page.getByRole('treeitem', { name: 'Gates', exact: true }).hover()
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/worker-columns-${width}-${theme}.png` })
})
