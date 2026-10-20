import { expect, it, vi } from 'vitest'
import { render, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { StartedSessions } from './ChildSessions'
import type { LiveChildSession } from '../lib/liveState'
import '../index.css'
import './SessionRail.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({apiFetch:vi.fn(() => Promise.reject(new Error('Unexpected network request in isolated rendering test')))}))

const now=Date.parse('2026-01-01T00:02:00Z')
const running:LiveChildSession={id:'worker-1',title:'Model validation',engine:'hermes',model:'nvidia-nemotron-3.5-lightning-30b-a3b-nvfp4',effort:'medium',status:'running',started_at:'2026-01-01T00:00:00Z',asking:false,tokens:12345,usage_status:'reported',tool_calls:13,activity:'running terminal · go test ./... '+ 'bounded-argument '.repeat(30),activity_since:'2026-01-01T00:01:40Z',local:true}
const pending:LiveChildSession={...running,id:'worker-2',title:'Shared capacity documentation',tokens:null,tool_calls:0,usage_status:'pending',activity:'Working…',activity_since:undefined,local:false}

it.each([390,1280])('keeps live child telemetry compact and truthful at %ipx',async width=>{
 await page.viewport(width,900)
 const tree=(children:LiveChildSession[])=><MemoryRouter><aside className="session-rail panel" style={{width:Math.min(340,width-24),padding:8}}><StartedSessions sessionId="parent" children={children} now={now}/></aside></MemoryRouter>
 const view=render(tree([running,pending]))
 const rows=view.getAllByTestId('rail-child')
 expect(within(rows[0]).getByText('13 tool calls')).not.toBeNull()
 expect(within(rows[0]).getByText('Local endpoint')).not.toBeNull()
 expect(within(rows[1]).getByText('Usage pending')).not.toBeNull()
 expect(within(rows[1]).queryByText('0 tokens')).toBeNull()
 expect(within(rows[1]).queryByText('Local endpoint')).toBeNull()
 expect(view.queryByText(/\$0/)).toBeNull()
 for(const row of rows){expect(row.getBoundingClientRect().height).toBeLessThan(240);expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth)}
 const activity=within(rows[0]).getByTestId('child-activity')
 expect(activity.getBoundingClientRect().height).toBeLessThan(40)
 expect(activity.querySelector('.rail-child-pulse')).not.toBeNull()
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 const dir=(import.meta as unknown as {env:Record<string,string>}).env.VITE_CHILD_TELEMETRY_SHOTS_DIR
 if(dir)await page.screenshot({path:`${dir}/child-telemetry-${width}.png`})
 view.rerender(tree([{...running,status:'idle',tokens:1021976,tool_calls:32,activity:'Turn complete',activity_since:undefined},pending]))
 const completed=view.getAllByTestId('rail-child')[0]
 expect(completed).toBe(rows[0])
 expect(within(completed).getByText('idle')).not.toBeNull()
 expect(within(completed).getByText('32 tool calls')).not.toBeNull()
 expect(within(completed).getByText('Turn complete')).not.toBeNull()
 expect(completed.querySelector('.rail-child-pulse')).toBeNull()
 expect(within(completed).queryByText('running')).toBeNull()
})
