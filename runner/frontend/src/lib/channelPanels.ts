import { createContext } from 'react'

/** Actual channel visibility, independent of the workspace route and dock selection. */
export const ChannelPanelsContext = createContext<{ visible: string[]; focused: string } | null>(null)
