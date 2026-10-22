// The person replies to and resolves review threads themselves, in real Chromium at 390 px and
// 3840 px: from the Conversation and from Files, the controls fit, counts and labels follow, and the
// Submit carries it all. Screenshots go to $VITE_PERSON_RESOLVE_SHOTS_DIR.
import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, screen, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ArtifactViewer, ChatContext, type ArtifactInfo, type ChatContextValue, type ReviewFile, type ReviewRequest, type TransportFiles} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const PATCH = 'diff --git a/src/upload.ts b/src/upload.ts\n--- a/src/upload.ts\n+++ b/src/upload.ts\n@@ -1,3 +1,3 @@\n one\n-sendOnce(file)\n+sendWithRetry(file, {retries: 3})\n three\n'
const art: ArtifactInfo = {id: 'pr-v1', name: 'retry.diff', size: PATCH.length, view: 'diff', content_type: 'text/x-diff', version: 1, origin: 'agent'}
const anchor = {kind: 'text' as const, quote: 'sendWithRetry(file, {retries: 3})', diff: {artifactId: art.id, file: 'src/upload.ts', side: 'new' as const, start: 2, end: 2, hunk: 1, fileIndex: 1}}
const threads: ReviewRequest[] = [
  {id: 'first', text: 'Why three retries?', status: 'open', createdAt: '2026-10-21T10:00:00Z', anchor},
  {id: 'second', text: 'Log each retry', status: 'done', settledBy: 'agent', reply: 'Added a log line', revision: 0, createdAt: '2026-10-21T10:01:00Z', anchor: {kind: 'general'}},
]
const files: TransportFiles = {list: async () => [art], raw: async () => new Blob([PATCH]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') }}

afterEach(async () => { await page.viewport(1280, 800) })

async function shot(name: string) {
  const dir = (import.meta as unknown as {env: Record<string, string>}).env.VITE_PERSON_RESOLVE_SHOTS_DIR
  if (dir) await page.screenshot({path: `${dir}/${name}.png`})
}

describe('the person resolves and replies in Chromium', () => {
  it.each([[390, 844], [3840, 2160]])('replies in the Conversation and resolves in Files at %ipx', async (width, height) => {
    await page.viewport(width, height)
    const sent: Array<{text: string; files: File[]}> = []
    const review: ReviewFile = {version: 1, file: {name: art.name, artifactId: art.id}, requests: threads}
    const chat: ChatContextValue = {sessionId: 'person-resolve', files, view: vi.fn(), latestVersion: () => 1, byName: () => undefined,
      reviewFor: async () => review, submitFeedback: async (text, fs) => { sent.push({text, files: fs}); return true }}
    render(<ChatContext.Provider value={chat}><ArtifactViewer sessionId="person-resolve" files={files} artifact={art} onClose={() => {}} /></ChatContext.Provider>)
    const conversation = await screen.findByTestId('review-conversation')
    const card = (await within(conversation).findByText('Why three retries?')).closest<HTMLElement>('[data-testid="conversation-thread"]')!
    for (const name of ['Reply to: Why three retries?', 'Resolve: Why three retries?', 'Reject: Why three retries?']) {
      const box = within(card).getByRole('button', {name}).getBoundingClientRect()
      expect(box.right).toBeLessThanOrEqual(width)
      expect(box.height).toBeGreaterThanOrEqual(24)
    }
    await userEvent.click(within(card).getByRole('button', {name: 'Reply to: Why three retries?'}))
    const editor = within(card).getByRole('textbox', {name: 'Reply'})
    expect(editor.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    await userEvent.fill(editor, 'Three matches the server limit?')
    await userEvent.click(within(card).getByRole('button', {name: 'Reply'}))
    expect(card.textContent).toContain('Person · replied')
    expect(card.textContent).toContain('Three matches the server limit?')
    await shot(`person-reply-${width}`)

    await userEvent.click(screen.getByRole('tab', {name: 'Files'}))
    const row = document.querySelector<HTMLElement>('.review-inline-scroll .diff-comment-row')!
    await userEvent.click(within(row).getByRole('button', {name: 'Resolve: Why three retries?'}))
    expect(document.querySelector('.review-summary-words')!.textContent).toBe('0 open · 2 resolved · 0 rejected')
    expect(row.textContent).toContain('✓ Resolved')
    expect(within(row).getByText('✓ Resolved').getAttribute('title')).toBe('Resolved: conversation by the person')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await shot(`person-resolved-${width}`)

    await userEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    const submitted = JSON.parse(await sent[0].files[0].text()) as ReviewFile
    expect(submitted.requests[0]).toMatchObject({status: 'done', settledBy: 'person', revision: 2})
    expect(submitted.requests[0].comments!.map(c => c.action)).toEqual(['reply', 'resolve'])
    expect(sent[0].text).toContain('Resolved by the person (revision 2)')
    expect(sent[0].text).toContain('Person · reply, revision 1:\n> Three matches the server limit?')
  })

  it.each([[390, 844], [3840, 2160]])('shows a thread the agent reopened as open, as the agent\'s, at %ipx (#125)', async (width, height) => {
    await page.viewport(width, height)
    const reopened: ReviewRequest = {...threads[0], status: 'open', revision: 2,
      comments: [{text: '', createdAt: '2026-10-21T11:00:00Z', revision: 1, action: 'resolve'}],
      agentReopens: [{text: 'Did you mean three retries in total, or three after the first try?', createdAt: '2026-10-21T12:00:00Z', revision: 2}]}
    const review: ReviewFile = {version: 1, file: {name: art.name, artifactId: art.id}, requests: [reopened]}
    const chat: ChatContextValue = {sessionId: 'agent-reopen', files, view: vi.fn(), latestVersion: () => 1, byName: () => undefined,
      reviewFor: async () => review, submitFeedback: async () => true}
    render(<ChatContext.Provider value={chat}><ArtifactViewer sessionId="agent-reopen" files={files} artifact={art} onClose={() => {}} /></ChatContext.Provider>)
    const conversation = await screen.findByTestId('review-conversation')
    const card = (await within(conversation).findByText('Why three retries?')).closest<HTMLElement>('[data-testid="conversation-thread"]')!
    const entry = within(card).getAllByTestId('thread-entry').find(e => e.textContent?.includes('Reopened by the agent'))!
    expect(entry.textContent).toContain('three after the first try')
    expect(entry.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    expect(document.querySelector('.review-summary-words')!.textContent).toBe('1 open · 0 resolved · 0 rejected')
    expect(within(card).getByRole('button', {name: 'Resolve: Why three retries?'})).toBeVisible()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await shot(`agent-reopen-${width}`)
  })
})
