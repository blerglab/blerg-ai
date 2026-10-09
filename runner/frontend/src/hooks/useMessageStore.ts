import { create } from 'zustand'
import type { MessageInfo, InitialState, MessageCreated, MessageAnswered } from '../types'
import { onMessage } from '../ws'
import { useToastStore } from './useToastStore'
import { useSessionStore } from './useSessionStore'
import { sessionLabel } from '../notify'

interface MessageStore {
  messages: MessageInfo[]
}

export const useMessageStore = create<MessageStore>(() => {
  // Register WS handlers once at store creation time
  onMessage<InitialState>('initial_state', (msg) => {
    useMessageStore.setState({ messages: msg.messages ?? [] })
  })

  onMessage<MessageCreated>('message_created', (msg) => {
    // De-dupe: if the message is already present, ignore it (and skip the toast).
    // Done outside setState so the side effect (addToast) never rides inside a
    // Zustand updater that React could re-run.
    if (useMessageStore.getState().messages.some(m => m.id === msg.message.id)) return

    // Fire a toast for the newly received live message.
    const session = useSessionStore.getState().sessions.find(s => s.id === msg.message.session_id)
    const label = session ? sessionLabel(session) : msg.message.session_id
    useToastStore.getState().addToast({
      title: `${label} · ${msg.message.kind}`,
      body: msg.message.body,
      url: `/sessions/${msg.message.session_id}`,
      variant: 'message',
    })

    // Prepend newest-first.
    useMessageStore.setState((state) => ({ messages: [msg.message, ...state.messages] }))
  })

  onMessage<MessageAnswered>('message_answered', (msg) => {
    useMessageStore.setState((state) => ({
      messages: state.messages.map(m => m.id === msg.message.id ? msg.message : m),
    }))
  })

  return { messages: [] }
})

// ─── Selectors ────────────────────────────────────────────────────────────────

/** Count of messages where status === 'open' (needs the user's attention). */
export function selectOpenCount(state: MessageStore): number {
  return state.messages.filter(m => m.status === 'open').length
}

/**
 * Returns a selector function that filters messages to a single session.
 *
 * WARNING: for one-shot `getState()` reads only. Do NOT pass directly to a
 * Zustand `useStore` hook — it creates a new array each call → infinite re-render.
 */
export function selectBySession(sessionId: string): (state: MessageStore) => MessageInfo[] {
  return (state) => state.messages.filter(m => m.session_id === sessionId)
}
