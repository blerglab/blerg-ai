// BLG-467: charts and diagrams fill their space in a tab and in full screen, instead of sitting
// small in a corner of an otherwise empty one — the real app (SessionDetail, its real CSS and
// dark theme), the same harness PopOutMessage.browser.test.tsx uses, reached through the real
// "Open in a tab" button on a chart drawn in the chat, not a stand-in ArtifactViewer host.
import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cdp, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

async function viewport(width: number, height: number) {
  await page.viewport(width, height)
}
async function screenshot(path: string, width: number) {
  // Remove only Vitest's outer preview scaling, never an app style. Playwright
  // captures the real iframe body at its authored CSS size, including full 4K.
  const preview = window.frameElement!.parentElement as HTMLElement
  const transform = preview.style.transform
  preview.style.transform = 'none'
  try {
    const result = await page.screenshot({path, base64: true})
    const image = new Image()
    image.src = 'data:image/png;base64,' + (typeof result === 'string' ? result : result.base64)
    await image.decode()
    expect(image.naturalWidth).toBe(width)
  } finally { preview.style.transform = transform }
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release chart' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(async () => { await cdp().send('Emulation.setEmulatedMedia', {features: []}); invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme'); document.body.innerHTML = '' })

function mount() {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh' }}>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seed(text: string) {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show me the release chart', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text, done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}


import done from '../test/fixtures/charts/060-cards-done-per-hour.vl.json'
import dashboard from '../test/fixtures/charts/dashboard.vl.json'
import merged from '../test/fixtures/charts/060-cards-merged-per-hour.vl.json'
import { compileInWorker } from '../../../../packages/chat/src/visual/vega'
import { chartSize } from '../../../../packages/chat/src/visual/chartSize'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
for (const width of [390, 520, 1024, 3840]) {
  for (const [name, spec] of Object.entries({done, merged})) {
    it(`${name} fits at ${width} with two series and a readable title`, async () => {
      const screenWidth = width === 520 ? 1040 : width === 1024 ? 1364 : width
      await viewport(screenWidth, width === 3840 ? 2160 : 900)
      if (width === 520) document.documentElement.setAttribute('data-theme', 'colorblind-dark')
      seed('```vega-lite\n' + JSON.stringify(spec) + '\n```')
      const view = mount()
      if (width === 520) view.container.style.width = '520px'
      await waitFor(() => expect(document.querySelector('.visual-surface[data-scene-state="exact"] canvas')).toBeTruthy(), {timeout: 15000})
      const surface = document.querySelector('.visual-surface')!
      await waitFor(() => expect(surface.querySelector('canvas')!.getBoundingClientRect().width).toBeGreaterThan(Math.min(640, surface.clientWidth - 20) * .8))
      const theme = readVisualTheme(surface as HTMLElement)
      const drawing = await compileInWorker(spec, theme, undefined, chartSize(640, 360, {width: surface.clientWidth - 20, height: 0}, false))
      const svg = new DOMParser().parseFromString(drawing.svg, 'image/svg+xml')
      expect([...svg.querySelectorAll('.role-legend-label text')]).toHaveLength(2)
      const colors = [...svg.querySelectorAll('.role-legend-symbol path')].map(el => el.getAttribute('fill') || el.getAttribute('stroke'))
      expect(new Set(colors).size).toBe(2)
      const probe = document.createElement('div'); probe.innerHTML = drawing.svg; document.body.append(probe)
      const title = probe.querySelector<SVGGraphicsElement>('.role-title-text text')!
      const titleSpans = [...title.querySelectorAll('tspan')]
      expect((titleSpans.length ? titleSpans : [title]).map(el => el.textContent).join(' ')).toBe(spec.title.text)
      expect(title.getBBox().width).toBeLessThan(drawing.width - 32)
      probe.remove()
      // Line points must never own the entire path's bounding rectangle.
      const lineRegions = drawing.regions.slice(spec.layer[0].data.values.length)
      expect(lineRegions.every(region => region.x2 - region.x1 < 30)).toBe(true)
      const bars = drawing.regions.slice(0, spec.layer[0].data.values.length).filter(region => region.y2 - region.y1 > 8)
      expect(new Set(bars.slice(0, 3).map(region => region.text)).size).toBe(3)
      expect(drawing.width).toBeLessThanOrEqual(Math.min(640, surface.clientWidth - 20) + 1)
      const canvas = surface.querySelector('canvas')!
      expect(canvas.getBoundingClientRect().height).toBeGreaterThan(250)
      const rect = canvas.getBoundingClientRect()
      const originalPixels = canvas.toDataURL()
      if (width === 390) await cdp().send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-reduced-motion', value: 'reduce'}]})
      for (const bar of bars.slice(0, 3)) {
        canvas.dispatchEvent(new PointerEvent('pointermove', {clientX: rect.left + (bar.x1 + bar.x2) / 2 * rect.width / drawing.width, clientY: rect.top + (bar.y1 + bar.y2) / 2 * rect.height / drawing.height}))
        expect(surface.querySelector('[role="tooltip"]')?.textContent).toBe(bar.text)
      }
      await waitFor(() => expect(canvas.toDataURL()).not.toBe(originalPixels))
      canvas.dispatchEvent(new PointerEvent('pointerleave'))
      expect(canvas.toDataURL()).toBe(originalPixels)
      for (const point of lineRegions.slice(-3)) {
        canvas.dispatchEvent(new PointerEvent('pointermove', {clientX: rect.left + (point.x1 + point.x2) / 2 * rect.width / drawing.width, clientY: rect.top + (point.y1 + point.y2) / 2 * rect.height / drawing.height}))
        expect(surface.querySelector('[role="tooltip"]')?.textContent).toBe(point.text)
      }
      canvas.dispatchEvent(new PointerEvent('pointerleave'))
      if (width === 390) await cdp().send('Emulation.setEmulatedMedia', {features: []})
      if (import.meta.env.VITE_LAYER_SHOTS_DIR) await screenshot(`${import.meta.env.VITE_LAYER_SHOTS_DIR}/${name}-${width}.png`, screenWidth)
      const open = document.querySelector<HTMLButtonElement>('.visual-actions button[aria-label="Open in a tab"]')!
      await act(async () => { open.click() })
      const hosted = await waitFor(() => {
        const el = document.querySelector<HTMLElement>('.hosted-view:not([hidden])')
        expect(el?.querySelector('.visual-surface[data-scene-state="exact"] canvas')).toBeTruthy()
        return el!
      }, {timeout: 15000})
      if (width === 520 || width === 1024) expect(hosted.getBoundingClientRect().width).toBe(width)
      await waitFor(() => expect(hosted.querySelector('canvas')!.getBoundingClientRect().height).toBeGreaterThan(200))
      if (import.meta.env.VITE_LAYER_SHOTS_DIR) await screenshot(`${import.meta.env.VITE_LAYER_SHOTS_DIR}/${name}-tab-${width}.png`, screenWidth)
      view.unmount()
    })
  }
}

// Sample every painted frame while opening and switching real hosted chart tabs.
// Pending draws are skeletons; every visible canvas must agree with its pane.
for (const width of [390, 520, 1024, 3840]) it(`never paints a stale-size chart on first open or tab switches at ${width}`, async () => {
  await viewport(width === 520 ? 1040 : width, width === 3840 ? 2160 : 900)
  seed('```vega-lite\n' + JSON.stringify(done) + '\n```\n\n```vega-lite\n' + JSON.stringify(merged) + '\n```')
  const samples: {width: number; available: number}[] = []
  let running = true
  const sample = () => {
    for (const surface of document.querySelectorAll<HTMLElement>('.hosted-view:not([hidden]) .visual-surface')) {
      const canvas = surface.querySelector('canvas')
      if (canvas && surface.getBoundingClientRect().width > 0) {
        samples.push({width: canvas.getBoundingClientRect().width, available: surface.clientWidth - 20})
      }
    }
    if (running) requestAnimationFrame(sample)
  }
  requestAnimationFrame(sample)
  const view = mount()
  try {
    await waitFor(() => expect(document.querySelectorAll('.visual-surface[data-scene-state="exact"] canvas')).toHaveLength(2), {timeout: 15000})
    const buttons = [...document.querySelectorAll<HTMLButtonElement>('.visual-actions button[aria-label="Open in a tab"]')]
    for (const button of buttons) {
      await act(async () => { button.click() })
      await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) .visual-surface[data-scene-state="exact"] canvas')).toBeTruthy(), {timeout: 15000})
      const active = document.querySelector('.session-tab.active [role="tab"]')!
      await act(async () => { active.dispatchEvent(new MouseEvent('dblclick', {bubbles: true})) })
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    }
    const tabs = [...document.querySelectorAll<HTMLElement>('[role="tab"]')].filter(el => el.closest('.session-tab') && el.dataset.tabId !== 'chat')
    expect(tabs).toHaveLength(2)
    for (const tab of [...tabs, ...tabs]) {
      await act(async () => { tab.click() })
      await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) .visual-surface[data-scene-state="exact"] canvas')).toBeTruthy(), {timeout: 15000})
    }
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    await waitFor(() => expect(samples.length).toBeGreaterThan(0))
    expect(samples.filter(s => !(s.width > 200 && s.width <= s.available + 1))).toEqual([])
  } finally { running = false; view.unmount() }
})

it('keeps native and layered charts and the dashboard painted throughout a splitter drag', async () => {
  await viewport(1600, 1100)
  const native = {data: {values: [{x: 'A', y: 4}, {x: 'B', y: 9}]}, mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}}}
  seed([native, done, merged, dashboard].map(spec => '```vega-lite\n' + JSON.stringify(spec) + '\n```').join('\n\n'))
  const view = mount()
  const samples: number[] = []
  let sampling = true
  try {
    await waitFor(() => expect(document.querySelectorAll('.visual-surface [data-chart-renderer]')).toHaveLength(4), {timeout: 20000})
    const dashboardSurface = [...document.querySelectorAll<HTMLElement>('.visual-surface')].at(-1)!
    dashboardSurface.scrollTop = 40
    const scroll = dashboardSurface.scrollTop
    const sample = () => {
      const drawings = [...document.querySelectorAll('.visual-surface [data-chart-renderer]')]
      samples.push(drawings.filter(drawing => drawing instanceof SVGElement
        ? drawing.querySelector('rect, path') !== null
        : (drawing.querySelector('canvas')?.width ?? 0) > 0).length)
      if (sampling) requestAnimationFrame(sample)
    }
    requestAnimationFrame(sample)
    const separator = document.querySelector<HTMLElement>('[role="separator"][aria-label="Workspace panel width"]')!
    expect(separator).toBeTruthy()
    const before = separator.getAttribute('aria-valuenow')
    const box = separator.getBoundingClientRect(), frame = window.frameElement!.getBoundingClientRect(), scale = frame.width / innerWidth
    const x = frame.left + (box.left + box.width / 2) * scale, y = frame.top + (box.top + box.height / 2) * scale
    await cdp().send('Input.dispatchMouseEvent', {type: 'mousePressed', x, y, button: 'left', clickCount: 1})
    for (let offset = 0; offset <= 180; offset += 15) {
      await cdp().send('Input.dispatchMouseEvent', {type: 'mouseMoved', x: x - offset * scale, y, button: 'left', buttons: 1})
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    }
    await cdp().send('Input.dispatchMouseEvent', {type: 'mouseReleased', x: x - 180 * scale, y, button: 'left', clickCount: 1})
    expect(separator.getAttribute('aria-valuenow')).not.toBe(before)
    expect(samples.length).toBeGreaterThan(10)
    expect(samples.every(count => count === 4)).toBe(true)
    expect(document.querySelector('.visual-surface svg rect')).toBeTruthy()
    expect(dashboardSurface.scrollTop).toBe(scroll)
  } finally { sampling = false; view.unmount() }
})

it('keeps a shorter authored layer height while fitting its width', async () => {
  await viewport(390, 900)
  seed('```vega-lite\n' + JSON.stringify({...done, title: 'Authored height', height: 180}) + '\n```')
  const view = mount()
  try {
    await waitFor(() => expect(document.querySelector('.visual-surface[data-scene-state="exact"] canvas')).toBeTruthy(), {timeout: 15000})
    expect(document.querySelector('canvas')!.getBoundingClientRect().height).toBeCloseTo(180, 0)
  } finally { view.unmount() }
})
