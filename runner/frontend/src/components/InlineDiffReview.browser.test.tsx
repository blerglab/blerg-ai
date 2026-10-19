import {useState} from 'react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ArtifactViewer, ChatContext, DiffView, ReviewMode, type Anchor, type ChatContextValue, type ReviewFile, type ReviewRequest, type TransportFiles} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const PATCH = 'diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\n@@ -1,4 +1,4 @@\n same\n-oldA\n-oldB\n+newA\n+newB\n tail\n'

function InlineHarness() {
  const [selection, setSelection] = useState<Anchor | null>(null)
  const [draft, setDraft] = useState<Anchor | null>(null)
  const [requests, setRequests] = useState<ReviewRequest[]>([])
  return <DiffView text={PATCH} selection={selection} onSelection={setSelection} onComment={setDraft} draftAnchor={draft} requests={requests}
    renderDraft={anchor => <form onSubmit={event => {
      event.preventDefault()
      const data = new FormData(event.currentTarget)
      setRequests(current => [...current, {id: `request-${current.length}`, anchor, text: String(data.get('comment')), status: 'open', createdAt: '2026-10-18T12:00:00Z'}])
      setDraft(null)
    }}><blockquote data-testid="draft-anchor">{anchor.quote}</blockquote><textarea autoFocus name="comment" aria-label="Inline comment" /><button type="submit">Save inline comment</button><button type="button" onClick={() => setDraft(null)}>Cancel inline comment</button></form>}
    renderThread={request => <article data-testid="inline-thread"><strong>Review comment</strong><p>{request.text}</p><code>{request.anchor.quote}</code></article>} />
}

afterEach(async () => {await page.viewport(1280, 800)})

describe('inline diff comment layout in Chromium', () => {
  it.each(['unified', 'split'] as const)('opens and saves a full-width inline composer from the plus button in %s', async layout => {
    await page.viewport(1400, 900)
    const {container, getByRole, getByTestId} = render(<InlineHarness />)
    if (layout === 'split') await userEvent.click(getByRole('button', {name: 'Side by side'}))
    await userEvent.click(getByRole('button', {name: 'Comment on new line 2 in f.ts'}))
    const editor = getByRole('textbox', {name: 'Inline comment'})
    expect(document.activeElement).toBe(editor)
    expect(editor.closest('tr')?.previousElementSibling?.textContent).toContain('newA')
    expect(editor.closest('td')?.getAttribute('colspan')).toBe(layout === 'split' ? '4' : '3')
    const tableWidth = container.querySelector('table')!.getBoundingClientRect().width
    expect(editor.closest('td')!.getBoundingClientRect().width).toBeGreaterThan(tableWidth * 0.95)
    await userEvent.fill(editor, 'Please explain why this changed.')
    await userEvent.click(getByRole('button', {name: 'Save inline comment'}))
    expect(within(container).queryByRole('textbox', {name: 'Inline comment'})).toBeNull()
    const thread = getByTestId('inline-thread')
    expect(thread.textContent).toContain('Please explain why this changed.')
    expect(thread.closest('tr')?.previousElementSibling?.textContent).toContain('newA')
  })

  it('opens the composer below the final selected line with the complete range', async () => {
    const {getByRole, getByTestId} = render(<InlineHarness />)
    await userEvent.click(getByRole('button', {name: 'Select old line 2 in f.ts'}))
    await userEvent.click(getByRole('button', {name: 'Select old line 3 in f.ts'}), {modifiers: ['Shift']})
    await userEvent.click(getByRole('button', {name: 'Comment on old line 3 in f.ts'}))
    expect(getByTestId('draft-anchor').textContent).toBe('oldA\noldB')
    expect(getByRole('textbox', {name: 'Inline comment'}).closest('tr')?.previousElementSibling?.textContent).toContain('oldB')
    await userEvent.click(getByRole('button', {name: 'Cancel inline comment'}))
    expect(document.querySelector('.diff-comment-draft')).toBeNull()
  })

  it('makes gutter comments available by keyboard and on a phone without a sidebar', async () => {
    await page.viewport(390, 844)
    const {container, getByRole} = render(<InlineHarness />)
    const add = getByRole('button', {name: 'Comment on new line 2 in f.ts'})
    add.focus()
    await waitFor(() => expect(getComputedStyle(add).opacity).toBe('1'))
    await userEvent.keyboard('{Enter}')
    const editor = getByRole('textbox', {name: 'Inline comment'})
    expect(document.activeElement).toBe(editor)
    expect(editor.closest('td')?.getAttribute('colspan')).toBe('3')
    expect(container.querySelector('.diff-comment-draft')!.getBoundingClientRect().width).toBeLessThanOrEqual(innerWidth)
  })

  it('shows saved threads in a readonly diff without turning on selection controls', () => {
    const request: ReviewRequest = {id: 'saved', text: 'Already discussed here.', status: 'done', reply: 'Updated.', createdAt: '2026-10-18T12:00:00Z', anchor: {kind: 'text', quote: 'newB', diff: {file: 'f.ts', side: 'new', start: 3, end: 3, hunk: 1, fileIndex: 1}}}
    const {container, getByTestId} = render(<DiffView text={PATCH} requests={[request]} renderThread={r => <article data-testid="saved-thread">{r.status}: {r.text} {r.reply}</article>} />)
    expect(getByTestId('saved-thread').textContent).toContain('done: Already discussed here. Updated.')
    expect(getByTestId('saved-thread').closest('tr')?.previousElementSibling?.textContent).toContain('newB')
    expect(within(container).queryByRole('button', {name: /Select new line/})).toBeNull()
    expect(container.querySelector('.diff-selection-toolbar')).toBeNull()
  })
})

const artifact = {id: 'patch-current', name: 'change.diff', view: 'diff', content_type: 'text/x-diff', size: PATCH.length, version: 2}

function reviewContext(existing: ReviewFile | null = null) {
  const sent: Array<{text: string; files: File[]}> = []
  const value: ChatContextValue = {sessionId: 'inline-review-browser', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    reviewFor: async () => existing, submitFeedback: async (text, files) => {sent.push({text, files}); return true}}
  return {value, sent}
}

function saved(id: string, line: number, status: ReviewRequest['status']): ReviewRequest {
  return {id, text: `${status} comment`, status, reply: `${status} agent reply`, revision: 0, createdAt: '2026-10-18T12:00:00Z', anchor: {kind: 'text', quote: line === 2 ? 'newA' : 'newB', diff: {file: 'f.ts', side: 'new', start: line, end: line, fileIndex: 1, hunk: 1, artifactId: artifact.id}}}
}

describe('complete inline review workflow in Chromium', () => {
  it('previews Markdown, adds a structured suggestion and general comment, then submits one review', async () => {
    const {value, sent} = reviewContext()
    const {container, getByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}} /></ChatContext.Provider>)
    await userEvent.click(getByRole('button', {name: 'Comment on new line 2 in f.ts'}))
    const editor = getByRole('textbox', {name: 'Request'})
    expect(document.activeElement).toBe(editor)
    expect(editor.closest('tr')?.previousElementSibling?.textContent).toContain('newA')
    await userEvent.fill(editor, '**Explain** this change.')
    await userEvent.click(getByRole('tab', {name: 'Preview'}))
    expect(container.querySelector('.comment-editor-preview strong')?.textContent).toBe('Explain')
    await userEvent.click(getByRole('tab', {name: 'Write'}))
    await userEvent.click(getByRole('button', {name: 'Insert suggestion'}))
    const suggestion = getByRole('textbox', {name: 'Suggested replacement'}) as HTMLTextAreaElement
    expect(suggestion.value).toBe('newA')
    await userEvent.fill(suggestion, 'newBetter')
    await userEvent.click(getByRole('button', {name: 'Save'}))
    await userEvent.click(getByRole('button', {name: 'Comment on old line 2 in f.ts'}))
    expect(within(container).queryByRole('button', {name: 'Insert suggestion'})).toBeNull()
    await userEvent.fill(getByRole('textbox', {name: 'Request'}), 'Why remove the earlier name?')
    await userEvent.click(getByRole('button', {name: 'Save'}))
    await userEvent.click(getByRole('button', {name: 'Add general comment'}))
    const general = getByRole('textbox', {name: 'Request'})
    expect(general.closest('tr')).toBeNull()
    await userEvent.fill(general, 'Please document the broader approach.')
    await userEvent.click(getByRole('button', {name: 'Save'}))
    expect(container.querySelectorAll('[data-testid="review-request"]')).toHaveLength(3)
    expect(sent).toHaveLength(0)
    await userEvent.click(getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    expect(sent[0]!.files).toHaveLength(1)
    const review = JSON.parse(await sent[0]!.files[0]!.text()) as ReviewFile
    expect(review.requests).toHaveLength(3)
    expect(review.requests[0]).toMatchObject({text: '**Explain** this change.', suggestion: {original: 'newA', replacement: 'newBetter'}, anchor: {diff: {side: 'new', start: 2, end: 2}}})
    expect(review.requests[1]?.anchor.diff?.side).toBe('old')
    expect(review.requests[2]?.anchor.kind).toBe('general')
    expect(sent[0]!.text).toContain('newBetter')
    expect((getByRole('button', {name: 'Submit'}) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows resolved, rejected and outdated comments and reopens without erasing the agent reply', async () => {
    const resolved = saved('resolved', 2, 'done')
    const rejected = saved('rejected', 3, 'declined')
    const outdated = {...saved('outdated', 2, 'done'), text: 'Earlier source comment', anchor: {...resolved.anchor, quote: 'source no longer in this patch', diff: {...resolved.anchor.diff!, artifactId: 'earlier-patch'}}}
    const {value, sent} = reviewContext({version: 1, file: {name: artifact.name, artifactId: artifact.id}, requests: [resolved, rejected, outdated]})
    const {container, getByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}} /></ChatContext.Provider>)
    await waitFor(() => expect(container.querySelectorAll('[data-testid="review-request"]')).toHaveLength(3))
    const threads = [...container.querySelectorAll<HTMLElement>('[data-testid="review-request"]')]
    const done = threads.find(thread => thread.textContent?.includes('done comment'))!
    const declined = threads.find(thread => thread.textContent?.includes('declined comment'))!
    const old = threads.find(thread => thread.textContent?.includes('Earlier source comment'))!
    expect(done.textContent).toContain('Resolved')
    expect(declined.textContent).toContain('Rejected')
    expect(done.closest('tr')).not.toBeNull()
    expect(old.closest('tr')).toBeNull()
    expect(old.textContent).toContain('Outdated')
    expect(old.textContent).toContain('source no longer in this patch')
    await userEvent.click(within(done).getByRole('button', {name: 'Reopen'}))
    expect(within(done).getByRole('button', {name: 'Reopen with comment'})).toBeDisabled()
    await userEvent.fill(within(done).getByRole('textbox', {name: 'Reason for reopening'}), '   ')
    expect(within(done).getByRole('button', {name: 'Reopen with comment'})).toBeDisabled()
    await userEvent.fill(within(done).getByRole('textbox', {name: 'Reason for reopening'}), 'Discarded diff explanation')
    await userEvent.click(within(done).getByRole('button', {name: 'Cancel'}))
    expect(done.textContent).toContain('Resolved')
    expect((getByRole('button', {name: 'Submit'}) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(within(done).getByRole('button', {name: 'Reopen'}))
    expect(within(done).getByRole('textbox', {name: 'Reason for reopening'})).toHaveValue('')
    await userEvent.fill(within(done).getByRole('textbox', {name: 'Reason for reopening'}), 'Please handle the missing edge case.')
    await userEvent.click(within(done).getByRole('button', {name: 'Reopen with comment'}))
    expect(done.textContent).toContain('Person · reopened')
    expect(done.textContent).toContain('Open')
    expect(done.textContent).toContain('done agent reply')
    await userEvent.click(getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    const review = JSON.parse(await sent[0]!.files[0]!.text()) as ReviewFile
    expect(review.requests.find(request => request.id === 'resolved')).toMatchObject({status: 'open', revision: 1, reply: 'done agent reply'})
    expect(review.requests.find(request => request.id === 'resolved')?.comments).toEqual([{text:'Please handle the missing edge case.',createdAt:expect.any(String),revision:1}])
    expect(sent[0]!.text).toContain('Please handle the missing edge case.')
    expect(sent[0]!.text).not.toContain('Discarded diff explanation')
    expect(review.requests.find(request => request.id === 'rejected')?.status).toBe('declined')
  })

  it('opens the real inline editor by a phone gutter tap and keeps its formatting controls usable', async () => {
    await page.viewport(390, 844)
    const {value} = reviewContext()
    const longPatch = PATCH.replace('+newB', '+const result = await sendWithRetry(file, { retries: 3, preserveMetadata: true })')
    const {container, getByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={longPatch} onClose={() => {}} /></ChatContext.Provider>)
    await userEvent.click(getByRole('button', {name: 'Comment on new line 3 in f.ts'}))
    const editor = getByRole('textbox', {name: 'Request'})
    expect(document.activeElement).toBe(editor)
    await userEvent.fill(editor, 'Keep this explanation.')
    await userEvent.click(getByRole('tab', {name: 'Preview'}))
    expect(container.querySelector('.comment-editor-preview')?.textContent).toContain('Keep this explanation.')
    await userEvent.click(getByRole('tab', {name: 'Write'}))
    expect((getByRole('textbox', {name: 'Request'}) as HTMLTextAreaElement).value).toBe('Keep this explanation.')
    expect(container.querySelector('.review-inline-composer')!.closest('td')?.getAttribute('colspan')).toBe('3')
    expect(container.querySelector('.review-inline-composer')!.getBoundingClientRect().width).toBeLessThanOrEqual(innerWidth)
    expect(getByRole('textbox', {name: 'Request'}).getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    expect(getByRole('toolbar', {name: 'Request formatting'}).getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    for (const button of container.querySelectorAll<HTMLButtonElement>('.review-inline-composer button, .diff-row button')) {
      const bounds = button.getBoundingClientRect()
      expect(bounds.left).toBeGreaterThanOrEqual(0)
      expect(bounds.right).toBeLessThanOrEqual(innerWidth)
    }
    const plus = getByRole('button', {name: 'Comment on new line 3 in f.ts'}).getBoundingClientRect()
    const line = getByRole('button', {name: 'Select new line 3 in f.ts'}).getBoundingClientRect()
    expect(plus.width).toBeGreaterThanOrEqual(24)
    expect(plus.right).toBeLessThanOrEqual(line.left)
  })

  it('keeps a desktop inline composer inside the review pane when source lines are very long', async () => {
    await page.viewport(1280, 900)
    const {value} = reviewContext()
    const longPatch = PATCH.replace('+newB', `+const payload = "${'longSourceToken'.repeat(30)}"`)
    const {container, getByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={longPatch} onClose={() => {}} /></ChatContext.Provider>)
    await userEvent.click(getByRole('button', {name: 'Comment on new line 3 in f.ts'}))
    const pane = container.querySelector('.diff-table-wrap')!.getBoundingClientRect()
    const editor = getByRole('textbox', {name: 'Request'}).getBoundingClientRect()
    const toolbar = getByRole('toolbar', {name: 'Request formatting'}).getBoundingClientRect()
    expect(editor.right).toBeLessThanOrEqual(pane.right)
    expect(toolbar.right).toBeLessThanOrEqual(pane.right)
    expect(container.querySelector('.review-inline-composer')!.getBoundingClientRect().width).toBeLessThanOrEqual(pane.width)
  })

  it('keeps an unsaved comment and its selected code together when another gutter is clicked', async () => {
    const {value} = reviewContext()
    const {getByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}} /></ChatContext.Provider>)
    await userEvent.click(getByRole('button', {name: 'Comment on new line 2 in f.ts'}))
    await userEvent.fill(getByRole('textbox', {name: 'Request'}), 'Keep this unsaved comment.')
    await userEvent.click(getByRole('button', {name: 'Comment on old line 3 in f.ts'}))
    expect(getByRole('alert').textContent).toContain('Save or cancel')
    const editor = getByRole('textbox', {name: 'Request'}) as HTMLTextAreaElement
    expect(editor.value).toBe('Keep this unsaved comment.')
    expect(editor.closest('tr')?.previousElementSibling?.textContent).toContain('newA')
    expect(getByRole('button', {name: 'Select new line 2 in f.ts'}).getAttribute('aria-pressed')).toBe('true')
  })

  it('exposes inline comments immediately when opening a published diff', async () => {
    const {value} = reviewContext()
    const files: TransportFiles = {list: async () => [], raw: async () => new Blob([PATCH], {type: 'text/x-diff'}), download: async () => {}, remove: async () => {}, upload: async () => {throw new Error('Not used')}}
    value.files = files
    const {findByRole, queryByRole} = render(<ChatContext.Provider value={value}><ArtifactViewer sessionId={value.sessionId} files={files} artifact={artifact} onClose={() => {}} /></ChatContext.Provider>)
    const add = await findByRole('button', {name: 'Comment on new line 2 in f.ts'})
    expect(queryByRole('button', {name: 'Review'})).toBeNull()
    await userEvent.click(add)
    expect(await findByRole('textbox', {name: 'Request'})).not.toBeNull()
  })
})


describe('resolved thread controls in Chromium', () => {
  it.each([1280, 390])('collapses, restores and filters threads at viewport %s', async width => {
    await page.viewport(width, 844)
    const existing: ReviewFile = {version: 1, file: {name: artifact.name, artifactId: artifact.id}, requests: [saved('resolved', 2, 'done'), saved('rejected', 3, 'declined'), {...saved('open', 2, 'open'), reply: undefined}]}
    const {value} = reviewContext(existing)
    const {getByRole, findByRole, queryByText, getByText} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={PATCH} onClose={() => {}} /></ChatContext.Provider>)
    await userEvent.click(await findByRole('button', {name: 'Collapse thread: done comment'}))
    expect(queryByText('done agent reply')).toBeNull()
    expect(getByText('done comment')).toBeVisible()
    await userEvent.click(getByRole('button', {name: 'Expand thread: done comment'}))
    expect(getByText('done agent reply')).toBeVisible()
    await userEvent.click(getByRole('checkbox', {name: 'Hide resolved'}))
    expect(queryByText('done comment')).toBeNull()
    expect(queryByText('declined comment')).toBeNull()
    expect(getByText('open comment')).toBeVisible()
    await userEvent.click(getByRole('checkbox', {name: 'Hide resolved'}))
    expect(getByText('done agent reply')).toBeVisible()
    expect(getByText('declined agent reply')).toBeVisible()
  })
})
