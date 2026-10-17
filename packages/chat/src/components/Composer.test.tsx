import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '@testing-library/react'
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

describe('Composer paste', () => {
  const html = '<h2>Plan</h2><p>See <a href="https://e.test/x">the doc</a></p>'
  const data = (over: Record<string, string> = {}, files: File[] = []) => ({
    getData: (t: string) => ({ 'text/html': html, 'text/plain': 'Plan\nSee the doc', ...over })[t] ?? '',
    files,
  })
  const setup = (over: Partial<ComposerProps> = {}) => {
    const onDraft = vi.fn()
    const { container } = render(<Composer {...props({ onDraft, ...over })} />)
    return { box: container.querySelector('textarea')!, onDraft }
  }

  it('inserts rich HTML as Markdown at the caret', () => {
    const { box, onDraft } = setup()
    fireEvent.paste(box, { clipboardData: data() })
    expect(onDraft).toHaveBeenCalledWith('## Plan\n\nSee [the doc](https://e.test/x)')
  })

  it('goes through the editing command, so undo is one step', () => {
    const exec = vi.fn(() => true)
    document.execCommand = exec as unknown as typeof document.execCommand
    const { box, onDraft } = setup()
    fireEvent.paste(box, { clipboardData: data() })
    expect(exec).toHaveBeenCalledWith('insertText', false, '## Plan\n\nSee [the doc](https://e.test/x)')
    expect(onDraft).not.toHaveBeenCalled()
    delete (document as { execCommand?: unknown }).execCommand
  })

  it('pastes as typed when the caret is inside an open code fence', () => {
    const { box, onDraft } = setup({ draft: '```go\n' })
    box.setSelectionRange(6, 6)
    fireEvent.paste(box, { clipboardData: data() })
    expect(onDraft).not.toHaveBeenCalled()
  })

  it('still converts when the fence is closed', () => {
    const closed = setup({ draft: '```go\nx\n```\n' })
    fireEvent.paste(closed.box, { clipboardData: data() })
    expect(closed.onDraft).toHaveBeenCalledWith(expect.stringContaining('## Plan\n\nSee [the doc](https://e.test/x)'))
  })

  it('leaves Ctrl+Shift+V alone, once', () => {
    const { box, onDraft } = setup()
    fireEvent.keyDown(box, { key: 'V', ctrlKey: true, shiftKey: true })
    fireEvent.paste(box, { clipboardData: data() })
    expect(onDraft).not.toHaveBeenCalled()
    fireEvent.paste(box, { clipboardData: data() })
    expect(onDraft).toHaveBeenCalledTimes(1)
  })

  it('leaves plain text, or HTML that is only spans, to the browser', () => {
    const { box, onDraft } = setup()
    fireEvent.paste(box, { clipboardData: data({ 'text/html': '' }) })
    fireEvent.paste(box, { clipboardData: data({ 'text/html': '<div><span>a</span></div>', 'text/plain': 'a' }) })
    fireEvent.paste(box, { clipboardData: data({ 'text/plain': '# Plan\n\n```x\ny\n```' }) })
    expect(onDraft).not.toHaveBeenCalled()
  })

  it('still attaches a pasted file', () => {
    const addFiles = vi.fn()
    const file = new File(['x'], 'a.png', { type: 'image/png' })
    const { box, onDraft } = setup({ onAttach: () => {}, attachments: { ...attachments, addFiles } as unknown as ComposerProps['attachments'] })
    fireEvent.paste(box, { clipboardData: data({ 'text/html': '', 'text/plain': '' }, [file]) })
    expect(addFiles).toHaveBeenCalledWith([file])
    expect(onDraft).not.toHaveBeenCalled()
  })
})

describe('Composer plain paste on another layout', () => {
  it('recognises Cmd+Shift+V by its physical key', () => {
    const onDraft = vi.fn()
    const { container } = render(<Composer {...props({ onDraft })} />)
    const box = container.querySelector('textarea')!
    fireEvent.keyDown(box, { key: 'м', code: 'KeyV', metaKey: true, shiftKey: true })
    fireEvent.paste(box, { clipboardData: { getData: (t: string) => (t === 'text/html' ? '<h2>x</h2>' : 'x'), files: [] } })
    expect(onDraft).not.toHaveBeenCalled()
  })
})
