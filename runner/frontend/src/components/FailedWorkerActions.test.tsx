import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import FailedWorkerActions from './FailedWorkerActions'
import { makeSession } from '../test/fixtures'
import { apiFetch } from '../apiFetch'
import * as ws from '../ws'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => Response.json({})) }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ send: async (session_id: string, text: string) => ({queued: ws.send({type:'agent_user_message',session_id,text})}) }) }))
afterEach(() => vi.clearAllMocks())
const worker = makeSession({ id: 'worker', parent_session_id: 'main', title: 'Review widget', status: 'error', started_by: {kind:'session'} })
it('discards only the named worker, never Main or its workspace', async () => {
 render(<MemoryRouter><FailedWorkerActions session={{...worker, ended_at: '2026-11-14T12:00:00Z', end_reason: 'start_failed'}} /></MemoryRouter>)
 expect(screen.queryByRole('button', {name:'Retry'})).toBeNull()
 expect(screen.queryByRole('button', {name:'End workspace'})).toBeNull()
 fireEvent.click(screen.getByRole('button', {name:'Discard worker'}))
 fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', {name:'Discard worker'}))
 await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/main/children/worker/discard', {method:'POST'}))
 expect(vi.mocked(apiFetch).mock.calls).toHaveLength(1)
 expect(ws.send).not.toHaveBeenCalled()
})
it('retries only a revivable worker through that worker’s input', async () => {
 vi.mocked(ws.send).mockReturnValue(true)
 render(<MemoryRouter><FailedWorkerActions session={worker} /></MemoryRouter>)
 fireEvent.click(screen.getByRole('button', {name:'Retry'}))
 await waitFor(() => expect(ws.send).toHaveBeenCalledWith({type:'agent_user_message',session_id:'worker',text:'Retry starting this worker.'}))
 expect(apiFetch).not.toHaveBeenCalled()
})
it('offers no actions for Main or an archived worker', () => {
 const {rerender} = render(<MemoryRouter><FailedWorkerActions session={{...worker,parent_session_id:null}} /></MemoryRouter>)
 expect(screen.queryByRole('button')).toBeNull()
 rerender(<MemoryRouter><FailedWorkerActions session={{...worker,archived_at:'2026-11-14T12:00:00Z'}} /></MemoryRouter>)
 expect(screen.queryByRole('button')).toBeNull()
})
