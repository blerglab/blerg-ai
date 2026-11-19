// Real session route: daemon pause -> runner store -> AgentChatView notice,
// then durable return claim -> existing composer. No injected notice slot.
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { localStorageChatStorage } from '../../../../packages/chat/src/model/storage'
import { resetChatState } from '../../../../packages/chat/src/test/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
import SessionDetail from './SessionDetail'

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
beforeEach(() => {
  resetChatState()
  localStorageChatStorage.set('blerg-runner.draft.sess-a', null)
  wsMock.resetWsMock()
  wsMock.send.mockReturnValue(true)
  apiFetch.mockImplementation(() => json({}))
})
afterEach(() => { invalidateChatTransport(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

for (const width of [390, 3840]) {
  it(`returns refused host recovery input through the session route at ${width}px`, async () => {
    await page.viewport(width, 900)
    const fetch = vi.fn((url: RequestInfo | URL) => String(url).includes('/inputs/') ? json({ text: 'Keep my interrupted work' }) : json([]))
    vi.stubGlobal('fetch', fetch)
    useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ status: 'running', kind: 'agent' })], statusChangedAt: {}, serverVersion: 'test' })
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><div style={{height:'100vh',display:'flex'}}><div style={{flex:1,minWidth:0}}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></MemoryRouter>)
    await screen.findByTestId('session-header-controls')
    act(() => wsMock.emit({type:'agent_events_replay_done',session_id:'sess-a',last_seq:0,has_more:false,has_older:false}))
    act(() => wsMock.emit({ type: 'session_paused_changed', session_id: 'sess-a', paused_reason: 'restart', paused_detail: 'Recovery paused: This session could not be recovered. Send a message to retry.' }))
    const notice = await screen.findByTestId('recovery-notice')
    expect(notice).toHaveTextContent('This session could not be recovered')
    expect(screen.queryByTestId('stop-turn')).toBeNull()
    const box = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'Keep my interrupted work' } })
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
    const sent = wsMock.send.mock.calls.map(([m]) => m).find(m => m.type === 'agent_user_message') as {client_id:string}
    expect(sent).toBeDefined()
    await act(async () => {
      wsMock.emit({ type:'agent_event', session_id:'sess-a', seq:1, client_event_id:'refusal', ts:new Date().toISOString(), kind:'error', payload:{client_id:sent.client_id,code:'input_recovery_failed',retryable:true,message:'This session could not be recovered. Send a message to retry recovery.'} })
    })
    await expect.poll(() => box.value).toBe('Keep my interrupted work')
    expect(fetch.mock.calls.some(([url]) => String(url).includes(`/inputs/${sent.client_id}/return`))).toBe(true)
    expect(screen.queryByTestId('queued-message')).toBeNull()
    expect(screen.getByTestId('message-dropped')).not.toHaveTextContent(/back in the composer/)
    expect(screen.getByTestId('recovery-notice')).toBeVisible()
    expect(box.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    const shots = (import.meta.env as Record<string, string | undefined>).VITE_HOST_RECOVERY_SHOTS_DIR
    if (shots) {
      await page.screenshot({ path: `${shots}/host-recovery-${width}.png` })
      document.documentElement.dataset.theme = 'colorblind-dark'
      await page.screenshot({ path: `${shots}/host-recovery-colorblind-${width}.png` })
      delete document.documentElement.dataset.theme
    }
  })
}
