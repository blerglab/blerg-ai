// The Conversation tab of a diff review in real Chromium: the body, the timeline across two
// versions and two submitted reviews, thread excerpts (one Outdated), the jump to Files, and the
// space it takes at 390 px and 3840 px. Screenshots go to $VITE_REVIEW_CONVERSATION_SHOTS_DIR.
import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, screen, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ArtifactViewer, ChatContext, ReviewRequestContext, type ArtifactInfo, type ChatContextValue, type ReviewFile, type ReviewRequest, type ReviewRequestPayload, type TransportFiles} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const SHA = 'd'.repeat(40)
const context = Array.from({length: 30}, (_, i) => ` line ${i + 1} of the unchanged context`).join('\n')
const head = `diff --git a/src/upload.ts b/src/upload.ts\n--- a/src/upload.ts\n+++ b/src/upload.ts\n@@ -1,35 +1,35 @@\n${context}\n one\n two\n-sendOnce(file)\n`
const V1 = `${head}+sendWithRetry(file)\n five\n six\n`
const V2 = `commit ${SHA}\nAuthor: Agent <agent@example.com>\n\n    Retry the upload with backoff\n\n${head}+sendWithRetry(file, {retries: 3, backoff: 'exponential'})\n five\n six\n`
const v1: ArtifactInfo = {id: 'conv-v1', name: 'retry-upload.diff', size: V1.length, view: 'diff', content_type: 'text/x-diff', version: 1, latest_version: 2, origin: 'agent', created_at: '2026-10-21T10:00:00Z'}
const v2: ArtifactInfo = {...v1, id: 'conv-v2', size: V2.length, version: 2, created_at: '2026-10-21T12:00:00Z'}
const changed: ReviewRequest = {id: 'changed', text: 'Retry more than once, with a backoff.', status: 'done', reply: 'Added three retries with exponential backoff.', createdAt: '2026-10-21T10:30:00Z',
  anchor: {kind: 'text', quote: 'sendWithRetry(file)', diff: {artifactId: v1.id, file: 'src/upload.ts', side: 'new', start: 33, end: 33, hunk: 1, fileIndex: 1}}}
const kept: ReviewRequest = {id: 'kept', text: 'Does `five` still belong here?', status: 'open', createdAt: '2026-10-21T12:30:00Z',
  anchor: {kind: 'text', quote: 'five', diff: {artifactId: v2.id, file: 'src/upload.ts', side: 'new', start: 34, end: 34, hunk: 1, fileIndex: 1, commit: SHA}}}
const round1: ReviewFile = {version: 1, file: {name: v1.name, artifactId: v1.id, artifactVersion: 1}, requests: [{...changed, status: 'open', reply: undefined}], submittedAt: '2026-10-21T11:00:00Z'}
const round2: ReviewFile = {version: 1, file: {name: v1.name, artifactId: v2.id, artifactVersion: 2}, requests: [changed, kept], submittedAt: '2026-10-21T13:00:00Z'}
const upload = (id: string, version: number, at: string): ArtifactInfo => ({id, name: `${v1.name}.review.json`, size: 10, view: 'json', content_type: 'application/json', version, origin: 'user', created_at: at})
const bodies: Record<string, string> = {[v1.id]: V1, [v2.id]: V2, r1: JSON.stringify(round1), r2: JSON.stringify(round2)}
const files: TransportFiles = {
  list: async () => [v1, v2, upload('r1', 1, '2026-10-21T11:00:01Z'), upload('r2', 2, '2026-10-21T13:00:01Z')],
  raw: async (_s, id) => new Blob([bodies[id] ?? '']), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') },
}
const ask: ReviewRequestPayload = {request_id: 'ask-2', artifact_id: v2.id, artifact_name: v2.name, artifact_version: 2, artifact_view: 'diff', state: 'pending', created_at: '2026-10-21T12:05:00Z',
  ask: 'Retries the upload three times with exponential backoff, so a dropped connection no longer loses the file. Please check the retry count and the backoff.'}
const chat: ChatContextValue = {sessionId: 'conversation-browser', files, view: vi.fn(), latestVersion: () => 2, byName: () => undefined,
  reviewFor: async () => round2, submitFeedback: async () => true}

afterEach(async () => { await page.viewport(1280, 800) })

async function mount() {
  render(<ChatContext.Provider value={chat}><ReviewRequestContext.Provider value={{forArtifact: id => (id === v2.id ? ask : null)}}>
    <ArtifactViewer sessionId={chat.sessionId} files={files} artifact={v2} onClose={() => {}} />
  </ReviewRequestContext.Provider></ChatContext.Provider>)
  // The viewer opens on the Conversation, as a pull request does.
  expect(await screen.findByRole('tab', {name: 'Conversation'})).toHaveAttribute('aria-selected', 'true')
  const conversation = await screen.findByTestId('review-conversation')
  await waitFor(() => expect(within(conversation).getAllByTestId('conversation-thread')).toHaveLength(2))
  await waitFor(() => expect(within(conversation).getAllByTestId('timeline-entry').filter(e => e.dataset.kind === 'round')).toHaveLength(2))
  return conversation
}

async function shot(name: string) {
  const dir = (import.meta as unknown as {env: Record<string, string>}).env.VITE_REVIEW_CONVERSATION_SHOTS_DIR
  if (dir) await page.screenshot({path: `${dir}/${name}.png`})
}

describe('review Conversation in Chromium', () => {
  it.each([[390, 844], [3840, 2160]])('shows the body, timeline and excerpts and jumps to Files at %ipx', async (width, height) => {
    await page.viewport(width, height)
    const conversation = await mount()
    expect(within(conversation).getByTestId('review-conversation-body')).toHaveTextContent('Retries the upload three times')
    expect(within(conversation).getAllByTestId('timeline-entry').map(e => e.dataset.kind)).toEqual(['revision', 'round', 'revision', 'requested', 'round'])
    const [outdated, current] = within(conversation).getAllByTestId('conversation-thread')
    expect(outdated).toHaveTextContent('src/upload.ts:33')
    expect(outdated).toHaveTextContent('Outdated')
    expect(within(outdated).getByTestId('conversation-excerpt')).toHaveTextContent('sendWithRetry(file)')
    // Three lines of context above, from v1, not the 30 lines of the hunk.
    expect(within(outdated).getByTestId('conversation-excerpt')).toHaveTextContent('one')
    expect(within(outdated).getByTestId('conversation-excerpt')).not.toHaveTextContent('line 30 of the unchanged context')
    expect(current).not.toHaveTextContent('Outdated')
    // Fills the dialog; never scrolls the page sideways; scrolls inside itself.
    const box = conversation.getBoundingClientRect()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(box.right).toBeLessThanOrEqual(width)
    for (const el of conversation.querySelectorAll<HTMLElement>('.review-conv-thread, .review-conv-body, .review-conv-excerpt')) {
      expect(el.getBoundingClientRect().right).toBeLessThanOrEqual(box.right + 1)
    }
    const side = conversation.querySelector<HTMLElement>('.review-conv-side')!
    if (width >= 1400) {
      expect(box.width).toBeGreaterThan(width * 0.85)
      expect(side.getBoundingClientRect().width).toBeGreaterThan(200)
      expect(side.getBoundingClientRect().left).toBeGreaterThan(conversation.querySelector('.review-conv-main')!.getBoundingClientRect().right - 1)
    } else {
      expect(getComputedStyle(side).display).toBe('none')
      expect(box.width).toBeGreaterThan(width * 0.9)
    }
    await shot(`conversation-${width}`)

    await userEvent.click(within(current).getByTestId('conversation-excerpt'))
    expect(screen.getByRole('tab', {name: 'Files'})).toHaveAttribute('aria-selected', 'true')
    const row = document.querySelector<HTMLElement>('[data-inline-request="kept"]')!
    const main = document.querySelector<HTMLElement>('.diff-main')!.getBoundingClientRect()
    await waitFor(() => {
      const r = row.getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(main.top - 1)
      expect(r.top).toBeLessThan(main.bottom)
    })
    expect(row.previousElementSibling!.querySelector('.diff-request-focused')).not.toBeNull()
    await shot(`conversation-jump-${width}`)
  })

  it('maximizes the review edge to edge at 3840 px', async () => {
    await page.viewport(3840, 2160)
    await mount()
    await userEvent.click(screen.getByRole('button', {name: 'Maximize'}))
    await waitFor(() => expect(document.querySelector<HTMLElement>('.artifact-viewer')!.getBoundingClientRect().width).toBeGreaterThan(3840 * 0.98))
    expect(screen.getByTestId('review-conversation').getBoundingClientRect().width).toBeGreaterThan(3840 * 0.9)
    await shot('conversation-maximized-3840')
    await userEvent.click(screen.getByRole('button', {name: 'Restore size'}))
  })
})

describe('review Conversation fixes in Chromium', () => {
  it('shows the ask once at 390 px, so the timeline starts in the first half of the screen (I1)', async () => {
    await page.viewport(390, 844)
    const conversation = await mount()
    const matches = [...document.querySelectorAll<HTMLElement>('body *')].filter(el => el.children.length === 0 && el.textContent?.includes('Retries the upload three times'))
    expect(matches).toHaveLength(1)
    expect(screen.getByTestId('review-request-bar')).toHaveTextContent('Review requested')
    const firstEntry = within(conversation).getAllByTestId('timeline-entry')[0].getBoundingClientRect()
    expect(firstEntry.top).toBeLessThan(844 * 0.6)
    await shot('conversation-ask-once-390')
  })

  it('keeps the comment readable in a collapsed bar at 390 px (M4)', async () => {
    await page.viewport(390, 844)
    const conversation = await mount()
    const bar = conversation.querySelector<HTMLElement>('.review-thread-bar')!
    const summary = bar.querySelector<HTMLElement>('.review-thread-summary')!
    expect(summary.getBoundingClientRect().width).toBeGreaterThan(120)
    expect(getComputedStyle(bar.querySelector('.review-thread-bar-who')!).display).toBe('none')
    expect(bar.querySelector('.review-thread-bar-state')!.getAttribute('title')).toBe('Resolved: conversation by the agent')
  })
})

describe('jumping to a thread in another file on a phone (M7)', () => {
  const file = (n: number) => `diff --git a/src/f${n}.ts b/src/f${n}.ts\n--- a/src/f${n}.ts\n+++ b/src/f${n}.ts\n@@ -1,2 +1,40 @@\n same\n-old\n${Array.from({length: 40}, (_, i) => `+f${n} line ${i}`).join('\n')}\n`
  const PATCH = file(0) + file(1) + file(2)
  const art: ArtifactInfo = {id: 'multi', name: 'multi.diff', size: PATCH.length, view: 'diff', content_type: 'text/x-diff', version: 1, origin: 'agent'}
  const thread: ReviewRequest = {id: 'far', text: 'Look at file two', status: 'open', createdAt: '2026-10-21T10:00:00Z',
    anchor: {kind: 'text', quote: 'f2 line 30', diff: {artifactId: art.id, file: 'src/f2.ts', side: 'new', start: 32, end: 32, hunk: 1, fileIndex: 3}}}
  const ops: TransportFiles = {list: async () => [art], raw: async () => new Blob([PATCH]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') }}
  const ctx: ChatContextValue = {sessionId: 'multi', files: ops, view: vi.fn(), latestVersion: () => 1, byName: () => undefined,
    reviewFor: async () => ({version: 1, file: {name: art.name, artifactId: art.id}, requests: [thread]}), submitFeedback: async () => true}

  it('scrolls the thread into the diff and the file picker follows', async () => {
    await page.viewport(390, 844)
    render(<ChatContext.Provider value={ctx}><ArtifactViewer sessionId="multi" files={ops} artifact={art} onClose={() => {}} /></ChatContext.Provider>)
    const conversation = await screen.findByTestId('review-conversation')
    await userEvent.click(await within(conversation).findByRole('button', {name: 'Show in Files: src/f2.ts:32'}))
    const row = document.querySelector<HTMLElement>('[data-inline-request="far"]')!
    const main = document.querySelector<HTMLElement>('.diff-main')!
    await waitFor(() => {
      const r = row.getBoundingClientRect()
      expect(r.top).toBeGreaterThanOrEqual(main.getBoundingClientRect().top - 1)
      expect(r.top).toBeLessThan(main.getBoundingClientRect().bottom)
    })
    await waitFor(() => expect(document.querySelector<HTMLSelectElement>('[data-testid=diff-file-picker]')!.value).toBe('2'))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  })
})
