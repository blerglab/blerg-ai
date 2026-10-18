import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render } from '@testing-library/react'
import { useRef } from 'react'
import { useSelectAllIn } from './useSelectAllIn'

function Modal({ id, children }: { id: string; children?: React.ReactNode }) {
  const ref = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(ref)
  return (
    <div ref={ref} data-testid={id}>
      <header>Title {id}</header>
      <div className="artifact-body">Body of {id}{children}</div>
    </div>
  )
}

afterEach(() => { cleanup(); window.getSelection()?.removeAllRanges() })

const selected = () => window.getSelection()?.toString() ?? ''

describe('useSelectAllIn', () => {
  it('Ctrl+A and Cmd+A select the modal body, not the page', () => {
    render(<><p>Page text behind</p><Modal id="m" /></>)
    const ev = fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(ev).toBe(false) // default prevented
    expect(selected()).toBe('Body of m')
    window.getSelection()?.removeAllRanges()
    fireEvent.keyDown(document.body, { key: 'A', metaKey: true })
    expect(selected()).toBe('Body of m')
  })

  it('only the topmost modal answers', () => {
    const { rerender } = render(<><Modal id="lower" /><Modal id="upper" /></>)
    fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(selected()).toBe('Body of upper')
    rerender(<Modal id="lower" />)
    fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(selected()).toBe('Body of lower')
  })

  it('leaves a text field its own select-all, and plain or shifted keys alone', () => {
    render(<Modal id="m"><textarea defaultValue="draft" data-testid="t" /></Modal>)
    const t = document.querySelector('textarea')!
    expect(fireEvent.keyDown(t, { key: 'a', ctrlKey: true })).toBe(true)
    expect(fireEvent.keyDown(document.body, { key: 'a' })).toBe(true)
    expect(fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true, shiftKey: true })).toBe(true)
    expect(selected()).toBe('')
  })

  it('does nothing once the modal is closed', () => {
    const { unmount } = render(<Modal id="m" />)
    unmount()
    expect(fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })).toBe(true)
  })
})
