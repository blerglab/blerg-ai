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
    await waitFor(() => expect(container.querySelectorAll('[data-testid="review-request"]')).toHaveLength(2))
    const threads = [...container.querySelectorAll<HTMLElement>('[data-testid="review-request"]')]
    const done = threads.find(thread => thread.textContent?.includes('done comment'))!
    const declined = threads.find(thread => thread.textContent?.includes('declined comment'))!
    expect(done.textContent).toContain('✓ Resolved')
    expect(declined.textContent).toContain('✕ Rejected')
    expect(done.closest('tr')).not.toBeNull()
    // The outdated thread is not in Files (GitHub's behavior): one line points to the Conversation.
    expect(container.querySelector('[data-testid="review-unplaced-notice"]')?.textContent).toContain('1 outdated conversation')
    expect(container.textContent).not.toContain('Earlier source comment')
    await userEvent.click(getByRole('button', {name: 'Show thread: done comment'}))
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
    expect(review.requests.find(request => request.id === 'resolved')?.comments).toEqual([{text:'Please handle the missing edge case.',createdAt:expect.any(String),revision:1,action:'reopen'}])
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
    // GitHub's place: right of the line numbers, at the start of the code, on the first line.
    expect(plus.left).toBeGreaterThanOrEqual(line.right)
    expect(Math.abs(plus.top - line.top)).toBeLessThan(2)
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

  it('exposes inline comments one tab from the Conversation when opening a published diff', async () => {
    const {value} = reviewContext()
    const files: TransportFiles = {list: async () => [], raw: async () => new Blob([PATCH], {type: 'text/x-diff'}), download: async () => {}, remove: async () => {}, upload: async () => {throw new Error('Not used')}}
    value.files = files
    const {findByRole, queryByRole} = render(<ChatContext.Provider value={value}><ArtifactViewer sessionId={value.sessionId} files={files} artifact={artifact} onClose={() => {}} /></ChatContext.Provider>)
    // The review opens on the Conversation; Files has the inline comments one tab away.
    expect(await findByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
    await userEvent.click(await findByRole('tab', {name: 'Files'}))
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
    const showDone = await findByRole('button', {name: 'Show thread: done comment'})
    expect(queryByText('done agent reply')).toBeNull()
    expect(getByText('done comment')).toBeVisible()
    await userEvent.click(showDone)
    expect(getByText('done agent reply')).toBeVisible()
    await userEvent.click(getByRole('button', {name: 'Hide thread: done comment'}))
    expect(queryByText('done agent reply')).toBeNull()
    await userEvent.click(getByRole('button', {name: 'Show thread: done comment'}))
    await userEvent.click(getByRole('button', {name: 'Show thread: declined comment'}))
    await userEvent.click(getByRole('checkbox', {name: 'Hide resolved'}))
    expect(queryByText('done comment')).toBeNull()
    expect(queryByText('declined comment')).toBeNull()
    expect(getByText('open comment')).toBeVisible()
    await userEvent.click(getByRole('checkbox', {name: 'Hide resolved'}))
    expect(getByText('done agent reply')).toBeVisible()
    expect(getByText('declined agent reply')).toBeVisible()
  })
})

describe('resolved threads stay on their lines in Chromium', () => {
  // v2 of the patch: newA kept (moved down a line), newB rewritten to newC.
  const LATER = 'diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\n@@ -1,4 +1,5 @@\n same\n-oldA\n-oldB\n+inserted\n+newA\n+newC\n tail\n'
  const fromV1 = (id: string, quote: string, line: number, status: ReviewRequest['status']): ReviewRequest =>
    ({...saved(id, line, status), text: `${id} comment`, anchor: {kind: 'text', quote, diff: {file: 'f.ts', side: 'new', start: line, end: line, fileIndex: 1, hunk: 1, artifactId: 'patch-v1'}}})

  it.each([[390, 844], [3840, 2160]])('keeps resolved and rejected threads inline as one-line bars, outdated ones in the Conversation, at %ipx', async (width, height) => {
    await page.viewport(width, height)
    const existing: ReviewFile = {version: 1, file: {name: artifact.name, artifactId: 'patch-v1'}, requests: [
      fromV1('kept', 'newA', 2, 'done'),
      fromV1('changed', 'newB', 3, 'declined'),
    ]}
    const {value} = reviewContext(existing)
    const {container, getByRole, findByRole} = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={LATER} onClose={() => {}} /></ChatContext.Provider>)
    const kept = (await findByRole('button', {name: 'Show thread: kept comment'})).closest('tr')!
    // The kept line moved to new line 3 and stays inline; the changed one is Outdated: not in Files.
    expect(kept.previousElementSibling?.textContent).toContain('newA')
    expect(kept.textContent).toContain('✓ Resolved')
    expect(kept.textContent).not.toContain('Outdated')
    expect(container.querySelectorAll('.diff-comment-row')).toHaveLength(1)
    const notice = container.querySelector<HTMLElement>('[data-testid="review-unplaced-notice"]')!
    expect(notice.textContent).toContain('1 outdated conversation · view')
    expect(notice.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    for (const row of [kept]) {
      const bar = row.querySelector('.review-thread-bar')!.getBoundingClientRect()
      expect(bar.height).toBeLessThan(44)
      expect(bar.right).toBeLessThanOrEqual(innerWidth)
      expect(row.querySelector('.review-thread-bar-show')!.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
    expect(container.textContent).not.toContain('Earlier revision comments')
    await userEvent.click(getByRole('button', {name: 'View 1 outdated conversation in the Conversation'}))
    const card = (await findByRole('button', {name: 'Show thread: changed comment'})).closest<HTMLElement>('[data-testid="conversation-thread"]')!
    expect(card.textContent).toContain('Outdated')
    expect(card.textContent).toContain('✕ Rejected')
    expect(card.querySelector('[data-testid="conversation-excerpt"]')?.textContent).toContain('newB')
    expect(card.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    await userEvent.click(getByRole('button', {name: 'Show thread: changed comment'}))
    expect(card.textContent).toContain('declined agent reply')
  })
})
