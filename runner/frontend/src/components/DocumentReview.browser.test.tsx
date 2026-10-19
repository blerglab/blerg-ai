import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, waitFor} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ChatContext, ReviewMode, type ChatContextValue, type ReviewFile} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const source = '# Review\n\nWe test weekly.\n\nAnother paragraph.\n'
const artifact = {id: 'document-demo', name: 'example.md', view: 'markdown', content_type: 'text/markdown', size: source.length, version: 1}
function mount(text = source, review: ReviewFile | null = null) {
  const sent: Array<{text: string; files: File[]}> = []
  const value: ChatContextValue = {sessionId: 'document-browser', files: null, view: vi.fn(), byName: () => undefined, latestVersion: () => undefined,
    reviewFor: async () => review, submitFeedback: async (text, files) => {sent.push({text,files}); return true}}
  const view = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={text} onClose={() => {}}/></ChatContext.Provider>)
  return {...view, sent}
}
function select(root: HTMLElement, quote: string, occurrence = 1) {
  root.focus()
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let left = occurrence
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.parentElement?.closest('[data-review-deletion]')) continue
    const text = (n as Text).data
    for (let at = text.indexOf(quote); at >= 0; at = text.indexOf(quote, at + quote.length)) {
      if (--left !== 0) continue
      const range = document.createRange();range.setStart(n, at);range.setEnd(n, at + quote.length)
      const selection = window.getSelection()!;selection.removeAllRanges();selection.addRange(range)
      return
    }
  }
  throw new Error(`Missing selection ${quote}`)
}
afterEach(async () => {localStorage.clear(); await page.viewport(1280,900)})

describe('document review in Chromium', () => {
  it('edits rendered text, toggles tracked changes, undoes and submits clean Markdown', async () => {
    const {findByRole, getByRole, container, sent} = mount()
    const editor = await findByRole('textbox', {name: 'Document'})
    select(editor, 'weekly')
    // Let Chromium deliver selectionchange to ProseMirror before the synthetic paste.
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    // A single paste transaction gives one undo step even when a parallel browser run
    // delays successive keystrokes beyond ProseMirror's normal typing-group timeout.
    const replacement = new DataTransfer();replacement.setData('text/plain', 'daily')
    editor.dispatchEvent(new ClipboardEvent('paste', {clipboardData: replacement, bubbles: true, cancelable: true}))
    await waitFor(() => expect(container.querySelector('.document-deletion')).toHaveTextContent('weekly'))
    expect(container.querySelector('.document-insertion')).toHaveTextContent('daily')
    await userEvent.click(getByRole('button', {name: 'Show changes'}))
    expect(container.querySelector('.document-deletion')).toBeNull()
    expect(editor).toHaveTextContent('We test daily.')
    await userEvent.click(getByRole('button', {name: 'Undo'}))
    expect(editor).toHaveTextContent('We test weekly.')
    expect(getByRole('button', {name: 'Submit'})).toBeDisabled()
    await userEvent.click(getByRole('button', {name: 'Redo'}))
    await userEvent.click(getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    const markdown = await sent[0].files[0].text()
    expect(markdown).toContain('We test daily.')
    expect(markdown).not.toContain('weekly')
    expect(markdown).not.toContain('document-deletion')
  })
  it.each([1280,390])('opens a margin comment with the familiar shortcut at width %s without losing a dirty draft', async width => {
    await page.viewport(width,900)
    const {findByRole, getByRole, container, sent} = mount()
    const editor = await findByRole('textbox', {name: 'Document'})
    select(editor, 'weekly')
    await userEvent.keyboard('{Control>}{Alt>}m{/Alt}{/Control}')
    const input = getByRole('textbox', {name: 'Request'})
    expect(input.closest('.document-margin')).not.toBeNull()
    await userEvent.fill(input, 'Test daily instead')
    expect(getByRole('button', {name: 'Add comment'})).toBeDisabled()
    await userEvent.click(getByRole('button', {name: 'Save'}))
    await waitFor(() => expect(getByRole('button', {name: 'Show passage'})).toBeEnabled())
    const bounds = container.querySelector('.document-margin')!.getBoundingClientRect()
    expect(bounds.right).toBeLessThanOrEqual(width + 1)
    await userEvent.click(getByRole('button', {name: 'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    const review = JSON.parse(await sent[0].files.at(-1)!.text())
    expect(review.requests[0].anchor.quote).toBe('weekly')
  })
  it('does not move a comment to another identical passage when its original text is deleted', async () => {
    const text = '# Repeats\n\nSame passage.\n\nSame passage.\n'
    const review: ReviewFile = {version: 1, file: {name: artifact.name, artifactId: artifact.id}, requests: [{id:'first',text:'Comment on first passage',status:'open',createdAt:'2026-01-01T00:00:00Z',anchor:{kind:'text',quote:'Same passage.',occurrence:1}}]}
    const {findByRole,getByRole} = mount(text,review)
    const editor = await findByRole('textbox',{name:'Document'})
    await waitFor(() => expect(getByRole('button',{name:'Show passage'})).toBeEnabled())
    select(editor,'Same passage.',1)
    await userEvent.keyboard('{Backspace}')
    await waitFor(() => expect(getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
    expect(editor).toHaveTextContent('Same passage.')
  })
  it('routes HTML and other unsupported Markdown to source editing without creating resource elements', async () => {
    const text = '<?xml version="1.0"?>\n\n<img src="https://invalid.example/private" onerror="alert(1)">\n'
    const {getByRole,container} = mount(text)
    expect(getByRole('textbox',{name:'Source'})).toHaveValue(text)
    expect(container.querySelector('.document-editor,img,iframe,svg')).toBeNull()
    expect(getByRole('button',{name:'Submit'})).toBeDisabled()
  })
  it('pastes HTML as plaintext before the Markdown HTML parser can run', async () => {
    const {findByRole,container,sent,getByRole} = mount()
    const editor = await findByRole('textbox',{name:'Document'})
    select(editor,'weekly')
    const clipboardData = new DataTransfer()
    clipboardData.setData('text/plain','safe words')
    clipboardData.setData('text/html','<img src="https://invalid.example/a"><script>alert(1)</script>')
    editor.dispatchEvent(new ClipboardEvent('paste',{clipboardData,bubbles:true,cancelable:true}))
    await waitFor(() => expect(editor).toHaveTextContent('safe words'))
    expect(container.querySelector('img,iframe,script')).toBeNull()
    await userEvent.click(getByRole('button',{name:'Submit'}))
    await waitFor(() => expect(sent).toHaveLength(1))
    expect(await sent[0].files[0].text()).not.toContain('<img')
  })
})
