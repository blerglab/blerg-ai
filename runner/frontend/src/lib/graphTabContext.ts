import { createContext } from 'react'
export const GraphTabContext = createContext<((sessionId: string, memberId: string, name: string) => void) | null>(null)
