import { createContext } from 'react'

/** The workspace shell opens its own files locally and routes other sessions to their file viewer. */
export const OpenReviewContext = createContext<((sessionId: string, artifactId: string) => void) | null>(null)
