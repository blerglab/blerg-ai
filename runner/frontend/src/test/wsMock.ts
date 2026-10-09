// Mock implementation of the `../ws` module for component tests.
//
// Usage in a test file:
//
//   import * as wsMock from '../test/wsMock'
//   vi.mock('../ws', () => import('../test/wsMock'))
//
// `vi.mock`'s factory and the direct import resolve to the SAME module
// instance, so the test drives components through the same bus the component
// imported. Components only consume `send` / `onMessage` / `onOpen`; the `emit`
// / `flushOpen` / `setSocketOpen` / `resetWsMock` helpers are test-only controls.

import { vi } from 'vitest'
import type { BrowserMessage, ServerMessage } from '../types'

const messageHandlers = new Map<string, Set<(msg: ServerMessage) => void>>()
const openHandlers = new Set<() => void>()
let socketOpen = true

// ── Mocked module surface (what components import) ─────────────────────────────

/** Spy for outbound messages. Assert on `send.mock.calls` in tests. */
export const send = vi.fn<(msg: BrowserMessage) => boolean | void>()

export function onMessage<T extends ServerMessage>(
  type: T['type'],
  handler: (msg: T) => void,
): () => void {
  if (!messageHandlers.has(type)) messageHandlers.set(type, new Set())
  const set = messageHandlers.get(type)!
  set.add(handler as (msg: ServerMessage) => void)
  return () => { set.delete(handler as (msg: ServerMessage) => void) }
}

export function onOpen(handler: () => void): () => void {
  openHandlers.add(handler)
  // Mirror the real module: fire immediately if the socket is already open.
  if (socketOpen) handler()
  return () => { openHandlers.delete(handler) }
}

const closeHandlers = new Set<() => void>()

export function onClose(handler: () => void): () => void {
  closeHandlers.add(handler)
  return () => { closeHandlers.delete(handler) }
}

/** Whether the mocked socket is open (see setSocketOpen). */
export function connected(): boolean {
  return socketOpen
}

// ── Test-only controls ────────────────────────────────────────────────────────

/** Dispatch a server→browser message to all registered handlers for its type. */
export function emit(msg: ServerMessage): void {
  messageHandlers.get(msg.type)?.forEach(h => h(msg))
}

/** Re-fire all onOpen handlers, simulating a (re)connection. */
export function flushOpen(): void {
  openHandlers.forEach(h => h())
}

/** Control whether onOpen fires immediately on registration (socket state). Going from open to
 *  closed fires the onClose handlers, as a real drop would. */
export function setSocketOpen(open: boolean): void {
  const dropped = socketOpen && !open
  socketOpen = open
  if (dropped) closeHandlers.forEach(h => h())
}

/** Reset call history and socket state. Call in beforeEach. */
export function resetWsMock(): void {
  send.mockClear()
  socketOpen = true
}

/** Number of currently-registered handlers for a message type (for assertions). */
export function handlerCount(type: ServerMessage['type']): number {
  return messageHandlers.get(type)?.size ?? 0
}
