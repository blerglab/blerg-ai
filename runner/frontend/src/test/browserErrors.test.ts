import { describe, expect, it, vi } from 'vitest'
import { collectBrowserErrors } from './browserErrors'

it('fails the owning case with every error and rejection, then resets for the next case', () => {
  const target = new EventTarget()
  const log = vi.fn()
  const errors = collectBrowserErrors(target, log)
  const cause = new SyntaxError('invalid fixture JSON')
  const event = new Event('unhandledrejection', { cancelable: true })
  Object.defineProperty(event, 'reason', { value: cause })
  target.dispatchEvent(event)
  target.dispatchEvent(new ErrorEvent('error', { error: new Error('render failed'), cancelable: true }))
  expect(event.defaultPrevented).toBe(true)
  expect(log).toHaveBeenCalledTimes(2)
  expect(() => errors.assertEmpty()).toThrow(AggregateError)
  expect(() => errors.assertEmpty()).not.toThrow()
  errors.dispose()
  target.dispatchEvent(event)
  expect(log).toHaveBeenCalledTimes(2)
})

it('retains errors without Error objects and non-Error rejection reasons', () => {
  const target = new EventTarget()
  const errors = collectBrowserErrors(target, vi.fn())
  target.dispatchEvent(new ErrorEvent('error', { message: 'script failed' }))
  const event = new Event('unhandledrejection')
  Object.defineProperty(event, 'reason', { value: 'rejected' })
  target.dispatchEvent(event)
  try { errors.assertEmpty(); expect.unreachable() } catch (cause) {
    expect(cause).toBeInstanceOf(AggregateError)
    expect((cause as AggregateError).errors.map((error: Error) => error.message)).toEqual(['script failed', 'rejected'])
  }
  errors.dispose()
})

describe('benign browser notices', () => {
  it('ignores only the ResizeObserver loop notices', async () => {
    const { collectBrowserErrors, isBenignBrowserNotice } = await import('./browserErrors')
    expect(isBenignBrowserNotice('ResizeObserver loop completed with undelivered notifications.')).toBe(true)
    expect(isBenignBrowserNotice('ResizeObserver loop limit exceeded')).toBe(true)
    expect(isBenignBrowserNotice('ResizeObserver is not defined')).toBe(false)
    const target = new EventTarget()
    const collector = collectBrowserErrors(target, () => {})
    target.dispatchEvent(Object.assign(new Event('error', { cancelable: true }), { error: new Error('ResizeObserver loop completed with undelivered notifications.') }))
    expect(() => collector.assertEmpty()).not.toThrow()
    target.dispatchEvent(Object.assign(new Event('error', { cancelable: true }), { error: new Error('real failure') }))
    expect(() => collector.assertEmpty()).toThrow(/real failure/)
    collector.dispose()
  })
})
