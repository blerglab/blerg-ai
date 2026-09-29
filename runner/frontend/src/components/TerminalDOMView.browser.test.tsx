import { createRef } from 'react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { userEvent } from 'vitest/browser'
import * as wsMock from '../test/wsMock'
import TerminalDOMView, { type TerminalHandle } from './TerminalDOMView'
import { SCROLLBACK_WINDOW_MS, MAX_SCROLLBACK_LINES } from '../terminalRenderer'
import type { SessionOutput, HistoryDone, SessionScrollback, FocusGranted } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

// ── Real-layout scroll tests ──────────────────────────────────────────────────
// Run in real Chromium (see vitest.config.ts `browser` project) because scrolling
// depends on real layout — scrollHeight/clientHeight/scrollTop are all 0 in jsdom.
// The wheel is driven by Playwright's real wheel input.
//
// Architecture under test: the live screen is rendered from the PTY byte stream
// (session_output); the SCROLLBACK is sourced from tmux (session_scrollback) and
// rendered into the history region above the live screen. This works for
// full-screen / alt-screen TUIs, which the old byte-stream reconstruction could not.

function output(sessionId: string, text: string): SessionOutput {
  return { type: 'session_output', session_id: sessionId, data: btoa(text), seq: 0 }
}
function historyDone(sessionId: string): HistoryDone {
  return { type: 'history_done', session_id: sessionId }
}
function scrollback(sessionId: string, text: string): SessionScrollback {
  return { type: 'session_scrollback', session_id: sessionId, data: btoa(text) }
}
function focusGranted(sessionId: string): FocusGranted {
  return { type: 'focus_granted', session_id: sessionId }
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
function distanceFromBottom(el: HTMLDivElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight
}
// Wait until scrollHeight stops changing — the harness mounts the terminal at a
// tiny size (clientHeight not laid out yet), then a ResizeObserver grows it; we
// must let that settle before testing scroll, or the resize's auto-pin races us.
async function settle(el: HTMLDivElement): Promise<void> {
  let last = -1, stable = 0
  await poll(() => {
    if (el.scrollHeight === last) stable++
    else { stable = 0; last = el.scrollHeight }
    return stable >= 5
  }, 3000)
}

// Bring a terminal up with a live screen and tmux scrollback applied, scrolled to
// the bottom. Returns the scroll container.
async function withScrollback(container: HTMLElement, sessionId: string, historyLines = 200) {
  wsMock.emit(output(sessionId, 'live screen line\r\n')) // triggers firstChunk + live render
  wsMock.emit(scrollback(sessionId, manyLines(historyLines)))
  const scrollEl = findScrollEl(container)
  await poll(() => scrollEl.scrollHeight > scrollEl.clientHeight)
  await settle(scrollEl)
  await poll(() => distanceFromBottom(scrollEl) <= 1)
  return scrollEl
}

describe('TerminalDOMView — scrolling (real browser)', () => {
  beforeEach(() => { wsMock.resetWsMock() })
  afterEach(() => { SCROLLBACK_WINDOW_MS.value = 30_000 }) // restore default after tests that shrink it

  it('requests scrollback from tmux after history replay, with max_lines', async () => {
    render(<TerminalDOMView sessionId="sess-req" />)
    wsMock.emit(historyDone('sess-req'))
    await waitFor(() =>
      expect(wsMock.send).toHaveBeenCalledWith({
        type: 'request_scrollback', session_id: 'sess-req', max_lines: 4000,
      }),
    )
  })

  it('renders tmux scrollback as a scrollable history region', async () => {
    const { container } = mountTerminal('sess-sb')
    const scrollEl = await withScrollback(container, 'sess-sb')
    expect(scrollEl.scrollHeight).toBeGreaterThan(scrollEl.clientHeight)
    // The oldest scrollback line is present and reachable above the live screen.
    expect(container.textContent).toContain('line 0 ')
  })

  it('auto-follows new output while pinned to the bottom (stick mode)', async () => {
    const { container } = mountTerminal('sess-follow')
    const scrollEl = await withScrollback(container, 'sess-follow')
    wsMock.emit(output('sess-follow', '\r\nmore live output\r\n'))
    await new Promise(r => setTimeout(r, 60))
    expect(distanceFromBottom(scrollEl)).toBeLessThanOrEqual(1)
  })

  it('wheel-up scrolls up into the tmux scrollback', async () => {
    const { container } = mountTerminal('sess-wheel')
    const scrollEl = await withScrollback(container, 'sess-wheel')
    const before = scrollEl.scrollTop
    await userEvent.wheel(scrollEl, { delta: { y: -200 } })
    await poll(() => scrollEl.scrollTop < before - 50)
    expect(scrollEl.scrollTop).toBeLessThan(before)
  })

  it('does NOT snap back to bottom when new output streams in after scrolling up', async () => {
    const { container } = mountTerminal('sess-nosnap')
    const scrollEl = await withScrollback(container, 'sess-nosnap')
    await userEvent.wheel(scrollEl, { delta: { y: -200 } })
    await poll(() => distanceFromBottom(scrollEl) > 50)
    const afterScroll = scrollEl.scrollTop

    // Stream live output (as a working session does). View must hold position.
    for (let i = 0; i < 10; i++) wsMock.emit(output('sess-nosnap', `tick ${i}\r\n`))
    await new Promise(r => setTimeout(r, 200))

    expect(distanceFromBottom(scrollEl)).toBeGreaterThan(40)
    expect(Math.abs(scrollEl.scrollTop - afterScroll)).toBeLessThan(40)
  })

  it('holds position when output streams after a NATIVE scroll-up (no wheel/touch synthetic events)', async () => {
    // Reproduces the mobile snap-back bug: real touchscreen/momentum scrolling
    // produces only a native 'scroll' event (not the synthetic wheel/touch events
    // the old gesture listeners keyed on), so stick mode was never released and
    // streaming output snapped the reader back to the bottom.
    const { container } = mountTerminal('sess-native')
    const scrollEl = await withScrollback(container, 'sess-native')

    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight - 300)
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) > 100)
    const afterScroll = scrollEl.scrollTop

    for (let i = 0; i < 12; i++) wsMock.emit(output('sess-native', `line ${i}\r\n`))
    await new Promise(r => setTimeout(r, 200))

    expect(distanceFromBottom(scrollEl)).toBeGreaterThan(80)
    expect(Math.abs(scrollEl.scrollTop - afterScroll)).toBeLessThan(40)
  })

  it('freezes the live screen while scrolled up so streaming output does not overwrite it', async () => {
    // The live screen always renders tmux's CURRENT screen; re-rendering it as
    // output streams scrolled the reader's lines off and overwrote them ("sheets of
    // text shifting"). While scrolled up the live screen must freeze.
    const { container } = mountTerminal('sess-freeze')
    wsMock.emit(output('sess-freeze', 'IMPORTANT LINE TO READ\r\n'))
    wsMock.emit(scrollback('sess-freeze', manyLines(200)))
    const scrollEl = findScrollEl(container)
    await poll(() => scrollEl.scrollHeight > scrollEl.clientHeight)
    await settle(scrollEl)
    await poll(() => (scrollEl.textContent ?? '').includes('IMPORTANT LINE TO READ'))

    // Scroll up to read.
    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight - 300)
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) > 100)

    // Stream enough output to push the live screen well past that line.
    for (let i = 0; i < 60; i++) wsMock.emit(output('sess-freeze', `noise line ${i}\r\n`))
    await new Promise(r => setTimeout(r, 200))

    // The line the user was reading must still be on screen (frozen, not overwritten).
    expect((scrollEl.textContent ?? '').includes('IMPORTANT LINE TO READ')).toBe(true)
  })

  it('re-follows output after the user scrolls back to the bottom', async () => {
    const { container } = mountTerminal('sess-refollow')
    const scrollEl = await withScrollback(container, 'sess-refollow')
    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight - 300)
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) > 100)
    scrollEl.scrollTop = scrollEl.scrollHeight
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) <= 1)
    for (let i = 0; i < 12; i++) wsMock.emit(output('sess-refollow', `tail ${i}\r\n`))
    await poll(() => distanceFromBottom(scrollEl) <= 1)
    expect(distanceFromBottom(scrollEl)).toBeLessThanOrEqual(1)
  })

  it('still forwards keystrokes to the PTY after the user scrolls up', async () => {
    const { container } = mountTerminal('sess-input')
    const scrollEl = await withScrollback(container, 'sess-input')
    await userEvent.wheel(scrollEl, { delta: { y: -200 } })
    await poll(() => distanceFromBottom(scrollEl) > 50)

    const textarea = container.querySelector('textarea')!
    textarea.focus()
    wsMock.send.mockClear()
    textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input', session_id: 'sess-input', data: btoa('\r'),
    })
  })

  it('focuses the capture textarea on mouse release even when streaming eats the click', async () => {
    // Regression: while a session is RUNNING, renderNow() calls
    // viewportEl.replaceChildren() every animation frame. A mousedown that lands
    // on a row node gets that node torn out before mouseup, so Chromium never
    // fires 'click' — the click-based focus path silently no-ops and the user
    // can't type until output stops. Focus must key off pointerup (which always
    // fires, regardless of target churn), not click.
    const { container } = mountTerminal('sess-press-focus')
    const scrollEl = await withScrollback(container, 'sess-press-focus')
    const textarea = container.querySelector('textarea')!

    textarea.blur()
    expect(document.activeElement).not.toBe(textarea)

    // A real mouse press+release whose click was eaten (no click dispatched).
    scrollEl.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'mouse' }))
    scrollEl.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerType: 'mouse' }))

    expect(document.activeElement).toBe(textarea)
  })

  it('does NOT steal focus on mouse release when a drag selection exists (copy-on-select)', async () => {
    // A focused text control owns the selection in Chromium, so re-focusing the
    // capture textarea while the user drags out page text would empty
    // window.getSelection() and silently kill copy-on-select. A release that
    // ends a selection drag must leave focus alone.
    const { container } = mountTerminal('sess-select-focus')
    const scrollEl = await withScrollback(container, 'sess-select-focus')
    const textarea = container.querySelector('textarea')!

    textarea.blur()
    // Simulate the drag having produced a selection over terminal text.
    const row = scrollEl.querySelector('div div') as HTMLElement
    const sel = window.getSelection()!
    sel.removeAllRanges()
    const range = document.createRange()
    range.selectNodeContents(row)
    sel.addRange(range)
    expect(sel.isCollapsed).toBe(false)

    scrollEl.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'mouse' }))
    scrollEl.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerType: 'mouse' }))

    expect(document.activeElement).not.toBe(textarea)
    sel.removeAllRanges()
  })
})

// ── Scrollback snapshot triggers (real browser) ──────────────────────────────
// These cover the trailing-edge repair mechanism and the additional forced
// snapshot points beyond the initial post-replay request: focus_granted, a
// recalcSize-driven resize, and the 500ms post-replay retry.

function sbRequestCalls(): unknown[] {
  return wsMock.send.mock.calls
    .map(c => c[0])
    .filter(m => (m as { type?: string }).type === 'request_scrollback')
}

describe('TerminalDOMView — scrollback snapshot triggers (real browser)', () => {
  beforeEach(() => { wsMock.resetWsMock() })
  afterEach(() => { SCROLLBACK_WINDOW_MS.value = 30_000 }) // restore default after tests that shrink it

  it('does not repeat the scrollback request within the window while output streams continuously', async () => {
    const { container } = mountTerminal('sess-throttle')
    await withScrollback(container, 'sess-throttle') // triggers one immediate (unforced) request
    wsMock.send.mockClear()

    // ~2s of continuous output is well inside the 30s window — enough to prove
    // suppression without inflating the suite runtime.
    const start = Date.now()
    while (Date.now() - start < 2_000) {
      wsMock.emit(output('sess-throttle', 'tick\r\n'))
      await new Promise(r => setTimeout(r, 200))
    }
    expect(sbRequestCalls()).toHaveLength(0)
  }, 10_000)

  it('fires exactly one trailing scrollback request after an output burst then silence past the window', async () => {
    SCROLLBACK_WINDOW_MS.value = 400
    const { container } = mountTerminal('sess-trailing')
    await withScrollback(container, 'sess-trailing') // triggers one immediate (unforced) request
    wsMock.send.mockClear()

    // Burst of output well inside the (now 400ms) window — each call is
    // suppressed and should (re)arm the trailing one-shot, not send directly.
    for (let i = 0; i < 5; i++) wsMock.emit(output('sess-trailing', `b${i}\r\n`))
    expect(sbRequestCalls()).toHaveLength(0)

    // Silence past the window: the trailing one-shot should fire exactly once.
    await new Promise(r => setTimeout(r, 700))
    expect(sbRequestCalls()).toHaveLength(1)
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'request_scrollback', session_id: 'sess-trailing', max_lines: 4000,
    })
  })

  it('suppresses the trailing fire if the user scrolls up before the window expires', async () => {
    SCROLLBACK_WINDOW_MS.value = 400
    const { container } = mountTerminal('sess-trailing-up')
    const scrollEl = await withScrollback(container, 'sess-trailing-up')
    wsMock.send.mockClear()

    // Burst arms the trailing one-shot while stuck to the bottom.
    for (let i = 0; i < 5; i++) wsMock.emit(output('sess-trailing-up', `b${i}\r\n`))
    expect(sbRequestCalls()).toHaveLength(0)

    // User scrolls up to read BEFORE the trailing timer expires. When it fires
    // it must no-op (an actual request would rebuild historyEl under the reader).
    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight - 300)
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) > 100)

    await new Promise(r => setTimeout(r, 700))
    expect(sbRequestCalls()).toHaveLength(0)
  })

  it('sends a forced scrollback snapshot after focus_granted', async () => {
    const { container } = mountTerminal('sess-focus')
    await withScrollback(container, 'sess-focus')
    wsMock.send.mockClear()

    wsMock.emit(focusGranted('sess-focus'))
    await waitFor(() =>
      expect(wsMock.send).toHaveBeenCalledWith({
        type: 'request_scrollback', session_id: 'sess-focus', max_lines: 4000,
      }),
    )
  })

  it('sends a forced scrollback snapshot after a recalcSize-driven resize_session', async () => {
    const host = document.createElement('div')
    host.style.cssText = 'position:fixed;top:0;left:0;width:800px;height:600px'
    document.body.appendChild(host)
    const { container } = render(<TerminalDOMView sessionId="sess-recalc" />, { container: host })
    await withScrollback(container, 'sess-recalc')
    wsMock.send.mockClear()

    host.style.width = '400px'
    host.style.height = '300px'

    await waitFor(() => {
      const resized = wsMock.send.mock.calls.some(c => (c[0] as { type: string }).type === 'resize_session')
      expect(resized).toBe(true)
    })
    await waitFor(() =>
      expect(wsMock.send).toHaveBeenCalledWith(
        expect.objectContaining({ type: 'request_scrollback', session_id: 'sess-recalc', max_lines: 4000 }),
      ),
    )
  })

  it('retries the post-replay scrollback snapshot ~500ms after the first', async () => {
    render(<TerminalDOMView sessionId="sess-retry" />)
    wsMock.emit(output('sess-retry', 'x\r\n'))
    wsMock.send.mockClear()

    wsMock.emit(historyDone('sess-retry'))
    await waitFor(() => expect(sbRequestCalls()).toHaveLength(1))

    await new Promise(r => setTimeout(r, 600))
    expect(sbRequestCalls()).toHaveLength(2)
  })
})

// ── Diff-append scrollback rendering (real browser) ──────────────────────────
// Consecutive overlapping captures append only their new tail rows to the
// history DOM instead of wholesale-rebuilding it. Non-overlapping captures fall
// back to a full rebuild. The cap is enforced by trimming whole line-groups from
// the top, and neither append nor trim may shift a scrolled-up reader.

// Short, non-wrapping, uniquely-tokenised lines so 1 capture line == 1 DOM
// line-group == 1 rendered row (keeps line-count == row-count == group-count).
function tok(i: number): string { return `[ln ${i}]` }
function tokLines(from: number, to: number): string {
  return Array.from({ length: to - from }, (_, k) => tok(from + k)).join('\n')
}
function historyEl(scrollEl: HTMLElement): HTMLElement {
  return scrollEl.firstElementChild as HTMLElement
}

describe('TerminalDOMView — diff-append scrollback (real browser)', () => {
  beforeEach(() => { wsMock.resetWsMock() })
  afterEach(() => {
    SCROLLBACK_WINDOW_MS.value = 30_000
    MAX_SCROLLBACK_LINES.value = 4000
  })

  it('(a) appends new tail rows without recreating existing history elements', async () => {
    const { container } = mountTerminal('sess-diff-a')
    wsMock.emit(output('sess-diff-a', 'live\r\n'))
    wsMock.emit(scrollback('sess-diff-a', tokLines(0, 20)))
    const scrollEl = findScrollEl(container)
    const hist = historyEl(scrollEl)
    await poll(() => hist.children.length >= 20)
    const before = [...hist.children]

    // Overlapping capture: same 20 lines + 5 new tail lines.
    wsMock.emit(scrollback('sess-diff-a', tokLines(0, 25)))
    await poll(() => hist.children.length >= 25)
    const after = [...hist.children]

    // The first 20 line-groups must be the SAME DOM nodes (appended, not rebuilt).
    for (let i = 0; i < before.length; i++) expect(after[i]).toBe(before[i])
    expect(container.textContent).toContain(tok(24))
  })

  it('(b) rebuilds history when a capture does not overlap the previous', async () => {
    const { container } = mountTerminal('sess-diff-b')
    wsMock.emit(output('sess-diff-b', 'live\r\n'))
    wsMock.emit(scrollback('sess-diff-b', tokLines(0, 20)))
    const scrollEl = findScrollEl(container)
    const hist = historyEl(scrollEl)
    await poll(() => hist.children.length >= 20)
    const before = [...hist.children]

    // Non-overlapping capture (e.g. after a clear): entirely different content.
    const cleared = Array.from({ length: 8 }, (_, i) => `[fresh ${i}]`).join('\n')
    wsMock.emit(scrollback('sess-diff-b', cleared))
    await poll(() => (container.textContent ?? '').includes('[fresh 7]'))

    // Old content gone, new content present, and it was a full rebuild (node 0 replaced).
    expect(container.textContent).not.toContain(tok(0))
    expect(container.textContent).toContain('[fresh 0]')
    expect(hist.children[0]).not.toBe(before[0])
  })

  it('(c) retains dropped-top lines across appends yet caps history at MAX line-groups', async () => {
    MAX_SCROLLBACK_LINES.value = 45
    const { container } = mountTerminal('sess-diff-c')
    wsMock.emit(output('sess-diff-c', 'live\r\n'))
    wsMock.emit(scrollback('sess-diff-c', tokLines(0, 30)))
    const scrollEl = findScrollEl(container)
    const hist = historyEl(scrollEl)
    await poll(() => hist.children.length >= 30)

    // Next capture drops the top 20 lines (tmux history saturated) and adds 20 new
    // ones. Overlap = lines 20..29; append keeps 0..19 that this capture no longer
    // contains. A plain rebuild would drop them.
    wsMock.emit(scrollback('sess-diff-c', tokLines(20, 50)))
    await poll(() => (container.textContent ?? '').includes(tok(49)))

    // Retained a line NO single recent capture contained (proves append, not rebuild).
    expect(container.textContent).toContain(tok(10))
    // Capped: rendered 0..49 (50) trimmed to the last 45 → 5..49.
    expect(hist.children.length).toBeLessThanOrEqual(45)
    expect(container.textContent).not.toContain(tok(4))
    expect(container.textContent).toContain(tok(49))
  })

  it('(e) an empty capture racing a pending non-empty render leaves history empty', async () => {
    // Race: a non-empty capture starts an async render (xterm write callback
    // pending), then an empty capture arrives in the same tick and clears
    // history. The pending callback must see itself superseded and NOT revive
    // the just-cleared scrollback into historyEl.
    const { container } = mountTerminal('sess-diff-e')
    wsMock.emit(output('sess-diff-e', 'live\r\n'))
    const scrollEl = findScrollEl(container)
    await poll(() => (scrollEl.textContent ?? '').includes('live'))
    const hist = historyEl(scrollEl)

    // Non-empty capture immediately followed by an empty one, same tick.
    wsMock.emit(scrollback('sess-diff-e', tokLines(0, 20)))
    wsMock.emit(scrollback('sess-diff-e', ''))

    // Let any pending render callback fire, then assert history stayed empty.
    await new Promise(r => setTimeout(r, 200))
    expect(hist.children.length).toBe(0)
    expect(container.textContent).not.toContain(tok(0))
  })

  it('(d) holds the reading position when appends+trims arrive while scrolled up', async () => {
    MAX_SCROLLBACK_LINES.value = 90
    const { container } = mountTerminal('sess-diff-d')
    wsMock.emit(output('sess-diff-d', 'live\r\n'))
    wsMock.emit(scrollback('sess-diff-d', tokLines(0, 100))) // rebuild slices to last 90 → 10..99
    const scrollEl = findScrollEl(container)
    const hist = historyEl(scrollEl)
    await poll(() => scrollEl.scrollHeight > scrollEl.clientHeight)
    await settle(scrollEl)

    // Scroll up to read (release stick).
    scrollEl.scrollTop = Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight - 300)
    scrollEl.dispatchEvent(new Event('scroll'))
    await poll(() => distanceFromBottom(scrollEl) > 100)

    // Anchor on a mid-history read line that will survive the trim.
    const readLine = tok(60)
    const readEl = [...hist.querySelectorAll('div')].find(d => d.textContent?.trimEnd() === readLine)!
    expect(readEl).toBeTruthy()
    const topBefore = readEl.getBoundingClientRect().top

    // Overlapping capture appends 20 new tail lines → rendered 10..119 (110) →
    // trims 20 from the top back to 90 (30..119). Read line 60 survives.
    wsMock.emit(scrollback('sess-diff-d', tokLines(10, 120)))
    await poll(() => (container.textContent ?? '').includes(tok(119)))
    await new Promise(r => setTimeout(r, 100))

    // Reading position unchanged: the anchor line did not visually move, and we
    // did not snap to the bottom.
    expect(container.textContent).toContain(readLine)
    const topAfter = readEl.getBoundingClientRect().top
    expect(Math.abs(topAfter - topBefore)).toBeLessThan(6)
    expect(distanceFromBottom(scrollEl)).toBeGreaterThan(80)
  })
})

describe('TerminalDOMView — session_scrollback mode_prefix (real browser)', () => {
  beforeEach(() => { wsMock.resetWsMock() })

  it('writes mode_prefix into the live terminal, flipping applicationCursorKeysMode', async () => {
    const ref = createRef<TerminalHandle>()
    const { container } = render(<TerminalDOMView sessionId="sess-mode" ref={ref} />)
    wsMock.emit(output('sess-mode', 'hello\r\n'))
    await poll(() => (findScrollEl(container).textContent ?? '').includes('hello'))
    expect(ref.current?.applicationCursorKeysMode).toBe(false)

    // DECCKM set (application cursor keys) — a mode-sync escape the daemon
    // captures alongside the scrollback text so the live terminal's mode state
    // matches the pane's even though replaying scrollback text alone can't
    // re-derive it.
    const modePrefix = btoa('\x1b[?1h')
    wsMock.emit({
      type: 'session_scrollback', session_id: 'sess-mode', data: btoa(''), mode_prefix: modePrefix,
    } satisfies SessionScrollback)

    await poll(() => ref.current?.applicationCursorKeysMode === true)
    expect(ref.current?.applicationCursorKeysMode).toBe(true)
  })
})

// ── Bracketed-paste wrapping (real browser) ───────────────────────────────────
// tmux 3.2a exposes no format variable for bracketed-paste mode, so it can't
// ride the mode-sync preamble (unlike applicationCursorKeysMode above). The
// only way the client learns it is by observing the raw \x1b[?2004h/l bytes
// in the live stream — and the 256KB replay tail can miss that entirely for
// long-running sessions. See task-6 experiment: a manual PTY-attach test
// showed tmux does NOT mediate bracketed-paste markers — it forwards
// whatever bytes are written to the client's PTY verbatim, regardless of the
// pane's actual mode (an app with the mode off, e.g. `cat`, receives the
// literal `\x1b[200~`/`\x1b[201~` markers as text). So the client must default
// to wrapping whenever it hasn't positively observed the mode this
// connection — Claude Code (bracketed paste on) is the common case; a raw
// non-bracketed target is the rare one.
describe('TerminalDOMView — paste bracketing (real browser)', () => {
  beforeEach(() => { wsMock.resetWsMock() })

  function dispatchPaste(textarea: HTMLTextAreaElement, text: string) {
    const dt = new DataTransfer()
    dt.setData('text/plain', text)
    textarea.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
  }

  it('wraps a paste once bracketed-paste mode is learned from output this connection', async () => {
    const { container } = mountTerminal('sess-paste-on')
    wsMock.emit(output('sess-paste-on', '\x1b[?2004hREADY'))
    await poll(() => (container.textContent ?? '').includes('READY'))
    const textarea = container.querySelector('textarea')!
    textarea.focus()
    wsMock.send.mockClear()
    dispatchPaste(textarea, 'pasted text')
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input', session_id: 'sess-paste-on', data: btoa('\x1b[200~pasted text\x1b[201~'),
    })
  })

  it('wraps a paste even when bracketed-paste mode was never observed this connection (truncated replay)', async () => {
    const { container } = mountTerminal('sess-paste-unknown')
    // No \x1b[?2004h ever arrives this connection — simulating a 256KB-tail
    // replay that misses the enable sequence emitted long before the tail
    // window (or a fresh reconnect mid-session).
    wsMock.emit(output('sess-paste-unknown', 'plain output, no mode signal\r\n'))
    await poll(() => (container.textContent ?? '').includes('plain output'))
    const textarea = container.querySelector('textarea')!
    textarea.focus()
    wsMock.send.mockClear()
    dispatchPaste(textarea, 'pasted text')
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input', session_id: 'sess-paste-unknown', data: btoa('\x1b[200~pasted text\x1b[201~'),
    })
  })

  it('does not wrap a paste once bracketed-paste mode is explicitly turned off this connection', async () => {
    const { container } = mountTerminal('sess-paste-off')
    // Two separate writes (as separate session_output chunks would arrive):
    // mode goes true, is observed, then explicitly goes false again.
    wsMock.emit(output('sess-paste-off', '\x1b[?2004h'))
    await new Promise(r => setTimeout(r, 30)) // let the first write parse before the mode flips off
    wsMock.emit(output('sess-paste-off', '\x1b[?2004lREADY'))
    await poll(() => (container.textContent ?? '').includes('READY'))
    const textarea = container.querySelector('textarea')!
    textarea.focus()
    wsMock.send.mockClear()
    dispatchPaste(textarea, 'pasted text')
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input', session_id: 'sess-paste-off', data: btoa('pasted text'),
    })
  })

  it('re-defaults to wrapping after a reconnect resets the terminal, even if mode was observed pre-reconnect', async () => {
    const { container } = mountTerminal('sess-paste-reconnect')
    // Learn bracketed-paste mode on this (first) connection.
    wsMock.emit(output('sess-paste-reconnect', '\x1b[?2004hREADY'))
    await poll(() => (container.textContent ?? '').includes('READY'))

    // Simulate a WS reconnect: onOpen fires again (re-subscribe), then the
    // first post-reconnect chunk arrives without the enable marker — e.g. the
    // daemon's 256KB replay tail missed it. The component resets the live
    // terminal on this first chunk, wiping terminal.modes.bracketedPasteMode;
    // the connection-scoped latch must be wiped alongside it.
    wsMock.flushOpen()
    wsMock.emit(output('sess-paste-reconnect', 'post-reconnect output, no mode signal\r\n'))
    await poll(() => (container.textContent ?? '').includes('post-reconnect output'))

    const textarea = container.querySelector('textarea')!
    textarea.focus()
    wsMock.send.mockClear()
    dispatchPaste(textarea, 'pasted text')
    expect(wsMock.send).toHaveBeenCalledWith({
      type: 'send_input', session_id: 'sess-paste-reconnect', data: btoa('\x1b[200~pasted text\x1b[201~'),
    })
  })
})
