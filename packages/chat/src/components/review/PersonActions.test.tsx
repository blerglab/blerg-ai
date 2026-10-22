import {fireEvent, render, screen, waitFor, within} from '@testing-library/react'
import {describe, expect, it, vi} from 'vitest'
import ReviewMode from './ReviewMode'
import {ChatContext, type ChatContextValue} from '../context'
import {mergeReviews, parseReviewFile, settleReviewRequest, type ReviewFile, type ReviewRequest} from '../../model/review'
import type {ArtifactInfo} from '../../transport/types'

const PATCH = 'diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,2 +1,2 @@\n-oldName\n+newName\n tail\n'
const artifact: ArtifactInfo = {id: 'v1', name: 'rename.diff', size: PATCH.length, view: 'diff', content_type: 'text/x-diff', version: 1}
const thread: ReviewRequest = {id: 't1', text: 'Pick a clearer name', status: 'open', createdAt: '2026-10-21T10:00:00Z',
  anchor: {kind: 'text', quote: 'newName', diff: {artifactId: artifact.id, file: 'src/a.ts', side: 'new', start: 1, end: 1, hunk: 1, fileIndex: 1}}}
const file = (requests: ReviewRequest[]): ReviewFile => ({version: 1, file: {name: artifact.name, artifactId: artifact.id}, requests})

function show(review: ReviewFile | null) {
  const submit = vi.fn(async (_text: string, _files: File[]) => true)
  const value: ChatContextValue = {sessionId: 's', files: null, view: vi.fn(), byName: () => undefined, latestVersion: () => undefined,
    reviewFor: async () => review, submitFeedback: submit}
  const shown = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}}/></ChatContext.Provider>)
  return {...shown, submit}
}
async function findRow(root: HTMLElement) {
  return waitFor(() => {
    const row = root.querySelector<HTMLElement>('.diff-comment-row')
    if (!row) throw new Error('no thread row yet')
    return row
  })
}
async function sent(submit: ReturnType<typeof show>['submit']) {
  await waitFor(() => expect(submit).toHaveBeenCalledOnce())
  const [message, files] = submit.mock.calls[0]
  return {message, review: parseReviewFile(await files[0].text())!}
}

describe('the person replies to and resolves review threads', () => {
  it('replies to a thread in Files; the agent gets the reply with the next review', async () => {
    const {submit, container} = show(file([thread]))
    const row = await findRow(container)
    fireEvent.click(within(row).getByRole('button', {name: 'Reply to: Pick a clearer name'}))
    fireEvent.change(within(row).getByRole('textbox', {name: 'Reply'}), {target: {value: 'Something like renameTarget?'}})
    fireEvent.click(within(row).getByRole('button', {name: 'Reply'}))
    expect(within(row).getByText('Person · replied')).toBeInTheDocument()
    expect(within(row).getByText('Pending review')).toBeInTheDocument()
    expect(screen.getByText('1 open · 0 resolved · 0 rejected')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {message, review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'open', revision: 1, comments: [{text: 'Something like renameTarget?', action: 'reply', revision: 1}]})
    expect(message).toContain('Open thread; the person replied (revision 1).\nPerson · reply, revision 1:\n> Something like renameTarget?')
  })

  it('resolves a thread from the Conversation: the counts update, it says who, and the agent is told it stands', async () => {
    const {submit} = show(file([thread]))
    fireEvent.click(await screen.findByRole('tab', {name: 'Conversation'}))
    const card = (await screen.findByTestId('conversation-thread')) as HTMLElement
    fireEvent.click(within(card).getByRole('button', {name: 'Resolve: Pick a clearer name'}))
    expect(screen.getByText('0 open · 1 resolved · 0 rejected')).toBeInTheDocument()
    expect(within(card).getByText(/✓ Resolved/)).toBeInTheDocument()
    expect(card).toHaveTextContent('conversation by the person')
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {message, review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'done', settledBy: 'person', revision: 1})
    expect(message).toContain('Resolved by the person (revision 1)')
  })

  it("keeps the person's resolve when an older agent status arrives, and shows an agent reply that came after it", async () => {
    const resolved = settleReviewRequest(thread, 'done')
    const olderAgent = file([{...thread, status: 'declined', settledBy: 'agent', reply: 'Kept it', revision: 0}])
    const afterAgent = file([{...resolved, status: 'declined', settledBy: 'agent', reply: 'I still think the old name is fine',
      replies: [{status: 'declined', text: 'I still think the old name is fine', createdAt: '2026-10-21T12:00:00Z', revision: 1}]}])
    expect(mergeReviews(file([resolved]), olderAgent)!.requests[0]).toMatchObject({status: 'done', settledBy: 'person'})
    show(mergeReviews(file([resolved]), afterAgent))
    fireEvent.click(await screen.findByRole('button', {name: 'Show thread: Pick a clearer name'}))
    expect(screen.getByText('0 open · 1 resolved · 0 rejected')).toBeInTheDocument()
    const entries = screen.getAllByTestId('thread-entry').map(e => e.textContent)
    expect(entries[0]).toContain('Person · resolved')
    expect(entries[1]).toContain('Agent · Rejected')
    expect(entries[1]).toContain('I still think the old name is fine')
    // Reply and Reopen stay available on a settled thread.
    expect(screen.getByRole('button', {name: 'Reply to: Pick a clearer name'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Reopen'})).toBeInTheDocument()
  })

  it('rejects an open thread, and a reopen afterwards clears who settled it', async () => {
    const {submit} = show(file([thread]))
    const row = await findRow(document.body)
    fireEvent.click(within(row).getByRole('button', {name: 'Reject: Pick a clearer name'}))
    expect(screen.getByText('0 open · 0 resolved · 1 rejected')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', {name: 'Show thread: Pick a clearer name'}))
    fireEvent.click(within(row).getByRole('button', {name: 'Reopen'}))
    fireEvent.change(within(row).getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'Changed my mind'}})
    fireEvent.click(within(row).getByRole('button', {name: 'Reopen with comment'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'open', revision: 2})
    expect(review.requests[0].settledBy).toBeUndefined()
    expect(review.requests[0].comments!.map(c => c.action)).toEqual(['reject', 'reopen'])
  })

  it('a reply on a thread the agent resolved reopens it: the counts and the agent message say so (I2)', async () => {
    const agentResolved: ReviewRequest = {...thread, status: 'done', settledBy: 'agent', reply: 'Renamed it', revision: 0}
    const {submit} = show(file([agentResolved]))
    expect(await screen.findByText('0 open · 1 resolved · 0 rejected')).toBeInTheDocument()
    const row = await findRow(document.body)
    fireEvent.click(within(row).getByRole('button', {name: 'Show thread: Pick a clearer name'}))
    fireEvent.click(within(row).getByRole('button', {name: 'Reply to: Pick a clearer name'}))
    fireEvent.change(within(row).getByRole('textbox', {name: 'Reply'}), {target: {value: 'The old name is still used in two places'}})
    fireEvent.click(within(row).getByRole('button', {name: 'Reply'}))
    expect(screen.getByText('1 open · 0 resolved · 0 rejected')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {message, review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'open', revision: 1})
    expect(review.requests[0].settledBy).toBeUndefined()
    expect(message).toContain('Open again: the person replied to your answer (revision 1). Answer the reply.')
  })

  it('undoes a pending Resolve or Reject before Submit', async () => {
    const {submit} = show(file([thread]))
    const row = await findRow(document.body)
    fireEvent.click(within(row).getByRole('button', {name: 'Resolve: Pick a clearer name'}))
    expect(screen.getByText('0 open · 1 resolved · 0 rejected')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Submit'})).toBeEnabled()
    fireEvent.click(within(row).getByRole('button', {name: 'Undo pending change: Pick a clearer name'}))
    expect(screen.getByText('1 open · 0 resolved · 0 rejected')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Submit'})).toBeDisabled()
    fireEvent.click(within(row).getByRole('button', {name: 'Reject: Pick a clearer name'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'declined', revision: 1, comments: [{action: 'reject'}]})
    expect(within(row).queryByRole('button', {name: /Undo pending change/})).toBeNull()
  })

  it('names controls by the first line of a long request only', async () => {
    show(file([{...thread, text: `Line one of the request\n${'x'.repeat(5000)}`}]))
    const row = await findRow(document.body)
    const resolve = within(row).getByRole('button', {name: 'Resolve: Line one of the request'})
    expect(resolve.getAttribute('aria-label')!.length).toBeLessThan(100)
  })

  it('shows no time for an agent answer carried over from an older peer', async () => {
    const legacy: ReviewRequest = {...thread, status: 'done', reply: 'Renamed it', revision: 0, createdAt: '2020-01-01T00:00:00Z'}
    show(file([settleReviewRequest(legacy, 'declined')]))
    const row = await findRow(document.body)
    fireEvent.click(within(row).getByRole('button', {name: 'Show thread: Pick a clearer name'}))
    const agent = within(row).getAllByTestId('thread-entry').find(e => e.textContent?.includes('Agent · Resolved'))!
    expect(agent.querySelector('time')).toBeNull()
    const person = within(row).getAllByTestId('thread-entry').find(e => e.textContent?.includes('Person · rejected'))!
    expect(person.querySelector('time')).not.toBeNull()
  })

  it('shows a thread the agent reopened as open and as the agent\'s, and the person answers it normally (#125)', async () => {
    const resolved = settleReviewRequest(thread, 'done')
    const reopened: ReviewRequest = {...resolved, status: 'open', settledBy: undefined, revision: 2,
      agentReopens: [{text: 'Which name did you prefer?', createdAt: '2026-10-21T12:00:00Z', revision: 2}]}
    const {submit} = show(mergeReviews(file([resolved]), file([reopened])))
    expect(await screen.findByText('1 open · 0 resolved · 0 rejected')).toBeInTheDocument()
    const row = await findRow(document.body)
    const entry = within(row).getAllByTestId('thread-entry').find(e => e.textContent?.includes('Reopened by the agent'))!
    expect(entry).toHaveTextContent('Which name did you prefer?')
    expect(entry.querySelector('time')).not.toBeNull()
    fireEvent.click(within(row).getByRole('button', {name: 'Reply to: Pick a clearer name'}))
    fireEvent.change(within(row).getByRole('textbox', {name: 'Reply'}), {target: {value: 'renameTarget'}})
    fireEvent.click(within(row).getByRole('button', {name: 'Reply'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    const {message, review} = await sent(submit)
    expect(review.requests[0]).toMatchObject({status: 'open', revision: 3, agentReopens: [{revision: 2}]})
    expect(message).toContain('Open: the person answered your reopen (revision 3).\nPerson · resolved it, revision 1.\nYour reopen, revision 2: "Which name did you prefer?"\nPerson · reply, revision 3:\n> renameTarget')
  })

  it('says when malformed agent entries were left out, and shows the rest (I2)', async () => {
    show({...file([{...thread, status: 'done', settledBy: 'agent', reply: 'Renamed', revision: 0}]), ignored: 2})
    expect(await screen.findByTestId('review-ignored')).toHaveTextContent('2 malformed entries left out')
    expect(screen.getByText('0 open · 1 resolved · 0 rejected')).toBeInTheDocument()
  })
})
