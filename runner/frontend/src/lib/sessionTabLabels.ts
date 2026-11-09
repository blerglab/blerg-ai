import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionLabelSource } from '../hooks/useSessionTabs'

// The runner supplies live session data; reusable shell/file tabs need no session socket.
export const sessionTabLabels: SessionLabelSource = {
  subscribe: listener => useSessionStore.subscribe(listener),
  title: id => {
    const session = useSessionStore.getState().sessions.find(session => session.id === id)
    return session ? session.title || '' : undefined
  },
}
