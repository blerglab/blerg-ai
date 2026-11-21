import { createContext, useContext, type RefObject } from 'react'

/** The chat's scrollable timeline element (Transcript's own ref): read by anything measuring how
 *  much of the visible pane a card fills, such as PopOutMessage's one-third rule. */
export const ChatPaneContext = createContext<RefObject<HTMLDivElement | null> | null>(null)
export function useChatPaneRef() { return useContext(ChatPaneContext) }
