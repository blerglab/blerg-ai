import { expect, it, vi } from 'vitest'
import { act, render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { makeSession } from '../test/fixtures'
import { NeedsYou } from './NeedsYouTabs'
import type { AttentionItem } from '../hooks/useQuestionItems'
vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn(async () => new Response('{}')))
vi.mock('../apiFetch', () => ({ apiFetch }))
it('groups newest questions by worker, filters, and removes answered items live', async () => {
 const session = makeSession(), worker = makeSession({id:'worker',title:'Review release'})
 const ask = (id:string, owner:typeof session, time:string):AttentionItem => ({id,session:owner,kind:'ask',question:{id,session_id:owner.id,kind:'ask',status:'open',answerable:true,body:`${id}?\nOption B keeps the draft.`,created_at:time}})
 const items=[ask('older',worker,'2026-11-01'),ask('newest',worker,'2026-11-03'),ask('main',session,'2026-11-02')]
 const show=(value:AttentionItem[]) => <MemoryRouter><NeedsYou attention={{root:session.id,items:value,ids:[]}} settled /></MemoryRouter>
 const view=render(show(items))
 expect(screen.getAllByRole('heading',{level:3}).map(n=>n.textContent)).toEqual(['Review release','Main'])
 expect(within(screen.getByRole('region',{name:'Review release'})).getAllByRole('heading',{level:2}).map(n=>n.textContent?.trim())).toEqual(['newest?','older?'])
 expect(screen.getAllByRole('textbox')).toHaveLength(3)
 fireEvent.change(screen.getByLabelText('Worker'),{target:{value:'worker'}})
 expect(screen.getAllByRole('textbox')).toHaveLength(2)
 view.rerender(show(items.filter(i=>i.id!=='newest')))
 expect(screen.getAllByRole('textbox')).toHaveLength(1)
 expect(screen.getByText('1 of 2 waiting')).toBeVisible()
 fireEvent.change(screen.getByRole('textbox'), {target:{value:'Keep the draft'}})
 fireEvent.click(screen.getByRole('button',{name:'Answer'}))
 await vi.waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/messages/older/reply',expect.objectContaining({method:'POST',body:'{"answer":"Keep the draft"}'})))
})

it('does not show a false empty count while the first read is pending', () => {
 render(<MemoryRouter><NeedsYou attention={{root:'pending',items:[],ids:[]}} settled={false} /></MemoryRouter>)
 expect(screen.queryByText(/0 waiting|0 of 0|caught up/)).toBeNull()
 expect(screen.queryByLabelText('Worker')).toBeNull()
})

it('keeps the panel empty state when the pop-out action is available', async () => {
 const { default: SessionRail } = await import('./SessionRail')
 const { makeLive } = await import('../test/planTabFixture')
 render(<MemoryRouter><SessionRail id="empty-needs" session={makeSession()} live={makeLive()} attention={{root:'sess-a',items:[],ids:[]}} toolChanges={0} mode="panel" open onClose={()=>{}} onOpenArtifact={()=>{}} onOpenNeedsYou={()=>{}} /></MemoryRouter>)
 expect(within(screen.getByTestId('rail-needs')).getByText('Nothing needs you.')).toBeVisible()
})

it('anchors a worker group with its runtime and count and uses the shared question conversation', () => {
 const session=makeSession({runtime:'daemon',title:'Release review'})
 const item:AttentionItem={id:'ask:surface',kind:'ask',session,question:{id:'surface',session_id:session.id,kind:'ask',status:'open',answerable:true,body:'Ready to publish?\nThe draft is fully reviewed.',created_at:''}}
 render(<MemoryRouter><NeedsYou attention={{root:session.id,items:[item],ids:[]}} settled /></MemoryRouter>)
 const group=screen.getByRole('region',{name:'Main'})
 expect(within(group).getByRole('img',{name:/Host/})).toBeVisible()
 expect(within(group).getByLabelText('1 waiting item')).toBeVisible()
 expect(within(group).getByRole('log',{name:'Question discussion'})).toHaveTextContent('Main')
 expect(within(group).getByRole('link',{name:'Open conversation'}).closest('header')).not.toBeNull()
})

it('greets an empty queue without a numeric counter or empty filter', () => {
 render(<MemoryRouter><NeedsYou attention={{root:'empty',items:[],ids:[]}} settled /></MemoryRouter>)
 expect(screen.getByText(/all caught up/)).toBeVisible()
 expect(screen.queryByText(/0 waiting|0 of 0/)).toBeNull()
 expect(screen.queryByLabelText('Worker')).toBeNull()
})


it('shows a skeleton and a friendly line while waiting for the first collection', () => {
 vi.useFakeTimers()
 try {
  render(<MemoryRouter><NeedsYou attention={{root:'loading',items:[],ids:[]}} settled={false} /></MemoryRouter>)
  act(()=>vi.advanceTimersByTime(150))
  expect(screen.getByText('Gathering what needs your attention.')).toBeVisible()
  expect(document.querySelector('.initial-skeleton')).toBeVisible()
  expect(screen.queryByText(/0 waiting|0 of 0|Loading…/)).toBeNull()
 } finally { vi.useRealTimers() }
})
