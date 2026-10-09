import { createContext } from 'react'

/** client clock − server clock (ms). Event timestamps are server time. */
export const ClockOffsetContext = createContext(0)
