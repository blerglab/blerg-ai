import { act, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import BlackboardTabs from './BlackboardTabs'
import type { SessionTabs } from '../hooks/useSessionTabs'
const read = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch: read }))
vi.mock('../hooks/useBlackboardDraft', () => ({ useBlackboardRevision: () => 0, useBlackboardDraft: () => null }))
const json = (body: unknown) => new Response(JSON.stringify(body))
const record = { name: 'Plan', goals: [{ id: 'g', name: 'Reliability' }], milestones: [{ id: 'm', name: 'Release' }], thrusts: [{ id: 't', name: 'Reliable work', milestone_id: 'm', goal_id: 'g' }] }
function mount(id: string, sessionId = 's') {
 const target = document.createElement('div'); document.body.append(target)
 const tabs = { tabs: [{ id: 'tab', view: { kind: 'blackboard', blackboardId: id, path: { level: 'milestone', milestoneId: 'm' } } }], visible: ['tab'], targetFor: () => target } as unknown as SessionTabs
 const app = render(<BlackboardTabs tabs={tabs} sessionId={sessionId} />)
 return () => { app.unmount(); target.remove() }
}
it('shows an accessible skeleton before the record and placeholders until slices arrive', async () => {
 let releaseRecord!: () => void, releaseSlices!: () => void
 const recordWait = new Promise<void>(r => { releaseRecord = r })
 const sliceWait = new Promise<void>(r => { releaseSlices = r })
 read.mockImplementation((url: string) => (url.includes('/slice') ? sliceWait.then(() => json({ cards: [], complete: true })) : recordWait.then(() => json({ record }))))
 const unmount = mount('first-load')
 expect(screen.getByRole('status')).toHaveTextContent('Loading blackboard')
 expect(document.querySelector('.blackboard-skeleton')).toBeTruthy()
 await act(async () => { releaseRecord() })
 expect(screen.getByRole('heading', { name: 'Release' })).toBeTruthy()
 expect(document.querySelectorAll('.blackboard-count-skeleton').length).toBeGreaterThan(3)
 expect(document.querySelector('.blackboard-filters b')).toBeNull()
 expect(screen.queryByText(/0 of 0|No cards yet|No cards assigned yet|All shipped/)).toBeNull()
 await act(async () => { releaseSlices() })
 expect(document.querySelector('.blackboard-bars-enter')).toBeTruthy()
 expect(document.querySelector('.blackboard-count-skeleton')).toBeNull()
 expect(document.querySelector('.blackboard-skeleton')).toBeNull()
 unmount()
})
it('renders complete cached data synchronously on remount while refreshing, scoped to the query', async () => {
 read.mockImplementation((url: string) => Promise.resolve(json(url.includes('/slice') ? { cards: [], complete: true } : { record })))
 const unmount = mount('cached')
 await screen.findByRole('heading', { name: 'Release' })
 await act(async () => {})
 unmount()
 read.mockClear(); read.mockImplementation(() => new Promise(() => {}))
 const again = mount('cached')
 expect(screen.getByRole('heading', { name: 'Release' })).toBeTruthy()
 expect(screen.queryByRole('status')).toBeNull()
 expect(document.querySelector('.blackboard-bars-enter')).toBeNull()
 expect(read).toHaveBeenCalled()
 again()
 const different = mount('cached', 'another-session')
 expect(screen.getByRole('status')).toHaveTextContent('Loading blackboard')
 different()
})
it.each(['revision', 'identity'])('discards cached data on %s invalidation', async reason => {
 read.mockImplementation((url: string) => Promise.resolve(json(url.includes('/slice') ? { cards: [], complete: true } : { record })))
 const unmount = mount('invalidate-' + reason)
 await screen.findByRole('heading', { name: 'Release' })
 await act(async () => {})
 unmount()
 if (reason === 'identity') window.dispatchEvent(new Event('blerg-identity-changed'))
 else {
  const { invalidateBlackboardReads } = await import('../lib/blackboardReads')
  invalidateBlackboardReads(['s'])
 }
 read.mockImplementation(() => new Promise(() => {}))
 const again = mount('invalidate-' + reason)
 expect(screen.getByRole('status')).toHaveTextContent('Loading blackboard')
 again()
})
it('logs a safe cause and preserves error and retry', async () => {
 const log = vi.spyOn(console, 'error').mockImplementation(() => {})
 read.mockResolvedValue(new Response(null, { status: 403 }))
 const unmount = mount('forbidden')
 await screen.findByText('This workspace cannot read this blackboard.')
 expect(screen.getByRole('button', { name: 'Retry blackboard' })).toBeTruthy()
 expect(log).toHaveBeenCalledWith('Could not load blackboard view.', { cause: 'response' })
 unmount(); log.mockRestore()
})
it('does not present unavailable slices as real zero counts', async () => {
 read.mockImplementation((url: string) => Promise.resolve(json(url.includes('/slice') ? { cards: [], complete: false } : { record })))
 const unmount = mount('incomplete')
 await screen.findByText('Some board data is unavailable. Counts may be incomplete.')
 expect(document.querySelector('.blackboard-filters b')).toBeNull()
 expect(screen.queryByText(/0 of 0|No cards yet|No cards assigned yet|All shipped/)).toBeNull()
 unmount()
})
