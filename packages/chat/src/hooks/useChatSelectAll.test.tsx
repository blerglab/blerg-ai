import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render } from '@testing-library/react'
import { useRef } from 'react'
import { useSelectAllIn } from './useSelectAllIn'
import { useChatSelectAll } from './useChatSelectAll'

function Pane() {
  const ref = useRef<HTMLDivElement | null>(null)
  useChatSelectAll(ref)
  return (
    <div ref={ref} data-testid="agent-chat">
      <div className="agent-toolbar">Files</div>
      <div className="agent-timeline">
        <div className="agent-card user" data-testid="card-1">
          <p>First message</p>
          <button type="button" className="msg-copy">Copy message</button>
        </div>
        <div className="agent-card assistant" data-testid="card-2">
          <p>Reply with code</p>
          <div className="md-codeblock">
            <button type="button" className="md-copy">Copy</button>
            <pre><code>const x = 1</code></pre>
          </div>
        </div>
      </div>
      <textarea data-testid="composer" defaultValue="draft" />
    </div>
  )
}

function Dialog() {
  const ref = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(ref)
  return <div ref={ref} data-testid="dialog"><div className="artifact-body">Dialog body</div></div>
}

afterEach(() => { cleanup(); window.getSelection()?.removeAllRanges() })

const selected = () => window.getSelection()?.toString() ?? ''

describe('useChatSelectAll', () => {
  it('a click inside a message then Ctrl+A selects that message only', () => {
    render(<><p>Page text behind</p><Pane /></>)
    fireEvent.mouseDown(document.querySelector('[data-testid="card-1"] p')!)
    const ev = fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(ev).toBe(false) // default prevented
    expect(selected()).toBe('First messageCopy message')
  })

  it('a click inside a code block then Ctrl+A selects the code only, not the Copy button', () => {
    render(<Pane />)
    fireEvent.mouseDown(document.querySelector('[data-testid="card-2"] pre')!)
    fireEvent.keyDown(document.body, { key: 'A', metaKey: true })
    expect(selected()).toBe('const x = 1')
  })

  it('a click on the toolbar then Ctrl+A selects the transcript, not the toolbar or the page', () => {
    render(<><p>Page text behind</p><Pane /></>)
    fireEvent.mouseDown(document.querySelector('.agent-toolbar')!)
    fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(selected()).toContain('First message')
    expect(selected()).toContain('Reply with code')
    expect(selected()).not.toContain('Page text behind')
    expect(selected()).not.toContain('Files')
  })

  it('keyboard focus alone (no click) scopes to the focused message', () => {
    render(<Pane />)
    const btn = document.querySelector('[data-testid="card-1"] .msg-copy') as HTMLElement
    btn.focus()
    fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(selected()).toBe('First messageCopy message')
  })

  it('leaves the composer its own select-all', () => {
    render(<Pane />)
    const t = document.querySelector('[data-testid="composer"]') as HTMLTextAreaElement
    expect(fireEvent.keyDown(t, { key: 'a', ctrlKey: true })).toBe(true)
    expect(selected()).toBe('')
  })

  it('stands aside while a viewer dialog is open', () => {
    render(<><Pane /><Dialog /></>)
    fireEvent.mouseDown(document.querySelector('[data-testid="card-1"] p')!)
    fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })
    expect(selected()).toBe('Dialog body')
  })

  it('does nothing when nothing in the pane was ever clicked or focused', () => {
    render(<><p>Page text behind</p><Pane /></>)
    expect(fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true })).toBe(true)
    expect(selected()).toBe('')
  })
})
