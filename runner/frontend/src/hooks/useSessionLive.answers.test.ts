import { expect, it, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { useSessionLive } from './useSessionLive'
import { apiFetch } from '../apiFetch'
import type { LiveReply } from '../lib/liveState'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))

it('carries server answer requests through to the session page', async () => {
  const request = {request_id:'r1',artifact_id:'a1',artifact_name:'rank.html',ask:'Rank the options'}
  const reply: LiveReply = {
    session_id:'s1',through_seq:0,generated_at:new Date().toISOString(),tokens_hidden:false,
    asks:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:1,running:0},answer_requests:[request],
  }
  vi.mocked(apiFetch).mockResolvedValue({ok:true,status:200,headers:new Headers({'Content-Type':'application/json'}),json:async()=>reply} as Response)
  const {result} = renderHook(() => useSessionLive('s1',true,''))
  await waitFor(() => expect(result.current.source).toBe('server'))
  expect(result.current).toMatchObject({answerRequests:[request]})
})
