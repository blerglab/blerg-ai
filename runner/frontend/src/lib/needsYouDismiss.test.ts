// Card #155 review: who may dismiss what (I2), the send when the page goes away (M2), and a 409
// for an item already closed (M3).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { canDismissAsk, dismissWithUndo, DISMISS_UNDO_MS, flushPendingDismissals, useDismissStore } from './needsYouDismiss'
import { useToastStore } from '../hooks/useToastStore'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
import { apiFetch } from '../apiFetch'
const fetchMock = vi.mocked(apiFetch)

describe('canDismissAsk', () => {
  it('never offers Dismiss on an engine command approval', () => {
    expect(canDismissAsk({ messageId: 'm1', ownerOnly: true }, true, false)).toBe(false)
  })
  it('offers it on a waited question to anyone, and on an owner-only or no-wait one to the owner', () => {
    expect(canDismissAsk({ messageId: 'm1' }, false, false)).toBe(true)
    expect(canDismissAsk({ messageId: 'm1', ownerOnly: true, noWait: true }, true, false)).toBe(true)
    expect(canDismissAsk({ messageId: 'm1', ownerOnly: true, noWait: true }, false, false)).toBe(false)
    expect(canDismissAsk({ messageId: 'm1', noWait: true }, false, false)).toBe(false)
    // Not known yet: offered, and the server decides.
    expect(canDismissAsk({ messageId: 'm1', noWait: true }, undefined, false)).toBe(true)
  })
  it('never without a message id, or in an archived session', () => {
    expect(canDismissAsk({ messageId: '' }, true, false)).toBe(false)
    expect(canDismissAsk({ messageId: 'm1' }, true, true)).toBe(false)
  })
})

describe('dismissWithUndo', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    fetchMock.mockReset()
    useDismissStore.setState({ hidden: new Set() })
    useToastStore.setState({ toasts: [] })
  })
  afterEach(() => { vi.useRealTimers() })

  it('sends a pending dismissal at once, with keepalive, when the page is hidden', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(new Response('{}', { status: 200 })))
    dismissWithUndo('ask:m1', 'Question: Which name?')
    expect(fetchMock).not.toHaveBeenCalled()
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/messages/m1/dismiss')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'POST', keepalive: true })
    expect(useToastStore.getState().toasts).toHaveLength(0)
    // Sent once: the Undo timer is gone.
    await vi.advanceTimersByTimeAsync(DISMISS_UNDO_MS + 10)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('sends on pagehide too', () => {
    fetchMock.mockImplementation(() => Promise.resolve(new Response('{}', { status: 200 })))
    dismissWithUndo('review:r1', 'Review: plan.md')
    window.dispatchEvent(new Event('pagehide'))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/review-requests/r1/decide')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ keepalive: true })
  })

  it('an undone dismissal is never sent by the flush', () => {
    dismissWithUndo('ask:m2', 'Question')
    useToastStore.getState().toasts[0].action!.onClick()
    flushPendingDismissals()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('says nothing when the item was already closed (409)', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ error: 'message already answered or not open' }), { status: 409 })))
    dismissWithUndo('ask:m3', 'Question')
    await vi.advanceTimersByTimeAsync(DISMISS_UNDO_MS + 10)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(useToastStore.getState().toasts.filter(t => t.title === 'Could not dismiss')).toHaveLength(0)
  })

  it('still says why on another failure, and shows the item again', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ error: 'boom' }), { status: 500 })))
    dismissWithUndo('ask:m4', 'Question')
    await vi.advanceTimersByTimeAsync(DISMISS_UNDO_MS + 10)
    await vi.waitFor(() => expect(useToastStore.getState().toasts.some(t => t.title === 'Could not dismiss')).toBe(true))
    expect(useDismissStore.getState().hidden.has('ask:m4')).toBe(false)
  })
})
