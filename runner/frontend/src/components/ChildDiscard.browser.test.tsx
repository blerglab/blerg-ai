import {beforeEach,describe,expect,it,vi} from 'vitest'
import {render,waitFor} from '@testing-library/react'
import {page,userEvent} from 'vitest/browser'
import {MemoryRouter} from 'react-router-dom'
import {StartedSessions} from './ChildSessions'
import SessionCard from './SessionCard'
import {OpenSessionContext} from '../lib/openSession'
import {makeDaemon,makeSession} from '../test/fixtures'
import type {LiveChildSession} from '../lib/liveState'
import '../index.css'
import './SessionRail.css'
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
vi.mock('../ws',()=>import('../test/wsMock'))
const child:LiveChildSession={id:'child',title:'Review runner',engine:'codex',model:'fixture-model',effort:'high',status:'running',started_at:'2026-10-19T10:00:00Z',asking:false,tokens:1000}
const now=Date.parse('2026-10-19T10:02:00Z')
beforeEach(()=>{apiFetch.mockReset()})
describe('human child discard in Chromium',()=>{
 it.each([1280,390])('confirms explicitly, traps keyboard focus and keeps the row until server data at width %s',async width=>{
  await page.viewport(width,800)
  let finish!:(response:{ok:boolean})=>void
  apiFetch.mockImplementation(()=>new Promise(resolve=>{finish=resolve}))
  const children=Array.from({length:7},(_,n)=>({...child,id:`c${n}`,title:`Child ${n}`}))
  const panel=(items:LiveChildSession[]) => <MemoryRouter><div style={{width:'min(360px, 100%)'}}><StartedSessions sessionId="parent" children={items} grants={[]} now={now}/></div></MemoryRouter>
  const{container,getByRole,rerender}=render(panel(children))
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(7)
  const trigger=getByRole('button',{name:'Discard Child 0'})
  expect(trigger.querySelector('svg')).not.toBeNull()
  expect(trigger.getBoundingClientRect().width).toBeGreaterThanOrEqual(24)
  expect(trigger.getBoundingClientRect().left).toBeGreaterThanOrEqual(trigger.closest('li')!.querySelector('.rail-child-button')!.getBoundingClientRect().right)
  await userEvent.click(trigger)
  const dialog=getByRole('dialog',{name:'Discard child session?'})
  expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
  expect(dialog.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)
  expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(document.activeElement).toBe(getByRole('button',{name:'Discard child session'}))
  await userEvent.keyboard('{Tab}')
  expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
  await userEvent.keyboard('{Escape}')
  expect(document.activeElement).toBe(trigger)
  expect(apiFetch).not.toHaveBeenCalled()
  await userEvent.click(trigger)
  await userEvent.click(getByRole('button',{name:'Discard child session'}))
  expect(getByRole('button',{name:'Discarding…'})).toBeDisabled()
  await userEvent.keyboard('{Tab}')
  expect(getByRole('dialog')).toContainElement(document.activeElement as HTMLElement)
  finish({ok:true})
  await waitFor(()=>expect(document.querySelector('[role="dialog"]')).toBeNull())
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent/children/c0/discard',{method:'POST'})
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(7)
  rerender(panel(children.map((item,n)=>n===0?{...item,archived_at:'2026-10-19T11:00:00Z'}:item)))
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(6)
 })
 it('uses the sidebar child menu without navigating and restores menu focus after cancelling',async()=>{
  const open=vi.fn()
  const{getByRole}=render(<MemoryRouter><OpenSessionContext.Provider value={open}><SessionCard session={makeSession({id:'child',parent_session_id:'parent',title:'Child review'})} daemon={makeDaemon()} nested/></OpenSessionContext.Provider></MemoryRouter>)
  const trigger=getByRole('button',{name:'Actions for Child review'})
  trigger.focus();await userEvent.keyboard('{ArrowDown}')
  expect(document.activeElement).toBe(getByRole('menuitem',{name:'Discard child session'}))
  await userEvent.keyboard('{Enter}')
  expect(getByRole('dialog')).toBeVisible()
  expect(open).not.toHaveBeenCalled()
  await userEvent.click(getByRole('button',{name:'Cancel'}))
  expect(document.activeElement).toBe(trigger)
  expect(apiFetch).not.toHaveBeenCalled()
 })
 it('keeps the same child row in place through running, idle and stopped updates',()=>{
  const panel=(status:string)=> <MemoryRouter><StartedSessions sessionId="parent" children={[{...child,status}]} grants={[]} now={now}/></MemoryRouter>
  const{container,rerender}=render(panel('running'))
  const row=container.querySelector('[data-testid="rail-child"]')
  for(const status of ['idle','stopped','running']){
   rerender(panel(status))
   expect(container.querySelector('[data-testid="rail-child"]')).toBe(row)
   expect(row).toHaveTextContent(status)
  }
 })
})
