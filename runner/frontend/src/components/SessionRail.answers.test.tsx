import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'

it('opens the requested page from Needs you',()=>{
 const open=vi.fn()
 render(<MemoryRouter><SessionRail id="rail" session={makeSession()} live={makeLive()} toolChanges={0} mode="panel" open onClose={()=>{}} onOpenArtifact={open}
 answerRequests={[{request_id:'r',artifact_id:'a',artifact_name:'rank.html',ask:'Rank these options'}]}/></MemoryRouter>)
 const needs=screen.getByTestId('rail-needs')
 expect(within(needs).queryByText('Nothing needs you.')).toBeNull()
 fireEvent.click(within(needs).getByRole('button',{name:/Answer: rank.html/}))
 expect(open).toHaveBeenCalledExactlyOnceWith('a')
})

for (const close of ['Escape', 'backdrop']) it(`keeps a rail question draft after ${close}`, () => {
 const live = makeLive({ openAsks: [{ eventId: 'ask-one', messageId: 'question-one', question: 'Choose a color?', askedAt: Date.now(), noWait: true }], answer: vi.fn(async () => true) })
 render(<MemoryRouter><SessionRail onOpenArtifact={()=>{}} id="draft-rail" session={makeSession()} live={live} toolChanges={0} mode="panel" open onClose={()=>{}} /></MemoryRouter>)
 const open = () => fireEvent.click(screen.getByRole('button', { name: /Choose a color/ }))
 open()
 fireEvent.change(screen.getByLabelText('Your answer'), { target: { value: 'Blue draft' } })
 if (close === 'Escape') fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
 else {
  const overlay = screen.getByTestId('ticket-modal-overlay')
  fireEvent.pointerDown(overlay)
  fireEvent.click(overlay)
 }
 expect(screen.queryByRole('dialog')).toBeNull()
 open()
 expect(screen.getByLabelText('Your answer')).toHaveValue('Blue draft')
})

it('offers decision buttons in Needs you before a transcript is loaded', async () => {
 const { useMessageStore } = await import('../hooks/useMessageStore')
 const { useTranscriptStore } = await import('@blerglab/chat')
 useTranscriptStore.setState({ sessions: {} })
 useMessageStore.setState({ messages: [{ id: 'board-decision', session_id: 'sess-a', kind: 'ask', body: 'Link Engineering (ENG)?', status: 'open', answerable: true, decision: true, created_at: '' }] })
 const answer = vi.fn(async () => true)
 const live = makeLive({ openAsks: [{ eventId: 'decision-event', messageId: 'board-decision', question: 'Link Engineering (ENG)?', askedAt: Date.now(), noWait: true }], answer })
 render(<MemoryRouter><SessionRail onOpenArtifact={()=>{}} id="decision-rail" session={makeSession()} live={live} toolChanges={0} mode="panel" open onClose={()=>{}} /></MemoryRouter>)
 fireEvent.click(screen.getByRole('button', { name: /Link Engineering/ }))
 expect(screen.queryByRole('textbox')).toBeNull()
 fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
 expect(answer).toHaveBeenCalledWith('yes', 'board-decision')
 useMessageStore.setState({ messages: [] })
})
