import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import DiffView, { DIFF_FILE_LINES } from './DiffView'
import type { Anchor } from '../model/review'

const PATCH = 'diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\n@@ -1,3 +1,3 @@\n same\n-old <img src=x>\n+new <img src=x>\n tail\n@@ -10 +10 @@\n-later\n+LATER\n'
function Controlled() {
  const [selection, setSelection] = useState<Anchor | null>(null)
  return <><DiffView text={PATCH} selection={selection} onSelection={setSelection}/><output data-testid="chosen">{JSON.stringify(selection)}</output></>
}
afterEach(() => vi.unstubAllGlobals())

describe('diff gutter selection', () => {
  it('selects exact side quote by click then Shift-click without interpreting code', () => {
    const {container} = render(<Controlled />)
    fireEvent.click(screen.getByRole('button', {name: 'Select old line 1 in f.ts'}))
    fireEvent.click(screen.getByRole('button', {name: 'Select old line 3 in f.ts'}), {shiftKey: true})
    expect(JSON.parse(screen.getByTestId('chosen').textContent!)).toMatchObject({quote: 'same\nold <img src=x>\ntail', diff: {side: 'old', start: 1, end: 3, fileIndex: 1, hunk: 1}})
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelectorAll('.diff-selected').length).toBeGreaterThan(0)
  })
  it('offers a two-tap range action and resets rather than crossing sides or hunks', () => {
    render(<Controlled />)
    fireEvent.click(screen.getByRole('button', {name: 'Select range'}))
    fireEvent.click(screen.getByRole('button', {name: 'Select new line 1 in f.ts'}))
    fireEvent.click(screen.getByRole('button', {name: 'Select new line 3 in f.ts'}))
    expect(JSON.parse(screen.getByTestId('chosen').textContent!).diff).toMatchObject({start: 1, end: 3, side: 'new'})
    fireEvent.click(screen.getByRole('button', {name: 'Select old line 2 in f.ts'}), {shiftKey: true})
    expect(JSON.parse(screen.getByTestId('chosen').textContent!).diff).toMatchObject({start: 2, end: 2, side: 'old'})
    fireEvent.click(screen.getByRole('button', {name: 'Select old line 10 in f.ts'}), {shiftKey: true})
    expect(JSON.parse(screen.getByTestId('chosen').textContent!).diff).toMatchObject({start: 10, end: 10, hunk: 2})
  })
  // This exercises the real 5,000-row DOM cap. Concurrent package suites can
  // delay jsdom; the deadline is not an assertion about product performance.
  it('reveals a readonly request beyond the file cap without expanding unrelated lines', () => {
    const count = DIFF_FILE_LINES + 10
    const text = '--- /dev/null\n+++ b/large.txt\n@@ -0,0 +1,' + count + ' @@\n' + Array.from({length: count}, (_, i) => '+value ' + (i + 1)).join('\n')
    const {container} = render(<DiffView text={text} focusedId="r1" requests={[{id: 'r1', status: 'open', text: 'Fix this', createdAt: '2026-01-01', anchor: {kind: 'text', quote: 'value ' + count, diff: {file: 'large.txt', fileIndex: 1, hunk: 1, side: 'new', start: count, end: count}}}]}/>)
    expect(screen.getByText('value ' + count)).toBeInTheDocument()
    expect(container.querySelector('.diff-request-focused')).not.toBeNull()
    expect(container.querySelectorAll('.diff-row').length).toBeLessThanOrEqual(DIFF_FILE_LINES + 7)
    expect(screen.queryByRole('button', {name: /Select new line/})).toBeNull()
  }, 15_000)
  it('shows a reason when a range exceeds the selection limit', () => {
    const text = '--- /dev/null\n+++ b/f.ts\n@@ -0,0 +1,201 @@\n' + Array.from({length: 201}, (_, i) => '+' + i).join('\n')
    render(<DiffView text={text} onSelection={() => {}}/>)
    fireEvent.click(screen.getByRole('button', {name: 'Select new line 1 in f.ts'}))
    fireEvent.click(screen.getByRole('button', {name: 'Select new line 201 in f.ts'}), {shiftKey: true})
    expect(screen.getByRole('status')).toHaveTextContent('at most 200 lines')
  })
  it('keeps plain nonreview mode free of gutter controls', () => {
    render(<DiffView text={PATCH}/>)
    expect(screen.queryByRole('button', {name: /Select (?:old|new) line/})).toBeNull()
    expect(screen.queryByRole('button', {name: 'Select range'})).toBeNull()
  })
})
