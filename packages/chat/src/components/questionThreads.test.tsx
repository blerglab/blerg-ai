import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { QuestionPanel, QuestionThreadsProvider, type QuestionThreadPage, type QuestionThreadsClient } from './questionThreads'

function fixture(over: Partial<QuestionThreadPage['question']> = {}) {
 const data: QuestionThreadPage = { question: { id: 'q1', session_id: 'main', body: 'Deploy to staging?', status: 'open', question_turn: 'person', answerable: true, ...over }, entries: [], more: false }
 const client: QuestionThreadsClient = {
  load: vi.fn(async () => structuredClone(data)),
  reply: vi.fn(async (_id, text) => { data.entries.push({seq: data.entries.length+1,author:'person',kind:'reply',body:text,created_at:'2026-11-05T12:00:00Z'}); data.question.question_turn='main' }),
  answer: vi.fn(async (_id,text) => {data.question.status='answered'; data.question.answer=text; return true}),
  dismiss: vi.fn(async () => {data.question.status='answered'; data.question.dismissed=true; return true}),
 }
 render(<QuestionThreadsProvider client={client}><QuestionPanel id="q1" title="Deploy to staging?" /></QuestionThreadsProvider>)
 return {client,data}
}
it('allows repeated discussion replies, then keeps the explicit answer read-only in its tab',async()=>{
 const {client}=fixture()
 await screen.findByRole('textbox',{name:'Your answer'})
 for(const text of ['Which environment?','What about rollback?','yes']) {
  fireEvent.change(screen.getByRole('textbox'),{target:{value:text}})
  fireEvent.click(screen.getByRole('button',{name:'Ask Main'}))
  await screen.findByText(text)
  await waitFor(()=>expect(screen.getByRole('textbox')).toHaveValue(''))
 }
 expect(client.reply).toHaveBeenCalledTimes(3)
 expect(client.answer).not.toHaveBeenCalled()
 expect(screen.getByRole('button',{name:'Ask Main'})).toHaveAttribute('title','Ask Main about this question; keep it open')
 expect(screen.getByRole('button',{name:'Answer'})).toHaveAttribute('title','Send this as your answer and close the question')
 expect(screen.getByRole('button',{name:'Dismiss'})).toHaveAttribute('title','Close without answering')
 expect(screen.getByText('Main is replying…')).toBeTruthy()
 fireEvent.change(screen.getByRole('textbox'),{target:{value:'Use staging'}})
 fireEvent.click(screen.getByRole('button',{name:'Answer'}))
 await screen.findByText('Answered: Use staging')
 expect(screen.queryByRole('textbox')).toBeNull()
 expect(screen.getByRole('region',{name:'Question: Deploy to staging?'})).toBeTruthy()
})
it('retains the draft and retry id when sending fails',async()=>{
 const {client}=fixture()
 const send=vi.mocked(client.reply).mockRejectedValueOnce(new Error('offline'))
 await screen.findByRole('textbox')
 fireEvent.change(screen.getByRole('textbox'),{target:{value:'Which environment?'}})
 fireEvent.click(screen.getByRole('button',{name:'Ask Main'}))
 await screen.findByText('Your reply was not sent. Try again.')
 expect(screen.getByRole('textbox')).toHaveValue('Which environment?')
 fireEvent.click(screen.getByRole('button',{name:'Ask Main'}))
 await screen.findByText('Which environment?')
 expect(send.mock.calls[0][2]).toBe(send.mock.calls[1][2])
})
it('retains a dismissed outcome in its tab',async()=>{
 fixture()
 await screen.findByRole('button',{name:'Dismiss'})
 fireEvent.click(screen.getByRole('button',{name:'Dismiss'}))
 await screen.findByText('Dismissed')
 expect(screen.queryByRole('textbox')).toBeNull()
})
it('reloads a revised question without retrying a stale answer',async()=>{
 const {client,data}=fixture()
 await screen.findByRole('textbox')
 vi.mocked(client.answer).mockImplementationOnce(async()=>{data.question.body='Deploy to production?';data.question.question_revision=8;throw new Error('Main revised this question')})
 fireEvent.change(screen.getByRole('textbox'),{target:{value:'yes'}})
 fireEvent.click(screen.getByRole('button',{name:'Answer'}))
 await screen.findByText('Main revised this question')
 expect(await screen.findByRole('heading',{name:'Deploy to production?'})).toBeTruthy()
 expect(client.answer).toHaveBeenCalledTimes(1)
 expect(client.answer).toHaveBeenCalledWith('q1','yes',0)
 expect(screen.getByRole('textbox')).toHaveValue('yes')
})

it('answers with Ctrl+Enter while plain Enter keeps composing', async () => {
 const {client}=fixture()
 const box=await screen.findByRole('textbox')
 fireEvent.change(box,{target:{value:'Use staging'}})
 fireEvent.keyDown(box,{key:'Enter'})
 expect(client.answer).not.toHaveBeenCalled()
 fireEvent.keyDown(box,{key:'Enter',ctrlKey:true})
 await waitFor(()=>expect(client.answer).toHaveBeenCalledWith('q1','Use staging',0))
})

it('keeps command approvals reply-free and revision-bound', async () => {
 const {client}=fixture({owner_only:true,no_wait:false,decision:false,question_revision:7})
 const approve=await screen.findByRole('button',{name:'Approve'})
 expect(screen.queryByRole('textbox')).toBeNull()
 expect(screen.queryByRole('button',{name:'Ask Main'})).toBeNull()
 expect(screen.queryByRole('button',{name:'Dismiss'})).toBeNull()
 fireEvent.click(approve)
 await waitFor(()=>expect(client.answer).toHaveBeenCalledWith('q1','yes',7))
 expect(client.reply).not.toHaveBeenCalled()
})
it('keeps bound decision clarification distinct from approval', async () => {
 const {client}=fixture({decision:true,question_revision:5})
 fireEvent.click(await screen.findByRole('button',{name:'Ask Main'}))
 const box=screen.getByRole('textbox')
 fireEvent.change(box,{target:{value:'yes'}})
 fireEvent.keyDown(box,{key:'Enter',metaKey:true})
 await waitFor(()=>expect(client.reply).toHaveBeenCalledTimes(1))
 expect(client.answer).not.toHaveBeenCalled()
})
