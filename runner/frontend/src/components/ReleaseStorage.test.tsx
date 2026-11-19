import {render,screen,fireEvent,waitFor} from '@testing-library/react'
import {it,expect,vi} from 'vitest'
import ReleaseStorage from './ReleaseStorage'
import {apiFetch} from '../apiFetch'
vi.mock('../apiFetch',()=>({apiFetch:vi.fn()}))
it('lists losses and releases only after the person confirms the exact preview',async()=>{
 const digest='a'.repeat(64)
 vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({digest,files:['private/local.txt'],commits:['refs/heads/unsaved abc'],released:false}))).mockResolvedValueOnce(new Response(JSON.stringify({released:true})))
 render(<ReleaseStorage sessionId="stopped"/>);fireEvent.click(screen.getByRole('button',{name:'Release storage'}))
 const dialog=await screen.findByRole('alertdialog')
 expect(dialog).toHaveTextContent('private/local.txt');expect(dialog).toHaveTextContent('refs/heads/unsaved abc')
 expect(apiFetch).toHaveBeenCalledTimes(1)
 fireEvent.click(screen.getByRole('button',{name:'Cancel'}));expect(apiFetch).toHaveBeenCalledTimes(1)
 vi.mocked(apiFetch).mockReset().mockResolvedValueOnce(new Response(JSON.stringify({digest,files:['private/local.txt'],commits:[],released:false}))).mockResolvedValueOnce(new Response(JSON.stringify({released:true})))
 fireEvent.click(screen.getByRole('button',{name:'Release storage'}));await screen.findByRole('alertdialog')
 const buttons=screen.getAllByRole('button',{name:'Release storage'});fireEvent.click(buttons[buttons.length-1])
 await waitFor(()=>expect(screen.getByRole('button',{name:'Storage released'})).toBeDisabled())
 expect(apiFetch).toHaveBeenLastCalledWith('/api/sessions/stopped/workspace/storage',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({confirm:digest})})
})

it('discloses reflog loss and confirms a truncated inventory against its full digest',async()=>{
 const digest='b'.repeat(64)
 vi.mocked(apiFetch).mockReset().mockResolvedValueOnce(new Response(JSON.stringify({digest,files:['shown.txt'],commits:[],file_count:501,commit_count:4,truncated:true,released:false}))).mockResolvedValueOnce(new Response(JSON.stringify({released:true})))
 render(<ReleaseStorage sessionId="large"/>);fireEvent.click(screen.getByRole('button',{name:'Release storage'}))
 const dialog=await screen.findByRole('alertdialog')
 expect(dialog).toHaveTextContent('The reflog is not kept.');expect(dialog).toHaveTextContent('1 of 501');expect(dialog).toHaveTextContent('0 of 4');expect(dialog).toHaveTextContent('including entries not shown');expect(dialog).toHaveTextContent('Not shown in this preview.')
 const buttons=screen.getAllByRole('button',{name:'Release storage'});fireEvent.click(buttons[buttons.length-1])
 await waitFor(()=>expect(screen.getByRole('button',{name:'Storage released'})).toBeDisabled())
 expect(apiFetch).toHaveBeenLastCalledWith('/api/sessions/large/workspace/storage',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({confirm:digest})})
})

it('lists unsaved commits before files and omission notices',async()=>{
 vi.mocked(apiFetch).mockReset().mockResolvedValueOnce(new Response(JSON.stringify({digest:'c'.repeat(64),files:[],commits:['refs/heads/unsaved abc'],file_count:600,commit_count:1,truncated:true,released:false})))
 render(<ReleaseStorage sessionId="prioritized"/>);fireEvent.click(screen.getByRole('button',{name:'Release storage'}))
 await screen.findByRole('alertdialog')
 const commit=screen.getByText('refs/heads/unsaved abc')
 const omitted=screen.getByText('Not shown in this preview.')
 expect(commit.compareDocumentPosition(omitted)&Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})
