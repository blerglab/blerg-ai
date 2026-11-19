// Original unmodified NVDA snapshot retained for fallback parity; the following revision is simulated without changing its producer.
import nvdaSpec from '../test/nvdaOriginalSpec.json'
// BLG-470/506: real chart cards and quiet live timestamps, mounted through the real
// SessionDetail page the way PopOutMessage.browser.test.tsx does (the
// real SessionDetail page, its real CSS and dark theme), not a bare component in an unstyled host.
import {act, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { commands, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { VISUAL_TEMPLATES, clearPreviewCache, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
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
const dashboard = {title:'Merges by day and hour · simulated',mark:'rect',data:{values:Array.from({length:35},(_,i)=>({day:['Mon','Tue','Wed','Thu','Fri'][i%5],hour:String(9+Math.floor(i/5)),count:i%9}))},encoding:{x:{field:'day',type:'ordinal',sort:['Mon','Tue','Wed','Thu','Fri']},y:{field:'hour',type:'ordinal',sort:['9','10','11','12','13','14','15']},color:{field:'count',type:'quantitative'},tooltip:[{field:'count',type:'quantitative'}]}}
const chartSource = JSON.stringify(dashboard)
let source = chartSource
const liveFile = { id: LIVE_ID, name: 'heatmap.vl.json', size: chartSource.length, content_type: 'application/json', view: 'json', origin: 'agent' as const, version: 1, live: true, updated_at: new Date().toISOString() }

beforeEach(() => {
  clearPreviewCache()
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


const renderCalls=vi.hoisted(()=>({count:0}))
vi.mock('../../../../packages/chat/src/visual/render',async original=>{
 const actual=await original<typeof import('../../../../packages/chat/src/visual/render')>()
 return {...actual,renderVisual:(...args:Parameters<typeof actual.renderVisual>)=>{renderCalls.count++;return actual.renderVisual(...args)}}
})
async function revision(spec:unknown,index:number,updatedAt?:string){
 source=JSON.stringify(spec)
 const next={...liveFile,size:source.length,updated_at:updatedAt ?? `2026-11-04T15:0${index}:00.000Z`}
 await act(async()=>{Object.assign(liveFile,next);useTranscriptStore.getState().applyLiveArtifact('sess-a',next)})
}
async function openFile(keyed=true){
 const view=mount(innerWidth)
 const rail=view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
 if(rail)fireEvent.click(rail)
 await waitFor(()=>expect(view.container.querySelector(keyed?'svg [data-datum-key]':'.visual-surface svg, .visual-surface canvas')).toBeTruthy(),{timeout:5000})
 fireEvent.click([...document.querySelectorAll<HTMLButtonElement>('[data-testid="artifact-card"] button')].find(button=>button.textContent==='View')!)
 return await waitFor(()=>{
  const tab=document.querySelector<HTMLElement>('.hosted-view:not([hidden])')!
  expect(tab?.querySelector(keyed?'.visual-surface svg [data-datum-key]':'.visual-surface svg, .visual-surface canvas')).toBeTruthy()
  expect(tab.querySelector<HTMLElement>('.visual-surface')?.dataset.sceneState).toBe('exact')
  return tab
 },{timeout:5000})
}
for(const id of ['heatmap','status-timeline','histogram']) for(const width of [390,520,1024,1920,3840]) for(const theme of ['dark','colorblind-dark']) it(`live ${id} ${width} ${theme}: rows retain DOM; config swaps; invalid revision keeps scene`,async()=>{
 await page.viewport(width,width===3840?2160:1000)
 document.documentElement.setAttribute('data-theme',theme)
 const card=VISUAL_TEMPLATES.find(template=>template.id===id)!.card
 const spec={...card.spec,title:card.title,data:{values:card.data.kind==='inline'?card.data.rows:[]}}
 source=JSON.stringify(spec);liveFile.name=id+'.vl.json';liveFile.size=source.length
 seedLiveArtifact()
 const tab=await openFile(),root=tab.querySelector<SVGSVGElement>('.visual-surface svg')!,axes=root.querySelector('[data-scene-key="axes"]'),legend=root.querySelector('[data-scene-key="legend"]')
 const titles=[...root.querySelectorAll('[data-axis-title]')]
 expect(titles.length).toBeGreaterThanOrEqual(2)
 const marks=[...root.querySelectorAll('[data-datum-key]')],calls=renderCalls.count,generation=Number(root.closest<HTMLElement>('.visual-surface')!.dataset.sceneGeneration)
 const next=structuredClone(spec)
 const field=id==='heatmap'?'merges':id==='histogram'?'count':'end'
 next.data.values=next.data.values.map(row=>({...row,[field]:id==='status-timeline'?new Date(Date.parse(String(row[field]))+1800000).toISOString():typeof row[field]==='number'?Number(row[field])+3:row[field]})).reverse()
 await revision(next,1)
 await waitFor(()=>expect(Number(root.closest<HTMLElement>('.visual-surface')?.dataset.sceneGeneration)).toBeGreaterThan(generation),{timeout:5000})
 await waitFor(()=>expect(root.dataset.updateState).toBe('idle'),{timeout:5000})
 expect(tab.querySelector('.visual-surface svg')).toBe(root)
 expect(renderCalls.count).toBe(calls)
 expect(root.querySelector('[data-scene-key="axes"]')).toBe(axes)
 expect([...root.querySelectorAll('[data-axis-title]')]).toEqual(titles)
 if(legend)expect(root.querySelector('[data-scene-key="legend"]')).toBe(legend)
 for(const mark of marks)expect(root.contains(mark)).toBe(true)
 // Geometry commits before the viewer's React freshness label commits.
 await waitFor(()=>expect(tab.querySelector('[data-testid="viewer-live-updated"] time')).toHaveAttribute('datetime','2026-11-04T15:01:00.000Z'))
 const corrupt=structuredClone(next);corrupt.data.values.push({...corrupt.data.values[0]})
 const refused=vi.spyOn(console,'warn').mockImplementation(()=>{})
 await revision(corrupt,2)
 await waitFor(()=>expect(tab.textContent).toContain('Chart update unavailable'),{timeout:5000})
 expect(tab.querySelector('.visual-surface svg')).toBe(root)
 expect(renderCalls.count).toBe(calls)
 expect(tab.querySelector('[data-testid="viewer-live-updated"] time')).toHaveAttribute('datetime','2026-11-04T15:01:00.000Z')
 refused.mockRestore()
 const config={...next,title:card.title+' · revised'}
 await revision(config,3)
 await waitFor(()=>expect(tab.querySelector('.visual-surface svg')).not.toBe(root),{timeout:5000})
 expect(tab.textContent).toContain('revised')
 await waitFor(()=>expect(tab.querySelector('[data-testid="viewer-live-updated"] time')).toHaveAttribute('datetime','2026-11-04T15:03:00.000Z'))
 const swapped=tab.querySelector('.visual-surface svg')
 const warn=vi.spyOn(console,'warn').mockImplementation(()=>{})
 await revision({...config,data:{url:'https://invalid.example/refused'}},4)
 await waitFor(()=>expect(tab.textContent).toContain('Could not update preview'),{timeout:5000})
 expect(tab.querySelector('.visual-surface svg')).toBe(swapped)
 expect(tab.querySelector('[data-testid="viewer-live-updated"] time')).toHaveAttribute('datetime','2026-11-04T15:03:00.000Z')
 warn.mockRestore()
 if(shotDir){
  const name=`live-${id}-${width}-${theme}`
  await commands.startChartCapture(name,false)
  const bytes=await commands.chartScreenshot(name,width,width===3840?2160:1000)
  const image=new Image();image.src='data:image/png;base64,'+bytes;await image.decode();expect(image.naturalWidth).toBe(width)
  await commands.stopChartCapture([])
 }
})


for(const width of [390,1920,3840])it(`NVDA follows staged Vega revisions and labels freshness truthfully at ${width}`,async()=>{
 await page.viewport(width,1000)
 const spec=structuredClone(nvdaSpec)
 // Keep this staged-Vega lifecycle case on a deliberately unsupported native guide.
 for(const layer of spec.layer)layer.encoding.color.legend.orient='bottom'
 source=JSON.stringify(spec);liveFile.name='nvda-today.vl.json';liveFile.size=source.length
 liveFile.updated_at='2026-11-05T17:21:00.000Z'
 seedLiveArtifact()
 const tab=await openFile(false)
 const label=()=>tab.querySelector('[data-testid="viewer-live-updated"]')!
 await waitFor(()=>expect(label()).toHaveTextContent('Live · updated'))
 expect(label()).not.toHaveTextContent(/Static snapshot|unsupported-live/)
 const capture=async(state:string)=>{
  if(!shotDir)return
  const name=`nvda-${state}-${width}`
  await commands.startChartCapture(name,false)
  const bytes=await commands.chartScreenshot(name,width,1000)
  const image=new Image();image.src='data:image/png;base64,'+bytes;await image.decode();expect(image.naturalWidth).toBe(width)
  await commands.stopChartCapture([])
 }
 const root=tab.querySelector('.visual-surface svg, .visual-surface canvas')
 await capture('before')
 const next=structuredClone(spec)
 next.title.text='NVDA $186.20 · live · as of 12:22 ET'
 next.layer[1].data.values.push({time:'2026-11-05T12:22:00',value:186.20,series:'NVDA'})
 await revision(next,1,'2026-11-05T17:22:00.000Z')
 await waitFor(()=>expect(tab.querySelector('.visual-surface svg, .visual-surface canvas')).not.toBe(root),{timeout:10000})
 await waitFor(()=>expect(label().querySelector('time')).toHaveAttribute('datetime','2026-11-05T17:22:00.000Z'))
 expect(tab.querySelector('[data-chart-renderer="vega"]')).toHaveAttribute('title', next.title.text)
 expect(label()).not.toHaveTextContent(/Static snapshot|unsupported-live/)
 await capture('after')
 const retained=tab.querySelector('.visual-surface svg, .visual-surface canvas')
 const warn=vi.spyOn(console,'warn').mockImplementation(()=>{})
 await revision({...next,data:{url:'https://invalid.example/refused'}},2)
 await waitFor(()=>expect(tab.textContent).toContain('Could not update preview'),{timeout:5000})
 expect(tab.querySelector('.visual-surface svg, .visual-surface canvas')).toBe(retained)
 expect(label().querySelector('time')).toHaveAttribute('datetime','2026-11-05T17:22:00.000Z')
 warn.mockRestore()
})

it('a native time series remains native and follows in a narrow dashboard preview at 390',async()=>{
 await page.viewport(390,1000)
 const card=structuredClone(VISUAL_TEMPLATES.find(template=>template.id==='time-series')!.card)
 const dashboard={format:'blerg-dashboard',version:1,title:'Narrow native telemetry · simulated',cards:[card]}
 source=JSON.stringify(dashboard);liveFile.name='runtime.dash.json';liveFile.size=source.length
 seedLiveArtifact()
 const view=mount(390)
 const rail=view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
 if(rail)fireEvent.click(rail)
 const preview=await waitFor(()=>{
  const el=view.container.querySelector<HTMLElement>('[data-testid="artifact-preview-diagram"]')!
  expect(el?.querySelector('[data-chart-renderer="blerg"]')).toBeTruthy()
  return el
 },{timeout:5000})
 const root=preview.querySelector('svg')
 expect(preview.textContent).not.toContain('unsupported-live')
 const reads=()=>vi.mocked(fetch).mock.calls.filter(([input])=>String(input).includes('/artifacts/'+LIVE_ID+'/raw')).length
 const before=reads()
 const next=structuredClone(dashboard)
 if(next.cards[0].data.kind==='inline')next.cards[0].data.rows[0].latency=42
 await revision(next,1)
 await waitFor(()=>expect(reads()).toBeGreaterThan(before))
 expect(preview.querySelector('[data-chart-renderer="blerg"]')).toBeTruthy()
 expect(preview.querySelector('svg')).toBe(root)
 expect(preview.textContent).not.toContain('unsupported-live')
})

it('dashboard freshness follows its committed chart revision through refused rows and resize',async()=>{
 await page.viewport(390,1000)
 const card=structuredClone(VISUAL_TEMPLATES.find(template=>template.id==='heatmap')!.card)
 const dashboard={format:'blerg-dashboard',version:1,title:'Committed dashboard clock · simulated',cards:[card]}
 source=JSON.stringify(dashboard);liveFile.name='freshness.dash.json';liveFile.size=source.length
 seedLiveArtifact()
 const tab=await openFile(),root=tab.querySelector('.visual-surface svg')!
 const timestamp=()=>tab.querySelector('[data-testid="viewer-live-updated"] time')
 const next=structuredClone(dashboard)
 if(next.cards[0].data.kind==='inline')next.cards[0].data.rows[0].merges=12
 await revision(next,1)
 await waitFor(()=>expect(timestamp()).toHaveAttribute('datetime','2026-11-04T15:01:00.000Z'))
 const corrupt=structuredClone(next)
 if(corrupt.cards[0].data.kind==='inline')corrupt.cards[0].data.rows.push({...corrupt.cards[0].data.rows[0]})
 const warning=vi.spyOn(console,'warn').mockImplementation(()=>{})
 try{
  await revision(corrupt,2)
  await waitFor(()=>expect(tab.textContent).toContain('Chart update unavailable'))
  await page.viewport(1024,1000)
  await waitFor(()=>expect(tab.querySelector('.visual-surface')?.getAttribute('data-scene-state')).toBe('exact'))
  expect(tab.querySelector('.visual-surface svg')).toBe(root)
  expect(timestamp()).toHaveAttribute('datetime','2026-11-04T15:01:00.000Z')
 }finally{warning.mockRestore()}
})

it('advances two-card dashboard freshness when only A changes and on a title-only revision',async()=>{
 await page.viewport(390,1000)
 const a=structuredClone(VISUAL_TEMPLATES.find(template=>template.id==='heatmap')!.card),b=structuredClone(VISUAL_TEMPLATES.find(template=>template.id==='histogram')!.card)
 const dashboard={format:'blerg-dashboard',version:1,title:'Two committed cards',cards:[a,b]}
 source=JSON.stringify(dashboard);liveFile.name='two-cards.dash.json';liveFile.size=source.length;seedLiveArtifact()
 const tab=await openFile(),time=()=>tab.querySelector('[data-testid="viewer-live-updated"] time')
 const roots=[...tab.querySelectorAll('.visual-surface svg')]
 const next=structuredClone(dashboard)
 if(next.cards[0].data.kind==='inline')next.cards[0].data.rows[0].merges=15
 await revision(next,1)
 await waitFor(()=>expect(time()).toHaveAttribute('datetime','2026-11-04T15:01:00.000Z'))
 await revision({...next,title:'Revised dashboard heading'},2)
 await waitFor(()=>expect(time()).toHaveAttribute('datetime','2026-11-04T15:02:00.000Z'))
 expect([...tab.querySelectorAll('.visual-surface svg')]).toEqual(roots)
})
