// BLG-470/506: real chart cards and quiet live timestamps, mounted through the real
// SessionDetail page the way PopOutMessage.browser.test.tsx does (the
// real SessionDetail page, its real CSS and dark theme), not a bare component in an unstyled host.
import { fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { commands, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { templateDashboard, VISUAL_TEMPLATES, clearPreviewCache, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
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


const timeline={title:'Worker status · UTC · simulated',mark:'rect',data:{values:Array.from({length:9},(_,i)=>({id:String(i),worker:`Worker ${i+1}`,start:'2026-11-04T09:00:00Z',end:`2026-11-04T${String(10+i%3).padStart(2,'0')}:00:00Z`,status:`State ${i+1}`,open:i===8}))},encoding:{x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'},y:{field:'worker',type:'ordinal'},color:{field:'status',type:'nominal'},tooltip:[{field:'status',type:'nominal'},{field:'start',type:'temporal'},{field:'end',type:'temporal'}]}}
const histogram=VISUAL_TEMPLATES.find(template=>template.id==='histogram')!.card
const histogramSpec={title:histogram.title,...histogram.spec,data:{values:histogram.data.kind==='inline'?histogram.data.rows:[]}}
for(const fixture of [{name:'heatmap',spec:dashboard,count:35},{name:'timeline',spec:timeline,count:9},{name:'histogram',spec:histogramSpec,count:3},{name:'catalog',spec:templateDashboard(),count:0}]) for(const width of [390,520,1024,1920,3840]) for(const theme of ['dark','colorblind-dark']) it(`${fixture.name} ${width} ${theme} through the session preview and file tab`,async()=>{
 await page.viewport(width,width===3840?2160:1000)
 document.documentElement.setAttribute('data-theme',theme)
 source=JSON.stringify(fixture.spec);liveFile.name=fixture.name+(fixture.name==='catalog'?'.dash.json':'.vl.json');liveFile.size=source.length
 seedLiveArtifact()
 const view=mount(width)
 const rail=view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
 if(rail)fireEvent.click(rail)
 if(fixture.name==='catalog')await waitFor(()=>expect(view.container.querySelector('.dashboard-chart [data-chart-renderer="blerg"]')).toBeTruthy(),{timeout:15000})
 else await waitFor(()=>expect(view.container.querySelectorAll('svg [data-datum-key]').length).toBe(fixture.count),{timeout:15000})
 fireEvent.click([...document.querySelectorAll<HTMLButtonElement>('[data-testid="artifact-card"] button')].find(button=>button.textContent==='View')!)
 await waitFor(()=>expect(document.querySelector('.hosted-view:not([hidden]) svg [data-datum-key]')).toBeTruthy(),{timeout:15000})
 if(fixture.name==='catalog'){
 for(const chart of document.querySelectorAll<HTMLElement>('.hosted-view:not([hidden]) .dashboard-chart')){
 chart.scrollIntoView({block:'center'})
 await waitFor(()=>expect(chart.querySelector('[data-chart-renderer="blerg"]'),chart.parentElement?.getAttribute('aria-label') ?? '').toBeTruthy(),{timeout:3000})
 }
 document.querySelector('.hosted-view:not([hidden]) .dashboard-card')?.scrollIntoView({block:'start'})
 }
 await document.fonts.ready
 const surface=document.querySelector('.hosted-view:not([hidden]) .visual-surface')!
 expect(surface.getBoundingClientRect().width).toBeLessThanOrEqual(width)
 expect(surface.querySelector('svg')!.outerHTML).not.toMatch(/NaN|Infinity/)
 if(shotDir){
 const name=`${fixture.name}-${width}-${theme}`
 await commands.startChartCapture(name,false)
 const result=await commands.chartScreenshot(name,width,width===3840?2160:1000)
 const image=new Image();image.src='data:image/png;base64,'+result;await image.decode();expect(image.naturalWidth).toBe(width)
 await commands.stopChartCapture([])
 }
})
