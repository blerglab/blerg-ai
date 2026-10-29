import {beforeEach, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import {ParentDiscardAction, ChildDiscardAction} from './ChildDiscard'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({apiFetch}))
vi.mock('../ws', () => import('../test/wsMock'))
const forgetSessionAttachments = vi.hoisted(() => vi.fn(async () => {}))
vi.mock('@blerglab/chat', async (original) => ({...(await original<typeof import('@blerglab/chat')>()), forgetSessionAttachments}))
beforeEach(() => {apiFetch.mockReset(); forgetSessionAttachments.mockClear()})

it('requires explicit confirmation and cancellation sends no request', () => {
  render(<ParentDiscardAction sessionId="parent /" title="Parent"/>)
  fireEvent.click(screen.getByRole('button', {name:'Discard Parent'}))
  expect(screen.getByRole('alertdialog', {name:'Discard session and all children?'})).toBeInTheDocument()
  expect(screen.getByText(/including children marked Keep running and work in progress/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name:'Cancel'}))
  expect(screen.queryByRole('alertdialog')).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})

it('leaves partial failure visible and retries the same idempotent route', async () => {
  const done = vi.fn()
  apiFetch.mockResolvedValueOnce({ok:false}).mockResolvedValueOnce({ok:true,json:async()=>({status:'discarded',session_id:'parent /',archived_at:'2026-10-20T12:00:00Z'})})
  render(<ParentDiscardAction sessionId="parent /" title="Parent" onDiscarded={done}/>)
  fireEvent.click(screen.getByRole('button', {name:'Discard Parent'}))
  fireEvent.click(screen.getByRole('button', {name:'Discard session and children'}))
  await waitFor(() => expect(screen.getByRole('alertdialog')).toHaveTextContent('Retry to complete any remaining steps'))
  expect(done).not.toHaveBeenCalled()
  expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name:'Retry'}))
  await waitFor(() => expect(done).toHaveBeenCalledTimes(1))
  // Only the confirmed discard forgets the session's waiting files on this device (#231 review, 7).
  expect(forgetSessionAttachments).toHaveBeenCalledTimes(1)
  expect(forgetSessionAttachments).toHaveBeenCalledWith('parent /')
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
  expect(screen.queryByRole('alertdialog')).toBeNull()
})


it.each([{}, {status:'discarded',session_id:'other',archived_at:'2026-10-20T12:00:00Z'}, {status:'discarded',session_id:'parent',archived_at:'invalid'}])('refuses an unconfirmed success receipt: %j', async receipt => {
  const done=vi.fn()
  apiFetch.mockResolvedValue({ok:true,json:async()=>receipt})
  render(<ParentDiscardAction sessionId="parent" title="Parent" onDiscarded={done}/>)
  fireEvent.click(screen.getByRole('button',{name:'Discard Parent'}))
  fireEvent.click(screen.getByRole('button',{name:'Discard session and children'}))
  // The confirmation dialog already exists; wait for the asynchronous error state.
  await waitFor(() => expect(screen.getByRole('alertdialog')).toHaveTextContent('Could not finish discarding'))
  expect(done).not.toHaveBeenCalled()
  expect(screen.getByRole('alertdialog')).toBeInTheDocument()
})


it('directs a child-discard conflict back to the pending parent operation', async () => {
  apiFetch.mockResolvedValue({ok:false,status:409})
  render(<ChildDiscardAction parentId="parent" childId="child" title="Worker"/>)
  fireEvent.click(screen.getByRole('button',{name:'Discard Worker'}))
  fireEvent.click(screen.getByRole('button',{name:'Discard child session'}))
  await waitFor(() => expect(screen.getByRole('alertdialog')).toHaveTextContent('Retry discarding its parent to finish'))
  expect(screen.getByRole('alertdialog',{name:"Discard didn't finish"})).toBeInTheDocument()
})

it('says a session is running before it is discarded, from the sidebar menu and the panel row', () => {
  const { unmount } = render(<ChildDiscardAction parentId="parent" childId="child" title="Busy worker" running />)
  fireEvent.click(screen.getByRole('button', {name:'Discard Busy worker'}))
  expect(screen.getByTestId('discard-running')).toHaveTextContent('It is running now. Discarding stops the work it is doing.')
  unmount()
  render(<ChildDiscardAction parentId="parent" childId="idle" title="Idle worker" />)
  fireEvent.click(screen.getByRole('button', {name:'Discard Idle worker'}))
  expect(screen.queryByTestId('discard-running')).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
})
