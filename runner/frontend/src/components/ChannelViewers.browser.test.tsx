import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent, commands } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent, type ArtifactInfo } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const patch = 'diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-old\n+new\n'
const pdf = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj
4 0 obj<</Length 52>>stream
BT /F1 18 Tf 20 120 Td (Launch campaign) Tj ET
endstream endobj
5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
trailer<</Root 1 0 R/Size 6>>
%%EOF`
const deck = '<!doctype html><html><body style="background:#183344;color:white;padding:48px;font:24px sans-serif"><h1>Autumn launch</h1><p>Marketing · Release campaign</p><p>01 / 03</p></body></html>'
const sha = 'abcdef1234567890abcdef1234567890abcdef12'
const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><rect width="200" height="100" fill="currentColor"/></svg>'
const artifacts: ArtifactInfo[] = [
 { id:'deck', name:'launch.html', view:'html', content_type:'text/html', size:deck.length, live:true, updated_at:'2026-11-01T12:00:00Z' },
 { id:'diff', name:'campaign.patch', view:'diff', content_type:'text/x-diff', size:patch.length },
 { id:'pdf', name:'campaign.pdf', view:'pdf', content_type:'application/pdf', size:pdf.length },
 { id:'md', name:'brief.md', view:'markdown', content_type:'text/markdown', size:100 },
 { id:'img', name:'campaign.svg', view:'image', content_type:'image/svg+xml', size:svg.length },
 { id:'chart', name:'results.vl.json', view:'json', content_type:'application/json', size:100 },
 { id:'diagram', name:'plan.mmd', view:'text', content_type:'text/plain', size:30 },
]
let revision = 1
let commitBearing = false
function event(session: string, payload: ArtifactInfo, seq: number): AgentEvent { return { session_id:session, client_event_id:`${session}-${seq}`, seq, ts:'2026-11-01T12:00:00Z', kind:'artifact', payload } as AgentEvent }
beforeEach(() => {
 localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY,'0'); revision = 1; commitBearing = false
 useTranscriptStore.setState({sessions:{}})
 useSessionStore.setState({daemons:[makeDaemon()],sessions:[makeSession({id:'main',status:'idle',title:'Launch workspace'}),makeSession({id:'channel',status:'idle',title:'Marketing',parent_session_id:'main'}),makeSession({id:'worker',status:'idle',title:'Campaign worker',parent_session_id:'main',started_by:{kind:'session'}})]})
 for(const session of ['main','channel','worker']) {
  artifacts.forEach((artifact,i)=>useTranscriptStore.getState().ingest(event(session,artifact,i+1)))
  const store = useTranscriptStore.getState()
  store.ingest({ ...event(session,artifacts[0],8),kind:'ticket',payload:{action:'pin',ticket:{source:'blerg-board',board_id:'board-a',key:'#743',title:'Campaign'}} } as AgentEvent)
  store.ingest({ ...event(session,artifacts[0],9),kind:'checklist',payload:{op:'tick',items:[{text:'Ship campaign',done:true,commits:[sha]}]} } as AgentEvent)
  store.ingest({ ...event(session,artifacts[0],10),kind:'assistant_text',payload:{text:'Read `app.ts` for the source.',done:true} } as AgentEvent)
  store.ingestReplayDone(session,{lastSeq:10,hasMore:false})
 }
 vi.stubGlobal('fetch',vi.fn(async(input: RequestInfo | URL)=>{
  const url = String(input)
  if(url.endsWith('/live')) return json({session_id:url.split('/').at(-2),through_seq:0,generated_at:'2026-11-01T12:00:00Z',asks:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:0,running:0},can_release_prompt:true})
  if(url.endsWith('/shell-capability')) return json({available:false})
  if(url.endsWith('/categories')) return json({categories:[]})
  if(url.includes('/workspace?')) return json({members:[],result:{ok:true,entries:[]}})
  if(url.includes('/commits/') && url.includes('/file?')) { const side=new URL(url,location.origin).searchParams.get('side'); return new Response(`complete ${side} campaign source`) }
  if(url.includes('/commits/') && url.endsWith('/diff')) return new Response(`commit ${sha}\n\n${patch}`)
  if(url.includes('/workspace-file?')) return json({path:'app.ts',view:'text',content_type:'text/plain',content_b64:btoa('export const campaign = true')})
  if(url.endsWith('/artifacts')) return json({artifacts:artifacts.map(a=>a.id==='deck'?{...a,updated_at:`2026-11-01T12:0${revision-1}:00Z`}:a)})
  if(url.endsWith('/raw')) {
   const id=url.split('/').at(-2)
   return new Response(id==='deck'?deck.replace('Autumn launch',revision===1?'Autumn launch':'Winter launch'):id==='diff'?(commitBearing?`commit ${sha}\n\n${patch}`:patch):id==='pdf'?pdf:id==='img'?svg:id==='chart'?JSON.stringify({data:{values:[{x:'Launch',y:3}]},mark:'bar',encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'quantitative'}}}):id==='diagram'?'graph TD; Plan-->Launch':'# Campaign brief\n\nReady for release.')
  }
  return json([])
 }))
})
afterEach(()=>{invalidateChatTransport();vi.unstubAllGlobals();document.documentElement.removeAttribute('data-theme')})
function mount(session='channel') { return render(<MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{height:'100vh'}}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></CommandPaletteProvider></MemoryRouter>) }
async function sourceLink() { return waitFor(() => { const link=screen.getAllByTitle('Open from the workspace’s folder').find(link=>link.checkVisibility()); expect(link).toBeDefined(); return link! }) }
async function view(name:string) {
 const card=(await screen.findAllByTestId('artifact-card')).find(card=>card.checkVisibility() && card.textContent?.includes(name))!
 fireEvent.click(within(card).getByRole('button',{name:'View'}))
}
for(const session of ['channel','worker']) it.each(artifacts)(`opens ${session} $view ($name) in desktop tabs`,async artifact=>{
 await page.viewport(1920,1080);mount(session);await view(artifact.name)
 expect(await screen.findByRole('tab',{name:new RegExp(artifact.name)})).toHaveAttribute('aria-selected','true')
 expect(await screen.findByRole('region',{name:artifact.name})).toBeVisible()
 expect(screen.queryByRole('dialog')).toBeNull()
 const calls=vi.mocked(fetch).mock.calls.map(([url])=>String(url))
 expect(calls).toContain(`/api/sessions/${session}/artifacts/${artifact.id}/raw`)
})
for(const theme of ['dark','colorblind-dark']) for(const width of [390,1920,3840]) it(`channel deck and diff at ${width}px in ${theme}`,async()=>{
 await page.viewport(width,width===390?844:width===1920?1080:2160)
 document.documentElement.setAttribute('data-theme',theme);mount()
 await view('launch.html')
 const viewer=await screen.findByRole(width===390?'dialog':'region',{name:'launch.html'})
 expect(viewer).toBeVisible()
 await waitFor(()=>expect(viewer.querySelector('iframe')).not.toBeNull())
 if(width===390 && import.meta.env.VITE_CHANNEL_SHOTS_DIR) await commands.channelScreenshot(width,theme,'deck')
 if(width===390) fireEvent.click(within(viewer).getByRole('button',{name:'Close'}))
 else { fireEvent.click(viewer); fireEvent.click(screen.getByRole('tab',{name:'Marketing'})) }
 await view('campaign.patch')
 const diff=await screen.findByRole(width===390?'dialog':'region',{name:'campaign.patch'})
 await waitFor(()=>expect(diff.textContent).toContain('new'))
 if(width===390) fireEvent.click(within(diff).getByRole('tab',{name:'Files'}))
 expect(diff.getBoundingClientRect().right).toBeLessThanOrEqual(width)
 if(width>390) {
  expect(screen.getByRole('tab',{name:/launch.html/})).toBeVisible();expect(screen.queryByRole('dialog')).toBeNull()
  fireEvent.click(diff)
  fireEvent.click(screen.getByRole('tab',{name:/launch.html/}))
  await waitFor(()=>expect(screen.getByRole('region',{name:'launch.html'})).toBeVisible())
  if(import.meta.env.VITE_CHANNEL_SHOTS_DIR) await commands.channelScreenshot(width,theme,'deck')
  fireEvent.click(screen.getByRole('tab',{name:/campaign.patch/}))
 }
 if(import.meta.env.VITE_CHANNEL_SHOTS_DIR) await commands.channelScreenshot(width,theme,'diff')
})

for (const session of ['channel','worker']) {
 it(`${session} opens a pinned ticket as a tab`,async()=>{
  await page.viewport(1920,1080);mount(session)
  const card=(await screen.findAllByTestId('ticket-card')).find(card=>card.checkVisibility())!
  fireEvent.click(within(card).getByTestId('ticket-chip'))
  expect(await screen.findByRole('tab',{name:'#743'})).toHaveAttribute('aria-selected','true')
  expect(screen.queryByRole('dialog')).toBeNull()
 })
 it(`${session} opens a commit as a tab`,async()=>{
  await page.viewport(1920,1080);mount(session)
  const checklist=(await screen.findAllByTestId('checklist-row')).find(card=>card.checkVisibility())!
  fireEvent.click(within(checklist).getByRole('button',{expanded:false}))
  fireEvent.click(within(checklist).getByRole('button',{name:/Open commit/}))
  expect(await screen.findByRole('tab',{name:new RegExp(sha.slice(0,8))})).toHaveAttribute('aria-selected','true')
  expect(screen.queryByRole('dialog')).toBeNull()
 })
 it(`${session} opens workspace source as a tab`,async()=>{
  await page.viewport(1920,1080);mount(session)
  const link=await sourceLink()
  fireEvent.click(link)
  expect(await screen.findByRole('tab',{name:'app.ts'})).toHaveAttribute('aria-selected','true')
  await waitFor(()=>expect(screen.getByRole('region',{name:'app.ts'})).toHaveTextContent('campaign'))
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(vi.mocked(fetch).mock.calls.some(([url])=>String(url).includes('/sessions/main/workspace-file?'))).toBe(false)
 })
 for(const [name,action] of [['brief.md','Review'],['campaign.svg','Mark up']]) it(`${session} keeps ${action} in its file tab`,async()=>{
  await page.viewport(1920,1080);mount(session);await view(name)
  const viewer=await screen.findByRole('region',{name})
  fireEvent.click(await within(viewer).findByRole('button',{name:action}))
  expect(screen.getByRole('tab',{name:new RegExp(name)})).toHaveAttribute('aria-selected','true')
  expect(screen.queryByRole('dialog')).toBeNull()
 })
}
it('retains channel file ownership after closing chat, then closes and reopens by keyboard',async()=>{
 await page.viewport(1920,1080);mount();await view('brief.md')
 const region=await screen.findByRole('region',{name:'brief.md'})
 fireEvent.click(region)
 fireEvent.click(screen.getByRole('button',{name:'Close Marketing'}))
 expect(region).toBeVisible()
 const tab=screen.getByRole('tab',{name:'brief.md'})
 tab.focus();await userEvent.keyboard('{Alt>}w{/Alt}')
 await waitFor(()=>expect(screen.queryByRole('tab',{name:'brief.md'})).toBeNull())
 await userEvent.keyboard('{Control>}{Shift>}t{/Shift}{/Control}')
 expect(await screen.findByRole('region',{name:'brief.md'})).toBeVisible()
 fireEvent.click(screen.getByRole('button',{name:'Close brief.md'}))
 fireEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 fireEvent.click(await screen.findByRole('treeitem',{name:'Marketing'}))
 await view('brief.md')
 expect(await screen.findByRole('region',{name:'brief.md'})).toBeVisible()
 expect(screen.queryByRole('dialog')).toBeNull()
})

it('updates a live channel deck in place',async()=>{
 await page.viewport(1920,1080);mount();await view('launch.html')
 await screen.findByRole('region',{name:'launch.html'})
 const region=()=>screen.getByRole('region',{name:'launch.html'})
 await waitFor(()=>expect(region().querySelector('iframe')?.getAttribute('srcdoc')).toContain('Autumn launch'))
 revision=2
 act(()=>useTranscriptStore.getState().applyLiveArtifact('channel',{...artifacts[0],updated_at:'2026-11-01T12:02:00Z'}))
 await waitFor(()=>expect(region().querySelector('iframe')?.getAttribute('srcdoc')).toContain('Winter launch'))
 expect(screen.getAllByRole('tab',{name:/launch.html/})).toHaveLength(1)
 expect(screen.queryByRole('dialog')).toBeNull()
})

it('keeps a desktop file readable after resizing to a phone',async()=>{
 await page.viewport(1920,1080);mount();await view('brief.md')
 await screen.findByRole('region',{name:'brief.md'})
 await page.viewport(390,844)
 expect(await screen.findByRole('region',{name:'brief.md'})).toBeVisible()
 fireEvent.click(screen.getByRole('tab',{name:'Marketing'}))
 await view('campaign.patch')
 expect(await screen.findByRole('dialog',{name:'campaign.patch'})).toBeVisible()
})
it('separates the same workspace path in a channel and worker',async()=>{
 await page.viewport(1920,1080);mount()
 fireEvent.click(await sourceLink())
 const first=await screen.findByRole('region',{name:'app.ts'});fireEvent.click(first)
 fireEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 const workers=await screen.findByRole('treeitem',{name:/^Workers/})
 if(workers.getAttribute('aria-expanded')!=='true') fireEvent.click(workers)
 fireEvent.click(await screen.findByRole('treeitem',{name:'Campaign worker'}))
 fireEvent.click(await sourceLink())
 await waitFor(()=>expect(screen.getAllByRole('tab',{name:'app.ts'})).toHaveLength(2))
 const requests=vi.mocked(fetch).mock.calls.map(([url])=>String(url))
 expect(requests.some(url=>url.includes('/sessions/worker/workspace-file?'))).toBe(true)
 expect(requests.some(url=>url.includes('/sessions/channel/workspace-file?'))).toBe(true)
 expect(requests.some(url=>url.includes('/sessions/main/workspace-file?'))).toBe(false)
})

it('keeps review mode after closing the originating chat and switching tabs',async()=>{
 await page.viewport(1920,1080);mount();await view('brief.md')
 const viewer=await screen.findByRole('region',{name:'brief.md'})
 fireEvent.click(await within(viewer).findByRole('button',{name:'Review'}))
 await within(viewer).findAllByRole('button',{name:'Back'})
 fireEvent.click(screen.getByRole('button',{name:'Close Marketing'}))
 fireEvent.click(screen.getByRole('tab',{name:'Main'}))
 fireEvent.click(screen.getByRole('tab',{name:'brief.md'}))
 expect((await within(screen.getByRole('region',{name:'brief.md'})).findAllByRole('button',{name:'Back'}))[0]).toBeVisible()
})

for (const session of ['channel','worker']) for (const side of ['original','changed']) it(`${session} opens the ${side} commit file from a diff as a tab`,async()=>{
 await page.viewport(1920,1080);commitBearing=true;mount(session);await view('campaign.patch')
 const diff=await screen.findByRole('region',{name:'campaign.patch'})
 fireEvent.click(await within(diff).findByRole('button',{name:`Open ${side}`}))
 expect(screen.queryByRole('dialog')).toBeNull()
 const tab=await screen.findByRole('tab',{name:new RegExp(`app.ts.*${side}`)})
 expect(tab).toHaveAttribute('aria-selected','true')
 await waitFor(()=>expect(screen.getByRole('region',{name:'app.ts'})).toHaveTextContent(`complete ${side} campaign source`))
 expect(vi.mocked(fetch).mock.calls.some(([url])=>String(url).includes(`/sessions/${session}/commits/${sha}/file?`) && String(url).includes(`side=${side}`))).toBe(true)
 fireEvent.click(screen.getByRole('button',{name:new RegExp(`Close app.ts.*${side}`)}))
 await userEvent.keyboard('{Control>}{Shift>}t{/Shift}{/Control}')
 expect(await screen.findByRole('tab',{name:new RegExp(`app.ts.*${side}`)})).toHaveAttribute('aria-selected','true')
 expect(screen.queryByRole('dialog')).toBeNull()
})
