import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useCallback, useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import InboxTabs from './Inbox'
import { ChatContext, type ChatContextValue } from '@blerglab/chat'
import type { SessionTabs, SessionView } from '../hooks/useSessionTabs'
import { openInbox, refreshInbox, type InboxItem } from '../hooks/useInbox'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', () => ({ getAccessToken: () => `x.${btoa(JSON.stringify({sub:'inbox-person'}))}.x` }))
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
let items: InboxItem[]
let failRead=false
beforeEach(async()=>{
 failRead=false
 items=['a','b'].map(id=>({id,subject:`Subject ${id}`,session_id:'source',kind:'summary',body:`Body ${id}\n\n[Relative file](some/file.md)`,created_at:'2026-11-05T12:00:00Z',read:false}))
 apiFetch.mockReset().mockImplementation(async(url:string,init?:RequestInit)=>{
  if(url==='/api/inbox')return Response.json({items,unread:items.filter(i=>!i.read).length,next:''})
  const item=items.find(i=>url===`/api/inbox/${i.id}`||url===`/api/inbox/${i.id}/read`)!
  if(init?.method==='DELETE'){items=items.filter(i=>i.id!==item.id);return Response.json({deleted:true})}
  if(init?.method==='PUT'){
   if(failRead&&JSON.parse(String(init.body)).read)return new Response('{}',{status:500})
   item.read=JSON.parse(String(init.body)).read
  }
  return Response.json(item)
 })
 await refreshInbox()
})
function Harness({ restored }: {restored?:string}){
 const [view,setView]=useState<SessionView>({kind:'inbox',itemId:restored})
 const [active,setActive]=useState(true)
 const [target]=useState(()=>document.createElement('div'))
 const open=useCallback((next:SessionView)=>{setView(next);setActive(true)},[])
 const tabs={open,tabs:[{id:'inbox-reader',view}],visible:active?['inbox-reader']:[],targetFor:()=>target} as unknown as SessionTabs
 return <MemoryRouter><output data-testid="selection">{view.kind==='inbox'?view.itemId:''}</output><button onClick={()=>setActive(value=>!value)}>Toggle active</button><div ref={node=>{if(node&&!node.contains(target))node.append(target)}}/><ChatContext.Provider value={{openWorkspaceFile: vi.fn()} as unknown as ChatContextValue}><InboxTabs tabs={tabs}/></ChatContext.Provider></MemoryRouter>
}
const readWrites=()=>apiFetch.mock.calls.filter(([,init])=>init?.method==='PUT'&&JSON.parse(String(init.body)).read)
it('reopens A after choosing B in the tab list',async()=>{
 render(<Harness/>);act(()=>openInbox('a'));await screen.findByRole('heading',{name:'Subject a'})
 act(()=>openInbox('b'));await screen.findByRole('heading',{name:'Subject b'}) // the dock list opens items this way; the tab has no list of its own
 act(()=>openInbox('a'));await screen.findByRole('heading',{name:'Subject a'})
})
it('keeps mark unread across tab activation and a restored reader',async()=>{
 const view=render(<Harness/>);act(()=>openInbox('a'));await screen.findByRole('heading',{name:'Subject a'})
 await waitFor(()=>expect(items[0].read).toBe(true))
 fireEvent.click(screen.getByRole('button',{name:'Mark unread'}));await waitFor(()=>expect(items[0].read).toBe(false))
 fireEvent.click(screen.getByText('Toggle active'));fireEvent.click(screen.getByText('Toggle active'))
 await screen.findByRole('heading',{name:'Subject a'});expect(readWrites()).toHaveLength(1)
 view.unmount();render(<Harness restored="a"/>);await screen.findByRole('heading',{name:'Subject a'})
 expect(readWrites()).toHaveLength(1);expect(items[0].read).toBe(false)
 act(()=>openInbox('a'));await waitFor(()=>expect(readWrites()).toHaveLength(2))
})
it('preserves the reading selection for an open without an item',async()=>{
 render(<Harness/>);act(()=>openInbox('a'));await screen.findByRole('heading',{name:'Subject a'})
 await act(async()=>openInbox());await waitFor(()=>expect(screen.getByRole('heading',{name:'Subject a'})).toBeInTheDocument())
 expect(readWrites()).toHaveLength(1);expect(screen.getByTestId('selection')).toHaveTextContent('a')
})
it('shows mark-read failure separately from successful content loading',async()=>{
 failRead=true;render(<Harness/>);act(()=>openInbox('a'))
 await screen.findByRole('heading',{name:'Subject a'})
 expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t mark this item read')
 expect(screen.queryByText(/Couldn’t open/)).toBeNull()
})
it('leaves relative file links out of the current workspace resolver',async()=>{
 render(<Harness restored="a"/>);await screen.findByRole('heading',{name:'Subject a'})
 expect(screen.queryByRole('button',{name:'Relative file'})).toBeNull()
 expect(screen.queryByRole('button',{name:'some/file.md'})).toBeNull()
})

it('deletes from the reader and offers Undo without a confirmation',async()=>{
 const {useToastStore}=await import('../hooks/useToastStore')
 render(<Harness restored="a"/>)
 await screen.findByRole('heading',{name:'Subject a'})
 fireEvent.click(screen.getByRole('button',{name:'Delete'}))
 await waitFor(()=>expect(screen.queryByRole('heading',{name:'Subject a'})).toBeNull())
 expect(screen.queryByRole('button',{name:'Subject a, unread'})).toBeNull()
 const toast=useToastStore.getState().toasts.find(t=>t.action?.label==='Undo')!
 expect(toast).toBeTruthy()
 await act(async()=>toast.action!.onClick())
 // Undo restores the item: the reader shows it again.
 await screen.findByRole('heading',{name:'Subject a'})
 expect(apiFetch.mock.calls.some(([,init])=>init?.method==='DELETE')).toBe(false)
})

it('keeps a loaded reader empty after the delete is committed',async()=>{
 render(<Harness restored="a"/>)
 await screen.findByRole('heading',{name:'Subject a'})
 vi.useFakeTimers()
 try{
  fireEvent.click(screen.getByRole('button',{name:'Delete'}))
  await act(async()=>{await vi.advanceTimersByTimeAsync(5000)})
  expect(apiFetch).toHaveBeenCalledWith('/api/inbox/a',{method:'DELETE'})
  expect(screen.queryByRole('heading',{name:'Subject a'})).toBeNull()
  expect(screen.queryByRole('button',{name:'Subject a, unread'})).toBeNull()
 }finally{vi.useRealTimers()}
})
