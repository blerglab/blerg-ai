import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ArtifactViewer, ChatContext, type ChatContextValue, type ReviewFile, type ReviewRequest, type TransportFiles} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const patch = Array.from({length: 6}, (_, file) => `diff --git a/src/file-${file}.txt b/src/file-${file}.txt\n--- a/src/file-${file}.txt\n+++ b/src/file-${file}.txt\n@@ -1,2 +1,40 @@\n same\n-old\n${Array.from({length: 40}, (_, line) => `+file-${file}-line-${line}-${'x'.repeat(200)}`).join('\n')}\n`).join('')
const files: TransportFiles = {list: async () => [], raw: async () => new Blob([patch]), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') }}
const context: ChatContextValue = {sessionId: 'phone-diff', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => null, submitFeedback: async () => true}

afterEach(async () => { await page.viewport(1280, 800) })

/** The viewer opens a diff review on its Conversation (as a pull request does); these tests are about Files. */
async function openFiles() {
  await waitFor(() => expect([...document.querySelectorAll('[role=tab]')].some(t => t.textContent === 'Files')).toBe(true))
  await userEvent.click([...document.querySelectorAll<HTMLElement>('[role=tab]')].find(t => t.textContent === 'Files')!)
}

async function mount() {
  render(<ChatContext.Provider value={context}><ArtifactViewer sessionId="phone-diff" files={files} artifact={{id: 'patch', name: 'changes.patch', size: patch.length, view: 'diff', content_type: 'text/x-diff'}} onClose={() => {}} /></ChatContext.Provider>)
  await openFiles()
  await waitFor(() => expect(document.querySelector('.diff-main')).not.toBeNull())
}

describe('diff viewer on a phone', () => {
  it('uses a file picker, gives the diff most of the screen, never scrolls the page sideways and keeps review comments usable', async () => {
    await page.viewport(390, 844)
    await mount()
    expect(document.querySelector('.diff-files')).toBeNull()
    const picker = document.querySelector<HTMLSelectElement>('[data-testid=diff-file-picker]')!
    expect(picker.options.length).toBe(6)
    const main = document.querySelector<HTMLElement>('.diff-main')!
    expect(main.getBoundingClientRect().height).toBeGreaterThanOrEqual(innerHeight * 0.7)
    await userEvent.selectOptions(picker, '3')
    await waitFor(() => expect(picker.value).toBe('3'))
    await waitFor(() => expect(main.querySelector('#diff-file-3')!.getBoundingClientRect().top - main.getBoundingClientRect().top).toBeLessThan(40))
    const wrap = document.querySelector<HTMLElement>('.diff-table-wrap')!
    // Long lines wrap inside the review diff; whatever does overflow scrolls inside the diff, not the page.
    expect(wrap.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
    const add = document.querySelector<HTMLButtonElement>('#diff-file-3 .diff-comment-add')!
    add.scrollIntoView({block: 'center'})
    await userEvent.click(add)
    const editor = document.querySelector<HTMLTextAreaElement>('.diff-comment-draft textarea')!
    await userEvent.fill(editor, 'Please explain.')
    await userEvent.click([...document.querySelectorAll<HTMLButtonElement>('.diff-comment-draft button')].find(b => b.textContent === 'Save')!)
    await waitFor(() => expect(document.querySelector('.diff-comment-row:not(.diff-comment-draft)')?.textContent).toContain('Please explain.'))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })
  it('keeps the file list beside the diff at 3840 px', async () => {
    await page.viewport(3840, 2000)
    await mount()
    expect(document.querySelector('[data-testid=diff-file-picker]')).toBeNull()
    const nav = document.querySelector<HTMLElement>('.diff-files')!
    expect(nav.getBoundingClientRect().width).toBeGreaterThan(200)
    expect(nav.getBoundingClientRect().left).toBeLessThan(document.querySelector('.diff-main')!.getBoundingClientRect().left)
    expect(document.querySelectorAll('.diff-files-item').length).toBe(6)
  })

  it('keeps the review counts readable beside Request change and lets a person reopen an existing thread on a phone', async () => {
    const quote = (line: number) => `file-0-line-${line}-${'x'.repeat(200)}`
    const req = (id: string, status: ReviewRequest['status'], line: number): ReviewRequest => ({id, text: `${status} comment`, status, reply: `${status} agent reply`, revision: 0, createdAt: '2026-10-18T12:00:00Z',
      anchor: {kind: 'text', quote: quote(line), diff: {file: 'src/file-0.txt', side: 'new', start: line + 2, end: line + 2, fileIndex: 1, hunk: 1, artifactId: 'patch'}}})
    const existing: ReviewFile = {version: 1, file: {name: 'changes.patch', artifactId: 'patch'}, requests: [req('a', 'done', 0), req('b', 'declined', 1), {...req('c', 'open', 2), reply: undefined}]}
    const sent: Array<{text: string; files: File[]}> = []
    const ctx: ChatContextValue = {...context, reviewFor: async () => existing, submitFeedback: async (text, fs) => { sent.push({text, files: fs}); return true }}
    await page.viewport(390, 844)
    render(<ChatContext.Provider value={ctx}><ArtifactViewer sessionId="phone-diff" files={files} artifact={{id: 'patch', name: 'changes.patch', size: patch.length, view: 'diff', content_type: 'text/x-diff'}} onClose={() => {}} /></ChatContext.Provider>)
    await openFiles()
    await waitFor(() => expect(document.querySelector('[data-testid=review-request]')).not.toBeNull())
    const summary = document.querySelector<HTMLElement>('.review-summary')!
    const chips = [...summary.querySelectorAll<HTMLElement>('.review-summary-chips > span')]
    expect(chips.map(c => c.getAttribute('aria-label'))).toEqual(['1 open', '1 resolved', '1 rejected'])
    for (const c of chips) expect(c.getBoundingClientRect().width).toBeGreaterThan(10)
    expect(summary.scrollWidth).toBeLessThanOrEqual(summary.clientWidth)
    expect(summary.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
    // a selection adds "Request change" to the bar; the counts stay whole
    const pick = document.querySelector<HTMLButtonElement>('.diff-line-select')
    pick!.scrollIntoView({block: 'center'})
    await userEvent.click(pick!)
    await waitFor(() => expect([...document.querySelectorAll('.review-bar button')].some(b => b.textContent === 'Request change')).toBe(true))
    const bar = document.querySelector<HTMLElement>('.review-bar')!
    expect(summary.scrollWidth).toBeLessThanOrEqual(summary.clientWidth)
    expect(summary.getBoundingClientRect().right).toBeLessThanOrEqual(bar.getBoundingClientRect().right + 1)
    expect(chips[0].getBoundingClientRect().width).toBeGreaterThan(10)
    // reopen the resolved thread with a comment, then submit
    const done = [...document.querySelectorAll<HTMLElement>('.review-inline-scroll [data-testid=review-request]')].find(t => t.textContent?.includes('done comment'))!
    done.scrollIntoView({block: 'center'})
    await userEvent.click(within(done).getByRole('button', {name: 'Show thread: done comment'}))
    await userEvent.click(within(done).getByRole('button', {name: 'Reopen'}))
    await userEvent.fill(within(done).getByRole('textbox', {name: 'Reason for reopening'}), 'Still wrong.')
    await userEvent.click(within(done).getByRole('button', {name: 'Reopen with comment'}))
    await waitFor(() => expect(done.textContent).toContain('Still wrong.'))
    await userEvent.click(document.querySelector<HTMLButtonElement>('.review-submit')!)
    await waitFor(() => expect(sent).toHaveLength(1))
    expect(sent[0].text).toContain('Still wrong.')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
  })
  it('keeps the words in the review summary at 3840 px', async () => {
    await page.viewport(3840, 2000)
    await mount()
    const summary = document.querySelector<HTMLElement>('.review-summary')!
    expect(summary.textContent).toContain('open')
    expect(getComputedStyle(summary.querySelector('.review-summary-chips')!).display).toBe('none')
    expect(summary.scrollWidth).toBeLessThanOrEqual(summary.clientWidth)
  })
})
