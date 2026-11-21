import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import { useTranscriptStore } from '../hooks/useTranscriptStore'

beforeEach(resetChatState)
it('renders recipient-only cards without adding them to the shared transcript', () => {
 seed(ev({ kind: 'assistant_text', payload: { text: 'Ready.', done: true } }))
 const notice=ev({ client_event_id:'inbox:item', kind:'inbox_item', payload:{id:'item',subject:'Morning summary'} })
 const listener=vi.fn();window.addEventListener('blerg-open-inbox',listener)
 const view=render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({status:'idle'})} privateEvents={[notice,{...notice,session_id:'other',client_event_id:'foreign',payload:{id:'foreign',subject:'Foreign summary'}}]} />)
 expect(screen.getByTestId('inbox-arrival')).toHaveTextContent('Morning summary')
 expect(screen.queryByText('Foreign summary')).toBeNull()
 expect(useTranscriptStore.getState().sessions[SESSION]?.events.some(e=>e.kind==='inbox_item')).toBe(false)
 fireEvent.click(screen.getByRole('button',{name:'Open inbox'}))
 expect(listener).toHaveBeenCalledTimes(1)
 expect((listener.mock.calls[0][0] as CustomEvent).detail).toEqual({id:'item'})
 view.rerender(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta({status:'idle'})} privateEvents={[]} />)
 expect(screen.queryByTestId('inbox-arrival')).toBeNull()
 window.removeEventListener('blerg-open-inbox',listener)
})
