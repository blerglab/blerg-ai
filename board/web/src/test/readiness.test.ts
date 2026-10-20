import { configure, getConfig, waitFor } from '@testing-library/dom'
import { afterEach, expect, it, vi } from 'vitest'

const asyncWrapper = getConfig().asyncWrapper
afterEach(() => {
  configure({ asyncWrapper })
  vi.useRealTimers()
})

function virtualClock() {
  // Exercise the real polling/deadline code without React's unrelated act flush timer.
  configure({ asyncWrapper: callback => callback() })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
}

it('waits for delayed readiness beyond a loaded runner scheduling pause', async () => {
  virtualClock()
  let ready = false
  const result = waitFor(() => expect(ready).toBe(true)).then(() => true, () => false)
  await vi.advanceTimersByTimeAsync(6000)
  ready = true
  await vi.advanceTimersByTimeAsync(50)
  expect(await result).toBe(true)
})

it('returns immediately when ready and still fails when readiness never arrives', async () => {
  virtualClock()
  const start = Date.now()
  await waitFor(() => expect(true).toBe(true))
  expect(Date.now()).toBe(start)
  const result = waitFor(() => expect(false).toBe(true), { timeout: 250 }).then(() => true, () => false)
  await vi.advanceTimersByTimeAsync(300)
  expect(await result).toBe(false)
})
