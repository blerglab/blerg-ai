import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render } from '@testing-library/react'
import { cdp, userEvent } from 'vitest/browser'
import * as wsMock from '../test/wsMock'
import TerminalDOMView from './TerminalDOMView'
import type { SessionOutput, SessionScrollback } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

function output(sessionId: string, text: string): SessionOutput {
  return { type: 'session_output', session_id: sessionId, data: btoa(text), seq: 0 }
}
function scrollback(sessionId: string, text: string): SessionScrollback {
  return { type: 'session_scrollback', session_id: sessionId, data: btoa(text) }
}
function manyLines(n: number): string {
  return Array.from({ length: n }, (_, i) => `line ${i} ${'x'.repeat(40)}`).join('\n')
}
function mountTerminal(sessionId: string) {
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;inset:0;display:flex;flex-direction:column'
  const region = document.createElement('div')
  region.style.cssText = 'position:relative;flex:1;overflow:hidden;min-height:0'
  host.appendChild(region)
  document.body.appendChild(host)
  const result = render(<TerminalDOMView sessionId={sessionId} />, { container: region })
  return { ...result, host }
}
function findScrollEl(root: ParentNode): HTMLDivElement {
  const el = [...root.querySelectorAll('div')].find(d => getComputedStyle(d).overflowY === 'auto')
  if (!el) throw new Error('scroll container not found')
  return el as HTMLDivElement
}
async function poll(fn: () => boolean, timeout = 2000): Promise<void> {
  const start = performance.now()
  while (!fn()) {
    if (performance.now() - start > timeout) throw new Error('poll timed out')
    await new Promise(r => requestAnimationFrame(() => r(null)))
  }
}
function distFromBottom(el: HTMLDivElement) {
  return el.scrollHeight - el.scrollTop - el.clientHeight
}
// Wait for the harness's mount-time resize to settle before testing scroll.
async function settle(el: HTMLDivElement): Promise<void> {
  let last = -1, stable = 0
  await poll(() => {
    if (el.scrollHeight === last) stable++
    else { stable = 0; last = el.scrollHeight }
    return stable >= 5
  }, 3000)
}
async function withScrollback(container: HTMLElement, sessionId: string, n = 200) {
  wsMock.emit(output(sessionId, 'live\r\n'))
  wsMock.emit(scrollback(sessionId, manyLines(n)))
  const scrollEl = findScrollEl(container)
  await poll(() => scrollEl.scrollHeight > scrollEl.clientHeight)
  await settle(scrollEl)
  await poll(() => distFromBottom(scrollEl) <= 1)
  return scrollEl
}

describe('TerminalDOMView — mobile touch scroll (real touch via CDP)', () => {
  beforeEach(() => { wsMock.resetWsMock() })

  it('a touch-drag downward scrolls up into the tmux scrollback', async () => {
    const { container } = mountTerminal('sess-touch')
    const scrollEl = await withScrollback(container, 'sess-touch')
    const before = scrollEl.scrollTop
    expect(before).toBeGreaterThan(50)

    const client = cdp() as unknown as { send(method: string, params?: object): Promise<unknown> }
    await client.send('Emulation.setTouchEmulationEnabled', { enabled: true, maxTouchPoints: 5 })
    const rect = scrollEl.getBoundingClientRect()
    const x = Math.round(rect.left + rect.width / 2)
    const yStart = Math.round(rect.top + rect.height * 0.2)
    const yEnd = Math.round(rect.top + rect.height * 0.85)
    await client.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: yStart }] })
    for (let y = yStart; y <= yEnd; y += 24) {
      await client.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] })
    }
    await client.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })

    await poll(() => scrollEl.scrollTop < before - 30, 2000)
    expect(scrollEl.scrollTop).toBeLessThan(before)
  })

  // The real-world failing case: a session ACTIVELY STREAMING output. With tmux
  // scrollback present, scrolling up must hold even as live output keeps arriving.
  it('holds scroll position while output streams continuously', async () => {
    const { container } = mountTerminal('sess-stream')
    const scrollEl = await withScrollback(container, 'sess-stream')
    await userEvent.wheel(scrollEl, { delta: { y: -300 } })
    await poll(() => distFromBottom(scrollEl) > 50)

    let i = 1000
    const streamId = setInterval(() => wsMock.emit(output('sess-stream', `stream ${i++}\r\n`)), 15)
    try {
      await new Promise(r => setTimeout(r, 500))
      expect(distFromBottom(scrollEl)).toBeGreaterThan(40)
    } finally {
      clearInterval(streamId)
    }
  })

  // Closest mimic of a live Claude session: the status line redraws in place
  // (carriage-return + erase, no newline) constantly. Scroll-up must still hold.
  it('holds scroll position while a status line redraws in place (spinner)', async () => {
    const { container } = mountTerminal('sess-spin')
    const scrollEl = await withScrollback(container, 'sess-spin')
    await userEvent.wheel(scrollEl, { delta: { y: -300 } })
    await poll(() => distFromBottom(scrollEl) > 50)

    let i = 0
    const spinId = setInterval(() => wsMock.emit(output('sess-spin', `\rEffecting... ${i++}s\x1b[K`)), 15)
    try {
      await new Promise(r => setTimeout(r, 500))
      expect(distFromBottom(scrollEl)).toBeGreaterThan(40)
    } finally {
      clearInterval(spinId)
    }
  })
})
