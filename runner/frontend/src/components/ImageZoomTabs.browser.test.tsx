import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="2400" height="1800"><rect width="2400" height="1800" fill="#20384b"/><path d="M0 1600L600 300L1200 1100L1800 200L2400 1600" fill="#4b8e78"/><circle cx="1800" cy="300" r="160" fill="#edc36b"/></svg>'
const artifact = { id: 'landscape', name: 'landscape.svg', view: 'image', size: svg.length, content_type: 'image/svg+xml' }
const shots = import.meta.env.VITE_IMAGE_ZOOM_SHOTS_DIR

afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
it.each([390, 1024, 3840])('zooms file and artifact tabs within real split groups and focus mode at %i px', async width => {
  await page.viewport(width, width === 3840 ? 2160 : 844)
  localStorage.setItem(RAIL_PREF_KEY, '0')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Inspect image details' })] })
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
  apiFetch.mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : json({}))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).includes('/workspace-file')) return json({ path: 'photo.svg', view: 'image', content_type: 'image/svg+xml', content_b64: btoa(svg), size: svg.length })
    if (String(url).endsWith('/raw')) return new Response(svg, { headers: { 'Content-Type': 'image/svg+xml' } })
    return json(String(url).endsWith('/artifacts') ? { artifacts: [artifact] } : [])
  }))
  localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({
    tabs: [{ id: 'artifact:landscape', view: { kind: 'artifact', artifact }, preview: false }, { id: 'workspace:photo.svg', view: { kind: 'workspace', path: 'photo.svg' }, preview: false }],
    active: 'artifact:landscape', focused: 'main',
    layout: { type: 'split', id: 'split', axis: 'horizontal', ratio: .5,
      first: { type: 'group', id: 'main', tabs: ['chat', 'artifact:landscape'], active: 'artifact:landscape' },
      second: { type: 'group', id: 'other', tabs: ['workspace:photo.svg'], active: 'workspace:photo.svg' } },
  }))
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></CommandPaletteProvider></MemoryRouter>)
  const viewer = await screen.findByRole('region', { name: artifact.name })
  await waitFor(() => expect((within(viewer).getByRole('button', { name: 'Fit' }) as HTMLButtonElement).disabled).toBe(false))
  const el = within(viewer).getByRole('region', { name: /image viewport/ })
  const img = within(viewer).getByRole('img')
  if (width >= 768) {
    const workspace = await screen.findByRole('region', { name: 'photo.svg' })
    await waitFor(() => expect((within(workspace).getByRole('button', { name: 'Fit' }) as HTMLButtonElement).disabled).toBe(false))
    const other = within(workspace).getByRole('img').getBoundingClientRect()
    fireEvent.keyDown(el, { key: '1' }); fireEvent.keyDown(el, { key: '+' })
    fireEvent.wheel(el, { deltaX: 120, deltaY: 140 })
    expect(within(workspace).getByRole('img').getBoundingClientRect().toJSON()).toEqual(other.toJSON())
    if (shots) await page.screenshot({ path: `${shots}/image-tabs-split-${width}.png` })
  }
  el.focus(); await userEvent.keyboard('1+')
  expect(img.getBoundingClientRect().width).toBeCloseTo(3000)
  // Image double-click is owned by the image, while tab double-click still enters focus mode.
  await userEvent.dblClick(el)
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  expect(within(viewer).getByRole('button', { name: 'Fit' }).getAttribute('aria-pressed')).toBe('true')
  await userEvent.dblClick(screen.getByRole('tab', { name: /landscape.svg/ }))
  expect(screen.getByRole('button', { name: 'Exit tab focus' })).toBeVisible()
  el.focus(); await userEvent.keyboard('1+')
  expect(img.getBoundingClientRect().width).toBeCloseTo(3000)
  if (shots) await page.screenshot({ path: `${shots}/image-tabs-focus-${width}.png` })
  fireEvent.keyDown(el, { key: '0' })
  const focusWidth = img.getBoundingClientRect().width
  await userEvent.click(screen.getByRole('button', { name: 'Exit tab focus' }))
  if (width >= 768) await waitFor(() => expect(img.getBoundingClientRect().width).toBeLessThan(focusWidth))
  for (const region of document.querySelectorAll('.session-columns,[data-session-root],.session-group,.artifact-body')) {
    expect(region.scrollHeight).toBe(region.clientHeight)
    expect(region.scrollWidth).toBe(region.clientWidth)
    expect(region.scrollTop).toBe(0)
  }
})
