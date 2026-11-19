import {beforeEach,describe,expect,it,vi} from 'vitest'
import {render,waitFor} from '@testing-library/react'
import {page,userEvent} from 'vitest/browser'
import {MemoryRouter} from 'react-router-dom'
import {StartedSessions} from './ChildSessions'
import SessionCard from './SessionCard'
import {ParentDiscardAction} from './ChildDiscard'
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
vi.spyOn(Date, 'now').mockReturnValue(now)
beforeEach(()=>{apiFetch.mockReset()})
describe('human child discard in Chromium',()=>{
 it.each([1280,390])('confirms explicitly, traps keyboard focus and keeps the row until server data at width %s',async width=>{
  await page.viewport(width,800)
  let finish!:(response:{ok:boolean})=>void
  apiFetch.mockImplementation(()=>new Promise(resolve=>{finish=resolve}))
  const children=Array.from({length:7},(_,n)=>({...child,id:`c${n}`,title:`Child ${n}`}))
  const panel=(items:LiveChildSession[]) => <MemoryRouter><div style={{width:'min(360px, 100%)'}}><StartedSessions sessionId="parent" children={items} clockOffset={0} active={false}/></div></MemoryRouter>
  const{container,getByRole,rerender}=render(panel(children))
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(7)
  const trigger=getByRole('button',{name:'Discard Child 0'})
  expect(trigger.querySelector('svg')).not.toBeNull()
  expect(trigger.getBoundingClientRect().width).toBeGreaterThanOrEqual(24)
  expect(trigger.getBoundingClientRect().left).toBeGreaterThanOrEqual(trigger.closest('li')!.querySelector('.rail-run-title')!.getBoundingClientRect().right)
  await userEvent.click(trigger)
  const dialog=getByRole('alertdialog',{name:'Discard worker?'})
  expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
  expect(dialog.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)
  expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(document.activeElement).toBe(getByRole('button',{name:'Discard worker'}))
  await userEvent.keyboard('{Tab}')
  expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
  await userEvent.keyboard('{Escape}')
  expect(document.activeElement).toBe(trigger)
  expect(apiFetch).not.toHaveBeenCalled()
  await userEvent.click(trigger)
  await userEvent.click(getByRole('button',{name:'Discard worker'}))
  expect(getByRole('button',{name:'Discarding…'})).toBeDisabled()
  await userEvent.keyboard('{Tab}')
  expect(getByRole('alertdialog')).toContainElement(document.activeElement as HTMLElement)
  finish({ok:true})
  await waitFor(()=>expect(document.querySelector('[role="alertdialog"]')).toBeNull())
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent/children/c0/discard',{method:'POST'})
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(7)
  rerender(panel(children.map((item,n)=>n===0?{...item,archived_at:'2026-10-19T11:00:00Z'}:item)))
  expect(container.querySelectorAll('[data-testid="rail-child"]')).toHaveLength(6)
 })
 it('keeps the discard icon quiet until it is hovered or focused',async()=>{
  await page.viewport(1280,800)
  const{getByRole}=render(<MemoryRouter><div style={{width:360}}><StartedSessions sessionId="parent" children={[child]} clockOffset={0} active={false}/></div></MemoryRouter>)
  const trigger=getByRole('button',{name:'Discard Review runner'})
  const tokenColor=(name:string)=>{const probe=document.createElement('span');probe.style.color=`var(${name})`;document.body.append(probe);const c=getComputedStyle(probe).color;probe.remove();return c}
  expect(getComputedStyle(trigger).color).toBe(tokenColor('--fog-dim'))
  await userEvent.hover(trigger)
  await waitFor(()=>expect(getComputedStyle(trigger).color).toBe(tokenColor('--danger')))
  await userEvent.unhover(trigger)
  trigger.focus()
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}{Tab}')
  await waitFor(()=>expect(getComputedStyle(trigger).color).toBe(tokenColor('--danger')))
 })
 it('uses the sidebar child menu without navigating and restores menu focus after cancelling',async()=>{
  const open=vi.fn()
  const{getByRole}=render(<MemoryRouter><OpenSessionContext.Provider value={open}><SessionCard session={makeSession({id:'child',parent_session_id:'parent',started_by:{kind:'session'},title:'Child review'})} daemon={makeDaemon()} nested/></OpenSessionContext.Provider></MemoryRouter>)
  const trigger=getByRole('button',{name:'Actions for Child review'})
  trigger.focus();await userEvent.keyboard('{ArrowDown}')
  expect(document.activeElement).toBe(getByRole('menuitem',{name:'Discard worker'}))
  await userEvent.keyboard('{Enter}')
  expect(getByRole('alertdialog')).toBeVisible()
  expect(open).not.toHaveBeenCalled()
  await userEvent.click(getByRole('button',{name:'Cancel'}))
  expect(document.activeElement).toBe(trigger)
  expect(apiFetch).not.toHaveBeenCalled()
 })
 it('keeps the same child row in place through running, idle and stopped updates',()=>{
  const panel=(status:string)=> <MemoryRouter><StartedSessions sessionId="parent" children={[{...child,status}]} clockOffset={0} active={false}/></MemoryRouter>
  const{container,rerender}=render(panel('running'))
  const row=container.querySelector('[data-testid="rail-child"]')
  for(const status of ['idle','stopped','running']){
   rerender(panel(status))
   expect(container.querySelector('[data-testid="rail-child"]')).toBe(row)
   expect(row!.querySelector('.rail-run-state [role="img"]')).toHaveAttribute('aria-label', status === 'running' ? 'Running' : status)
  }
 })
})


it.each([1280,390])('confirms parent discard including Keep running workers at width %s', async width => {
 await page.viewport(width,800)
 const {getByRole,getByText} = render(<ParentDiscardAction sessionId="parent /" title="Parent"/>)
 apiFetch.mockResolvedValue({ok:true,json:async()=>({status:'discarded',session_id:'parent /',archived_at:'2026-10-20T12:00:00Z'})})
 const trigger = getByRole('button',{name:'Discard Parent'})
 await userEvent.click(trigger)
 const dialog=getByRole('alertdialog',{name:'Discard workspace and all workers and channels?'})
 expect(dialog.getBoundingClientRect().left).toBeGreaterThanOrEqual(0)
 expect(dialog.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
 expect(getByText(/including those marked Keep running and work in progress/)).toBeVisible()
 expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
 await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
 expect(document.activeElement).toBe(getByRole('button',{name:'Discard workspace'}))
 await userEvent.keyboard('{Tab}')
 expect(document.activeElement).toBe(getByRole('button',{name:'Cancel'}))
 expect(apiFetch).not.toHaveBeenCalled()
 await userEvent.keyboard('{Escape}')
 expect(trigger).toHaveFocus()
 await userEvent.click(trigger)
 await userEvent.click(getByRole('button',{name:'Discard workspace'}))
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/parent%20%2F/discard',{method:'POST'}))
 expect(trigger).toBeDisabled()
})

it('bounds a hung discard and releases page focus after dismissal', async () => {
 const schedule = window.setTimeout.bind(window)
 const timer = vi.spyOn(globalThis, 'setTimeout').mockImplementation(((callback: TimerHandler, delay?: number, ...args: unknown[]) => schedule(callback, delay === 35000 ? 50 : delay, ...args)) as typeof setTimeout)
 apiFetch.mockImplementation(() => new Promise(() => {}))
 try {
  const {getByRole,getByText,container} = render(<ParentDiscardAction sessionId="parent" title="Parent"/>)
  await userEvent.click(getByRole('button',{name:'Discard Parent'}))
  await userEvent.click(getByRole('button',{name:'Discard workspace'}))
  await waitFor(() => expect(getByText("Discard didn't finish")).toBeVisible())
  await userEvent.click(getByRole('button',{name:'Dismiss'}))
  expect(document.querySelector('[role="alertdialog"]')).toBeNull()
  expect(container.inert).toBe(false)
  expect(getByRole('button',{name:'Discard Parent'})).toBeEnabled()
 } finally {timer.mockRestore()}
})

it('allows dismissal while a discard request is still pending', async () => {
 apiFetch.mockImplementation(() => new Promise(() => {}))
 const {getByRole} = render(<ParentDiscardAction sessionId="parent" title="Parent"/>)
 await userEvent.click(getByRole('button',{name:'Discard Parent'}))
 await userEvent.click(getByRole('button',{name:'Discard workspace'}))
 await userEvent.keyboard('{Escape}')
 expect(document.querySelector('[role="alertdialog"]')).toBeNull()
})
