import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import AnswerPage from './AnswerPage'
import type { TransportFiles } from '../transport/types'

const request = {request_id:'r',artifact_id:'a',artifact_name:'choose.html',ask:'Choose one',schema:{type:'string'},delivered:false}
function setup() {
 const submitAnswer=vi.fn(async()=>{})
 const files={answerRequest:vi.fn(async()=>request),submitAnswer} as unknown as TransportFiles
 render(<AnswerPage sessionId="s" artifactId="a" name="choose.html" text="<h1>Choose</h1>" files={files}/> )
 const frame=document.querySelector('iframe')!
 fireEvent.load(frame)
 const message=(result:unknown,source:MessageEventSource|null=frame.contentWindow)=>act(()=>{window.dispatchEvent(new MessageEvent('message',{source,data:{type:'blerg:answer',result}}))})
 return {submitAnswer,frame,message}
}
describe('answer page trust boundary',()=>{
 it('freezes exactly the reviewed result, with two owner clicks',async()=>{
  const {message,submitAnswer,frame}=setup()
  await screen.findByText('Choose one')
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
  message('a')
  fireEvent.click(screen.getByRole('button',{name:'Review result'}))
  message('b')
  expect(screen.getByText('"a"')).toBeTruthy()
  fireEvent.click(screen.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(submitAnswer).toHaveBeenCalledWith('s','a','"a"'))
  await screen.findByText('Answer sent.')
  expect(submitAnswer).toHaveBeenCalledTimes(1)
 })
 it('ignores sibling frames, oversized values, and non-JSON data',async()=>{
  const {message}=setup();await screen.findByText('Choose one')
  message('forged',window)
  expect(screen.getByRole('button',{name:'Review result'})).toBeDisabled()
  for(const value of ['x'.repeat(17000),{bad:BigInt(1)},new Date()]) {
   message(value)
   expect(screen.getByRole('button',{name:'Review result'})).toBeDisabled()
  }
 })
 it('clears the frozen result when the frame loads again',async()=>{
  const {message,frame}=setup();await screen.findByText('Choose one');message('a')
  fireEvent.click(screen.getByRole('button',{name:'Review result'}))
  fireEvent.load(frame)
  expect(screen.queryByRole('button',{name:'Submit'})).toBeNull()
  expect(screen.getByRole('button',{name:'Review result'})).toBeDisabled()
 })
})

it('restores an accepted answer for delivery retry without allowing it to change',async()=>{
 const submitAnswer=vi.fn(async()=>{})
 const files={answerRequest:vi.fn(async()=>({...request,result:null})),submitAnswer} as unknown as TransportFiles
 render(<AnswerPage sessionId="s" artifactId="a" name="choose.html" text="<p>Choose</p>" files={files}/>)
 await screen.findByRole('button',{name:'Submit'})
 expect(screen.queryByRole('button',{name:'Back to editing'})).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(submitAnswer).toHaveBeenCalledExactlyOnceWith('s','a','null'))
})
