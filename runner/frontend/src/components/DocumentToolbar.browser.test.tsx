import {afterEach, describe, expect, it, vi} from 'vitest'
import {render, waitFor} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ChatContext, ReviewMode, type ChatContextValue} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const text='# Heading\n\nSelect this phrase.\n'
function mount() {
  const sent: File[][]=[]
  const context: ChatContextValue={sessionId:'toolbar-browser', files:null,view:vi.fn(),byName:()=>undefined,latestVersion:()=>undefined,reviewFor:async()=>null,submitFeedback:async(_text,files)=>{sent.push(files);return true}}
  return {...render(<ChatContext.Provider value={context}><ReviewMode artifact={{id:'toolbar',name:'example.md',view:'markdown',content_type:'text/markdown',size:text.length}} text={text} onClose={()=>{}}/></ChatContext.Provider>),sent}
}
async function select(editor: HTMLElement, quote: string) {
  editor.focus()
  const walker=document.createTreeWalker(editor,NodeFilter.SHOW_TEXT)
  for(let node=walker.nextNode();node;node=walker.nextNode()) {
    if(node.parentElement?.closest('[data-review-deletion]'))continue
    const start=(node as Text).data.indexOf(quote)
    if(start<0)continue
    const range=document.createRange();range.setStart(node,start);range.setEnd(node,start+quote.length)
    const selection=window.getSelection()!;selection.removeAllRanges();selection.addRange(range)
    await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))
    return
  }
  throw new Error('missing quote')
}
afterEach(async()=>{localStorage.clear();await page.viewport(1280,900)})
describe('document icon toolbar in Chromium',()=>{
 it.each([1280,390])('uses accessible icon controls that fit at width %s and preserve the selected phrase',async width=>{
  await page.viewport(width,900)
  const view=mount();const editor=await view.findByRole('textbox',{name:'Document'})
  const toolbar=view.getByRole('toolbar',{name:'Document formatting'})
  for(const button of toolbar.querySelectorAll('button')) {
   expect(button.querySelector('svg')).not.toBeNull()
   expect(button.textContent).toBe('')
   expect(button.getAttribute('title')).toBe(button.getAttribute('aria-label'))
   expect(button.getBoundingClientRect().width).toBeGreaterThanOrEqual(24)
   expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(width+1)
  }
  await select(editor,'this phrase')
  await userEvent.click(view.getByRole('button',{name:'Bold'}))
  await waitFor(()=>expect(editor.querySelector('strong')).toHaveTextContent('this phrase'))
  expect(view.getByRole('button',{name:'Bold'})).toHaveAttribute('aria-pressed','true')
  await select(editor,'Select')
  await waitFor(()=>expect(view.getByRole('button',{name:'Bold'})).toHaveAttribute('aria-pressed','false'))
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  expect(await view.sent[0][0].text()).toContain('Select **this phrase**.')
 })
 it('applies a safe link to the retained selection and rejects executable schemes',async()=>{
  const view=mount(), editor=await view.findByRole('textbox',{name:'Document'})
  await select(editor,'this phrase')
  await userEvent.click(view.getByRole('button',{name:'Link'}))
  await userEvent.fill(view.getByRole('textbox',{name:'Link URL'}),'javascript:alert(1)')
  await userEvent.click(view.getByRole('button',{name:'Apply link'}))
  expect(view.getByRole('alert')).toHaveTextContent('Use an https://')
  expect(editor.querySelector('a')).toBeNull()
  await userEvent.fill(view.getByRole('textbox',{name:'Link URL'}),'https://example.test/docs')
  await userEvent.click(view.getByRole('button',{name:'Apply link'}))
  expect(editor.querySelector('a')).toHaveTextContent('this phrase')
  expect(editor.querySelector('a')).toHaveAttribute('href','https://example.test/docs')
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  expect(await view.sent[0][0].text()).toContain('[this phrase](https://example.test/docs)')
 })
 it('can undo and redo icon formatting and inserts a bounded table',async()=>{
  const view=mount(),editor=await view.findByRole('textbox',{name:'Document'})
  expect(view.getByRole('button',{name:'Undo'})).toBeDisabled()
  await select(editor,'this phrase')
  await userEvent.click(view.getByRole('button',{name:'Strikethrough'}))
  expect(editor.querySelector('s')).toHaveTextContent('this phrase')
  await userEvent.click(view.getByRole('button',{name:'Undo'}))
  expect(editor.querySelector('s')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:'Redo'}))
  expect(editor.querySelector('s')).toHaveTextContent('this phrase')
  await userEvent.click(view.getByRole('button',{name:'Insert table'}))
  expect(editor.querySelectorAll('table tr')).toHaveLength(3)
  expect(editor.querySelectorAll('table th,table td')).toHaveLength(9)
  expect(view.getByRole('button',{name:'Insert table'})).toBeDisabled()
 })
})
