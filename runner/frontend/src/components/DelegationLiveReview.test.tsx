import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import DelegationLiveReview from './DelegationLiveReview'
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => fetchMock(...args) }))
const fetchMock = vi.fn()
const request = { request_id: 'request-a', parent_session_id: 'parent-a', status: 'pending', expires_at: '2099-01-01T00:00:00Z', offered: ['once', 'deny'], request: { engine: 'codex', runtime: 'docker', title: 'Review fixture', prompt: 'Public fixture prompt', model: 'fixture-model' } }
function mount() { return render(<MemoryRouter><DelegationLiveReview requestId="request-a" sessionId="parent-a" /></MemoryRouter>) }
describe('live delegation approval independent of conversation history', () => {
 beforeEach(() => fetchMock.mockReset())
 it('loads authoritative details with no transcript event and decides only after a click', async () => {
  fetchMock.mockResolvedValueOnce({ok:true,json:async()=>request}).mockResolvedValueOnce({ok:true,json:async()=>({request_id:'request-a',status:'denied'})})
  mount()
  expect(fetchMock).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  expect(await screen.findByText('Review fixture')).toBeInTheDocument()
  expect(screen.getByText('Public fixture prompt')).toBeInTheDocument()
  expect(screen.getByRole('button',{name:'Allow once'})).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Allow for this session'})).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button',{name:'Deny'}))
  await waitFor(()=>expect(fetchMock).toHaveBeenCalledTimes(2))
  expect(fetchMock.mock.calls[1]).toEqual(['/api/delegation-requests/request-a/decide',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({choice:'deny'})}])
  expect(await screen.findByText(/Denied/)).toBeInTheDocument()
 })
 it('reviews a cluster request as one on the cluster',async()=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>({...request,offered:['once','always','deny'],request:{...request.request,runtime:'cluster'}})})
  mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  expect(await screen.findByText('on the cluster')).toBeInTheDocument()
  expect(screen.getByRole('button',{name:'Always allow Codex on the cluster'})).toBeInTheDocument()
 })
 it('makes the full canonical long prompt reviewable before deciding',async()=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>({...request,request:{...request.request,prompt:'Public instructions '.repeat(50)+'TAIL_FIXTURE'}})})
  mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  fireEvent.click(await screen.findByRole('button',{name:'Show all'}))
  expect(await screen.findByText(/TAIL_FIXTURE/)).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
 })
 it('ignores a late response after switching sessions',async()=>{
  let resolve!: (value: unknown)=>void
  fetchMock.mockReturnValueOnce(new Promise(done=>{resolve=done})).mockResolvedValueOnce({ok:false,status:404})
  const view=mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  view.rerender(<MemoryRouter><DelegationLiveReview requestId="request-b" sessionId="parent-b" /></MemoryRouter>)
  resolve({ok:true,json:async()=>request})
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load')
  expect(screen.queryByRole('button',{name:'Allow once'})).not.toBeInTheDocument()
 })
 it('fails closed on an unauthorized response without posting any decision',async()=>{
  fetchMock.mockResolvedValue({ok:false,status:404});mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load')
  expect(screen.queryByRole('button',{name:'Allow once'})).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
 })
 it('disables decisions after a compare-and-set conflict until explicitly reopened',async()=>{
  fetchMock.mockResolvedValueOnce({ok:true,json:async()=>request}).mockResolvedValueOnce({ok:false,status:409})
  mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  fireEvent.click(await screen.findByRole('button',{name:'Allow once'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Already decided')
  expect(screen.getByRole('button',{name:'Allow once'})).toBeDisabled()
  expect(screen.getByRole('button',{name:'Deny'})).toBeDisabled()
  expect(fetchMock).toHaveBeenCalledTimes(2)
 })
 it.each(['claude','codex','hermes'])('shows %s requests after replay without requiring history',async engine=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>({...request,request:{...request.request,engine}})})
  mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  expect(await screen.findByRole('button',{name:'Allow once'})).toBeInTheDocument()
 })
 it.each([{...request,parent_session_id:'foreign'}, {...request,status:['pending']}, {...request,request:{...request.request,runtime:['daemon']}}, {...request,request_id:'foreign'}, {...request,status:'expired'}, {...request,expires_at:'2000-01-01T00:00:00Z'}])('never offers choices for foreign or expired response',async body=>{
  fetchMock.mockResolvedValue({ok:true,json:async()=>body});mount();fireEvent.click(screen.getByRole('button',{name:'Review request'}))
  await waitFor(()=>expect(screen.queryByText('Loading request…')).not.toBeInTheDocument())
  expect(screen.queryByRole('button',{name:'Allow once'})).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
 })
})
