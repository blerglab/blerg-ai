import { describe, it, expect, vi } from 'vitest'
import { render } from '@testing-library/react'
import * as wsMock from '../test/wsMock'
import TerminalDOMView from './TerminalDOMView'
import type { SessionOutput, SessionScrollback } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

// ── Selection geometry ────────────────────────────────────────────────────────
// Real Chromium, because this is about what a selection PAINTS and what it
// yields as text — both need real layout.
//
// Regression: renderRow emitted one character per COLUMN, padding every short
// line with spaces out to the full terminal width. Those spaces are invisible
// but selectable, so dragging from the middle of one line to the middle of the
// next ran the selection through ~50 columns of padding: both rows highlighted
// edge-to-edge (reported as "it selects the whole frame") and the copied text
// was mostly whitespace.

function output(sessionId: string, text: string): SessionOutput {
  return { type: 'session_output', session_id: sessionId, data: btoa(text), seq: 0 }
}
function scrollback(sessionId: string, text: string): SessionScrollback {
  return { type: 'session_scrollback', session_id: sessionId, data: btoa(text) }
}
function findScrollEl(root: ParentNode): HTMLDivElement {
  const el = [...root.querySelectorAll('div')].find(d => getComputedStyle(d).overflowY === 'auto')
  if (!el) throw new Error('scroll container not found')
  return el as HTMLDivElement
}
async function poll(fn: () => boolean, timeout = 3000): Promise<void> {
  const start = performance.now()
  while (!fn()) {
    if (performance.now() - start > timeout) throw new Error('poll timed out')
    await new Promise(r => requestAnimationFrame(() => r(null)))
  }
}

describe('terminal selection geometry', () => {
  it('a two-line selection covers only the dragged characters, not the full rows', async () => {
    const host = document.createElement('div')
    host.style.cssText = 'position:fixed;inset:0;display:flex;flex-direction:column'
    const region = document.createElement('div')
    region.style.cssText = 'position:relative;flex:1;overflow:hidden;min-height:0'
    host.appendChild(region)
    document.body.appendChild(host)
    const { container } = render(<TerminalDOMView sessionId="sess-sel" />, { container: region })

    // Short lines — most of each row is empty columns.
    wsMock.emit(output('sess-sel', 'AAAA\r\nBBBB\r\nCCCC\r\n'))
    wsMock.emit(scrollback('sess-sel', 'hist one\nhist two'))
    const scrollEl = findScrollEl(container)
    await poll(() => scrollEl.scrollHeight > 0)
    await new Promise(r => setTimeout(r, 400))

    const findRow = (needle: string) =>
      [...scrollEl.querySelectorAll('div')].find(d =>
        d.querySelector('div') === null && (d.textContent ?? '').includes(needle),
      ) as HTMLElement
    const rowA = findRow('AAAA')
    const rowB = findRow('BBBB')

    // The row itself must no longer carry trailing padding.
    expect(rowA.textContent).toBe('AAAA')

    // Select mid-"AAAA" → mid-"BBBB", i.e. exactly what a small two-line drag
    // produces. A Range is used instead of a synthetic mouse drag because
    // CDP drags are flaky here; the resulting selection is identical.
    const firstText = (el: HTMLElement) =>
      document.createTreeWalker(el, NodeFilter.SHOW_TEXT).nextNode() as Text
    const range = document.createRange()
    range.setStart(firstText(rowA), 2)
    range.setEnd(firstText(rowB), 2)
    const sel = window.getSelection()!
    sel.removeAllRanges()
    sel.addRange(range)

    // Text: "AA\nBB" — no run of padding spaces.
    expect(sel.toString()).toBe('AA\nBB')

    // Paint: neither highlight rectangle spans the row. Before the fix the first
    // rect was 384px of a 402px row; now it is only as wide as "AA".
    const rowWidth = rowA.getBoundingClientRect().width
    const widest = Math.max(...[...range.getClientRects()].map(r => r.width))
    expect(widest).toBeLessThan(rowWidth / 2)
  })
})
