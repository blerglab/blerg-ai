import { afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import WorkspaceLiveMetrics from './WorkspaceLiveMetrics'
import { richActivity } from '../test/activityFixture'

const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
const report={generated_at:'2026-10-31T20:00:00Z',workers:[{session_id:'worker-1',status:'blocked',age_seconds:120,gate_fraction:.69,turn_count:2}],machine:{at:'2026-10-31T19:59:00Z',load_1:3.2,cpus:4,disk_free_bytes:10*1024**3},runs:[{id:'running',session_id:'worker-1',suite:'Runner unit',state:'running',started_at:'2026-10-31T19:58:00Z'},{id:'previous',session_id:'worker-1',suite:'Runner unit',state:'finished',started_at:'2026-10-31T19:50:00Z',ended_at:'2026-10-31T19:51:00Z'}]}
afterEach(()=>{vi.useRealTimers();vi.restoreAllMocks();apiFetch.mockReset()})
it('shows the owned worker state, registered duration samples and fresh load without ids',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>report})
 render(<WorkspaceLiveMetrics workspace="workspace-a" sessions={richActivity.sessions}/>)
 expect(await screen.findByText(/Last status update 2 min ago/)).toHaveTextContent('69% in gates across 2 recorded turns')
 expect(screen.getByRole('img',{name:'Waiting for an answer'})).toBeInTheDocument()
 expect(screen.getByText(/median 1 min, n 1 \(small group\)/)).toHaveTextContent('2 min elapsed')
 expect(screen.getByText(/Load 3.2 across 4 CPUs/)).toBeInTheDocument()
 expect(screen.getByRole('link',{name:'Improve ticket flow'})).toHaveAttribute('href','/sessions/worker-1')
 expect(screen.queryByText('worker-1')).toBeNull()
})
it('distinguishes unavailable live data, retries and marks stale machine samples unknown',async()=>{
 apiFetch.mockResolvedValueOnce({ok:false}).mockResolvedValue({ok:true,json:async()=>({...report,machine:{...report.machine,at:'2026-10-31T19:00:00Z'}})})
 render(<WorkspaceLiveMetrics workspace="workspace-a" sessions={[]}/>)
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not refresh live metrics')
 fireEvent.click(screen.getByRole('button',{name:'Retry live metrics'}))
 expect(await screen.findByText('No recent machine sample is available.')).toBeInTheDocument()
})
it('polls only while visible, aborts on unmount and hides the previous workspace',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>report})
 const visibility=vi.spyOn(document,'visibilityState','get').mockReturnValue('visible')
 vi.useFakeTimers({toFake:['setInterval','clearInterval']})
 const view=render(<WorkspaceLiveMetrics workspace="first" sessions={[]}/>)
 await screen.findByText(/Load 3.2/)
 await act(async()=>{vi.advanceTimersByTime(30_000)})
 expect(apiFetch).toHaveBeenCalledTimes(2)
 visibility.mockReturnValue('hidden')
 await act(async()=>{vi.advanceTimersByTime(30_000)})
 expect(apiFetch).toHaveBeenCalledTimes(2)
 apiFetch.mockReturnValue(new Promise(()=>{}))
 view.rerender(<WorkspaceLiveMetrics workspace="second" sessions={[]}/>)
 expect(screen.queryByText(/Load 3.2/)).toBeNull()
 const signal=(apiFetch.mock.calls.at(-1)![1] as RequestInit).signal!
 view.unmount()
 expect(signal.aborted).toBe(true)
})
