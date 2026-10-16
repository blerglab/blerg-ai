import { afterEach, describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import Composer, { type ComposerProps } from './Composer'

const attachments = {
  items: [], done: [], uploading: false, addFiles: () => {}, remove: () => {}, clear: () => {},
} as unknown as ComposerProps['attachments']

const props = (over: Partial<ComposerProps> = {}): ComposerProps => ({
  draft: '', onDraft: () => {}, placeholder: 'Message', locked: false, busy: false, onSubmit: () => {}, attachments, ...over,
})

afterEach(() => { vi.unstubAllGlobals() })

describe('Composer focus', () => {
  it('puts the cursor in the box when it appears', () => {
    const { container } = render(<Composer {...props()} />)
    expect(document.activeElement).toBe(container.querySelector('textarea'))
  })

  it('waits for the session to unlock', () => {
    const { container, rerender } = render(<Composer {...props({ locked: true })} />)
    expect(document.activeElement).not.toBe(container.querySelector('textarea'))
    rerender(<Composer {...props({ locked: false })} />)
    expect(document.activeElement).toBe(container.querySelector('textarea'))
  })

  it('leaves a touch screen alone, so the keyboard does not pop up', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q === '(pointer: coarse)' }))
    const { container } = render(<Composer {...props()} />)
    expect(document.activeElement).not.toBe(container.querySelector('textarea'))
  })

  it('does not take the cursor from a field the person is typing in', () => {
    const other = document.createElement('input')
    document.body.appendChild(other)
    other.focus()
    const { container, rerender } = render(<Composer {...props({ locked: true })} />)
    rerender(<Composer {...props({ locked: false })} />)
    expect(document.activeElement).toBe(other)
    expect(document.activeElement).not.toBe(container.querySelector('textarea'))
    other.remove()
  })

  it('takes the cursor from the button that opened the session', () => {
    const button = document.createElement('button')
    document.body.appendChild(button)
    button.focus()
    const { container } = render(<Composer {...props()} />)
    expect(document.activeElement).toBe(container.querySelector('textarea'))
    button.remove()
  })

  it('can be told not to', () => {
    const { container } = render(<Composer {...props({ autoFocus: false })} />)
    expect(document.activeElement).not.toBe(container.querySelector('textarea'))
  })
})
