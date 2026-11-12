import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const sha = 'a'.repeat(40)
const member = { id: 'app', name: 'app', path: 'app' }
const baseGraph = { member_id: 'app', generation: 7, launch_generation: 'launch', default_branch: 'trunk', refs: [{ name: 'trunk', sha }], more: false, commits: [{ sha, parents: [], subject: 'Readable change' }] }
const id = (n: number) => n.toString(16).padStart(40, '0')
const commits = [
 ...Array.from({ length: 8 }, (_, offset) => {
   const i = 8 - offset
   return [
    { sha: i === 8 ? sha : id(i), parents: [i === 1 ? id(99) : id(i - 1), id(100 + i)], subject: i === 8 ? 'Merge search BLG-12 for release' : 'Merge feature ' + i + ' #12', date: '2026-10-31T10:00:00Z' },
    { sha: id(100 + i), parents: [id(200 + i)], subject: 'Finish feature ' + i, date: '2026-10-30T10:00:00Z' },
    { sha: id(200 + i), parents: [id(99)], subject: 'Build feature ' + i, date: '2026-10-29T10:00:00Z' },
   ]
 }).flat(),
 { sha: id(99), parents: [], subject: 'Previous release', date: '2026-10-20T10:00:00Z' },
]
const graph = { ...baseGraph, commits, more: true, refs: [{ name: 'trunk', sha }, ...Array.from({ length: 8 }, (_, i) => ({ name: ['feature/search', 'feature/release-landmarks', 'fix/keyboard-navigation', 'feature/colors', 'fix/read-bounds', 'feature/tags', 'feature/graph', 'fix/mobile'][i], sha: id(101 + i) }))], tags: [{ name: 'v0.6.0', sha, contains: commits.map(c => c.sha), previous: 'v0.5.4', complete: true }, { name: 'v0.5.4', sha: id(99), contains: [id(99)], complete: true }] }

const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

for (const width of [390, 3840]) it(`opens a repository graph from the real dock, then a member-qualified commit at ${width}px`, async () => {
  await page.viewport(width, 900)
  localStorage.clear()
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', workspace: { members: [member] } })] })
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), location.origin)
    requests.push(url.pathname + url.search)
    if (url.pathname === '/api/session-boards') return json({ boards: [{ id: 'board-a', key: 'BLG' }] })
    if (url.pathname.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'board-a', level: 'read' }] })
    if (url.pathname.includes('/board-cards/board-a/12')) return json({ card: { id: 'card-12', board_id: 'board-a', number: 12, title: 'Search', reference: 'BLG-12' }, url: '' })
    if (url.pathname.endsWith('/git/graph')) {
      if (Number(url.searchParams.get('depth')) > 50) {
        const older = [{ sha: id(300), parents: [id(301)], subject: 'Older release work' }, { sha: id(301), parents: [], subject: 'Initial work' }]
        return json({ ...graph, more: false, commits: [...graph.commits.map(commit => commit.sha === id(99) ? { ...commit, parents: [id(300)] } : commit), ...older], tags: graph.tags.map(tag => ({ ...tag, contains: [...tag.contains, id(300), id(301)] })) })
      }
      return json(graph)
    }
    if (url.pathname.endsWith(`/commits/${sha}/diff`)) return new Response('diff --git a/app.txt b/app.txt\n--- a/app.txt\n+++ b/app.txt\n@@ -1 +1 @@\n-old\n+new\n', { headers: { 'Content-Type': 'text/x-diff', 'X-Blerg-Member-ID': 'app', 'X-Blerg-Generation': '7', 'X-Blerg-Launch-Generation': 'launch' } })
    if (url.pathname.endsWith('/workspace')) return json({ members: [member], result: { ok: true, generation: 7, launch_generation: 'launch', entries: [], status: { branch: 'trunk', changed: 0, untracked: 0, partial: false } } })
    if (url.pathname.endsWith('/shells')) return json([])
    if (url.pathname.endsWith('/shell-capability')) return json({ available: false })
    if (url.pathname.endsWith('/live')) return json({ can_release_prompt: true })
    return json({})
  }))
  invalidateChatTransport()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></CommandPaletteProvider></MemoryRouter>)
  fireEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  fireEvent.click(await screen.findByRole('tab', { name: 'Repositories' }))
  await screen.findByRole('button', { name: 'app' })
  expect(requests.filter(url => url.includes('/git/graph'))).toHaveLength(0)
  expect(screen.queryByText('Branch graph')).toBeNull()
  if (import.meta.env.VITE_GRAPH_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_GRAPH_SHOTS_DIR}/repositories-${width}.png` })
  fireEvent.click(screen.getByRole('button', { name: 'app' }))
  const tab = await screen.findByRole('tab', { name: /^app(?: |$)/ })
  expect(tab).toHaveAttribute('aria-selected', 'true')
  const focused = document.querySelector<HTMLElement>('[aria-label="Branch graph for app"]')!
  await waitFor(() => expect(within(focused).getByRole('button', { name: 'aaaaaaa Merge search BLG-12 for release' })).toBeVisible())
  await waitFor(() => expect(requests.some(url => url.includes('/workspace?op=status'))).toBe(true))
  expect(within(focused).queryByRole('button', { name: 'Add repository' })).toBeNull()
  expect(within(focused).queryByRole('heading')).toBeNull()
  expect(focused.querySelector('.repository-details')).toBeNull()
  expect(focused.querySelector('.repository-graph-summary')).toHaveTextContent('25 commits · 1 branch · 2 tags')
  expect(within(focused).queryByText('feature/search')).toBeNull()
  fireEvent.click(within(focused).getByRole('checkbox', { name: 'Show merged' }))
  expect(within(focused).getByText('feature/search')).toBeVisible()
  fireEvent.click(within(focused).getByRole('checkbox', { name: 'Show merged' }))
  expect(focused.querySelectorAll('details[open]')).toHaveLength(0)
  await waitFor(() => expect(within(focused).getAllByRole('button', { name: 'Open card BLG-12: Search' }).length).toBeGreaterThan(1))
  expect(focused.getBoundingClientRect().width).toBeLessThanOrEqual(width)
  const commit = within(focused).getByRole('button', { name: 'aaaaaaa Merge search BLG-12 for release' })
  const scroll = focused.querySelector<HTMLElement>('.repository-graph-scroll')!
  expect(focused.querySelectorAll('.repository-graph-lines')).toHaveLength(1)
  expect(scroll.scrollWidth).toBeLessThanOrEqual(scroll.clientWidth)
  expect(focused.querySelector('.repository-graph-rows > li')!.getBoundingClientRect().height).toBe(48)
  const svg = focused.querySelector('.repository-graph-lines')!.getBoundingClientRect()
  const subject = focused.querySelector('.repository-graph-subject')!.getBoundingClientRect()
  expect(subject.left).toBeGreaterThanOrEqual(svg.right)
  expect(focused.querySelector('.repository-graph-refs')!.getBoundingClientRect().left).toBe(svg.right)
  expect(svg.width).toBeLessThanOrEqual(56)
  const lastLane = Math.max(...[...focused.querySelectorAll('circle')].map(node => Number(node.getAttribute('cx'))))
  expect(svg.width - lastLane).toBeLessThanOrEqual(12)
  expect(svg.left - focused.querySelector('.repository-graph-rows > li')!.getBoundingClientRect().left).toBeLessThanOrEqual(150)
  fireEvent.click(within(focused).getByText('v0.6.0', { selector: 'summary strong' }))
  expect(within(focused).getByText('v0.6.0', { selector: 'summary strong' }).parentElement).toHaveTextContent('since v0.5.4')
  expect(within(focused).getByRole('region', { name: 'Release v0.6.0' })).toHaveTextContent('Merge search')
  for (const theme of ['colorblind-dark', 'colorblind-light']) {
    document.documentElement.dataset.theme = theme
    const strokes = [...focused.querySelectorAll('path')].map(path => getComputedStyle(path).stroke)
    expect(strokes.every(stroke => stroke !== 'none' && stroke !== 'rgba(0, 0, 0, 0)')).toBe(true)
    expect(new Set(strokes).size).toBeGreaterThan(3)
  }
  fireEvent.click(within(focused).getByText('v0.6.0', { selector: 'summary strong' }))
  expect(focused.querySelectorAll('details[open]')).toHaveLength(0)
  document.documentElement.dataset.theme = 'dark'
  if (import.meta.env.VITE_GRAPH_SHOTS_DIR) await page.screenshot({ path: `${import.meta.env.VITE_GRAPH_SHOTS_DIR}/graph-${width}.png` })
  const canvas = focused.querySelector('.repository-graph-lines')!
  fireEvent.click(within(focused).getByRole('button', { name: 'More history' }))
  await waitFor(() => expect(requests.some(url => url.includes('/git/graph?') && url.includes('depth=100'))).toBe(true))
  await waitFor(() => expect(focused.querySelectorAll('.repository-graph-rows > li')).toHaveLength(27))
  expect(focused.querySelector('.repository-graph-lines')).toBe(canvas)
  expect(scroll.scrollLeft).toBe(0)
  fireEvent.click(within(focused).getAllByRole('button', { name: 'Open card BLG-12: Search' })[0])
  const card = await screen.findByRole('dialog')
  expect(card).toHaveTextContent('Search')
  fireEvent.keyDown(card, { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  fireEvent.click(commit)
  await waitFor(() => expect(document.querySelector('.diff-view')).toBeTruthy())
  const diffRead = requests.find(url => url.includes(`/commits/${sha}/diff`))!
  expect(new URL(diffRead, location.origin).searchParams.get('member_id')).toBe('app')
  expect(new URL(diffRead, location.origin).searchParams.get('generation')).toBe('7')
  expect(new URL(diffRead, location.origin).searchParams.get('launch_generation')).toBe('launch')
})
