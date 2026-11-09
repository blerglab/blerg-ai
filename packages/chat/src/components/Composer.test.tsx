import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '@testing-library/react'
import Composer, { type ComposerProps } from './Composer'

const attachments = {
  items: [], done: [], uploading: false, addFiles: () => {}, remove: () => {}, clear: () => {},
} as unknown as ComposerProps['attachments']

const props = (over: Partial<ComposerProps> = {}): ComposerProps => ({
  draft: '', onDraft: () => {}, placeholder: 'Message', locked: false, onSubmit: () => {}, attachments, ...over,
})

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

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

describe('pasted image names', () => {
  it('gives two images in one paste and a later paste distinct useful names', () => {
    const addFiles = vi.fn()
    const { container } = render(<Composer {...props({ onAttach: () => {}, attachments: { ...attachments, addFiles } })} />)
    const paste = (files: File[]) => fireEvent.paste(container.querySelector('textarea')!, { clipboardData: { files, getData: () => '' } })
    const first = new File(['first'], 'image.png', { type: 'image/png' })
    const second = new File(['second'], 'image.png', { type: 'image/png' })
    paste([first, second]); paste([first])
    const images = addFiles.mock.calls.flatMap(call => call[0] as File[])
    expect(new Set(images.map(f => f.name)).size).toBe(3)
    expect(images.every(f => /^screenshot-.*\.png$/.test(f.name))).toBe(true)
    expect(images.map(f => f.type)).toEqual(['image/png', 'image/png', 'image/png'])
    expect(images.map(f => f.size)).toEqual([5, 6, 5])
  })
  it('keeps a meaningful name a copied file brings with it', () => {
    const addFiles = vi.fn()
    const { container } = render(<Composer {...props({ onAttach: () => {}, attachments: { ...attachments, addFiles } })} />)
    const named = new File(['chart'], 'q3-revenue-chart.png', { type: 'image/png' })
    const bare = new File(['bare'], '', { type: 'image/jpeg' })
    fireEvent.paste(container.querySelector('textarea')!, { clipboardData: { files: [named, bare], getData: () => '' } })
    const names = (addFiles.mock.calls[0][0] as File[]).map(f => f.name)
    expect(names[0]).toBe('q3-revenue-chart.png')
    expect(names[1]).toMatch(/^screenshot-.*\.jpg$/)
  })
})


describe('Composer height', () => {
  const setup = (over: Partial<ComposerProps> = {}) => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(1000)
    const ui = render(<div className="agent-chat"><Composer {...props(over)} /></div>)
    const pane = ui.container.firstElementChild as HTMLElement
    let paneHeight = 1000
    Object.defineProperty(pane, 'clientHeight', { get: () => paneHeight })
    const box = ui.container.querySelector('textarea')!
    Object.defineProperty(box, 'scrollHeight', { configurable: true, value: 2000 })
    return { ...ui, box, setPaneHeight: (height: number) => { paneHeight = height },
      update: (next: Partial<ComposerProps>) => ui.rerender(<div className="agent-chat"><Composer {...props({ ...over, ...next })} /></div>) }
  }
  it('caps a long paste at 420px or half the chat pane, then shrinks after clearing', () => {
    const ui = setup()
    ui.update({ draft: '60 lines' })
    expect(ui.box.style.height).toBe('420px')
    ui.setPaneHeight(500)
    ui.update({ draft: '60 lines edited' })
    expect(ui.box.style.height).toBe('250px')
    ui.update({ draft: '' })
    expect(ui.box.style.height).toBe('40px')
  })

  it('exposes a keyboard separator and clamps manual sizing to 85% of the pane', () => {
    const ui = setup()
    const handle = ui.getByRole('separator', { name: 'Resize message box' })
    expect(handle).toHaveAttribute('aria-orientation', 'horizontal')
    fireEvent.keyDown(handle, { key: 'ArrowUp' })
    expect(ui.box.style.height).toBe('64px')
    expect(handle).toHaveAttribute('aria-valuenow', '64')
    fireEvent.keyDown(handle, { key: 'ArrowDown' })
    expect(ui.box.style.height).toBe('40px')
    for (let i=0;i<50;i++) fireEvent.keyDown(handle, { key: 'ArrowUp' })
    expect(ui.box.style.height).toBe('850px')
    fireEvent.keyDown(handle, { key: 'Home' })
    expect(ui.box.style.height).toBe('40px')
  })

  it('restores the device height across remounts, isolates accounts, and double-click resets it', () => {
    const first = setup({ heightScope: 'height-account-a' })
    fireEvent.keyDown(first.getByRole('separator'), { key: 'ArrowUp' })
    first.unmount()
    const restored = setup({ heightScope: 'height-account-a' })
    expect(restored.box.style.height).toBe('64px')
    restored.update({ heightScope: 'height-account-b' })
    expect(restored.box.style.height).toBe('40px')
    restored.update({ heightScope: 'height-account-a' })
    expect(restored.box.style.height).toBe('64px')
    fireEvent.doubleClick(restored.getByRole('separator'))
    restored.unmount()
    const reset = setup({ heightScope: 'height-account-a' })
    expect(reset.box.style.height).toBe('40px')
  })
})
