// The transcript's clock for timing the reader's input (how far apart wheel events are, how long a
// stream is held back). A test can set `now` to drive that timing by hand instead of the wall clock.
export const inputClock = { now: (): number => performance.now() }
