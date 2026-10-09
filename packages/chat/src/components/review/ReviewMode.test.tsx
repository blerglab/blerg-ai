import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ReviewMode from './ReviewMode'
import { ChatContext, type ChatContextValue } from '../context'
import { composeReviewMessage } from '../../model/feedback'
import { reviewFileName, type ReviewFile } from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'

const SOURCE = '# Report\n\n## Results\n\nThe mean rose by 12% this year.\n\n## Method\n\nWe sampled weekly.\n'

const artifact: ArtifactInfo = {
  id: 'a1', name: 'report.md', size: SOURCE.length, content_type: 'text/markdown', view: 'markdown', version: 3, latest_version: 3,
}

interface Fake {
  value: ChatContextValue
  submitted: Array<{ text: string; files: File[] }>
  submitResult: boolean
  existing: ReviewFile | null
}

function fakeChat(existing: ReviewFile | null = null): Fake {
  const fake: Fake = {
    submitted: [],
    submitResult: true,
    existing,
    value: {
      sessionId: 's1',
      files: null,
      view: vi.fn(),
      latestVersion: () => undefined,
      byName: () => undefined,
      submitFeedback: async (text, files) => {
        fake.submitted.push({ text, files })
        return fake.submitResult
      },
      reviewFor: async () => fake.existing,
    },
  }
  return fake
}

function show(fake: Fake, props: Partial<React.ComponentProps<typeof ReviewMode>> = {}) {
  const onClose = vi.fn()
  const outerKey = vi.fn()
  const utils = render(
    <div onKeyDown={outerKey}>
      <ChatContext.Provider value={fake.value}>
        <ReviewMode artifact={artifact} text={SOURCE} onClose={onClose} {...props} />
      </ChatContext.Provider>
    </div>,
  )
  return { ...utils, onClose, outerKey }
}

/** Selects `text` inside the rendered page, as a person dragging over it would. */
function selectInPage(text: string) {
  const page = screen.getByTestId('review-page')
  const walker = document.createTreeWalker(page, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const at = (n as Text).data.indexOf(text)
    if (at < 0) continue
    const range = document.createRange()
    range.setStart(n, at)
    range.setEnd(n, at + text.length)
    const sel = window.getSelection()!
    sel.removeAllRanges()
    sel.addRange(range)
    return
  }
  throw new Error(`"${text}" is not on the page`)
}

/** The text of an uploaded file (jsdom's File is not Node's: it is read the browser way). */
function fileText(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result))
    r.onerror = () => reject(r.error)
    r.readAsText(f)
  })
}

const existingReview: ReviewFile = {
  version: 1,
  file: { name: 'report.md', artifactId: 'a0', artifactVersion: 2 },
  requests: [
    { id: 'r1', anchor: { kind: 'text', quote: 'mean rose', heading: 'Results' }, text: 'median, not mean', status: 'done', reply: 'Changed to median.', createdAt: '2026-10-06T10:00:00Z' },
    { id: 'r2', anchor: { kind: 'text', quote: 'sampled weekly', heading: 'Method' }, text: 'say how many weeks', status: 'declined', reply: 'The count is in the appendix.', createdAt: '2026-10-06T11:00:00Z' },
  ],
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
  window.getSelection()?.removeAllRanges()
})

describe('ReviewMode: an existing review', () => {
  it('shows the requests with their status and the reply under each, newest first', async () => {
    show(fakeChat(existingReview))
    const items = await screen.findAllByTestId('review-request')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('say how many weeks')
    expect(items[0]).toHaveTextContent('Declined')
    expect(items[0]).toHaveTextContent('The count is in the appendix.')
    expect(items[1]).toHaveTextContent('median, not mean')
    expect(items[1]).toHaveTextContent('Done')
    expect(items[1]).toHaveTextContent('Changed to median.')
    expect(items[1]).toHaveTextContent('Results')
    // Nothing of its own to send: the replies came from the session.
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
    // A request from an earlier sitting cannot be deleted here.
    expect(screen.queryByRole('button', { name: /Delete request/ })).toBeNull()
  })
})

describe('ReviewMode: the source', () => {
  it('renders the edited source on the page and shows the word diff under Changes', async () => {
    show(fakeChat())
    const source = screen.getByRole('textbox', { name: 'Source' }) as HTMLTextAreaElement
    expect(source.value).toBe(SOURCE)
    expect(screen.queryByRole('button', { name: 'Discard edits' })).toBeNull()
    fireEvent.change(source, { target: { value: SOURCE.replace('We sampled weekly.', 'We sampled daily.') } })
    expect(screen.getByTestId('review-page')).toHaveTextContent('We sampled daily.')
    expect(screen.getByRole('button', { name: 'Discard edits' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Changes' }))
    const page = screen.getByTestId('review-page')
    const del = page.querySelectorAll('.diff-del')
    const ins = page.querySelectorAll('.diff-ins')
    expect(Array.from(del).map(e => e.textContent)).toEqual(['weekly.'])
    expect(Array.from(ins).map(e => e.textContent)).toEqual(['daily.'])
    expect(screen.getByRole('button', { name: 'Submit' })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Discard edits' }))
    expect((screen.getByRole('textbox', { name: 'Source' }) as HTMLTextAreaElement).value).toBe(SOURCE)
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
  })

  it('keeps an edit in localStorage under the artifact id until it is submitted', async () => {
    const { unmount } = show(fakeChat())
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'More.\n' } })
    expect(localStorage.getItem('chat.review.edit.a1')).toBe(SOURCE + 'More.\n')
    unmount()
    show(fakeChat())
    expect((screen.getByRole('textbox', { name: 'Source' }) as HTMLTextAreaElement).value).toBe(SOURCE + 'More.\n')
  })

  it('numbers the lines in the gutter', () => {
    show(fakeChat())
    const gutter = screen.getByTestId('review-gutter')
    expect(gutter.textContent).toContain('10')
  })
})

describe('ReviewMode: requesting a change', () => {
  it('N over a selection opens the form; Save adds an open request with the quote and heading', async () => {
    show(fakeChat())
    await screen.findByTestId('review-page')
    selectInPage('rose by 12%')
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'n' })
    const note = await screen.findByRole('textbox', { name: 'Request' })
    fireEvent.change(note, { target: { value: 'this is the median' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    const item = await screen.findByTestId('review-request')
    expect(item).toHaveTextContent('rose by 12%')
    expect(item).toHaveTextContent('Results')
    expect(item).toHaveTextContent('this is the median')
    expect(item).toHaveTextContent('Open')
    expect(within(item).getByRole('button', { name: /Delete request/ })).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Request' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeEnabled()
  })

  it('the Request change button does the same, and Cancel drops the form', async () => {
    show(fakeChat())
    selectInPage('sampled weekly')
    fireEvent.click(screen.getByRole('button', { name: 'Request change' }))
    expect(screen.getByRole('textbox', { name: 'Request' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('textbox', { name: 'Request' })).toBeNull()
    expect(screen.queryByTestId('review-request')).toBeNull()
  })

  it('says so when nothing is selected', () => {
    show(fakeChat())
    fireEvent.click(screen.getByRole('button', { name: 'Request change' }))
    expect(screen.queryByRole('textbox', { name: 'Request' })).toBeNull()
    expect(screen.getByText(/Select some text/)).toBeInTheDocument()
  })

  it('N typed into the source does not open the form', () => {
    show(fakeChat())
    selectInPage('sampled weekly')
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Source' }), { key: 'n' })
    expect(screen.queryByRole('textbox', { name: 'Request' })).toBeNull()
  })

  it('deletes a request made in this sitting', async () => {
    show(fakeChat())
    selectInPage('sampled weekly')
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'N' })
    fireEvent.change(screen.getByRole('textbox', { name: 'Request' }), { target: { value: 'how many?' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const item = await screen.findByTestId('review-request')
    fireEvent.click(within(item).getByRole('button', { name: /Delete request/ }))
    expect(screen.queryByTestId('review-request')).toBeNull()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
  })

  it('hovering a request focuses it', async () => {
    show(fakeChat(existingReview))
    const items = await screen.findAllByTestId('review-request')
    fireEvent.mouseEnter(items[0])
    expect(items[0]).toHaveClass('review-focus')
    fireEvent.mouseLeave(items[0])
    expect(items[0]).not.toHaveClass('review-focus')
  })
})

describe('ReviewMode: submit', () => {
  it('uploads the edited source and the review json, and sends the composed message', async () => {
    const fake = fakeChat()
    show(fake)
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE.replace('weekly', 'daily') } })
    selectInPage('rose by 12%')
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'n' })
    fireEvent.change(screen.getByRole('textbox', { name: 'Request' }), { target: { value: 'median' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))

    await waitFor(() => expect(fake.submitted).toHaveLength(1))
    const { text, files } = fake.submitted[0]
    expect(files.map(f => f.name)).toEqual(['report.md', reviewFileName('report.md')])
    expect(files[0].type).toBe('text/markdown')
    expect(await fileText(files[0])).toBe(SOURCE.replace('weekly', 'daily'))
    expect(files[1].type).toBe('application/json')
    const json = await fileText(files[1])
    expect(json).toContain('\n  "version": 1')
    const review = JSON.parse(json) as ReviewFile
    expect(review.file).toEqual({ name: 'report.md', artifactId: 'a1', artifactVersion: 3 })
    expect(review.requests).toHaveLength(1)
    expect(review.requests[0]).toMatchObject({ text: 'median', status: 'open', anchor: { kind: 'text', quote: 'rose by 12%', heading: 'Results' } })
    expect(review.edit?.artifactId).toBe('')
    expect(review.edit?.diff).toContain('-We sampled weekly.')
    expect(review.edit?.diff).toContain('+We sampled daily.')
    expect(review.submittedAt).toBeTruthy()
    expect(text).toBe(composeReviewMessage(review, { name: 'report.md', version: 3 }))

    expect(await screen.findByText('Sent')).toBeInTheDocument()
    expect(localStorage.getItem('chat.review.edit.a1')).toBeNull()
    // The request stays listed, now sent: nothing more to submit, and it can no longer be deleted.
    expect(screen.getByTestId('review-request')).toHaveTextContent('median')
    expect(screen.queryByRole('button', { name: /Delete request/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
    expect(screen.getByRole('textbox', { name: 'Source' })).toBeInTheDocument() // the mode stays open
  })

  it('sends only the review json when the source is unchanged', async () => {
    const fake = fakeChat()
    show(fake)
    selectInPage('sampled weekly')
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'n' })
    fireEvent.change(screen.getByRole('textbox', { name: 'Request' }), { target: { value: 'how many weeks' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(fake.submitted).toHaveLength(1))
    expect(fake.submitted[0].files.map(f => f.name)).toEqual([reviewFileName('report.md')])
    const review = JSON.parse(await fileText(fake.submitted[0].files[0])) as ReviewFile
    expect(review.edit).toBeUndefined()
  })

  it('keeps the earlier requests in the file it uploads', async () => {
    const fake = fakeChat(existingReview)
    show(fake)
    await screen.findAllByTestId('review-request')
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(fake.submitted).toHaveLength(1))
    const review = JSON.parse(await fileText(fake.submitted[0].files[1])) as ReviewFile
    expect(review.requests.map(r => r.id)).toEqual(['r1', 'r2'])
  })

  it('says when it could not be sent and keeps everything', async () => {
    const fake = fakeChat()
    fake.submitResult = false
    show(fake)
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    expect(await screen.findByText('Not sent — not connected')).toBeInTheDocument()
    expect(localStorage.getItem('chat.review.edit.a1')).toBe(SOURCE + 'x')
    expect(screen.getByRole('button', { name: 'Submit' })).toBeEnabled()
  })

  it('is disabled with nothing to submit', () => {
    show(fakeChat())
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
  })
})

describe('ReviewMode: keys and panes', () => {
  it('Escape closes the mode without reaching the dialog', () => {
    const { onClose, outerKey } = show(fakeChat())
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(outerKey).not.toHaveBeenCalled()
  })

  it('the Back button closes the mode', () => {
    const { onClose } = show(fakeChat())
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('toggles between Source and Page on a narrow screen', () => {
    show(fakeChat())
    const mode = screen.getByTestId('review-mode')
    expect(mode).toHaveAttribute('data-pane', 'page')
    fireEvent.click(screen.getByRole('button', { name: 'Source', pressed: false }))
    expect(mode).toHaveAttribute('data-pane', 'source')
    fireEvent.click(screen.getByRole('button', { name: 'Page', pressed: false }))
    expect(mode).toHaveAttribute('data-pane', 'page')
  })
})

describe('ReviewMode: pdf', () => {
  it('shows the pages with no source pane and no Changes toggle', async () => {
    const pdf = new Blob([new Uint8Array([0x25, 0x50, 0x44, 0x46])], { type: 'application/pdf' })
    show(fakeChat(existingReview), { text: undefined, blob: pdf, artifact: { ...artifact, name: 'r.pdf', view: 'pdf' } })
    expect(await screen.findAllByTestId('review-request')).toHaveLength(2)
    expect(screen.queryByRole('textbox', { name: 'Source' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Changes' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Source$/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
  })
})

describe('ReviewMode: timers', () => {
  afterEach(() => vi.useRealTimers())

  it('the Sent note goes away after two seconds', async () => {
    vi.useFakeTimers()
    const fake = fakeChat()
    show(fake)
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(fake.submitted).toHaveLength(1)
    expect(screen.getByText('Sent')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(screen.queryByText('Sent')).toBeNull()
  })

  it('a submitted edit is not sent again by a second Submit', async () => {
    const fake = fakeChat()
    show(fake)
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(fake.submitted).toHaveLength(1))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled())
    // A further edit is measured against what was submitted, not the original.
    fireEvent.change(screen.getByRole('textbox', { name: 'Source' }), { target: { value: SOURCE + 'xy' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(fake.submitted).toHaveLength(2))
    const review = JSON.parse(await fileText(fake.submitted[1].files[1])) as ReviewFile
    expect(review.edit?.diff).toContain('-x\n')
    expect(review.edit?.diff).toContain('+xy')
  })
})
