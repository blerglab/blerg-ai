import {act, fireEvent, render, screen, waitFor, within} from '@testing-library/react'
import {describe, expect, it, vi} from 'vitest'
import ReviewMode from './ReviewMode'
import ArtifactViewer from '../ArtifactViewer'
import {ChatContext, type ChatContextValue} from '../context'
import {createFakeFiles} from '../../test/fakeTransport'
import {parseReviewFile, type ReviewFile, type ReviewRequest} from '../../model/review'
import type {ArtifactInfo} from '../../transport/types'

const SHA = 'a'.repeat(40)
const PATCH = `commit ${SHA}\n\n    Example\ndiff --git a/src/example.ts b/src/example.ts\n--- a/src/example.ts\n+++ b/src/example.ts\n@@ -1,2 +1,2 @@\n-oldLine\n+newLine\n tail\n`
const artifact: ArtifactInfo = {id: 'patch-current', name: 'changes.patch', size: PATCH.length, view: 'diff', content_type: 'text/x-diff', version: 2}
function context(reviewFor: ChatContextValue['reviewFor'] = async () => null, submitFeedback: ChatContextValue['submitFeedback'] = async () => true): ChatContextValue {
  return {sessionId: 'inline-review-test', files: null, view: vi.fn(), byName: () => undefined,
    latestVersion: () => undefined, reviewFor, submitFeedback}
}
function existing(overrides: Partial<ReviewRequest> = {}): ReviewFile {
  return {version: 1, file: {name: artifact.name, artifactId: artifact.id}, requests: [{
    id: 'previous', text: 'Please improve this name', status: 'done', reply: 'Renamed it.', createdAt: '2026-01-01T00:00:00Z',
    anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: artifact.id, file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: SHA}},
    ...overrides,
  }]}
}
function show(value: ChatContextValue) {
  return render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}}/></ChatContext.Provider>)
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => {resolve = done})
  return {promise, resolve}
}
function fileText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result)); reader.onerror = () => reject(reader.error)
    reader.readAsText(file)
  })
}
function beginInline(side: 'old' | 'new' = 'new') {
  fireEvent.click(screen.getByRole('button', {name: `Comment on ${side} line 1 in src/example.ts`}))
}
/** Opens a settled thread's collapsed bar. */
async function showThread(text = 'Please improve this name') {
  fireEvent.click(await screen.findByRole('button', {name: `Show thread: ${text}`}))
}
function writeAndSave(text: string) {
  fireEvent.change(screen.getByRole('textbox', {name: 'Request'}), {target: {value: text}})
  fireEvent.click(screen.getByRole('button', {name: 'Save'}))
}

describe('independent inline diff review', () => {
  it('opens a published patch directly with comments and review controls', async () => {
    const files = createFakeFiles(); files.serve(PATCH)
    render(<ChatContext.Provider value={{...context(async () => existing()), files}}>
      <ArtifactViewer sessionId="inline-review-test" files={files} artifact={artifact} onClose={() => {}}/>
    </ChatContext.Provider>)
    expect(await screen.findByRole('button', {name: 'Add general comment'})).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Review'})).toBeNull()
    // The viewer opens on the Conversation (GitHub's default); the thread is there and in Files.
    expect(screen.getByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    const card = (await screen.findByRole('button', {name: 'Show thread: Please improve this name'})).closest('[data-testid="conversation-thread"]')
    expect(card).not.toBeNull()
    fireEvent.click(screen.getByRole('tab', {name: 'Files'}))
    const row = document.querySelector<HTMLElement>('.diff-comment-row')!
    fireEvent.click(within(row).getByRole('button', {name: 'Show thread: Please improve this name'}))
    expect(within(row).getByText('Renamed it.')).toBeInTheDocument()
  })
  it('sends general and inline structured suggestions as one review with no automatic application', async () => {
    const submit = vi.fn(async () => true)
    const {container} = show(context(undefined, submit))
    fireEvent.click(screen.getByRole('button', {name: 'Add general comment'}))
    expect(screen.queryByRole('button', {name: 'Insert suggestion'})).toBeNull()
    writeAndSave('**Overall** this needs an explanation')
    beginInline()
    fireEvent.click(screen.getByRole('button', {name: 'Insert suggestion'}))
    expect(screen.getByRole('textbox', {name: 'Suggested replacement'})).toHaveValue('newLine')
    fireEvent.change(screen.getByRole('textbox', {name: 'Suggested replacement'}), {target: {value: '<img src="/private" onerror="alert(1)">'}})
    writeAndSave('Use this replacement')
    expect(container.querySelector('.diff-comment-row')).toHaveTextContent('Use this replacement')
    expect(container.querySelector('img,script')).toBeNull()
    expect(screen.queryByRole('button', {name: /Apply suggestion/i})).toBeNull()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    const [message, files] = submit.mock.calls[0] as unknown as [string, File[]]
    expect(files).toHaveLength(1)
    const review = parseReviewFile(await fileText(files[0]))!
    expect(review).not.toBeNull()
    expect(review.requests).toHaveLength(2)
    expect(review.requests[0].anchor).toEqual({kind: 'general'})
    expect(review.requests[1].suggestion).toEqual({original: 'newLine', replacement: '<img src="/private" onerror="alert(1)">'})
    expect(review.requests[1].anchor.diff?.artifactId).toBe(artifact.id)
    expect(message).toContain('check the current source')
  })
  it('keeps old-side comments descriptive and never offers a replacement suggestion', () => {
    show(context())
    beginInline('old')
    expect(screen.queryByRole('button', {name: 'Insert suggestion'})).toBeNull()
    writeAndSave('Keep the old behavior')
    expect(screen.getByText('Keep the old behavior')).toBeInTheDocument()
  })
  it('reopens a resolved inline thread with the same id and preserved agent history', async () => {
    const submit = vi.fn(async () => true)
    show(context(async () => existing(), submit))
    await showThread()
    expect(await screen.findByText('Renamed it.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Reopen'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The change still misses the requested behavior.'}})
    fireEvent.click(screen.getByRole('button', {name: 'Reopen with comment'}))
    expect(screen.getByText('Renamed it.')).toBeInTheDocument()
    expect(screen.getByText('Pending review')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    const [, files] = submit.mock.calls[0] as unknown as [string, File[]]
    const review = parseReviewFile(await fileText(files[0]))!
    expect(review.requests[0]).toMatchObject({id: 'previous', status: 'open', revision: 1, reply: 'Renamed it.',
      replies: [expect.objectContaining({status: 'done', text: 'Renamed it.', revision: 0})]})
  })
  it('marks changed or ambiguous old-revision code as outdated while preserving its original anchor', async () => {
    const review = existing({anchor: {kind: 'text', quote: 'missing previous code', diff: {artifactId: 'patch-old', file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: 'b'.repeat(40)}}})
    const {container} = show(context(async () => review))
    // As on GitHub, an Outdated thread is not drawn in this version's Files: the Conversation has it,
    // with its original code.
    expect(await screen.findByTestId('review-unplaced-notice')).toHaveTextContent('1 outdated conversation · view')
    expect(container.querySelector('.diff-comment-row')).toBeNull()
    expect(container.querySelector('.diff-requested')).toBeNull()
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    const card = (await screen.findByText('Please improve this name')).closest('[data-testid="conversation-thread"]')!
    expect(card).toHaveTextContent('Outdated')
    expect(card).toHaveTextContent('missing previous code')
    expect(review.requests[0].anchor.quote).toBe('missing previous code')
  })
  it('keeps an unsaved draft when another line or the general-comment action is pressed', () => {
    show(context())
    beginInline()
    fireEvent.change(screen.getByRole('textbox', {name: 'Request'}), {target: {value: 'Do not lose this draft'}})
    fireEvent.click(screen.getByRole('button', {name: 'Add general comment'}))
    expect(screen.getByRole('textbox', {name: 'Request'})).toHaveValue('Do not lose this draft')
    beginInline('old')
    expect(screen.getByRole('textbox', {name: 'Request'})).toHaveValue('Do not lose this draft')
    expect(screen.getByRole('button', {name: 'Select new line 1 in src/example.ts'})).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', {name: 'Select old line 1 in src/example.ts'})).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByRole('form', {name: 'Inline comment'})).toBeInTheDocument()
  })
  it('keeps a reopened thread local when a delayed review load returns an older resolved state', async () => {
    const loaded = deferred<ReviewFile | null>()
    const initial = existing()
    const value = context(async () => initial)
    const {rerender} = show(value)
    await showThread()
    fireEvent.click(await screen.findByRole('button', {name: 'Reopen'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The change still misses the requested behavior.'}})
    fireEvent.click(screen.getByRole('button', {name: 'Reopen with comment'}))
    rerender(<ChatContext.Provider value={{...value, reviewFor: () => loaded.promise}}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}}/></ChatContext.Provider>)
    await act(async () => loaded.resolve(initial))
    expect(screen.getByText('Pending review')).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Reopen'})).toBeNull()
    expect(screen.getByRole('button', {name: 'Submit'})).toBeEnabled()
  })
  it('never permits reviewing old loaded bytes under a newly opened artifact id', async () => {
    const files = createFakeFiles(); files.serve(PATCH)
    const value = {...context(), files}
    const {rerender} = render(<ChatContext.Provider value={value}><ArtifactViewer sessionId="inline-review-test" files={files} artifact={artifact} onClose={() => {}}/></ChatContext.Provider>)
    await screen.findByRole('button', {name: 'Add general comment'})
    const next = {...artifact, id: 'next-patch', version: 3}
    const pending = deferred<Blob>()
    files.raw = vi.fn(() => pending.promise)
    rerender(<ChatContext.Provider value={value}><ArtifactViewer sessionId="inline-review-test" files={files} artifact={next} onClose={() => {}}/></ChatContext.Provider>)
    expect(screen.queryByRole('button', {name: 'Add general comment'})).toBeNull()
    await act(async () => pending.resolve(new Blob([PATCH.replace('newLine', 'newerLine')], {type: 'text/x-diff'})))
    expect(await screen.findByRole('button', {name: 'Add general comment'})).toBeInTheDocument()
  })
  it('does not send a suggestion that the persisted review parser would reject', async () => {
    const submit = vi.fn(async () => true)
    show(context(undefined, submit)); beginInline()
    fireEvent.click(screen.getByRole('button', {name: 'Insert suggestion'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Suggested replacement'}), {target: {value: 'bad\0code'}})
    fireEvent.click(screen.getByRole('button', {name: 'Save'}))
    expect(screen.getByRole('button', {name: 'Submit'})).toBeDisabled()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(submit).not.toHaveBeenCalled()
  })
  it('does not close a successfully reopened thread when a later load contains an older reply', async () => {
    const value = context(async () => existing())
    const {rerender} = show(value)
    await showThread()
    fireEvent.click(await screen.findByRole('button', {name: 'Reopen'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The change still misses the requested behavior.'}})
    fireEvent.click(screen.getByRole('button', {name: 'Reopen with comment'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await screen.findByRole('status')
    const loaded = deferred<ReviewFile | null>()
    rerender(<ChatContext.Provider value={{...value, reviewFor: () => loaded.promise}}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}}/></ChatContext.Provider>)
    await act(async () => loaded.resolve(existing()))
    expect(screen.queryByRole('button', {name: 'Reopen'})).toBeNull()
    expect(screen.getByText(/1 open · 0 resolved/)).toBeInTheDocument()
  })
  it('keeps legacy text comments without diff metadata visible', async () => {
    show(context(async () => existing({anchor: {kind: 'text', quote: 'previous passage'}})))
    // A passage with no code cannot be called outdated: it is just not in this version.
    expect(await screen.findByTestId('review-unplaced-notice')).toHaveTextContent('1 conversation not in this version · view')
    fireEvent.click(screen.getByRole('button', {name: 'View 1 conversation in the Conversation'}))
    expect(await screen.findByText('Please improve this name')).toBeInTheDocument()
    expect(screen.getByText('previous passage')).toBeInTheDocument()
  })
  it('lets idle Escape inside an embedded patch close its artifact viewer', async () => {
    const files = createFakeFiles(); files.serve(PATCH)
    const close = vi.fn()
    render(<ChatContext.Provider value={{...context(), files}}><ArtifactViewer sessionId="inline-review-test" files={files} artifact={artifact} onClose={close}/></ChatContext.Provider>)
    fireEvent.click(await screen.findByRole('tab', {name: 'Files'}))
    const line = await screen.findByRole('button', {name: 'Select new line 1 in src/example.ts'})
    fireEvent.keyDown(line, {key: 'Escape'})
    expect(close).toHaveBeenCalledOnce()
  })
  it('projects unchanged code for display while retaining the original revision in a reopened request', async () => {
    const old = existing({anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: 'patch-old', file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: 'b'.repeat(40)}}})
    const submit = vi.fn(async () => true)
    const {container} = render(<ChatContext.Provider value={context(async () => old, submit)}><ReviewMode artifact={artifact} text={PATCH.replace('@@ -1,2 +1,2 @@', '@@ -10,2 +10,2 @@')} onClose={() => {}}/></ChatContext.Provider>)
    expect(container.querySelector('.diff-comment-row')).toBeNull()
    await showThread()
    expect(await screen.findByText('Earlier revision')).toBeInTheDocument()
    expect(container.querySelector('.diff-comment-row')).toHaveTextContent('Please improve this name')
    fireEvent.click(screen.getByRole('button', {name: 'Reopen'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The change still misses the requested behavior.'}})
    fireEvent.click(screen.getByRole('button', {name: 'Reopen with comment'}))
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submit).toHaveBeenCalledOnce())
    const [, files] = submit.mock.calls[0] as unknown as [string, File[]]
    const saved = parseReviewFile(await fileText(files[0]))!
    expect(saved.requests[0].anchor).toEqual(old.requests[0].anchor)
  })

  it('does not guess which repeated code block an older revision comment belongs to', async () => {
    const old = existing({anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: 'patch-old', file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: 'b'.repeat(40)}}})
    const repeated = PATCH.replace('@@ -1,2 +1,2 @@', '@@ -1,3 +1,3 @@').replace(' tail\n', ' tail\n newLine\n')
    const {container} = render(<ChatContext.Provider value={context(async () => old)}><ReviewMode artifact={artifact} text={repeated} onClose={() => {}}/></ChatContext.Provider>)
    // Never placed on either copy of the repeated code: it is an outdated conversation.
    expect(await screen.findByTestId('review-unplaced-notice')).toHaveTextContent('1 outdated conversation')
    expect(container.querySelector('.diff-comment-row')).toBeNull()
    expect(container.querySelector('.diff-requested')).toBeNull()
  })

})


describe('resolved thread visibility', () => {
  it('keeps a resolved thread on its line, collapsed to one bar, and opens it in place', async () => {
    const {container} = show(context(async () => existing()))
    const show_ = await screen.findByRole('button', {name: 'Show thread: Please improve this name'})
    expect(show_).toHaveAttribute('aria-expanded', 'false')
    const row = show_.closest('tr')!
    expect(row).not.toBeNull()
    expect(row.previousElementSibling).toHaveTextContent('newLine')
    expect(row).toHaveTextContent('✓ Resolved · conversation by the agent')
    expect(container.querySelector('.diff-requested')).not.toBeNull()
    expect(screen.queryByText('Renamed it.')).toBeNull()
    expect(screen.queryByRole('button', {name: 'Reopen'})).toBeNull()
    expect(screen.queryByText('Outdated · not in this diff')).toBeNull()
    fireEvent.click(show_)
    expect(screen.getByText('Renamed it.')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Reopen'}).closest('tr')).toBe(container.querySelector('.diff-comment-row'))
    const hide = screen.getByRole('button', {name: 'Hide thread: Please improve this name'})
    expect(hide).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(hide)
    expect(screen.queryByText('Renamed it.')).toBeNull()
    expect(screen.getByText('Please improve this name').closest('tr')).toBe(row)
  })
  it('collapses and labels a rejected thread like a resolved one', async () => {
    show(context(async () => existing({status: 'declined', reply: 'Not needed.'})))
    const bar = (await screen.findByRole('button', {name: 'Show thread: Please improve this name'})).closest('tr')!
    expect(bar).toHaveTextContent('✕ Rejected · conversation by the agent')
    expect(screen.queryByText('Not needed.')).toBeNull()
  })
  it('keeps a thread inline in a later revision that keeps its line', async () => {
    const old = existing({anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: 'patch-old', file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: 'b'.repeat(40)}}})
    const later = PATCH.replace('@@ -1,2 +1,2 @@', '@@ -1,3 +1,3 @@').replace('-oldLine\n', '-oldLine\n-more\n+added\n').replace(' tail\n', ' tail\n')
    const {container} = render(<ChatContext.Provider value={context(async () => old)}><ReviewMode artifact={artifact} text={later} onClose={() => {}}/></ChatContext.Provider>)
    const row = (await screen.findByRole('button', {name: 'Show thread: Please improve this name'})).closest('tr')!
    expect(row.previousElementSibling).toHaveTextContent('newLine')
    expect(row).not.toHaveTextContent('Outdated')
    expect(container.querySelector('.diff-requested')).not.toBeNull()
  })
  it('moves a thread whose line changed in a later revision out of Files, as Outdated', async () => {
    const old = existing({anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: 'patch-old', file: 'src/example.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1, commit: 'b'.repeat(40)}}})
    const changed = PATCH.replace('+newLine', '+renamedLine')
    const {container} = render(<ChatContext.Provider value={context(async () => old)}><ReviewMode artifact={artifact} text={changed} onClose={() => {}}/></ChatContext.Provider>)
    expect(await screen.findByTestId('review-unplaced-notice')).toHaveTextContent('1 outdated conversation · view')
    expect(container.querySelector('.diff-comment-row')).toBeNull()
    expect(container.querySelector('.diff-requested')).toBeNull()
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    const card = (await screen.findByRole('button', {name: 'Show thread: Please improve this name'})).closest('[data-testid="conversation-thread"]')!
    expect(card).toHaveTextContent('Outdated')
    expect(card).toHaveTextContent('✓ Resolved · conversation by the agent')
  })
  it('lists a thread whose file left the patch apart, still collapsed and labeled', async () => {
    const old = existing({anchor: {kind: 'text', quote: 'newLine', diff: {artifactId: 'patch-old', file: 'src/gone.ts', side: 'new', start: 1, end: 1, fileIndex: 1, hunk: 1}}})
    const {container} = show(context(async () => old))
    // Not in Files (no "Earlier revision comments" section): the Conversation has it, collapsed.
    await screen.findByTestId('review-unplaced-notice')
    expect(container.querySelector('.diff-comment-row')).toBeNull()
    expect(screen.queryByText('Please improve this name')).toBeNull()
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    const card = (await screen.findByRole('button', {name: 'Show thread: Please improve this name'})).closest('[data-testid="conversation-thread"]')!
    expect(card).toHaveTextContent('src/gone.ts:1')
    // Its file is not in the open (newest) version: not claimed to have changed.
    expect(card).toHaveTextContent('Not in v2')
    expect(card).not.toHaveTextContent('Outdated')
    expect(card).toHaveTextContent('✓ Resolved · conversation by the agent')
  })
  it('hides settled inline, general and outdated threads, retains open threads and restores hidden threads', async () => {
    const review = existing()
    review.requests.push(
      {...review.requests[0], id: 'general', anchor: {kind: 'general'}, status: 'declined', text: 'General rejected'},
      {...review.requests[0], id: 'outdated', anchor: {kind: 'text', quote: 'missing'}, text: 'Old resolved'},
      {...review.requests[0], id: 'open', anchor: {kind: 'general'}, status: 'open', text: 'Still open', reply: undefined},
    )
    show(context(async () => review))
    await screen.findByText('General rejected')
    fireEvent.click(screen.getByRole('checkbox', {name: 'Hide resolved'}))
    expect(screen.queryByText('Please improve this name')).toBeNull()
    expect(screen.queryByText('General rejected')).toBeNull()
    expect(screen.queryByText('Old resolved')).toBeNull()
    expect(screen.getByText('Still open')).toBeInTheDocument()
    expect(screen.getByText('1 open · 2 resolved · 1 rejected')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('checkbox', {name: 'Hide resolved'}))
    expect(screen.getByText('Please improve this name')).toBeInTheDocument()
    expect(screen.getByText('General rejected')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', {name: 'Conversation'}))
    expect(screen.getByText('Old resolved')).toBeInTheDocument()
  })
  it('keeps a reopened thread visible when resolved threads are hidden', async () => {
    show(context(async () => existing()))
    await showThread()
    fireEvent.click(await screen.findByRole('button', {name: 'Reopen'}))
    fireEvent.change(screen.getByRole('textbox', {name: 'Reason for reopening'}), {target: {value: 'The change still misses the requested behavior.'}})
    fireEvent.click(screen.getByRole('button', {name: 'Reopen with comment'}))
    fireEvent.click(screen.getByRole('checkbox', {name: 'Hide resolved'}))
    expect(screen.getByText('Please improve this name')).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: /Collapse thread:/})).toBeNull()
    expect(screen.getByText('Pending review')).toBeInTheDocument()
  })
})
