import { afterEach, describe, expect, it, vi } from 'vitest'
import { useCallback, useState } from 'react'
import { render, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ChatContext, DiffView, ReviewMode, type Anchor, type ChatContextValue, type ReviewFile, type ReviewRequest } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const SHA = 'a'.repeat(40)
const PATCH = `commit ${SHA}
Author: Example
Date:   2026-10-17

    Change three files

diff --git a/src/main.ts b/src/main.ts
--- a/src/main.ts
+++ b/src/main.ts
@@ -1,4 +1,4 @@
 keep
-oldAlpha
-oldBeta
+newAlpha
+newBeta
 tail
@@ -20,2 +20,2 @@
-stale
+fresh
 end
diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
--- a/deleted.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-deletedAlpha
-deletedBeta
diff --git a/old-name.ts b/new-name.ts
similarity index 80%
rename from old-name.ts
rename to new-name.ts
--- a/old-name.ts
+++ b/new-name.ts
@@ -1,2 +1,2 @@
-previousName
+currentName
 stable
`

function SelectionHarness({text = PATCH, commit, requests = [], focusedId}: {text?: string; commit?: string; requests?: ReviewRequest[]; focusedId?: string}) {
  const [selection, setSelection] = useState<Anchor | null>(null)
  const select = useCallback((anchor: Anchor | null) => setSelection(anchor), [])
  return <>
    <DiffView text={text} commit={commit} selection={selection} onSelection={select} requests={requests} focusedId={focusedId} />
    <output data-testid="chosen-anchor">{JSON.stringify(selection)}</output>
  </>
}

function line(container: HTMLElement, side: 'old' | 'new', number: number, file = 'src/main.ts') {
  return within(container).getByRole('button', {name: `Select ${side} line ${number} in ${file}`})
}

function chosen(container: HTMLElement): Anchor | null {
  return JSON.parse(within(container).getByTestId('chosen-anchor').textContent ?? 'null') as Anchor | null
}

afterEach(async () => {
  localStorage.clear()
  await page.viewport(1280, 800)
})

describe('diff line review in Chromium', () => {
  it.each(['unified', 'split'] as const)('selects exact old/new ranges in %s layout', async layout => {
    await page.viewport(1400, 900)
    const {container, getByRole} = render(<SelectionHarness />)
    if (layout === 'split') await userEvent.click(getByRole('button', {name: 'Side by side'}))
    await userEvent.click(line(container, 'new', 2))
    await userEvent.click(line(container, 'new', 3), {modifiers: ['Shift']})
    expect(chosen(container)).toMatchObject({kind: 'text', quote: 'newAlpha\nnewBeta', diff: {file: 'src/main.ts', side: 'new', start: 2, end: 3, hunk: 1, fileIndex: 1, commit: SHA}})
    expect(container.querySelectorAll('.diff-selected').length).toBeGreaterThanOrEqual(2)
    await userEvent.click(line(container, 'old', 2))
    await userEvent.click(line(container, 'old', 3), {modifiers: ['Shift']})
    expect(chosen(container)).toMatchObject({quote: 'oldAlpha\noldBeta', diff: {side: 'old', start: 2, end: 3}})
  })

  it('supports keyboard-only line and range selection and clearing', async () => {
    const {container, getByRole} = render(<SelectionHarness />)
    line(container, 'new', 2).focus()
    await userEvent.keyboard('{Enter}')
    expect(chosen(container)?.diff).toMatchObject({start: 2, end: 2})
    line(container, 'new', 3).focus()
    await userEvent.keyboard('{Shift>}{Enter}{/Shift}')
    expect(chosen(container)?.diff).toMatchObject({start: 2, end: 3})
    await userEvent.click(getByRole('button', {name: 'Clear line selection'}))
    expect(chosen(container)).toBeNull()
    expect(container.querySelector('.diff-selected')).toBeNull()
  })

  it('offers a narrow-screen tap-start/tap-end range without a mouse modifier', async () => {
    await page.viewport(390, 844)
    const {container, getByRole} = render(<SelectionHarness />)
    expect(within(container).queryByRole('button', {name: 'Side by side'})).toBeNull()
    await userEvent.click(getByRole('button', {name: 'Select range'}))
    await userEvent.click(line(container, 'new', 2))
    await userEvent.click(line(container, 'new', 3))
    expect(chosen(container)?.diff).toMatchObject({file: 'src/main.ts', side: 'new', start: 2, end: 3})
    const numberButton = line(container, 'new', 3).getBoundingClientRect()
    expect(numberButton.width).toBeGreaterThanOrEqual(24)
    expect(numberButton.height).toBeGreaterThanOrEqual(24)
  })

  it('never extends a selection into a different side, hunk, or file', async () => {
    const {container} = render(<SelectionHarness />)
    await userEvent.click(line(container, 'new', 2))
    await userEvent.click(line(container, 'old', 3), {modifiers: ['Shift']})
    expect(chosen(container)?.diff).toMatchObject({side: 'old', start: 3, end: 3})
    await userEvent.click(line(container, 'new', 20), {modifiers: ['Shift']})
    expect(chosen(container)?.diff).toMatchObject({side: 'new', start: 20, end: 20, hunk: 2})
    await userEvent.click(line(container, 'old', 1, 'deleted.txt'), {modifiers: ['Shift']})
    expect(chosen(container)).toMatchObject({quote: 'deletedAlpha', diff: {file: 'deleted.txt', side: 'old', start: 1, end: 1, fileIndex: 2}})
    expect(within(container).queryByRole('button', {name: 'Select new line 1 in deleted.txt'})).toBeNull()
  })

  it('anchors renames to the appropriate old and new paths', async () => {
    const {container} = render(<SelectionHarness />)
    await userEvent.click(line(container, 'old', 1, 'old-name.ts'))
    expect(chosen(container)).toMatchObject({quote: 'previousName', diff: {file: 'old-name.ts', side: 'old', fileIndex: 3}})
    await userEvent.click(line(container, 'new', 1, 'new-name.ts'))
    expect(chosen(container)).toMatchObject({quote: 'currentName', diff: {file: 'new-name.ts', side: 'new', fileIndex: 3}})
  })

  it('clears a stale selection when the displayed patch or commit changes', async () => {
    const {container, rerender} = render(<SelectionHarness commit={SHA} />)
    await userEvent.click(line(container, 'new', 2))
    expect(chosen(container)).not.toBeNull()
    rerender(<SelectionHarness text={PATCH.replaceAll(SHA, 'b'.repeat(40))} commit={'b'.repeat(40)} />)
    await waitFor(() => expect(chosen(container)).toBeNull())
    await userEvent.click(line(container, 'new', 3))
    expect(chosen(container)?.diff?.commit).toBe('b'.repeat(40))
  })

  it('starts a new anchor rather than crossing commits in a multi-commit patch', async () => {
    const next = PATCH.slice(0, PATCH.indexOf('diff --git a/deleted.txt')).replaceAll(SHA, 'b'.repeat(40)).replace('newBeta', 'newBetaSecond')
    const {container} = render(<SelectionHarness text={PATCH + next} />)
    const starts = within(container).getAllByRole('button', {name: 'Select new line 2 in src/main.ts'})
    const ends = within(container).getAllByRole('button', {name: 'Select new line 3 in src/main.ts'})
    await userEvent.click(starts[0]!)
    await userEvent.click(ends[1]!, {modifiers: ['Shift']})
    expect(chosen(container)).toMatchObject({quote: 'newBetaSecond', diff: {file: 'src/main.ts', start: 3, end: 3, fileIndex: 4, commit: 'b'.repeat(40)}})
  })

  it('expands folded context before allowing those real lines to be selected', async () => {
    const context = Array.from({length: 20}, (_, index) => ` context-${index + 2}`).join('\n')
    const patch = `diff --git a/fold.txt b/fold.txt\n--- a/fold.txt\n+++ b/fold.txt\n@@ -1,22 +1,22 @@\n-old\n+new\n${context}\n-tail\n+TAIL\n`
    const {container, getByRole} = render(<SelectionHarness text={patch} />)
    expect(within(container).queryByRole('button', {name: 'Select new line 10 in fold.txt'})).toBeNull()
    await userEvent.click(getByRole('button', {name: /unchanged lines/}))
    await userEvent.click(line(container, 'new', 10, 'fold.txt'))
    expect(chosen(container)).toMatchObject({quote: 'context-10', diff: {file: 'fold.txt', start: 10, end: 10}})
  })

  it('reveals and focuses a saved request inside folded context', async () => {
    const context = Array.from({length: 20}, (_, index) => ` context-${index + 2}`).join('\n')
    const patch = `diff --git a/fold.txt b/fold.txt\n--- a/fold.txt\n+++ b/fold.txt\n@@ -1,22 +1,22 @@\n-old\n+new\n${context}\n-tail\n+TAIL\n`
    const request: ReviewRequest = {id: 'folded-request', text: 'Explain this context', status: 'open', createdAt: '2026-10-17T12:00:00Z', anchor: {kind: 'text', quote: 'context-10', diff: {file: 'fold.txt', side: 'new', start: 10, end: 10, fileIndex: 1, hunk: 1}}}
    const {container} = render(<SelectionHarness text={patch} requests={[request]} focusedId={request.id} />)
    await waitFor(() => expect(line(container, 'new', 10, 'fold.txt')).not.toBeNull())
    const focused = line(container, 'new', 10, 'fold.txt')
    expect(focused.closest('td')?.classList.contains('diff-request-focused')).toBe(true)
    await waitFor(() => {
      const box = focused.getBoundingClientRect()
      expect(box.top).toBeGreaterThanOrEqual(0)
      expect(box.bottom).toBeLessThanOrEqual(innerHeight)
    })
  })
})

describe('diff review submission in Chromium', () => {
  it('sends multiple anchored requests as one review attachment and one message', async () => {
    const submitted: Array<{text: string; files: File[]}> = []
    const value: ChatContextValue = {
      sessionId: 'diff-review-browser', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
      reviewFor: async () => null,
      submitFeedback: async (text, files) => {submitted.push({text, files}); return true},
    }
    const {container, getByRole} = render(<ChatContext.Provider value={value}>
      <ReviewMode artifact={{id: 'diff1', name: 'change.patch', size: PATCH.length, view: 'diff', content_type: 'text/x-diff'}} text={PATCH} onClose={() => {}} />
    </ChatContext.Provider>)
    for (const [number, request] of [[2, 'Use the established naming convention'], [3, 'Add an explanatory comment']] as const) {
      await userEvent.click(line(container, 'new', number))
      await userEvent.click(getByRole('button', {name: 'Request change'}))
      await userEvent.fill(getByRole('textbox', {name: 'Request'}), request)
      await userEvent.click(getByRole('button', {name: 'Save'}))
    }
    expect(submitted).toHaveLength(0)
    await userEvent.click(getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(submitted).toHaveLength(1))
    expect(submitted[0]!.files).toHaveLength(1)
    const file = submitted[0]!.files[0]!
    expect(file.name).toBe('change.patch.review.json')
    const review = JSON.parse(await file.text()) as ReviewFile
    expect(review.requests).toHaveLength(2)
    expect(review.requests.map(request => request.anchor.diff)).toEqual([
      expect.objectContaining({file: 'src/main.ts', side: 'new', start: 2, end: 2, commit: SHA}),
      expect.objectContaining({file: 'src/main.ts', side: 'new', start: 3, end: 3, commit: SHA}),
    ])
    expect(submitted[0]!.text).toContain('change.patch')
    expect(submitted[0]!.text).toContain('Use the established naming convention')
    expect(submitted[0]!.text).toContain('Add an explanatory comment')
    expect((getByRole('button', {name: 'Submit'}) as HTMLButtonElement).disabled).toBe(true)
  })

  it('retains existing done/declined replies and focuses their exact reviewed lines', async () => {
    const requests: ReviewRequest[] = [
      {id: 'done', text: 'Rename this', status: 'done', reply: 'Renamed as requested.', createdAt: '2026-10-17T10:00:00Z', anchor: {kind: 'text', quote: 'newAlpha', diff: {artifactId: 'diff1', file: 'src/main.ts', side: 'new', start: 2, end: 2, hunk: 1, fileIndex: 1, commit: SHA}}},
      {id: 'declined', text: 'Remove this', status: 'declined', reply: 'Kept for compatibility.', createdAt: '2026-10-17T11:00:00Z', anchor: {kind: 'text', quote: 'newBeta', diff: {artifactId: 'diff1', file: 'src/main.ts', side: 'new', start: 3, end: 3, hunk: 1, fileIndex: 1, commit: SHA}}},
    ]
    const existing: ReviewFile = {version: 1, file: {name: 'change.patch', artifactId: 'diff1'}, requests}
    const value: ChatContextValue = {sessionId: 'diff-review-browser', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => existing, submitFeedback: vi.fn()}
    const {container, getByRole} = render(<ChatContext.Provider value={value}>
      <ReviewMode artifact={{id: 'diff1', name: 'change.patch', size: PATCH.length, view: 'diff', content_type: 'text/x-diff'}} text={PATCH} onClose={() => {}} />
    </ChatContext.Provider>)
    await waitFor(() => expect(container.textContent).toContain('Renamed as requested.'))
    expect(container.textContent).toContain('Resolved')
    expect(container.textContent).toContain('Rejected')
    expect(container.textContent).toContain('Kept for compatibility.')
    expect(container.querySelectorAll('.diff-requested').length).toBeGreaterThanOrEqual(2)
    await userEvent.click(getByRole('button', {name: 'Show lines for request: Rename this'}))
    expect(container.querySelector('.diff-request-focused')).not.toBeNull()
    expect((getByRole('button', {name: 'Submit'}) as HTMLButtonElement).disabled).toBe(true)
  })

  it('keeps the requested lines focused after a phone tap leaves the request list', async () => {
    await page.viewport(390, 844)
    const current: ReviewRequest = {id: 'current-request', text: 'Check this renamed value', status: 'open', createdAt: '2026-10-17T12:00:00Z', anchor: {kind: 'text', quote: 'newAlpha', diff: {artifactId: 'diff1', file: 'src/main.ts', side: 'new', start: 2, end: 2, hunk: 1, fileIndex: 1, commit: SHA}}}
    const previous: ReviewRequest = {...current, id: 'old-request', text: 'An earlier patch request', anchor: {...current.anchor, quote: 'previousAlpha', diff: {...current.anchor.diff!, artifactId: 'previous-diff'}}}
    const existing: ReviewFile = {version: 1, file: {name: 'change.patch', artifactId: 'diff1'}, requests: [current, previous]}
    const value: ChatContextValue = {sessionId: 'diff-review-mobile', files: null, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => existing, submitFeedback: vi.fn()}
    const {container, findByRole, getByRole} = render(<ChatContext.Provider value={value}>
      <ReviewMode artifact={{id: 'diff1', name: 'change.patch', size: PATCH.length, view: 'diff', content_type: 'text/x-diff'}} text={PATCH} onClose={() => {}} />
    </ChatContext.Provider>)
    const showLines = await findByRole('button', {name: `Show lines for request: ${current.text}`})
    expect(within(container).queryByRole('button', {name: `Show lines for request: ${previous.text}`})).toBeNull()
    await userEvent.click(showLines)
    await userEvent.hover(getByRole('button', {name: 'Add general comment'}))
    // Leaving the inline thread does not clear the person's explicit code focus.
    await waitFor(() => expect(line(container, 'new', 2).closest('td')?.classList.contains('diff-request-focused')).toBe(true))
    const focused = line(container, 'new', 2)
    await waitFor(() => {
      const box = focused.getBoundingClientRect()
      expect(box.top).toBeGreaterThanOrEqual(0)
      expect(box.bottom).toBeLessThanOrEqual(innerHeight)
    })
  })
})
