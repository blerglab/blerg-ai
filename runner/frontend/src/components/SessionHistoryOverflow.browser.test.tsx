// Older transcript rows must remain contained when upward scrolling mounts a new window.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  // Never let a test navigate the test page itself.
  redirectToRefresh: vi.fn(),
}))
// The chat's transport reads the files with that token, taken as fresh.
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import { useTranscriptStore } from '@blerglab/chat'


const PATCH = 'diff --git a/fixture.txt b/fixture.txt\n--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1 +1 @@\n-old\n+' + 'n'.repeat(2000) + '\n'
const FILES = [
  { id: 'f2', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 6144, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 2 },
  { id: 'f1', name: 'a-rather-long-file-name-for-the-release-plan-of-the-session-store.md', size: 4096, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 2 },
  { id: 'f3', name: 'coverage.csv', size: 18432, content_type: 'text/csv', view: 'csv', version: 1, latest_version: 1 },
]
const CAPS = {
  seq: 3, ts: '2026-10-15T10:00:00Z',
  capabilities: { engine: 'claude', groups: [{ id: 'skills', label: 'Skills', items: [{ name: 'release-notes', description: 'Writes release notes from the merged pull requests since the last tag.' }] }] },
}

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => json(
    url.startsWith('/api/tool-changes') ? { tool_changes: [{ id: 't1', kind: 'changed', muted: false }] }
      : url.endsWith('/topics') ? reelReply()
        : url.endsWith('/live') ? live
          : url.endsWith('/capabilities') ? CAPS
            : {},
  ))
  vi.stubGlobal('fetch', vi.fn((url: string) => String(url).endsWith('/raw') ? Promise.resolve(new Response(PATCH)) : json(String(url).endsWith('/artifacts') ? { artifacts: FILES } : [])))
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
  localStorage.removeItem(RAIL_TAB_KEY)
})

// Match App.Layout's desktop flex ancestors, including the main pane's own scroller.
function renderPage() {
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}>
    <div style={{display: 'flex', flexDirection: 'column', height: '100vh', overflow: 'hidden'}}>
      <div style={{display: 'flex', flexDirection: 'row', flex: 1, minHeight: 0}}>
        {innerWidth >= 900 && <SessionList variant="sidebar" />}
        <div data-testid="app-main" style={{flex: 1, minWidth: 0, overflow: 'auto'}}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </div>
    </div>
  </MemoryRouter>)
}

const noOverflow = (width: number) => {
  expect(document.documentElement.clientWidth).toBe(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
}


describe('virtualized history width', () => {
 it.each([1280,390,2048])('stays bounded when older wide rows mount at %ipx',async width=>{
  await page.viewport(width,800)
  useTranscriptStore.setState({sessions:{}})
  const state=useTranscriptStore.getState()
  let seq=0
  const emit=(kind:string,payload:unknown)=>state.ingest({type:'agent_event',session_id:'sess-a',client_event_id:`history-${++seq}`,seq,ts:new Date(Date.now()-1000000+seq).toISOString(),kind,payload} as Parameters<typeof state.ingest>[0])
  emit('assistant_text',{text:'OLDER_WIDE_MARKER\n\n| '+Array.from({length:30},(_,i)=>'Column'+i).join(' | ')+' |\n| '+Array(30).fill('---').join(' | ')+' |\n| '+Array(30).fill('x'.repeat(300)).join(' | ')+' |',done:true})
  emit('assistant_text',{text:'```text\n'+'x'.repeat(5000)+'\n```',done:true})
  emit('tool_call',{tool:'functions.exec',call_id:'older-code',input:{code:'const code = '+ 'x'.repeat(5000)}})
  emit('tool_result',{call_id:'older-code',output:'old result',is_error:false})
  emit('tool_call',{tool:'Bash',call_id:'older-shell',input:{command:'cat > example.txt <<EOF\n'+'x'.repeat(5000)+'\nEOF'}})
  emit('tool_result',{call_id:'older-shell',output:'done',is_error:false})
  emit('tool_call',{tool:'Agent',call_id:'old-worker',input:{description:'x'.repeat(2000),prompt:'x'.repeat(5000)}})
  emit('subagent_started',{child_id:'old-worker',summary:'x'.repeat(2000)})
  emit('subagent_done',{child_id:'old-worker',summary:'x'.repeat(2000)})
  emit('error',{message:'x'.repeat(5000),retryable:false})
  for(let i=0;i<700;i++)emit('user_message',{text:`filler message ${i}`,source:'chat'})
  state.ingestReplayDone('sess-a',{lastSeq:seq,hasMore:false})
  renderPage()
  const timeline=await waitFor(()=>{const e=document.querySelector<HTMLElement>('.agent-timeline')!;expect(e?.querySelectorAll('.agent-card.user').length).toBeGreaterThan(0);return e}, { timeout: 15_000 })
  await waitFor(()=>expect(timeline.scrollTop).toBeGreaterThan(0), { timeout: 15_000 })
  expect(screen.queryByText(/OLDER_WIDE_MARKER/)).toBeNull()
  expect(timeline.scrollWidth).toBeLessThanOrEqual(timeline.clientWidth)
  const main=screen.getByTestId('app-main')
  expect(main.scrollWidth).toBeLessThanOrEqual(main.clientWidth)
  for(let i=0;i<3 && !screen.queryByText(/OLDER_WIDE_MARKER/);i++){
   const before=timeline.querySelectorAll('.agent-card.user').length
   timeline.dispatchEvent(new WheelEvent('wheel',{deltaY:-1000,bubbles:true}))
   timeline.scrollTop=0
   timeline.dispatchEvent(new Event('scroll',{bubbles:true}))
   await waitFor(()=>expect(timeline.querySelectorAll('.agent-card.user').length).toBeGreaterThan(before), { timeout: 15_000 })
  }
  expect(screen.queryByText(/OLDER_WIDE_MARKER/)).not.toBeNull()
  expect(main.scrollWidth).toBeLessThanOrEqual(main.clientWidth)
  expect(timeline.scrollWidth).toBeLessThanOrEqual(timeline.clientWidth)
  const error = timeline.querySelector<HTMLElement>('.agent-card.error')!
  expect(error.textContent).toContain('x'.repeat(5000))
  expect(error.scrollWidth).toBeLessThanOrEqual(error.clientWidth)
  // Wide tables retain their own horizontal scroller instead of widening the transcript.
  const table = timeline.querySelector<HTMLElement>('.md-table-wrap')!
  expect(getComputedStyle(table).overflowX).toBe('auto')
  if (width < 2048) {
    expect(table.scrollWidth).toBeGreaterThan(table.clientWidth)
    table.scrollLeft = 50
    expect(table.scrollLeft).toBeGreaterThan(0)
  }
  const code = timeline.querySelector<HTMLElement>('.md-codeblock pre')!
  expect(getComputedStyle(code).overflowX).toBe('auto')
  expect(code.textContent).toContain('x'.repeat(5000))
  noOverflow(width)
 },30000)
})
