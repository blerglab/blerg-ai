import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import CommentEditor from './CommentEditor'

function Controlled({initial = '', onSuggest}: {initial?: string; onSuggest?: () => void}) {
  const [value, setValue] = useState(initial)
  return <CommentEditor value={value} onChange={setValue} label="Review comment" onSuggest={onSuggest}/>
}
function select(start: number, end: number) {
  const textarea = screen.getByRole('textbox', {name: 'Review comment'}) as HTMLTextAreaElement
  textarea.focus(); textarea.setSelectionRange(start, end)
  return textarea
}

describe('review Markdown editor', () => {
  it('formats only selected text and restores the logical selection', () => {
    render(<Controlled initial="Use this name"/>)
    const textarea = select(4, 8)
    fireEvent.click(screen.getByRole('button', {name: 'Bold'}))
    expect(textarea.value).toBe('Use **this** name')
    expect(textarea.selectionStart).toBe(6)
    expect(textarea.selectionEnd).toBe(10)
    fireEvent.click(screen.getByRole('button', {name: 'Bold'}))
    expect(textarea.value).toBe('Use this name')
    expect(textarea.selectionStart).toBe(4)
    expect(textarea.selectionEnd).toBe(8)
  })
  it.each([['Italic', '_word_'], ['Code', '`word`'], ['Heading', '### word'],
    ['Quote', '> word'], ['Bulleted list', '- word'], ['Numbered list', '1. word']])('offers %s formatting', (button, expected) => {
    render(<Controlled initial="word"/>)
    const textarea = select(0, 4)
    fireEvent.click(screen.getByRole('button', {name: button}))
    expect(textarea.value).toBe(expected)
  })
  it('formats multiline selections as lists without including the following line', () => {
    render(<Controlled initial={'alpha\nbeta\nuntouched'}/>)
    const textarea = select(0, 11)
    fireEvent.click(screen.getByRole('button', {name: 'Numbered list'}))
    expect(textarea.value).toBe('1. alpha\n2. beta\nuntouched')
  })
  it('inserts a link and selects its editable destination', () => {
    render(<Controlled initial="docs"/>)
    const textarea = select(0, 4)
    fireEvent.click(screen.getByRole('button', {name: 'Link'}))
    expect(textarea.value).toBe('[docs](https://)')
    expect(textarea.value.slice(textarea.selectionStart, textarea.selectionEnd)).toBe('https://')
  })
  it('handles keyboard formatting through the same controlled selection', () => {
    render(<Controlled initial="name"/>)
    const textarea = select(0, 4)
    fireEvent.keyDown(textarea, {key: 'b', ctrlKey: true})
    expect(textarea.value).toBe('**name**')
    fireEvent.keyDown(textarea, {key: 'i', metaKey: true})
    expect(textarea.value).toBe('**_name_**')
  })
  it('previews safe Markdown, preserves the draft, and never loads images or raw HTML', () => {
    const draft = '**Bold**\n\n<img src="/private" onerror="alert(1)">\n\n![track](https://example.com/pixel)\n\n[bad](javascript:alert(1))'
    const {container} = render(<Controlled initial={draft}/>)
    fireEvent.click(screen.getByRole('tab', {name: 'Preview'}))
    expect(container.querySelector('strong')?.textContent).toBe('Bold')
    expect(container.querySelector('img,script,iframe')).toBeNull()
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull()
    fireEvent.click(screen.getByRole('tab', {name: 'Write'}))
    expect(screen.getByRole('textbox', {name: 'Review comment'})).toHaveValue(draft)
  })
  it('uses a longer code fence when the selected source contains backticks', () => {
    const original = 'one\n```\n<img src=x>'
    const {container} = render(<Controlled initial={original}/>)
    const textarea = select(0, original.length)
    fireEvent.click(screen.getByRole('button', {name: 'Code'}))
    expect(textarea.value).toBe('````\n' + original + '\n````')
    fireEvent.click(screen.getByRole('tab', {name: 'Preview'}))
    expect(container.querySelector('code')?.textContent).toBe(original)
    expect(container.querySelector('img')).toBeNull()
  })
  it('keeps formatting scoped to the focused editor and supports keyboard tab changes', () => {
    const first = vi.fn()
    const second = vi.fn()
    render(<><CommentEditor value="first" onChange={first} label="First comment"/>
      <CommentEditor value="second" onChange={second} label="Second comment"/></>)
    const textarea = screen.getByRole('textbox', {name: 'Second comment'}) as HTMLTextAreaElement
    textarea.setSelectionRange(0, 6)
    fireEvent.keyDown(textarea, {key: 'b', ctrlKey: true})
    expect(second).toHaveBeenCalledWith('**second**')
    expect(first).not.toHaveBeenCalled()
    const tabs = screen.getAllByRole('tab', {name: 'Write'})
    fireEvent.keyDown(tabs[1], {key: 'ArrowRight'})
    expect(screen.getAllByRole('tab', {name: 'Preview'})[1]).toHaveAttribute('aria-selected', 'true')
    expect(screen.getAllByRole('tab', {name: 'Preview'})[1]).toHaveFocus()
  })
  it('clamps the caret when removing a prefix that contains the selection', () => {
    render(<Controlled initial="### word"/>)
    const textarea = select(1, 2)
    fireEvent.click(screen.getByRole('button', {name: 'Heading'}))
    expect(textarea.value).toBe('word')
    expect(textarea.selectionStart).toBe(0)
    expect(textarea.selectionEnd).toBe(0)
  })
  it('offers structured suggestions only when the parent enables them', () => {
    const suggest = vi.fn()
    const {rerender} = render(<CommentEditor value="draft" onChange={() => {}} onSuggest={suggest}/>)
    fireEvent.click(screen.getByRole('button', {name: 'Insert suggestion'}))
    expect(suggest).toHaveBeenCalledOnce()
    rerender(<CommentEditor value="draft" onChange={() => {}}/>)
    expect(screen.queryByRole('button', {name: 'Insert suggestion'})).toBeNull()
  })
})
