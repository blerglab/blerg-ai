import { afterEach, expect, it } from 'vitest'

// Run deliberately failing cases only when validating the harness. Normal runs
// keep the sentinel; VITE_VERIFY_BROWSER_ERRORS=1 must fail two cases, run the
// sentinel, and report zero run-level unhandled errors.
const verify = import.meta.env.VITE_VERIFY_BROWSER_ERRORS === '1'
afterEach(context => {
  if (verify && context.task.name === 'contains an event-handler error even when teardown fails') throw new Error('deliberate teardown failure')
})
;(verify ? it : it.skip)('contains an event-handler error even when teardown fails', () => {
  window.dispatchEvent(new ErrorEvent('error', { error: new Error('deliberate event-handler failure'), cancelable: true }))
})
;(verify ? it : it.skip)('contains a rejected background promise', async () => {
  await new Promise<void>(resolve => {
    window.addEventListener('unhandledrejection', () => resolve(), { once: true })
    void Promise.reject(new SyntaxError('deliberate background failure'))
  })
})
it('runs the next case after browser errors without inheriting them', () => {
  expect(true).toBe(true)
})
