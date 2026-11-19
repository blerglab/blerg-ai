import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { commands, page } from 'vitest/browser'
import { setTheme } from '../../../../packages/settings/theme'
import BlackboardTabs from './BlackboardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
import '../index.css'
import '@blerglab/chat/blerg.css'
const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../hooks/useBlackboardDraft', () => ({ useBlackboardRevision: () => 0, useBlackboardDraft: () => null }))
const json = (body: unknown) => new Response(JSON.stringify(body))
const record = { name: 'Shared work', vision: 'Make collaboration feel natural', goals: [{id:'g',name:'Work with confidence'}], milestones: [{ id: 'm', name: '0.6.0' }], thrusts: ['Recover work','Review in context','Clear navigation'].map((name,i)=>({id:'t'+i,name,milestone_id:'m',goal_id:'g'})) }
const cards = Array.from({length:24}, (_,i)=>({card:{board_id:'board',number:i+1,title:'Example work'},membership:{thrust_id:'t'+i%3,state:'planned'},stage:['done','to_test','in_work','next','needs_you'][i%5]}))
function mount(id: string, width: number) {
 const frame = document.createElement('div'); frame.style.cssText = `width:${width}px;height:700px;display:flex;background:var(--basalt)`; document.body.append(frame)
 const tabs = { tabs: [{ id:'tab',view:{kind:'blackboard',blackboardId:id,path:{level:'milestone',milestoneId:'m'}}}],visible:['tab'],targetFor:()=>frame,navigateBlackboard:vi.fn() } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId="s" />)
 return {frame,close:()=>{app.unmount();frame.remove()}}
}
it.each([390,3840])('captures first load, real counts and cached revisit at %i px', async width => {
 await page.viewport(width,900)
 for (const theme of ['dark','colorblind-dark'] as const) {
  setTheme(theme)
  let releaseRecord!:()=>void, releaseSlices!:()=>void
  const recordWait = new Promise<void>(r=>{releaseRecord=r}), sliceWait = new Promise<void>(r=>{releaseSlices=r})
  read.mockImplementation((url:string)=>url.includes('/slice')?sliceWait.then(()=>json({cards:url.includes('/milestones/')?cards:[],complete:true})):recordWait.then(()=>json({record})))
  const id = `${theme}-${width}`
  const view = mount(id,width)
  const dir = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  const shot = async (name:string) => {if(dir) await page.screenshot({element:view.frame,path:`${dir}/${theme}-${width}-${name}.png`})}
  expect(view.frame.getBoundingClientRect().width).toBe(width)
  expect(screen.getByRole('status').textContent).toBe('Loading blackboard')
  expect(screen.getByRole('status').getBoundingClientRect().width).toBe(1)
  await shot('skeleton')
  await act(async()=>{releaseRecord()})
  expect(screen.getByRole('heading',{name:'0.6.0'})).toBeTruthy()
  expect(view.frame.querySelector('.blackboard-filters b')).toBeNull()
  await shot('pending-slices')
  await act(async()=>{releaseSlices()})
  await waitFor(()=>expect(screen.queryByText('Loading card details…')).toBeNull())
  const bars = [...view.frame.querySelectorAll<HTMLElement>('.blackboard-summary .blackboard-bar i, .blackboard-thrust .blackboard-bar i')]
  const animations = bars.flatMap(bar=>bar.getAnimations())
  expect(animations.length).toBeGreaterThan(0)
  for(const animation of animations) animation.pause()
  for(const time of [0,150,300,580]) {
   for(const animation of animations) animation.currentTime=time
   await shot(`animation-${time}`)
  }
  for(const animation of animations) animation.finish()
  await shot('loaded')
  expect(view.frame.scrollWidth).toBeLessThanOrEqual(width)
  fireEvent.click(screen.getByRole('button',{name:'Refresh blackboard'}))
  await act(async()=>{})
  expect(view.frame.querySelector('.blackboard-bars-enter')).toBeNull()
  view.close()
  read.mockImplementation(()=>new Promise(()=>{}))
  const cached = mount(id,width)
  expect(screen.getByText('10 of 24 shipped')).toBeTruthy()
  expect(screen.queryByRole('status')).toBeNull()
  expect(cached.frame.querySelector('.blackboard-bars-enter')).toBeNull()
  cached.close()
 }
 setTheme('dark')
})
it('disables shimmer and bar growth for reduced motion', async()=>{
 await commands.setUsageReducedMotion(true)
 try {
  let release!:()=>void
  const pending=new Promise<void>(r=>{release=r})
  read.mockImplementation((url:string)=>pending.then(()=>json(url.includes('/slice')?{cards:url.includes('/milestones/')?cards:[],complete:true}:{record})))
  const view=mount('reduced',390)
  expect(getComputedStyle(view.frame.querySelector('.initial-skeleton-value')!,'::after').animationName).toBe('none')
  await act(async()=>{release()})
  await screen.findByText('10 of 24 shipped')
  expect(getComputedStyle(view.frame.querySelector('.blackboard-summary i')!).animationName).toBe('none')
  view.close()
 } finally {await commands.setUsageReducedMotion(false)}
})
