import {chartFrameWitness} from '../test/chartFrameWitness'
import {readVisualTheme} from '../../../../packages/chat/src/visual/theme'
// BLG-470/506: real chart cards and quiet live timestamps, mounted through the real
// SessionDetail page the way PopOutMessage.browser.test.tsx does (the
// real SessionDetail page, its real CSS and dark theme), not a bare component in an unstyled host.
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { cdp, commands, page } from 'vitest/browser'
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

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_LIVE_FILE_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

const LIVE_ID = 'live00000000000000000000000001'
import telemetry from '../test/fixtures/charts/telemetry.dash.json'
import { liveFileSupport } from '../../../../packages/chat/src/visual/liveFile'

// Live lifecycle cases require native adapters. Keep the telemetry layout, but use
// native axis titles, elapsed minutes and no static sparkline (BLG-518).
const dashboard = structuredClone(telemetry)
for (const card of dashboard.cards) {
  if (card.view) delete card.view.sparkline
  if (card.spec) {
    if (card.spec.encoding.x.type === 'temporal') {
      Object.assign(card.spec.encoding.x, {field: 'minute', type: 'quantitative', title: 'Elapsed time (min)'})
      card.data.rows.forEach((row, minute) => Object.assign(row, {minute}))
    }
    for (const [channel, definition] of Object.entries(card.spec.encoding)) {
      for (const field of Array.isArray(definition) ? definition : [definition]) {
        if ('title' in field) {
          if (channel === 'x' || channel === 'y') Object.assign(field, {axis: {title: field.title}})
          delete field.title
        }
      }
    }
  }
}
const chartSource = JSON.stringify(dashboard)
let source = chartSource
const liveFile = { id: LIVE_ID, name: 'telemetry.dash.json', size: chartSource.length, content_type: 'application/json', view: 'json', origin: 'agent' as const, version: 1, live: true, updated_at: new Date().toISOString() }

beforeEach(() => {
  source = chartSource
  liveFile.updated_at = '2026-11-04T12:00:00.000Z'
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release notes' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.endsWith('/artifacts/' + LIVE_ID + '/raw')) return new Response(source)
    if (url.endsWith('/artifacts')) return json({ artifacts: [liveFile], limits: {}, can_delete: false })
    return json([])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })

function mount(pane: number) {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh', width: pane, maxWidth: '100%', transform: 'translateZ(0)' }}>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seedLiveArtifact() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'f1', seq: 1, ts: '2026-11-03T00:00:00Z', kind: 'artifact', payload: {...liveFile} } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
}


const bar = {
  title: 'Eight teams completed work · simulated',
  data: {values: Array.from({length: 8}, (_, i) => ({team: `Team ${i + 1}`, n: i + 2, series: `Series ${i + 1}`}))},
  mark: 'bar', encoding: {x: {field: 'team', type: 'nominal'}, y: {field: 'n', type: 'quantitative'}, color: {field: 'series', type: 'nominal'}},
}
const line = {
  title: 'Throughput increased during the sample',
  data: {values: Array.from({length: 12}, (_, i) => ({minute: i, n: i + 2}))},
  layer: [{mark: 'line', encoding: {x: {field: 'minute', type: 'quantitative'}, y: {field: 'n', type: 'quantitative'}}}, {mark: 'rule', encoding: {y: {datum: 6}}}],
}
const nvda = {
  title: 'NVDA · simulated prices (USD), minutes since session open',
  data: {values: Array.from({length: 12}, (_, i) => ({minute: i, price: 140 + i / 2}))},
  layer: [{mark: 'line', encoding: {x: {field: 'minute', type: 'quantitative'}, y: {field: 'price', type: 'quantitative'}}}, {mark: 'rule', encoding: {y: {datum: 142}}}],
}
const cases = [
  {name: 'line', width: 390, pane: 390, theme: 'dark', spec: line},
  {name: 'bar', width: 520, pane: 520, theme: 'colorblind-dark', spec: bar},
  {name: 'nvda', width: 390, pane: 390, theme: 'dark', spec: nvda},
  {name: 'dashboard', width: 3840, pane: 1024, theme: 'dark', spec: dashboard},
  {name: 'dashboard', width: 3840, pane: 3840, theme: 'colorblind-dark', spec: dashboard},
]
const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()))

for (const fixture of cases) it(`frame samples ${fixture.name} at pane ${fixture.pane} in ${fixture.theme}`, async () => {
  await page.viewport(fixture.width, fixture.width === 3840 ? 2160 : 1000)
  document.documentElement.setAttribute('data-theme', fixture.theme)
  const preview = window.frameElement!.parentElement as HTMLElement
  const previousTransform = preview.style.transform
  preview.style.transform = 'none'
  const name = `${fixture.name}-${fixture.pane}`
  const expectedScenes = fixture.name === 'dashboard' ? 3 : 1
  const expectReady = (root: ParentNode) => expect([...root.querySelectorAll<HTMLElement>('.visual-surface')].map(el => el.dataset.sceneState)).toEqual(Array<string>(expectedScenes).fill('exact'))
  source = JSON.stringify(fixture.spec)
  expect(liveFileSupport(fixture.name === 'dashboard' ? 'dashboard' : 'vega-lite', source)).toBe('native')
  liveFile.name = fixture.name === 'dashboard' ? 'telemetry.dash.json' : `${fixture.name}.vl.json`
  liveFile.size = source.length
  liveFile.updated_at = `2026-11-04T12:0${cases.indexOf(fixture)}:00.000Z`
  seedLiveArtifact()
  let stage = 'mount', sampling = true, completed = false
  let serial = 0
  let hostedReady = false
  const identities = new WeakMap<Element, number>()
  const seen = new WeakSet<Element>()
  const samples: {frame?: number; timestamp: number; stage: string; state: string; generation: number; box: number[]; scene: number[]; identity: number; blank: boolean; overflow: boolean; labels: number; legend: number; plot: number[] | null; rect?: number[]; committed?: number[]; surface?: number; natural?: number[]; hosted?: boolean; legendClipped?: boolean; sizeMismatch?: boolean}[] = []
  const witness = chartFrameWitness(!shotDir)
  const measure = (event?: Event) => {
    const hosted = document.querySelector('.hosted-view:not([hidden])')
    samples.push({timestamp: performance.timeOrigin + performance.now(), stage, state: 'view', generation: 0, box: [], scene: [], identity: 0, blank: false, overflow: false, labels: 0, legend: 0, plot: null, hosted: !!hosted})
    const visibleCharts = hosted?.querySelectorAll('.visual-surface svg, .visual-surface canvas').length ?? 0
    if (visibleCharts) hostedReady = true
    if (hostedReady && hosted && !visibleCharts) samples.push({timestamp: performance.timeOrigin + performance.now(), stage, state: 'blank', generation: 0, box: [], scene: [], identity: 0, blank: true, overflow: false, labels: 0, legend: 0, plot: null})
    for (const surface of document.querySelectorAll<HTMLElement>('.visual-surface')) {
      const box = surface.getBoundingClientRect()
      if (!box.width || !box.height || !surface.checkVisibility()) continue
      let top = box.top, bottom = box.bottom, left = box.left, right = box.right
      for (let parent = surface.parentElement; parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent), clip = parent.getBoundingClientRect()
        if (/(auto|scroll|hidden|clip)/.test(style.overflowY)) {top = Math.max(top, clip.top); bottom = Math.min(bottom, clip.bottom)}
        if (/(auto|scroll|hidden|clip)/.test(style.overflowX)) {left = Math.max(left, clip.left); right = Math.min(right, clip.right)}
      }
      if (right <= left || bottom <= top) continue
      // Record pre-RO layout as pending, but fail any before-paint commit that claims
      // exact geometry for a different measured box. Never drop a sampled drawing.
      const style = getComputedStyle(surface)
      const availableWidth = surface.clientWidth - (parseFloat(style.paddingLeft) || 0) - (parseFloat(style.paddingRight) || 0)
      const availableHeight = surface.clientHeight - (parseFloat(style.paddingTop) || 0) - (parseFloat(style.paddingBottom) || 0)
      const pendingBox = surface.dataset.sceneState === 'exact' && (Math.abs(Number(surface.dataset.measuredWidth) - availableWidth) > 1 || Math.abs(Number(surface.dataset.measuredHeight) - availableHeight) > 1)
      const sizeMismatch = pendingBox && event?.type === 'blerg-chart-frame' && event.target instanceof Node && surface.contains(event.target)
      const drawing = surface.querySelector('svg, canvas')
      if (!drawing) {
        if (seen.has(surface)) samples.push({timestamp: performance.timeOrigin + performance.now(), stage, state: 'blank', generation: 0, box: [box.width, box.height], scene: [], identity: 0, blank: true, overflow: false, labels: 0, legend: 0, plot: null})
        continue
      }
      seen.add(surface)
      if (!identities.has(surface)) identities.set(surface, ++serial)
      if (!identities.has(drawing)) identities.set(drawing, ++serial)
      const bounds = drawing.getBoundingClientRect()
      let blank = bounds.width <= 1 || bounds.height <= 1
      if (drawing instanceof HTMLCanvasElement) {
        const thumbnail = document.createElement('canvas'); thumbnail.width = thumbnail.height = 24
        const context = thumbnail.getContext('2d')!
        context.drawImage(drawing, 0, 0, 24, 24)
        blank ||= !context.getImageData(0, 0, 24, 24).data.some((value, i) => i % 4 === 3 && value > 0)
      } else blank ||= !drawing.querySelector('path, rect, line')
      const geometry = drawing instanceof SVGSVGElement ? drawing.getBBox() : null
      const metadata = drawing.closest<HTMLElement>('[data-plot-bounds]')
      const legendClipped = [...drawing.querySelectorAll('[data-legend-swatch]')].some(entry => {const rect = entry.getBoundingClientRect(); return rect.bottom > bounds.bottom + 1 || rect.right > bounds.right + 1})
      const cached = drawing.closest<HTMLElement>('[data-label-count]')
      samples.push({timestamp: performance.timeOrigin + performance.now(), stage, sizeMismatch, state: pendingBox && !sizeMismatch ? 'pending' : surface.dataset.sceneState ?? 'reserved', generation: Number(surface.dataset.sceneGeneration ?? 0), box: [box.width, box.height], scene: [bounds.width, bounds.height], rect: [Math.max(bounds.x,left), Math.max(bounds.y,top), Math.max(0,Math.min(bounds.right,right)-Math.max(bounds.x,left)), Math.max(0,Math.min(bounds.bottom,bottom)-Math.max(bounds.y,top))], hosted: !!surface.closest('.hosted-view:not([hidden])'), surface: identities.get(surface), committed: [Number(surface.dataset.sceneWidth), Number(surface.dataset.sceneHeight)], natural: [Number(drawing.closest<HTMLElement>('[data-snapshot-width]')?.dataset.snapshotWidth ?? surface.dataset.sceneWidth), Number(drawing.closest<HTMLElement>('[data-snapshot-height]')?.dataset.snapshotHeight ?? surface.dataset.sceneHeight)], identity: identities.get(drawing)!, blank, legendClipped,
        overflow: bounds.width > availableWidth + 1 || (bounds.height > availableHeight + 1 && !['auto','scroll','hidden','clip'].includes(style.overflowY)),
        labels: Number(cached?.dataset.labelCount ?? drawing.querySelectorAll('text').length),
        legend: Number(cached?.dataset.legendCount ?? drawing.querySelectorAll('[data-legend-swatch]').length),
        plot: metadata?.dataset.plotBounds ? JSON.parse(metadata.dataset.plotBounds) as number[] : geometry ? [geometry.x, geometry.y, geometry.width, geometry.height] : null})
    }
  }
  const sample = (event?: Event) => { const start = samples.length; measure(event); witness.stamp(samples.slice(start)) }
  const tick = () => { if (!sampling) return; sample(); requestAnimationFrame(tick) }
  document.addEventListener('blerg-chart-frame', sample)
  await commands.startChartCapture(name, !shotDir)
  requestAnimationFrame(tick)
  const view = mount(fixture.pane)
  const capture = async (state: string) => {
    if (!shotDir) return
    const result = await commands.chartScreenshot(`${name}-${state}`, fixture.width, fixture.width === 3840 ? 2160 : 1000)
    const image = new Image(); image.src = 'data:image/png;base64,' + result
    await image.decode(); expect(image.naturalWidth).toBe(fixture.width)
  }
  try {
    const rail = view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
    if (rail) fireEvent.click(rail)
    await waitFor(() => expectReady(view.container), {timeout: 15000})
    stage = 'transfer'
    fireEvent.click([...document.querySelectorAll<HTMLButtonElement>('[data-testid="artifact-card"] button')].find(button => button.textContent === 'View')!)
    expect(document.querySelector('.hosted-view:not([hidden]) [data-testid="artifact-loading"]')).toBeNull()
    const tab = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('.hosted-view:not([hidden])')
      expect(el).toBeTruthy()
      expectReady(el!)
      return el!
    }, {timeout: 15000})
    stage = 'roundtrip'
    const chartTab = document.querySelector<HTMLButtonElement>('.session-tab [role="tab"][aria-selected="true"]')!
    const generations = [...tab.querySelectorAll<HTMLElement>('.visual-surface')].map(el => el.dataset.sceneGeneration)
    const roots = [...tab.querySelectorAll('.visual-surface svg, .visual-surface canvas')]
    for (let trip = 0; trip < 2; trip++) {
      fireEvent.click(document.querySelector<HTMLButtonElement>('[data-tab-id="chat"]')!); sample()
      await nextFrame(); await nextFrame()
      fireEvent.click(chartTab); sample()
      await nextFrame(); await nextFrame()
      expect([...tab.querySelectorAll<HTMLElement>('.visual-surface')].map(el => el.dataset.sceneGeneration)).toEqual(generations)
      expect([...tab.querySelectorAll('.visual-surface svg, .visual-surface canvas')]).toEqual(roots)
    }
    const beforeFont = [...tab.querySelectorAll<HTMLElement>('.visual-surface')].map(el => el.dataset.sceneGeneration)
    document.fonts.dispatchEvent(Object.assign(new Event('loadingdone'), {fontfaces: [new FontFace('Unrelated family', 'local(sans-serif)')]}))
    await nextFrame(); await nextFrame()
    expect([...tab.querySelectorAll<HTMLElement>('.visual-surface')].map(el => el.dataset.sceneGeneration)).toEqual(beforeFont)
    await capture('ready')
    if (fixture.name === 'dashboard') {
      stage = 'height'
      const card = tab.querySelector<HTMLElement>('.dashboard-chart')!
      const surface = card.querySelector<HTMLElement>('.visual-surface')!
      const before = Number(surface.dataset.sceneHeight)
      card.style.height = '340px'
      await waitFor(() => expect(Number(surface.dataset.sceneHeight)).toBeGreaterThan(before))
      card.style.height = ''
      await waitFor(() => expect(Number(surface.dataset.sceneHeight)).toBe(before))
    }
    stage = 'drag'
    // Resize the real app's owning pane continuously; its chart bodies receive actual RO entries.
    // LayeredCharts.browser.test.tsx separately proves splitter pointer delivery.
    for (let step = 0; step < 12; step++) {
      view.container.firstElementChild!.setAttribute('style', `display:flex;height:100vh;width:${fixture.pane - 8 * step}px;max-width:100%;transform:translateZ(0)`)
      await nextFrame()
    }
    stage = 'grow'
    for (let step = 11; step >= 0; step--) {
      view.container.firstElementChild!.setAttribute('style', `display:flex;height:100vh;width:${fixture.pane - 8 * step}px;max-width:100%;transform:translateZ(0)`)
      await nextFrame()
    }
    if (fixture.name !== 'dashboard') {
      stage = 'narrow'
      view.container.firstElementChild!.setAttribute('style', 'display:flex;height:100vh;width:260px;max-width:100%;transform:translateZ(0)')
      await waitFor(() => expectReady(tab)); await nextFrame(); await nextFrame()
    }
    for (const title of tab.querySelectorAll<SVGGraphicsElement>('[data-scene-key="heading"] text')) {
      const root = title.ownerSVGElement!
      expect(title.getBBox().x + title.getBBox().width).toBeLessThanOrEqual(Number(root.getAttribute('width')) + 1)
    }
    await capture('drag')
    view.container.firstElementChild!.setAttribute('style', `display:flex;height:100vh;width:${fixture.pane}px;max-width:100%;transform:translateZ(0)`)
    stage = 'settled'
    await waitFor(() => expectReady(tab), {timeout: 15000})
    stage = 'font'
    document.fonts.dispatchEvent(Object.assign(new Event('loadingdone'), {fontfaces: []}))
    await nextFrame()
    stage = 'theme'
    document.documentElement.setAttribute('data-theme', fixture.theme === 'dark' ? 'colorblind-dark' : 'dark')
    await waitFor(() => expectReady(tab), {timeout: 15000})
    stage = 'arrival'
    const next = structuredClone(fixture.spec)
    // An inline data correction arrives through the same published-file transport as the app.
    if ('data' in next) { const row = next.data.values[0] as Record<string, unknown>; row['price' in row ? 'price' : 'n'] = 149 }
    else { next.cards[0].title = 'Workers running · updated snapshot'; (next.cards[0].data.rows.at(-1) as Record<string, unknown>).n = 9 }
    const drawingBefore = [...tab.querySelectorAll('.visual-surface svg')].map(svg => svg.innerHTML).join('')
    source = JSON.stringify(next)
    const revision = {...liveFile, size: source.length, updated_at: `2026-11-04T13:0${cases.indexOf(fixture)}:00.000Z`}
    await act(async () => { Object.assign(liveFile, revision); useTranscriptStore.getState().applyLiveArtifact('sess-a', revision) })
    await waitFor(() => expect(tab.querySelector('[data-testid="viewer-live-updated"] time')?.getAttribute('datetime')).toBe(revision.updated_at), {timeout: 15000})
    await waitFor(() => expectReady(tab), {timeout: 15000})
    if (fixture.name === 'dashboard') {
      await waitFor(() => expect(tab.querySelector('[aria-label="Workers running · updated snapshot"] .dashboard-number')?.textContent).toBe('9'))
    } else {
      await waitFor(() => expect([...tab.querySelectorAll('.visual-surface svg')].map(svg => svg.innerHTML).join('')).not.toBe(drawingBefore))
    }
    await nextFrame()
    await capture('arrival')
    expect(samples.length).toBeGreaterThan(12)
    expect(samples.filter(sample => sample.blank || sample.overflow || sample.legendClipped || sample.sizeMismatch).slice(0, 3)).toEqual([])
    expect(samples.some(sample => sample.stage === 'arrival')).toBe(true)
    expect(samples.filter(sample => sample.state === 'exact').every(sample => sample.labels > 0 && sample.plot?.every(Number.isFinite))).toBe(true)
    const lastGeneration = new Map<number, number>()
    for (const item of samples) {
      if (item.surface && item.generation > 0) {
        expect(item.generation).toBeGreaterThanOrEqual(lastGeneration.get(item.surface) ?? 0)
        lastGeneration.set(item.surface, item.generation)
      }
      if (item.state === 'exact') {
        expect(Math.abs(item.scene[0] - item.committed![0])).toBeLessThanOrEqual(1)
        expect(Math.abs(item.scene[1] - item.committed![1])).toBeLessThanOrEqual(1)
      } else if (item.state === 'retained') {
        expect(item.scene[0]).toBeLessThanOrEqual(item.box[0] + 1)
        expect(item.scene[1]).toBeLessThanOrEqual(item.box[1] + 1)
        expect(Math.abs(item.scene[0] / item.scene[1] - item.natural![0] / item.natural![1])).toBeLessThan(.02)
      }
    }
    if (fixture.name === 'bar') expect(samples.filter(sample => ['exact','retained'].includes(sample.state)).every(sample => sample.legend === 8)).toBe(true)
    completed = true
  } finally {
    sampling = false
    document.removeEventListener('blerg-chart-frame', sample)
    try {
      const capture = await commands.stopChartCapture(samples)
      if (!shotDir) await commands.chartFrames(name, samples)
      if (completed) expect(capture.truncated).toBe(false)
      if (completed && !shotDir) {
        expect(capture.count).toBeGreaterThan(0)
        expect(capture.terminal).toBe(true)
        expect(capture.analyzed).toBeGreaterThan(0)
        expect(capture.blankFrames).toEqual([])
      }
    } finally {
      witness.remove()
      view.unmount()
      preview.style.transform = previousTransform
    }
  }
}, 45000)


// Slice C: real publication transport, persistent native scene, actual motion/media policy.
for (const fixture of [
  {mark:'line',width:390,reduced:false}, {mark:'bar',width:520,reduced:false},
  {mark:'area',width:1024,reduced:false}, {mark:'point',width:3840,reduced:false},
  {mark:'point',width:390,reduced:true},
]) it(`live native ${fixture.mark} at ${fixture.width}${fixture.reduced ? ' reduced' : ''}`, async()=>{
  await page.viewport(fixture.width, fixture.width===3840 ? 2160 : 1000)
  const client=cdp() as unknown as {send:(method:string,params:object)=>Promise<unknown>}
  await client.send('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:fixture.reduced?'reduce':'no-preference'}]})
  document.documentElement.setAttribute('data-theme','dark')
  const preview=window.frameElement!.parentElement as HTMLElement, previous=preview.style.transform
  preview.style.transform='none'
  const name=`live-${fixture.mark}-${fixture.width}${fixture.reduced ? '-reduced' : ''}`
  // A small authored bar height exercises the native minimum plot without aspect-fit narrowing.
  const spec={title:'Live native updates · simulated',...(fixture.mark==='bar'?{height:32}:{width:640,height:360}),mark:fixture.mark,
    data:{values:[{id:'a',x:fixture.mark==='bar'?'A':0,n:1,g:'First'},{id:'b',x:fixture.mark==='bar'?'B':5,n:3,g:'First'},{id:'c',x:fixture.mark==='bar'?'C':10,n:2,g:'First'}]},
    encoding:{x:{field:'x',type:fixture.mark==='bar'?'nominal':'quantitative'},y:{field:'n',type:'quantitative'},...(fixture.mark==='point'?{}:{color:{field:'g',type:'nominal'}}),tooltip:[{field:'id',type:'nominal'},{field:'n',type:'quantitative'}]}}
  const revisionIndex=fixture.reduced ? 4 : ['line','bar','area','point'].indexOf(fixture.mark)
  source=JSON.stringify(spec);liveFile.name=`native-${fixture.mark}.vl.json`;liveFile.size=source.length;liveFile.updated_at=`2026-11-04T14:0${revisionIndex}:00.000Z`
  seedLiveArtifact()
  await commands.startChartCapture(name,!shotDir)
  const view=mount(fixture.width)
  const witness = chartFrameWitness(!shotDir)
  const samples: {frame?:number;timestamp:number;stage:string;state:string;hosted:boolean;surface:number;rect?:number[];progress?:number;motion?:string;generation?:number;geometry?:string;identity?:boolean}[]=[]
  let root: SVGSVGElement | undefined, stage='mount', completed=false
  const sample=()=>{
    if(!root)return
    const bounds=root.getBoundingClientRect(), surface=root.closest<HTMLElement>('.visual-surface')
    if(!surface)return
    const common={timestamp:performance.timeOrigin+performance.now(),stage,hosted:true,surface:1}
    const start = samples.length
    samples.push({...common,state:'view'})
    samples.push({...common,state:'exact',rect:[bounds.x,bounds.y,bounds.width,bounds.height],progress:Number(root.dataset.updateProgress ?? 1),motion:root.dataset.updateState ?? 'initial',generation:Number(surface.dataset.sceneGeneration),geometry:[...root.querySelectorAll('[data-datum-key]')].map(node=>node.getAttribute('d') ?? node.getAttribute('height') ?? node.getAttribute('cy')).join('|'),identity:surface.querySelector('svg')===root})
    witness.stamp(samples.slice(start))
  }
  try {
    const rail=view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]');if(rail)fireEvent.click(rail)
    await waitFor(()=>expect(view.container.querySelector('[data-scene-state="exact"]')).toBeTruthy(),{timeout:15000})
    fireEvent.click([...document.querySelectorAll<HTMLButtonElement>('[data-testid="artifact-card"] button')].find(button=>button.textContent==='View')!)
    root=await waitFor(()=>{const candidate=document.querySelector<SVGSVGElement>('.hosted-view:not([hidden]) .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]');expect(candidate).toBeTruthy();return candidate!},{timeout:15000})
    const tab=root.closest('.hosted-view')!, axes=root.querySelector('[data-scene-key="axes"]'), legend=root.querySelector('[data-scene-key="legend"]'), mark=root.querySelector('[data-datum-key]')!
    if(fixture.mark==='bar') {
      const surface=root.closest<HTMLElement>('.visual-surface')!,originalHeight=surface.clientHeight,originalWidth=surface.clientWidth
      surface.style.cssText='flex:1 1 auto;height:48px;max-height:48px;min-height:0;box-sizing:border-box'
      await waitFor(()=>expect(surface.dataset.sceneOverflow).toBe('scroll'))
      expect(getComputedStyle(surface).overflowY).toBe('auto');expect(surface.tabIndex).toBe(0)
      expect(surface.getBoundingClientRect().height).toBe(48);expect(surface.clientWidth).toBe(originalWidth)
      expect(root.getBoundingClientRect().width).toBeGreaterThan(400)
      expect(root.getBoundingClientRect().height).toBeGreaterThan(surface.clientHeight)
      surface.scrollTop=surface.scrollHeight;expect(surface.scrollTop).toBeGreaterThan(0);surface.scrollTop=0
      if(shotDir)await commands.chartScreenshot(`${name}-short`,fixture.width,1000)
      surface.removeAttribute('style')
      await waitFor(()=>{expect(surface.clientHeight).toBe(originalHeight);expect(surface.dataset.sceneOverflow).toBe('fit')})
    }
    const generation=Number(root.closest<HTMLElement>('.visual-surface')!.dataset.sceneGeneration)
    expect(axes).toBeTruthy()
    document.addEventListener('blerg-chart-frame',sample);sample()
    if(shotDir)await commands.chartScreenshot(`${name}-ready`,fixture.width,fixture.width===3840?2160:1000)
    const next=structuredClone(spec);next.data.values=[{...next.data.values[2],n:5},{...next.data.values[0],n:7},{id:'d',x:fixture.mark==='bar'?'D':15,n:4,g:'First'}]
    source=JSON.stringify(next);stage='arrival'
    const revision={...liveFile,size:source.length,updated_at:`2026-11-04T15:0${revisionIndex}:00.000Z`}
    await act(async()=>{Object.assign(liveFile,revision);useTranscriptStore.getState().applyLiveArtifact('sess-a',revision)})
    await waitFor(()=>{expect(tab.querySelector('.visual-surface svg')).toBe(root);expect(Number(root!.closest<HTMLElement>('.visual-surface')!.dataset.sceneGeneration)).toBeGreaterThan(generation)},{timeout:15000})
    await waitFor(()=>expect(root!.dataset.updateState).toBe('idle'),{timeout:15000});sample()
    expect(tab.querySelector('.visual-surface svg')).toBe(root)
    expect(root.contains(mark)).toBe(true);expect(root.querySelector('[data-scene-key="axes"]')).toBe(axes)
    if(legend)expect(root.querySelector('[data-scene-key="legend"]')).toBe(legend)
    expect(samples.filter(item=>item.state==='exact').every(item=>item.identity)).toBe(true)
    const motion=samples.filter(item=>item.motion==='animating')
    if(fixture.reduced)expect(motion).toHaveLength(0)
    else {expect(motion.some(item=>item.progress!>0 && item.progress!<1)).toBe(true);expect(new Set(motion.map(item=>item.geometry)).size).toBeGreaterThan(2)}
    expect(tab.querySelector('table')).toHaveTextContent('7')
    if(shotDir)await commands.chartScreenshot(`${name}-arrival`,fixture.width,fixture.width===3840?2160:1000)
    completed=true
  } finally {
    document.removeEventListener('blerg-chart-frame',sample)
    try {
      const capture=await commands.stopChartCapture(samples)
      if(completed){expect(capture.truncated).toBe(false);if(!shotDir){expect(capture.analyzed).toBeGreaterThan(0);expect(capture.blankFrames).toEqual([])}}
      if(!shotDir)await commands.chartFrames(name,samples)
    } finally {witness.remove();view.unmount();preview.style.transform=previous;await client.send('Emulation.setEmulatedMedia',{features:[]})}
  }
},45000)

// BLG-483: shipped SessionDetail path, including transfer to a hosted tab and repaint.
for (const width of [390, 3840]) it(`chart polish hover and first-render lifecycle at ${width}`, async () => {
  await page.viewport(width, width === 3840 ? 2160 : 1000)
  const client = cdp() as unknown as {send:(method:string,params:object)=>Promise<unknown>}
  await client.send('Emulation.setEmulatedMedia', {features:[{name:'prefers-reduced-motion',value:'no-preference'}]})
  const preview = window.frameElement!.parentElement as HTMLElement, previous = preview.style.transform
  preview.style.transform = 'none'
  source = JSON.stringify({...bar, title:`Completed work · entrance ${width}`, encoding:{...bar.encoding,tooltip:[{field:'team',type:'nominal'},{field:'n',type:'quantitative'}]}})
  liveFile.name = `polish-${width}.vl.json`; liveFile.size = source.length; liveFile.updated_at = width === 390 ? '2026-11-04T16:00:00.000Z' : '2026-11-04T17:00:00.000Z'
  seedLiveArtifact()
  const animate = vi.spyOn(Element.prototype, 'animate')
  if (shotDir) await commands.startChartCapture(`polish-${width}`, false)
  const view = mount(width)
  try {
    const rail = view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]'); if (rail) fireEvent.click(rail)
    const initial = await waitFor(() => {
      const root = view.container.querySelector<SVGSVGElement>('.visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')
      expect(root).toBeTruthy(); return root!
    }, {timeout:15000})
    expect(animate.mock.calls.length).toBeGreaterThan(0)
    expect(animate.mock.calls.some(([frames]) => JSON.stringify(frames).includes('scaleY(0)'))).toBe(true)
    const count = animate.mock.calls.length
    initial.dispatchEvent(new Event('blerg-chart-before-snapshot'))
    expect(initial.getAnimations({subtree:true})).toHaveLength(0)
    fireEvent.click([...document.querySelectorAll<HTMLButtonElement>('[data-testid="artifact-card"] button')].find(button => button.textContent === 'View')!)
    const root = await waitFor(() => {
      const el = document.querySelector<SVGSVGElement>('.hosted-view:not([hidden]) .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')
      expect(el).toBeTruthy(); return el!
    }, {timeout:15000})
    expect(animate.mock.calls).toHaveLength(count)
    for (const theme of ['dark', 'colorblind-dark']) {
      document.documentElement.setAttribute('data-theme', theme)
      // Theme resolution creates a temporary probe; keep it outside waitFor's mutation observer.
      const accent = readVisualTheme(root.parentElement!).accent
      await waitFor(() => expect(root.getAttribute('color')).toBe(accent))
      root.focus()
      fireEvent.keyDown(root, {key:'ArrowRight'})
      expect(root.querySelectorAll('[data-chart-active]')).toHaveLength(1)
      const bars = [...root.querySelectorAll<SVGGraphicsElement>('[data-datum-key]')]
      expect(bars.some(mark => getComputedStyle(mark).filter === 'opacity(0.6)')).toBe(true)
      const mark = bars[2], bounds = mark.getBoundingClientRect()
      fireEvent.pointerMove(root, {clientX:bounds.x + bounds.width/2,clientY:bounds.y + bounds.height/2})
      expect(mark).toHaveAttribute('data-chart-active','true')
      expect(root.parentElement!.querySelector('.visual-tooltip')).toHaveTextContent('Team 3')
      if (shotDir) {
        const data = await commands.chartScreenshot(`polish-${width}-${theme}-hover`,width,width === 3840 ? 2160 : 1000)
        const image = new Image(); image.src = 'data:image/png;base64,'+data; await image.decode()
        expect(image.naturalWidth).toBe(width)
      }
    }
    const chartTab = document.querySelector<HTMLButtonElement>('.session-tab [role="tab"][aria-selected="true"]')!
    fireEvent.click(document.querySelector<HTMLButtonElement>('[data-tab-id="chat"]')!)
    fireEvent.click(chartTab)
    await page.viewport(width - 10, 1000); await nextFrame(); await nextFrame()
    expect(animate.mock.calls).toHaveLength(count)
  } finally {
    if (shotDir) await commands.stopChartCapture([])
    view.unmount(); preview.style.transform = previous
    await client.send('Emulation.setEmulatedMedia', {features:[]})
  }
}, 45000)
