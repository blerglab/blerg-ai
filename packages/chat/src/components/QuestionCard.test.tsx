import {fireEvent, render, screen, within} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import QuestionCard from './QuestionCard'
import { QuestionThreadsProvider, type QuestionThreadsClient } from './questionThreads'
import AskReply from './AskReply'
const instruction='Reply to this question with yes to commit these exact changes, or no to decline. Other replies ask for clarification.'
const question=`Plan changes · 10 add, 0 remove, 0 move\n\nRevision 0\n\n${Array.from({length:10},(_,i)=>`- Add: DEMO #${i+1}\n  Reason: Bug\n  Thrust: Reliable sessions`).join('\n')}\n\n${instruction} Expires in 24 hours.`
for(const buttons of [false,true]) it(`hides instructions only with decision buttons: ${buttons}`,()=>{
 render(<QuestionCard audience="you" id="q" question={question} decisionButtons={buttons}><AskReply binary={buttons} answer={async()=>true}/></QuestionCard>)
 expect(screen.getByRole('button',{name:/Plan changes/})).toBeTruthy()
 fireEvent.click(screen.getByRole('button',{name:/Plan changes/}))
 const dialog=screen.getByRole('dialog')
 expect(dialog.textContent?.includes(instruction)).toBe(!buttons)
 expect(dialog).toHaveTextContent('Expires in 24 hours.')
 // A decision's row is its title only (owner 2026-11-05: the clamped body showed a bare '…');
 // the full text stays in the hovercard and the dialog.
 const row=screen.getByRole('button',{name:/Plan changes/}).textContent ?? ''
 expect(row).toContain('Plan changes · 10 add, 0 remove, 0 move')
 expect(row).not.toContain(instruction)
})
it('groups all cards by thrust without repeating the thrust in each row',()=>{
 render(<QuestionCard id="q" question={question} decisionButtons><AskReply binary answer={async()=>true}/></QuestionCard>)
 fireEvent.click(screen.getByRole('button',{name:/Plan changes/}))
 const dialog=screen.getByRole('dialog')
 expect(within(dialog).getByRole('heading',{name:'Reliable sessions · 10'})).toBeVisible()
 expect(dialog.querySelectorAll('.question-plan-title')[9]).toHaveTextContent('DEMO #10')
 expect(dialog.querySelectorAll('.question-plan-operation')).toHaveLength(10)
 expect(dialog.querySelector('details')).toBeNull()
 expect(dialog.querySelector('.question-plan-context')).toHaveTextContent('Add · Bug')
 expect(dialog.querySelector('.question-plan-operation')).toHaveTextContent('DEMO #1')
 expect(dialog.querySelector('.question-plan-operation')).not.toHaveTextContent('Reliable sessions')
})

it('hides typed instructions for non-plan decisions while preserving warnings',()=>{
 const body='Approve live binding · tool\n\nPinned revision 1\n\nReply to this question with yes to approve this exact revision, or no to decline. Polling is not available yet.'
 render(<QuestionCard id="live" question={body} decisionButtons><AskReply binary answer={async()=>true}/></QuestionCard>)
 fireEvent.click(screen.getByRole('button',{name:/Approve live binding/}))
 const dialog=screen.getByRole('dialog')
 expect(dialog).not.toHaveTextContent('Reply to this question with yes')
 expect(dialog).toHaveTextContent('Polling is not available yet.')
})

it('preserves mixed operations, destinations, and ungrouped changes',()=>{
 const mixed='Plan changes · 1 add, 1 remove, 1 move\n\nMilestone: 0.6.0\n\n- Add: BLG-1 · Clear decisions\n  Reason: Bug\n  Thrust: Readability\n- Move: BLG-2 · Deferred work\n  Reason: Scope changed\n  Destination: 0.7.0\n- Remove: BLG-3 · Old work\n  Reason: No longer needed\n  Thrust: Readability\n\nExpires in 24 hours.'
 render(<QuestionCard id="mixed" question={mixed} decisionButtons/>)
 fireEvent.click(screen.getByRole('button',{name:/Plan changes/}))
 const dialog=screen.getByRole('dialog')
 expect(within(dialog).getByRole('heading',{name:'Readability · 2'})).toBeVisible()
 expect(within(dialog).getByRole('heading',{name:'Other changes · 1'})).toBeVisible()
 expect(dialog).toHaveTextContent('Scope changed · Destination: 0.7.0')
 expect(dialog).toHaveTextContent('No longer needed')
 expect(dialog.querySelectorAll('.question-plan-operation')).toHaveLength(3)
})

it('drops the heading when every change is ungrouped',()=>{
 const cut='Cut 0.6.0 · 1 archive, 1 move\n\nMilestone: 0.6.0\n\n- Archive: #2 Finished work\n- Move: #1 Carry this forward\n\nExpires in 24 hours.'
 render(<QuestionCard id="cut" question={cut} decisionButtons/>)
 fireEvent.click(screen.getByRole('button',{name:/Cut 0.6.0/}))
 const dialog=screen.getByRole('dialog')
 expect(within(dialog).queryByRole('heading',{name:/Other changes/})).toBeNull()
 expect(dialog.querySelectorAll('.question-plan-operation')).toHaveLength(2)
})

it.each([
 'Plan changes · 1 add\n\nRevision 1\n\n- Add: BLG-514\n  Reason: Bug\n  Thrust: Questions\n\nExpires in 24 hours.',
 'Approve change\n\nReview BLG-514 before approving.',
])('keeps ticket references active inside decision snapshots: %s', async body => {
 const { ReferenceScope } = await import('./RefLinks')
 const openCard = vi.fn()
 const refs = { boardCards: async () => new Map([[514, { kind: 'card' as const, source: 'blerg-board', boardId: 'board', key: 'BLG-514', title: 'Question layout' }]]), openCard }
 render(<ReferenceScope sessionId={`snapshot:${body}`} refs={refs}><QuestionCard id="snapshot" question={body} decisionButtons /></ReferenceScope>)
 fireEvent.click(screen.getByRole('button', { name: /Plan changes|Approve change/ }))
 fireEvent.click(await within(screen.getByRole('dialog')).findByRole('button', { name: /Open card BLG-514/ }))
 expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ key: 'BLG-514', boardId: 'board' }))
})

it('uses one turn glyph with accessible words in the Needs you row',()=>{
 render(<QuestionCard id="turn" question="Which branch?" audience="you" turn="main" />)
 const glyph=screen.getByLabelText('Main is replying')
 expect(glyph.textContent).toBe('↩')
 expect(glyph).toHaveAttribute('title','Main is replying')
 expect(screen.queryByText('Main is replying')).toBeNull()
})

it('labels a worker that has the turn as replying rather than Needs you',()=>{
 render(<QuestionCard id="replying" question="Which branch?" worker="Release worker" audience="you" turn="main" />)
 expect(screen.getByRole('button',{name:/Which branch/})).toHaveTextContent('Release worker · replying')
 expect(screen.getByRole('button',{name:/Which branch/})).not.toHaveTextContent('Needs you')
})

// Thread transport must not own metadata or navigation visibility.
it('keeps waiting details in the message tooltip and navigation while loading',()=>{
 const client: QuestionThreadsClient = { load: () => new Promise(()=>{}), reply: vi.fn(), answer: vi.fn(), dismiss: vi.fn() }
 render(<QuestionThreadsProvider client={client}><QuestionCard id="pending" question="Which branch?" wait="asked just now" activity="The agent stopped waiting. Your answer will be sent as a message." wrapActivity statusAction={<a href="/worker" aria-label="Open worker">↗</a>}><AskReply messageId="pending" answer={async()=>true} actions={<button>Dismiss</button>}/></QuestionCard></QuestionThreadsProvider>)
 fireEvent.click(screen.getByRole('button',{name:/Which branch/}))
 const dialog=screen.getByRole('dialog')
 expect(within(dialog).queryByText('Your turn')).toBeNull()
 expect(within(dialog).getByTitle(/The agent stopped waiting/)).toBeTruthy()
 expect(within(dialog).getByRole('link',{name:'Open worker'})).toHaveAttribute('href','/worker')
 expect(within(dialog).getByRole('textbox',{name:'Your answer'})).toBeVisible()
})

it('drops only the first title line while preserving every later line',()=>{
 const question='Choose rollout\n\nKeep the migration notes.\n\nChoose rollout\nKeep the rollback notes.'
 render(<QuestionCard id="title-once" question={question} />)
 fireEvent.click(screen.getByRole('button',{name:/Choose rollout/}))
 const dialog=screen.getByRole('dialog')
 const message=dialog.querySelector('.question-conversation')!
 expect(message.textContent?.match(/Choose rollout/g)).toHaveLength(1)
 expect(message).toHaveTextContent('Keep the migration notes.')
 expect(message).toHaveTextContent('Keep the rollback notes.')
 expect(dialog.querySelector('.ticket-modal-title')).toHaveAttribute('title','Choose rollout')
})

it('keeps a one-paragraph question fully readable once in the header',()=>{
 const long='The clean-shutdown marker prototype fails a real SQL test: clean stop, boot while the authority is down, crash, restore. May I keep the conservative fallback?'
 const {unmount}=render(<QuestionCard id="one-paragraph" question={long} />)
 fireEvent.click(screen.getByRole('button',{name:/clean-shutdown/}))
 const dialog=screen.getByRole('dialog')
 expect(dialog.querySelector('.ticket-modal-title')).toHaveAttribute('title',long)
 expect(dialog.textContent).toContain('May I keep the conservative fallback?')
 expect(within(dialog).getByRole('heading', { name: long })).toHaveTextContent(long)
 expect(dialog.textContent!.split('May I keep the conservative fallback?')).toHaveLength(2)
 expect(dialog.querySelector('.question-conversation')).not.toHaveTextContent(long)
 unmount()
})

it.each([['Main', 'Ask Main'], ['BLG-514 · Improve question layout', 'Ask BLG-514']])('names the asker %s in open-ended replies', async (worker, label) => {
 const reply = vi.fn(async () => true)
 render(<QuestionCard id="asker" question="Approve this change?" worker={worker}><AskReply binary answer={async () => true} reply={reply} /></QuestionCard>)
 fireEvent.click(screen.getByRole('button', { name: /Approve this change/ }))
 const button = screen.getByRole('button', { name: label })
 expect(button.title).toContain(worker)
 fireEvent.click(button)
 expect(screen.getByRole('button', { name: label }).title).toContain(worker)
 fireEvent.change(screen.getByRole('textbox'), { target: { value: 'What changed?' } })
 fireEvent.click(screen.getByRole('button', { name: label }))
 await screen.findByRole('textbox')
 expect(reply).toHaveBeenCalledWith('What changed?', expect.any(String))
})

it('embeds the same complete question conversation and composer without a dialog', () => {
 render(<QuestionCard asCard id="embedded" worker="Review release" question={'Choose the release?\nOption B preserves the reviewed draft.'} statusAction={<a href="#conversation">Open conversation</a>}><AskReply compact answer={async()=>true}/></QuestionCard>)
 const card=screen.getByRole('region',{name:'Question: Choose the release?'})
 expect(within(card).getByRole('log',{name:'Question discussion'})).toHaveTextContent('Review release')
 expect(card).toHaveTextContent('Option B preserves the reviewed draft.')
 expect(within(card).getByRole('textbox')).toBeVisible()
 expect(within(card).getByRole('link',{name:'Open conversation'}).closest('header')).not.toBeNull()
 expect(screen.queryByRole('dialog')).toBeNull()
})
