import { browserWidths } from '../test/browserWidths'
import { expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import ArtifactViewer from '../../../../packages/chat/src/components/ArtifactViewer'
import type { TransportFiles } from '../../../../packages/chat/src/transport/types'
import '../index.css'

it.each(browserWidths([390, 3840]))('reviews and submits a real sandboxed page at %ipx without network', async width => {
  await page.viewport(width, width === 3840 ? 2160 : 844)
  const tag = `answer-${width}`
  const text = `<h1>Choose</h1><script>
    fetch('${location.origin}/__beacon/${tag}').catch(()=>{});
    new Image().src='${location.origin}/__beacon/${tag}.png';
    try { parent.document.body.dataset.leaked='yes' } catch {}
    parent.postMessage({type:'blerg:answer',result:{order:['b','a']}},'*');
    addEventListener('message',()=>parent.postMessage({type:'blerg:answer',result:{order:['changed']}},'*'));
  </script>`
  const submitAnswer=vi.fn(async()=>{})
  const files={raw:vi.fn(async()=>new Blob([text])),download:vi.fn(),
    answerRequest:vi.fn(async()=>({request_id:'r',artifact_id:'a',artifact_name:'choose.html',ask:'Rank these options',schema:{type:'object'},delivered:false})),submitAnswer} as unknown as TransportFiles
  render(<ArtifactViewer sessionId="s" files={files} artifact={{id:'a',name:'choose.html',size:text.length,content_type:'text/html',view:'html',origin:'agent',version:1}} onClose={()=>{}}/>)
  const review=await screen.findByRole('button',{name:'Review result'})
  await waitFor(()=>expect(review).toBeEnabled())
  const frame=document.querySelector('iframe')!
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
  expect(frame.contentDocument).toBeNull()
  expect(document.body.dataset.leaked).toBeUndefined()
  expect(submitAnswer).not.toHaveBeenCalled()
  await userEvent.click(review)
  expect(screen.getByText('{"order":["b","a"]}')).toBeTruthy()
  let changed = false
  const sawChange = (e: MessageEvent) => {if (e.source === frame.contentWindow && e.data?.result?.order?.[0] === 'changed') changed = true}
  window.addEventListener('message', sawChange)
  try {
    frame.contentWindow!.postMessage('test-update', '*')
    await waitFor(() => expect(changed).toBe(true))
  } finally { window.removeEventListener('message', sawChange) }
  expect(screen.getByText('{"order":["b","a"]}')).toBeTruthy()
  const submit=screen.getByRole('button',{name:'Submit'})
  for(const el of [review,frame,submit,screen.getByRole('region',{name:'Page answer'})]) {
    if (!el.isConnected) continue
    const rect=el.getBoundingClientRect()
    expect(rect.width).toBeGreaterThan(0)
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(width)
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await userEvent.click(submit)
  await screen.findByText('Answer sent.')
  expect(submitAnswer).toHaveBeenCalledExactlyOnceWith('s','a','{"order":["b","a"]}')
  const hits=await (await fetch('/__beacon_hits',{cache:'no-store'})).json() as string[]
  expect(hits.filter(hit=>hit.includes(tag))).toEqual([])
})
