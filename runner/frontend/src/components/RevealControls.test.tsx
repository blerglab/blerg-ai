import { cleanup, render, screen, fireEvent, act } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import RevealControls from './RevealControls'
const hooks = vi.hoisted(() => ({ event: undefined as undefined | ((event: unknown) => void) }))
vi.mock('../ws', () => ({ onMessage: (_: string, callback: (event: unknown) => void) => { hooks.event = callback; return () => {} }, onClose: () => () => {} }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => 'person' }))
afterEach(() => { cleanup(); history.replaceState(null,'','/') })
it('follows live requests only after opt-in and defers editing, modals and other sessions', () => {
 history.replaceState(null,'','/sessions/fixture')
 const attachment = vi.fn()
 render(<RevealControls sessionId="fixture" onAttachment={attachment} host={document.body}/>)
 const emit = (session = 'fixture', ts = new Date().toISOString(), replayed = false) => act(() => hooks.event?.({type:'agent_event',session_id:session,kind:'ui_reveal',ts,replayed,payload:{action:'attachment',target:'11111111111111111111111111111111'}}))
 emit(); expect(attachment).not.toHaveBeenCalled()
 expect(screen.getByRole('status').textContent).not.toContain('11111111111111111111111111111111')
 screen.getByRole('checkbox').focus()
 fireEvent.click(screen.getByRole('checkbox'))
 emit('other'); emit('fixture','2020-01-01T00:00:00Z',true); emit('fixture','2099-01-01T00:00:00Z',true); expect(attachment).not.toHaveBeenCalled()
 const input = document.createElement('textarea'); document.body.append(input); input.focus()
 emit(); expect(attachment).not.toHaveBeenCalled(); input.remove()
 const dialog = document.createElement('div'); dialog.setAttribute('role','dialog'); document.body.append(dialog)
 emit(); expect(attachment).not.toHaveBeenCalled(); dialog.remove()
 history.replaceState(null,'','/sessions/other'); emit(); expect(attachment).not.toHaveBeenCalled()
 history.replaceState(null,'','/sessions/fixture'); emit('fixture','2020-01-01T00:00:00Z'); expect(attachment).toHaveBeenCalledOnce()
})

it('handles explicit Show in archived and viewer chats without Follow', () => {
 const attachment=vi.fn()
 render(<RevealControls sessionId="fixture" onAttachment={attachment} enabled={false}/>)
 expect(screen.queryByRole('checkbox')).toBeNull()
 act(()=>window.dispatchEvent(new CustomEvent('blerg-ui-reveal',{cancelable:true,detail:{session:'fixture',action:'attachment',target:'11111111111111111111111111111111'}})))
 expect(attachment).toHaveBeenCalledOnce()
})

it('does not place Follow in a side conversation without a settings host', () => {
 const attachment = vi.fn()
 render(<RevealControls sessionId="side" onAttachment={attachment} />)
 expect(screen.queryByRole('checkbox')).toBeNull()
 act(() => window.dispatchEvent(new CustomEvent('blerg-ui-reveal', { cancelable: true, detail: { session: 'side', action: 'attachment', target: '11111111111111111111111111111111' } })))
 expect(attachment).toHaveBeenCalledOnce()
})
