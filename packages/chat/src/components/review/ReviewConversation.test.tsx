import {fireEvent, render, screen, waitFor, within} from '@testing-library/react'
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import ReviewMode from './ReviewMode'
import {HISTORY_READS} from './useReviewHistory'
import ArtifactViewer from '../ArtifactViewer'
import {ChatContext, type ChatContextValue} from '../context'
import {ReviewRequestContext} from '../reviewRequestContext'
import type {ReviewFile, ReviewRequest} from '../../model/review'
import type {ArtifactInfo, TransportFiles} from '../../transport/types'
import type {ReviewRequestPayload} from '../../types'

const SHA = 'c'.repeat(40)
const head = 'diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,6 +1,6 @@\n one\n two\n three\n-oldName()\n'
const V1 = `${head}+newName()\n five\n six\n`
const V2 = `commit ${SHA}\nAuthor: Agent <agent@example.com>\n\n    Rename it again\n\n${head}+betterName()\n five\n six\n`

const v1: ArtifactInfo = {id: 'diff-v1', name: 'rename.diff', size: V1.length, view: 'diff', content_type: 'text/x-diff', version: 1, latest_version: 2, origin: 'agent', created_at: '2026-10-21T10:00:00Z'}
const v2: ArtifactInfo = {...v1, id: 'diff-v2', size: V2.length, version: 2, created_at: '2026-10-21T12:00:00Z'}

const threadA: ReviewRequest = {id: 'thread-a', text: 'Pick a clearer name', status: 'done', reply: 'Renamed.', createdAt: '2026-10-21T10:30:00Z',
  anchor: {kind: 'text', quote: 'newName()', diff: {artifactId: v1.id, file: 'src/app.ts', side: 'new', start: 4, end: 4, hunk: 1, fileIndex: 1}}}
const threadB: ReviewRequest = {id: 'thread-b', text: 'Is five still right?', status: 'open', createdAt: '2026-10-21T12:30:00Z',
  anchor: {kind: 'text', quote: 'five', diff: {artifactId: v2.id, file: 'src/app.ts', side: 'new', start: 5, end: 5, hunk: 1, fileIndex: 1, commit: SHA}}}
const round1: ReviewFile = {version: 1, file: {name: v1.name, artifactId: v1.id, artifactVersion: 1}, requests: [{...threadA, status: 'open', reply: undefined}], submittedAt: '2026-10-21T11:00:00Z'}
const round2: ReviewFile = {version: 1, file: {name: v1.name, artifactId: v2.id, artifactVersion: 2}, requests: [threadA, threadB], submittedAt: '2026-10-21T13:00:00Z'}
const upload = (id: string, version: number, at: string): ArtifactInfo =>
  ({id, name: 'rename.diff.review.json', size: 10, view: 'json', content_type: 'application/json', version, origin: 'user', created_at: at})
const bodies: Record<string, string> = {
  [v1.id]: V1, [v2.id]: V2,
  'review-1': JSON.stringify(round1), 'review-2': JSON.stringify(round2),
}
const request: ReviewRequestPayload = {request_id: 'rr-2', artifact_id: v2.id, artifact_name: v2.name, artifact_version: 2, artifact_view: 'diff',
  ask: 'Renames **the helper**; check the call sites.', state: 'changes_requested', note: 'One more call site.',
  created_at: '2026-10-21T12:05:00Z', decided_at: '2026-10-21T13:00:30Z'}

function files(): TransportFiles {
  return {
    list: async () => [v2, v1, upload('review-2', 2, '2026-10-21T13:00:01Z'), upload('review-1', 1, '2026-10-21T11:00:01Z')],
    raw: async (_s, id) => new Blob([bodies[id] ?? '']),
    download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
  }
}

function show({withRequest = true, merged = round2, artifact = v2, text = V2, submitFeedback = async () => true, ops = files(), commit}: {withRequest?: boolean; merged?: ReviewFile; artifact?: ArtifactInfo; text?: string; submitFeedback?: ChatContextValue['submitFeedback']; ops?: TransportFiles; commit?: string} = {}) {
  const viewDiff = vi.fn()
  const value: ChatContextValue = {sessionId: 'conversation-test', files: ops, view: vi.fn(), byName: () => undefined,
    latestVersion: () => 2, reviewFor: async () => merged, submitFeedback, viewDiff: id => (sha?: string) => viewDiff(id, sha)}
  const forArtifact = (id: string) => (withRequest && id === v2.id ? request : null)
  const shown = render(<ChatContext.Provider value={value}><ReviewRequestContext.Provider value={{forArtifact}}>
    <ReviewMode artifact={artifact} text={text} commit={commit} onClose={() => {}}/>
  </ReviewRequestContext.Provider></ChatContext.Provider>)
  return {...shown, viewDiff, value}
}

async function openConversation() {
  fireEvent.click(await screen.findByRole('tab', {name: 'Conversation'}))
  return screen.findByTestId('review-conversation')
}

describe('the Conversation view of a diff review', () => {
  it('opens on Files and switches to the Conversation with its tabs', async () => {
    show()
    expect(await screen.findByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    await openConversation()
    expect(screen.getByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('diff-view').closest('.review-inline-scroll')).toHaveStyle({display: 'none'})
  })

  it("shows the agent's ask as the body, like a pull request's", async () => {
    show()
    await openConversation()
    const body = await screen.findByTestId('review-conversation-body')
    expect(body).toHaveTextContent('Renames the helper; check the call sites.')
    expect(body.querySelector('strong')).toHaveTextContent('the helper')
    expect(screen.queryByTestId('review-conversation-body-empty')).toBeNull()
  })

  it('shows a quiet placeholder when the agent wrote no description', async () => {
    show({withRequest: false})
    await openConversation()
    expect(await screen.findByTestId('review-conversation-body-empty')).toHaveTextContent('No description.')
  })

  it('orders the timeline across two versions and two submitted reviews', async () => {
    show()
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')).toHaveLength(2))
    const entries = within(conversation).getAllByTestId('timeline-entry')
    expect(entries.map(e => e.dataset.kind)).toEqual(['revision', 'round', 'revision', 'requested', 'round', 'decision'])
    expect(entries[0]).toHaveTextContent('v1 · 1 file · +1 −1')
    expect(entries[1]).toHaveTextContent('Review submitted on v1 · 1 comment')
    expect(within(entries[1]).getByText('Pick a clearer name')).toBeInTheDocument()
    expect(entries[2]).toHaveTextContent('v2 · 1 file · +1 −1')
    expect(entries[2]).toHaveTextContent('Rename it again')
    expect(entries[3]).toHaveTextContent('Review requested on v2')
    expect(entries[4]).toHaveTextContent('Review submitted on v2 · 1 comment')
    expect(within(entries[4]).getByText('Is five still right?')).toBeInTheDocument()
    expect(entries[5]).toHaveTextContent('Changes requested on v2')
    expect(entries[5]).toHaveTextContent('One more call site.')
  })

  it("draws each thread's code from the version it was written on, marked Outdated when it changed", async () => {
    show()
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('conversation-thread')).toHaveLength(2))
    const [a, b] = within(conversation).getAllByTestId('conversation-thread')
    expect(a).toHaveTextContent('src/app.ts:4')
    expect(a).toHaveTextContent('on v1')
    expect(a).toHaveTextContent('Outdated')
    const rows = [...within(a).getByTestId('conversation-excerpt').querySelectorAll('tr')]
    expect(rows.map(r => r.querySelector('.diff-code')?.textContent?.slice(1))).toEqual(['two', 'three', 'oldName()', 'newName()', 'five', 'six'])
    expect(rows.filter(r => r.dataset.marked).map(r => r.textContent)).toEqual(['4+newName()'])
    expect(a).not.toHaveTextContent('betterName()')
    // Resolved: collapsed like in Files.
    expect(within(a).getByRole('button', {name: 'Show thread: Pick a clearer name'})).toBeInTheDocument()
    expect(b).toHaveTextContent('src/app.ts:5')
    expect(b).not.toHaveTextContent('Outdated')
    expect([...within(b).getByTestId('conversation-excerpt').querySelectorAll('tr[data-marked]')].map(r => r.textContent)).toEqual(['55 five'])
  })

  it("jumps to a thread's lines in Files when its code is clicked", async () => {
    const {container} = show()
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('conversation-thread')).toHaveLength(2))
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    fireEvent.click(within(within(conversation).getAllByTestId('conversation-thread')[1]).getByTestId('conversation-excerpt'))
    expect(screen.getByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('review-conversation')).not.toBeVisible()
    const row = container.querySelector('[data-inline-request="thread-b"]')!
    expect(row.previousElementSibling).toHaveTextContent('five')
    expect(row.previousElementSibling!.querySelector('.diff-request-focused')).not.toBeNull()
    expect(scroll.mock.contexts).toContain(row)
    // The Outdated thread is not in v2's Files at all.
    expect(container.querySelector('[data-inline-request="thread-a"]')).toBeNull()
  })

  // Owner's safety conditions for keeping Outdated threads out of Files (GitHub's behavior).
  const openA: ReviewFile = {...round2, requests: [{...threadA, status: 'open', reply: undefined}, threadB]}
  it('keeps an open Outdated thread open everywhere and never drops it from the review', async () => {
    const submit = vi.fn(async (_text: string, _files: File[]) => true)
    show({merged: openA, submitFeedback: submit})
    const notice = await screen.findByTestId('review-unplaced-notice')
    expect(notice).toHaveTextContent('1 outdated conversation (1 open) · view')
    expect(screen.getByText('2 open · 0 resolved · 0 rejected')).toBeInTheDocument()
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('conversation-thread')).toHaveLength(2))
    expect(conversation.querySelector('.review-conv-side')).toHaveTextContent('2 open · 0 settled · 1 outdated')
    const a = within(conversation).getAllByTestId('conversation-thread')[0]
    expect(a).toHaveTextContent('Outdated')
    expect(within(a).getByText('Open')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Add general comment'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Request'}), {target: {value: 'One more thing'}})
    fireEvent.click(screen.getByRole('button', {name: 'Save'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    const sent = JSON.parse(await submit.mock.calls[0][1][0].text()) as ReviewFile
    expect(sent.requests.find(r => r.id === 'thread-a')).toMatchObject({status: 'open', anchor: threadA.anchor})
  })

  it("'view' on Files' outdated line opens the Conversation at the thread", async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    show()
    fireEvent.click(await screen.findByRole('button', {name: 'View 1 outdated conversation in the Conversation'}))
    expect(screen.getByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    await waitFor(() => expect(scroll.mock.contexts.some(el => (el as HTMLElement).dataset?.thread === 'thread-a')).toBe(true))
  })

  it("reopens an Outdated thread from the Conversation and shows the agent's resolve or reject", async () => {
    const submit = vi.fn(async (_text: string, _files: File[]) => true)
    show({submitFeedback: submit})
    const conversation = await openConversation()
    fireEvent.click(await within(conversation).findByRole('button', {name: 'Show thread: Pick a clearer name'}))
    expect(within(conversation).getByText('Renamed.')).toBeInTheDocument()
    fireEvent.click(within(conversation).getByRole('button', {name: 'Reopen'}))
    fireEvent.change(within(conversation).getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The old call site still uses it.'}})
    fireEvent.click(within(conversation).getByRole('button', {name: 'Reopen with comment'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    const sent = JSON.parse(await submit.mock.calls[0][1][0].text()) as ReviewFile
    expect(sent.requests.find(r => r.id === 'thread-a')).toMatchObject({status: 'open', revision: 1, comments: [expect.objectContaining({text: 'The old call site still uses it.'})]})
  })

  it("shows the agent's rejection of an Outdated thread in the Conversation", async () => {
    show({merged: {...round2, requests: [{...threadA, status: 'declined', reply: 'The name is used by the API.'}, threadB]}})
    const conversation = await openConversation()
    const bar = await within(conversation).findByRole('button', {name: 'Show thread: Pick a clearer name'})
    expect(bar.closest('[data-testid="conversation-thread"]')).toHaveTextContent('✕ Rejected · conversation by the agent')
    fireEvent.click(bar)
    expect(within(conversation).getByText('The name is used by the API.')).toBeInTheDocument()
    expect(within(conversation).getByRole('button', {name: 'Reopen'})).toBeInTheDocument()
  })

  it('opens the version an Outdated thread was written on, where it is inline on its line', async () => {
    const {viewDiff, unmount} = show()
    const conversation = await openConversation()
    fireEvent.click(await within(conversation).findByRole('button', {name: 'Open v1: src/app.ts:4'}))
    expect(viewDiff).toHaveBeenCalledWith(v1.id, undefined)
    unmount()
    const {container} = show({artifact: v1, text: V1})
    const row = (await screen.findByRole('button', {name: 'Show thread: Pick a clearer name'})).closest('tr')!
    expect(row.previousElementSibling).toHaveTextContent('newName()')
    expect(container.querySelector('[data-testid="review-unplaced-notice"]')).toBeNull()
  })

  it('opens a commit chip in Blerg: this version at the commit', async () => {
    const {viewDiff} = show()
    const conversation = await openConversation()
    fireEvent.click(await within(conversation).findByRole('button', {name: `Open commit ${SHA.slice(0, 7)} in the diff`}))
    expect(screen.getByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    expect(viewDiff).not.toHaveBeenCalled()
    expect(screen.getByTestId('diff-commit')).toHaveClass('target')
  })
})

describe('review fixes: the Conversation', () => {
  it('reads the versions and reviews once, and only the new upload after a Submit (M2)', async () => {
    const ops = files()
    const raw = vi.spyOn(ops, 'raw')
    const submit = vi.fn(async () => true)
    show({ops, submitFeedback: submit})
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')).toHaveLength(2))
    const reads = raw.mock.calls.length
    expect(reads).toBe(3) // v1 and the two review uploads; v2 is the open text
    fireEvent.click(screen.getByRole('tab', {name: 'Files'}))
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    expect(raw.mock.calls.length).toBe(reads)
    fireEvent.click(screen.getByRole('button', {name: 'Add general comment'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Request'}), {target: {value: 'Thanks'}})
    fireEvent.click(screen.getByRole('button', {name: 'Save'}))
    const list = ops.list
    ops.list = async () => [...await list('conversation-test'), upload('review-3', 3, '2026-10-21T14:00:00Z')]
    bodies['review-3'] = JSON.stringify(round2)
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(raw.mock.calls.map(c => c[1])).toContain('review-3'))
    expect(raw.mock.calls.length).toBe(reads + 1)
    delete bodies['review-3']
  })

  it('draws the timeline once, in order, after the history is read (M2)', async () => {
    let release!: () => void
    const ops = files()
    const list = ops.list
    ops.list = () => new Promise(resolve => { release = () => resolve(list('conversation-test')) })
    show({ops})
    const conversation = await openConversation()
    expect(within(conversation).getByRole('status')).toHaveTextContent('Loading the conversation')
    expect(within(conversation).queryAllByTestId('timeline-entry')).toHaveLength(0)
    release()
    await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry')).toHaveLength(6))
  })

  it('scrolls to a commit again when its chip is chosen again (M3)', async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    show()
    const conversation = await openConversation()
    const chip = await within(conversation).findByRole('button', {name: `Open commit ${SHA.slice(0, 7)} in the diff`})
    fireEvent.click(chip)
    const commitScrolls = () => scroll.mock.contexts.filter(el => (el as HTMLElement).classList?.contains('diff-commit')).length
    await waitFor(() => expect(commitScrolls()).toBeGreaterThanOrEqual(1))
    const first = commitScrolls()
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    fireEvent.click(within(conversation).getByRole('button', {name: `Open commit ${SHA.slice(0, 7)} in the diff`}))
    await waitFor(() => expect(commitScrolls()).toBeGreaterThan(first))
  })

  it('opens on Files at a commit, and on the Conversation otherwise, in the viewer (M1)', async () => {
    show({commit: SHA})
    expect(await screen.findByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('diff-commit')).toHaveClass('target')
  })

  it('does not call a thread from a later version Outdated when an older version is open (M5)', async () => {
    const later: ReviewRequest = {...threadB, id: 'thread-later', text: 'Spell it out', anchor: {...threadB.anchor, quote: 'betterName()', diff: {...threadB.anchor.diff!, start: 4, end: 4}}}
    const {viewDiff} = show({artifact: v1, text: V1, merged: {...round2, requests: [threadA, threadB, later]}})
    const notice = await screen.findByTestId('review-unplaced-notice')
    await waitFor(() => expect(notice).toHaveTextContent('1 conversation from a later version'))
    expect(notice).not.toHaveTextContent('outdated')
    const conversation = await openConversation()
    const card = (await within(conversation).findByText('Spell it out')).closest('[data-testid="conversation-thread"]') as HTMLElement
    expect(card).not.toHaveTextContent('Outdated')
    fireEvent.click(within(card).getByRole('button', {name: 'Open v2: src/app.ts:4'}))
    expect(viewDiff).toHaveBeenCalledWith(v2.id, undefined)
  })

  it('counts only the comments drawn under a round when resolved threads are hidden (M8)', async () => {
    show()
    const conversation = await openConversation()
    await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')).toHaveLength(2))
    fireEvent.click(screen.getByRole('checkbox', {name: 'Hide resolved'}))
    const round1Entry = within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')[0]
    expect(round1Entry).toHaveTextContent('Review submitted on v1 · 1 hidden')
    expect(within(round1Entry).queryAllByTestId('conversation-thread')).toHaveLength(0)
  })

  it('reaches a thread by keyboard from its file:line, keeps the code readable, and moves between tabs with the arrow keys (M9, N3)', async () => {
    const {container} = show()
    const conversation = await openConversation()
    const conversationTab = screen.getByRole('tab', {name: 'Conversation'})
    expect(document.getElementById(conversationTab.getAttribute('aria-controls')!)).toBe(conversation)
    expect(conversation).toHaveAttribute('role', 'tabpanel')
    const where = await within(conversation).findByRole('button', {name: 'Show in Files: src/app.ts:5'})
    // The excerpt is not a button (whose children a screen reader would not read): its code is a table.
    const card = where.closest('[data-testid="conversation-thread"]') as HTMLElement
    const excerpt = within(card).getByTestId('conversation-excerpt')
    expect(excerpt).not.toHaveAttribute('role')
    expect(within(card).getByRole('table', {name: 'Code at src/app.ts:5'})).toHaveTextContent('five')
    expect(within(card).getAllByRole('row').map(r => r.textContent)).toContain('55 five')
    where.focus()
    fireEvent.click(where)
    expect(screen.getByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    expect(container.querySelector('[data-inline-request="thread-b"]')!.previousElementSibling!.querySelector('.diff-request-focused')).not.toBeNull()
    const filesTab = screen.getByRole('tab', {name: 'Files'})
    filesTab.focus()
    fireEvent.keyDown(filesTab, {key: 'ArrowRight'})
    expect(screen.getByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    expect(document.activeElement).toBe(screen.getByRole('tab', {name: 'Conversation'}))
    expect(filesTab).toHaveAttribute('tabindex', '-1')
  })

  it("opens a thread whose file left the diff in its own version, and the 'Reopened' link stays in the review (M11)", async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const gone: ReviewRequest = {...threadA, id: 'thread-gone', text: 'Old file note', anchor: {kind: 'text', quote: 'newName()', diff: {...threadA.anchor.diff!, file: 'src/old.ts'}}}
    const reopened: ReviewFile = {...round2, requests: [{...threadA, status: 'open', revision: 1, comments: [{text: 'Still wrong', createdAt: '2026-10-21T12:59:00Z', revision: 1}]}, threadB, gone]}
    const saved = bodies['review-2']
    bodies['review-2'] = JSON.stringify(reopened)
    const {viewDiff} = show({merged: reopened})
    const conversation = await openConversation()
    const card = (await within(conversation).findByText('Old file note')).closest('[data-testid="conversation-thread"]') as HTMLElement
    expect(card).toHaveTextContent('Not in v2')
    fireEvent.click(within(card).getByRole('button', {name: 'Open v1: src/old.ts:4'}))
    expect(viewDiff).toHaveBeenCalledWith(v1.id, undefined)
    const link = await within(conversation).findByRole('button', {name: 'Pick a clearer name'})
    fireEvent.click(link)
    expect(scroll.mock.contexts).toContain(within(conversation).getAllByTestId('conversation-thread').find(el => el.dataset.thread === 'thread-a'))
    bodies['review-2'] = saved
  })
})

describe('review fixes: the viewer', () => {
  it("shows the agent's ask once: the Conversation's body, not also the review bar (I1)", async () => {
    const ops = files()
    const pending = {...request, state: 'pending' as const, decided_at: undefined, note: undefined}
    const value: ChatContextValue = {sessionId: 'conversation-test', files: ops, view: vi.fn(), byName: () => undefined,
      latestVersion: () => 2, reviewFor: async () => round2, submitFeedback: async () => true}
    render(<ChatContext.Provider value={value}><ReviewRequestContext.Provider value={{forArtifact: id => (id === v2.id ? pending : null), decide: vi.fn()}}>
      <ArtifactViewer sessionId="conversation-test" files={ops} artifact={v2} onClose={() => {}}/>
    </ReviewRequestContext.Provider></ChatContext.Provider>)
    expect(await screen.findByTestId('review-conversation-body')).toHaveTextContent('check the call sites')
    expect(screen.queryByTestId('review-request-bar-ask')).toBeNull()
    expect(screen.getAllByText(/check the call sites/)).toHaveLength(1)
    // The bar keeps the decision.
    expect(screen.getByTestId('review-request-bar')).toHaveTextContent('Review requested')
    expect(screen.getByRole('button', {name: 'Approve'})).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', {name: 'Files'}))
    expect(screen.getByTestId('review-request-bar-ask')).toHaveTextContent('check the call sites')
  })
})

describe('re-review fixes', () => {
  it('switches to Files when the host asks for a commit while the Conversation shows (N1)', async () => {
    const {rerender, value} = show()
    await openConversation()
    expect(screen.getByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    rerender(<ChatContext.Provider value={value}><ReviewRequestContext.Provider value={{forArtifact: () => null}}>
      <ReviewMode artifact={v2} text={V2} commit={SHA} onClose={() => {}}/>
    </ReviewRequestContext.Provider></ChatContext.Provider>)
    expect(screen.getByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('diff-commit')).toHaveClass('target')
  })

  it("names a thread whose file left the newest diff the same in Files and the Conversation (N2)", async () => {
    const gone: ReviewRequest = {...threadA, id: 'gone', text: 'Old file note', anchor: {kind: 'text', quote: 'newName()', diff: {...threadA.anchor.diff!, file: 'src/old.ts'}}}
    show({merged: {...round2, requests: [threadB, gone]}})
    const notice = await screen.findByTestId('review-unplaced-notice')
    await waitFor(() => expect(notice).toHaveTextContent('1 conversation not in v2 · view'))
    expect(notice).not.toHaveTextContent('outdated')
    const conversation = await openConversation()
    expect((await within(conversation).findByText('Old file note')).closest('[data-testid="conversation-thread"]')).toHaveTextContent('Not in v2')
  })

  it('does not call any thread outdated while the versions are still being read (N4)', async () => {
    let release!: () => void
    const ops = files()
    const list = ops.list
    ops.list = () => new Promise(resolve => { release = () => resolve(list('conversation-test')) })
    const later: ReviewRequest = {...threadB, id: 'later', text: 'Spell it out', anchor: {...threadB.anchor, quote: 'betterName()', diff: {...threadB.anchor.diff!, start: 4, end: 4}}}
    show({artifact: v1, text: V1, ops, merged: {...round2, requests: [threadA, threadB, later]}})
    const notice = await screen.findByTestId('review-unplaced-notice')
    expect(notice).toHaveTextContent('1 conversation not in this version (1 open) · checking the versions…')
    expect(notice).not.toHaveTextContent('outdated')
    release()
    await waitFor(() => expect(notice).toHaveTextContent('1 conversation from a later version (1 open) · view'))
  })

  describe('reads that fail or never finish (N5)', () => {
    const saved = {...HISTORY_READS}
    beforeEach(() => Object.assign(HISTORY_READS, {timeoutMs: 40, retries: 2, backoffMs: 1}))
    afterEach(() => Object.assign(HISTORY_READS, saved))

    it('retries a failed read with backoff and keeps no failure', async () => {
      const ops = files()
      const raw = ops.raw
      let failures = 2
      ops.raw = async (s, id) => { if (id === v1.id && failures-- > 0) throw new Error('flaky'); return raw(s, id) }
      show({ops})
      const conversation = await openConversation()
      await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry')[0]).toHaveTextContent('v1 · 1 file · +1 −1'))
      expect(within(conversation).queryByTestId('review-conversation-error')).toBeNull()
    })

    it('times out a read that never finishes, says so, and reads again on Retry', async () => {
      const ops = files()
      const list = ops.list
      let hang = true
      ops.list = (s) => (hang ? new Promise<never>(() => {}) : list(s))
      show({ops})
      const conversation = await openConversation()
      const error = await within(conversation).findByTestId('review-conversation-error', {}, {timeout: 2000})
      expect(error).toHaveTextContent('could not be read')
      expect(within(conversation).queryByText('Loading the conversation…')).toBeNull()
      hang = false
      fireEvent.click(within(error).getByRole('button', {name: 'Retry'}))
      await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')).toHaveLength(2))
      expect(within(conversation).queryByTestId('review-conversation-error')).toBeNull()
    })

    it('reads a version that failed earlier again on a later refresh', async () => {
      Object.assign(HISTORY_READS, {retries: 0})
      const ops = files()
      const raw = ops.raw
      let fail = true
      ops.raw = async (s, id) => { if (id === v1.id && fail) throw new Error('down'); return raw(s, id) }
      show({ops})
      const conversation = await openConversation()
      const error = await within(conversation).findByTestId('review-conversation-error')
      expect(within(conversation).getAllByTestId('timeline-entry')[0]).not.toHaveTextContent('+1 −1')
      fail = false
      fireEvent.click(within(error).getByRole('button', {name: 'Retry'}))
      await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry')[0]).toHaveTextContent('v1 · 1 file · +1 −1'))
    })
  })
})
