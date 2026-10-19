import {fireEvent, render, screen, waitFor} from '@testing-library/react'
import {createRef} from 'react'
import {describe, expect, it, vi} from 'vitest'
import DocumentEditor, {richDocumentSupported} from './DocumentEditor'

describe('safe rendered document editing', () => {
  it.each(['<img src="https://invalid.example/a">', '![image](https://invalid.example/a)', '---\ntitle: test\n---\n', '[x][ref]\n\n[ref]: https://example.test', '```mermaid\ngraph TD\n```'])('routes unsupported content to source editing: %s', source => {
    expect(richDocumentSupported(source)).toBe(false)
  })
  it('keeps source bytes unchanged when mounting and switching track visibility', async () => {
    const change = vi.fn(), rootRef = createRef<HTMLDivElement>()
    const source = '# Heading\n\nSome **bold** text.\n'
    const {rerender} = render(<DocumentEditor value={source} published={source} showChanges onChange={change} rootRef={rootRef}/>)
    await screen.findByRole('textbox', {name: 'Document'})
    expect(screen.getByRole('heading', {name: 'Heading'})).toBeInTheDocument()
    rerender(<DocumentEditor value={source} published={source} showChanges={false} onChange={change} rootRef={rootRef}/>)
    expect(change).not.toHaveBeenCalled()
  })
  it('shows tracked words in the rendered document and excludes decoration text from uploads', async () => {
    const change = vi.fn(), rootRef = createRef<HTMLDivElement>()
    const {container, rerender} = render(<DocumentEditor value="We test daily." published="We test weekly." showChanges onChange={change} rootRef={rootRef}/>)
    await waitFor(() => expect(container.querySelector('.document-deletion')).toHaveTextContent('weekly.'))
    expect(container.querySelector('.document-insertion')).toHaveTextContent('daily.')
    rerender(<DocumentEditor value="We test daily." published="We test weekly." showChanges={false} onChange={change} rootRef={rootRef}/>)
    await waitFor(() => expect(container.querySelector('.document-deletion')).toBeNull())
    expect(screen.getByRole('textbox', {name: 'Document'})).toHaveTextContent('We test daily.')
    expect(change).not.toHaveBeenCalled()
  })
  it('rejects dropped content rather than allowing HTML or files into the document', async () => {
    const change = vi.fn()
    render(<DocumentEditor value="Safe" published="Safe" showChanges onChange={change} rootRef={createRef<HTMLDivElement>()}/>)
    const editor = await screen.findByRole('textbox', {name: 'Document'})
    const event = new Event('drop', {bubbles: true, cancelable: true})
    fireEvent(editor, event)
    expect(event.defaultPrevented).toBe(true)
    expect(change).not.toHaveBeenCalled()
  })
})

it('keeps a task list and table intact when entering rendered editing', async () => {
  const source = '# Plan\n\n- [x] Tested\n- [ ] Release\n\n| Name | Value |\n| --- | --- |\n| count | 3 |\n'
  const change = vi.fn()
  render(<DocumentEditor value={source} published={source} showChanges onChange={change} rootRef={createRef<HTMLDivElement>()}/>)
  await screen.findByRole('textbox', {name: 'Document'})
  expect(screen.getAllByRole('checkbox')).toHaveLength(2)
  expect(screen.getByRole('table')).toHaveTextContent('count')
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(change).not.toHaveBeenCalled()
})
