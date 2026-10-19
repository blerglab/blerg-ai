import type {ReactNode} from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatContext, type ChatContextValue } from '../context'
import ReviewMode from './ReviewMode'
import type { Anchor, ReviewFile, ReviewRequest } from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'

const selected = {
  kind: 'text', quote: 'new line',
  diff: { file: 'src/example.ts', side: 'new', start: 1, end: 1, hunk: 1, fileIndex: 1 },
}

vi.mock('../DiffView', () => ({
  default: ({ onSelection, requests, draftAnchor, renderDraft, renderThread }: { onSelection?: (anchor: unknown) => void; requests?: ReviewRequest[]; draftAnchor?:Anchor|null; renderDraft?:(anchor:Anchor)=>ReactNode; renderThread?:(request:ReviewRequest)=>ReactNode }) => <section>
    <button onClick={() => onSelection?.(selected)}>Select diff line</button>
    {draftAnchor&&renderDraft?.(draftAnchor)}
    {requests?.map(r=><div key={r.id}>{renderThread?.(r)}</div>)}
    <output data-testid="diff-highlight-targets">{requests?.map(request => request.id).join(',')}</output>
  </section>,
}))

const artifact: ArtifactInfo = {
  id: 'diff-version-two', name: 'changes.diff', size: 100,
  content_type: 'text/x-diff', view: 'diff', version: 2, latest_version: 2,
}
const source = 'diff --git a/src/example.ts b/src/example.ts\n--- a/src/example.ts\n+++ b/src/example.ts\n@@ -1 +1 @@\n-old line\n+new line\n'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

function storedReview(id = 'persisted-request', artifactId = artifact.id): ReviewFile {
  return {
    version: 1, file: {name: artifact.name, artifactId, artifactVersion: 1},
    requests: [{
      id, text: 'Earlier review request', status: 'open', createdAt: '2026-01-01T00:00:00Z',
      anchor: { ...selected, diff: { ...selected.diff, artifactId } } as Anchor,
    }],
  }
}

function context(reviewFor: ChatContextValue['reviewFor'], submitFeedback: ChatContextValue['submitFeedback'] = async () => true): ChatContextValue {
  return {
    sessionId: 'session-one', files: null, view: vi.fn(), latestVersion: () => undefined,
    byName: () => undefined, reviewFor, submitFeedback,
  }
}

function show(value: ChatContextValue, file = artifact) {
  return render(<ChatContext.Provider value={value}>
    <ReviewMode artifact={file} text={source} onClose={() => {}} />
  </ChatContext.Provider>)
}

function addRequest(text: string) {
  fireEvent.click(screen.getByRole('button', {name: 'Select diff line'}))
  fireEvent.click(screen.getByRole('button', {name: 'Request change'}))
  fireEvent.change(screen.getByRole('textbox', {name: 'Request'}), {target: {value: text}})
  fireEvent.click(screen.getByRole('button', {name: 'Save'}))
}

function fileText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(file)
  })
}

afterEach(() => { vi.restoreAllMocks() })

describe('independent diff review asynchronous state safety', () => {
  it('merges a late existing review without overwriting unsent local requests', async () => {
    const loaded = deferred<ReviewFile | null>()
    show(context(() => loaded.promise))
    addRequest('Keep my local request')
    await act(async () => loaded.resolve(storedReview()))
    expect(screen.getByText('Earlier review request')).toBeInTheDocument()
    expect(screen.getByText('Keep my local request')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Delete request: Keep my local request'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Submit'})).toBeEnabled()
  })

  it('keeps requests created during a successful submission unsent for the next submission', async () => {
    const first = deferred<boolean>()
    const submissions: File[][] = []
    const send = vi.fn(async (_text: string, files: File[]) => {
      submissions.push(files)
      return submissions.length === 1 ? first.promise : true
    })
    show(context(async () => null, send))
    await act(async () => {})
    addRequest('First request')
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    addRequest('Created during delivery')
    await act(async () => first.resolve(true))
    expect(screen.getByRole('button', {name: 'Submit'})).toBeEnabled()
    expect(screen.getByRole('button', {name: 'Delete request: Created during delivery'})).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2))
    const firstReview = JSON.parse(await fileText(submissions[0].find(file => file.name.endsWith('.review.json'))!))
    const secondReview = JSON.parse(await fileText(submissions[1].find(file => file.name.endsWith('.review.json'))!))
    expect(firstReview.requests.map((request: ReviewRequest) => request.text)).not.toContain('Created during delivery')
    expect(secondReview.requests.map((request: ReviewRequest) => request.text)).toContain('Created during delivery')
  })

  it('does not offer to delete requests already being delivered, and restores deletion after failure', async () => {
    const delivery = deferred<boolean>()
    const send = vi.fn(() => delivery.promise)
    show(context(async () => null, send))
    await act(async () => {})
    addRequest('Already in the upload')
    expect(screen.getByRole('button', {name: 'Delete request: Already in the upload'})).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('button', {name: 'Delete request: Already in the upload'})).toBeNull()
    expect(screen.getByText('Already in the upload')).toBeInTheDocument()
    await act(async () => delivery.resolve(false))
    expect(screen.getByRole('button', {name: 'Delete request: Already in the upload'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Submit'})).toBeEnabled()
  })

  it('shows older-version requests in the list without highlighting the current source', async () => {
    const older=storedReview('older-target', 'diff-version-one')
    older.requests[0].anchor.quote='previous code that changed'
    show(context(async () => older))
    await screen.findByText('Earlier review request')
    expect(screen.getByTestId('diff-highlight-targets')).not.toHaveTextContent('older-target')
  })

  it('renders hostile filenames as text without creating links, images or executable markup', async () => {
    const review = storedReview()
    const path = 'src/<img src="https://example.com/track" onerror="alert(1)"> [open](https://example.com).ts'
    review.requests[0].anchor.diff!.file = path
    const shown = show(context(async () => review))
    await screen.findByText('Earlier review request')
    expect(screen.getByTestId('review-request')).toHaveTextContent(path)
    expect(shown.container.querySelector('img,a,script,iframe')).toBeNull()
  })

  it('ignores a delayed review load after switching source versions', async () => {
    const oldLoad = deferred<ReviewFile | null>()
    const load = vi.fn().mockReturnValueOnce(oldLoad.promise).mockResolvedValue(null)
    const value = context(load)
    const shown = show(value, {...artifact, id: 'diff-version-one', version: 1})
    shown.rerender(<ChatContext.Provider value={value}>
      <ReviewMode artifact={artifact} text={source.replace('new line', 'newer line')} onClose={() => {}} />
    </ChatContext.Provider>)
    await act(async () => oldLoad.resolve(storedReview('stale-delivery', 'diff-version-one')))
    expect(screen.queryByText('Earlier review request')).toBeNull()
    expect(screen.getByTestId('diff-highlight-targets')).not.toHaveTextContent('stale-delivery')
    expect(screen.getByRole('button', {name: 'Submit'})).toBeDisabled()
  })
})
