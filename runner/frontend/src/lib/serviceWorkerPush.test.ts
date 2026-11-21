import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { expect, it, vi } from 'vitest'

it('asks authenticated windows to repair a changed push subscription without storing credentials', async () => {
  const handlers: Record<string, (event: { waitUntil: (promise: Promise<void>) => void }) => void> = {}
  const postMessage = vi.fn()
  const matchAll = vi.fn(async () => [{ postMessage }])
  const fetch = vi.fn()
  const subscribe = vi.fn()
  runInNewContext(readFileSync('public/sw.js', 'utf8'), {
    self: { addEventListener: (name: string, handler: typeof handlers[string]) => { handlers[name] = handler }, registration: { pushManager: { subscribe } } },
    clients: { matchAll }, fetch,
  })
  let completed!: Promise<void>
  handlers.pushsubscriptionchange({ waitUntil: promise => { completed = promise } })
  await completed
  expect(matchAll).toHaveBeenCalledWith({ type: 'window', includeUncontrolled: true })
  expect(postMessage).toHaveBeenCalledWith({ type: 'push_subscription_changed' })
  expect(fetch).not.toHaveBeenCalled()
  expect(subscribe).not.toHaveBeenCalled()
})
