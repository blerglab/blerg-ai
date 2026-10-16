import { createContext } from 'react'

/** Opens a session from the session list, which highlights the row at once (an optimistic
 *  selection) while the switch to it renders. Without a provider a card navigates by itself. */
export const OpenSessionContext = createContext<((id: string) => void) | null>(null)
