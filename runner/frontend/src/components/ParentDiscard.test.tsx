import {beforeEach, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import {ParentDiscardAction, ChildDiscardAction} from './ChildDiscard'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({apiFetch}))
vi.mock('../ws', () => import('../test/wsMock'))
beforeEach(() => {apiFetch.mockReset()})

it('requires explicit confirmation and cancellation sends no request', () => {
  render(<ParentDiscardAction sessionId="parent /" title="Parent"/>)
  fireEvent.click(screen.getByRole('button', {name:'Discard Parent'}))
  expect(screen.getByRole('dialog', {name:'Discard session and all children?'})).toBeInTheDocument()
  expect(screen.getByText(/including children marked Keep running and work in progress/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name:'Cancel'}))
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})

it('leaves partial failure visible and retries the same idempotent route', async () => {
  const done = vi.fn()
  apiFetch.mockResolvedValueOnce({ok:false}).mockResolvedValueOnce({ok:true,json:async()=>({status:'discarded',session_id:'parent /',archived_at:'2026-10-20T12:00:00Z'})})
  render(<ParentDiscardAction sessionId="parent /" title="Parent" onDiscarded={done}/>)
  fireEvent.click(screen.getByRole('button', {name:'Discard Parent'}))
  fireEvent.click(screen.getByRole('button', {name:'Discard session and children'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Retry to complete any remaining steps')
  expect(done).not.toHaveBeenCalled()
  expect(screen.getByRole('dialog')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name:'Discard session and children'}))
  await waitFor(() => expect(done).toHaveBeenCalledTimes(1))
  expect(apiFetch).toHaveBeenCalledTimes(2)
  for (const call of apiFetch.mock.calls) expect(call).toEqual(['/api/sessions/parent%20%2F/discard', {method:'POST'}])
})

it('does not navigate away from another session when an old request completes', async () => {
  let finish!: (r:{ok:boolean;json:()=>Promise<unknown>}) => void
  apiFetch.mockImplementation(() => new Promise(resolve => {finish = resolve}))
  const done = vi.fn()
  const view = render(<ParentDiscardAction sessionId="first" title="First" onDiscarded={done}/>)
  fireEvent.click(screen.getByRole('button', {name:'Discard First'}))
  fireEvent.click(screen.getByRole('button', {name:'Discard session and children'}))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  view.rerender(<ParentDiscardAction sessionId="second" title="Second" onDiscarded={done}/>)
  await act(async () => {finish({ok:true,json:async()=>({status:'discarded',session_id:'first',archived_at:'2026-10-20T12:00:00Z'})})})
  expect(done).not.toHaveBeenCalled()
  expect(screen.getByRole('button', {name:'Discard Second'})).toBeEnabled()
  expect(screen.queryByRole('dialog')).toBeNull()
})


it.each([{}, {status:'discarded',session_id:'other',archived_at:'2026-10-20T12:00:00Z'}, {status:'discarded',session_id:'parent',archived_at:'invalid'}])('refuses an unconfirmed success receipt: %j', async receipt => {
  const done=vi.fn()
  apiFetch.mockResolvedValue({ok:true,json:async()=>receipt})
  render(<ParentDiscardAction sessionId="parent" title="Parent" onDiscarded={done}/>)
  fireEvent.click(screen.getByRole('button',{name:'Discard Parent'}))
  fireEvent.click(screen.getByRole('button',{name:'Discard session and children'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not finish discarding')
  expect(done).not.toHaveBeenCalled()
  expect(screen.getByRole('dialog')).toBeInTheDocument()
})


it('directs a child-discard conflict back to the pending parent operation', async () => {
  apiFetch.mockResolvedValue({ok:false,status:409})
  render(<ChildDiscardAction parentId="parent" childId="child" title="Worker"/>)
  fireEvent.click(screen.getByRole('button',{name:'Discard Worker'}))
  fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Retry discarding its parent to finish')
  expect(screen.getByRole('dialog',{name:'Discard child session?'})).toBeInTheDocument()
})
