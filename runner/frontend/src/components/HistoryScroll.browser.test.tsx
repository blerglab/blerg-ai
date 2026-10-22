// Scrolling up into the older part of a long transcript, in a real Chromium: a session opened from
// its end (the tail page), its history paged in as the reader scrolls up. Every row has its own
// height, some size late (a card that grows after it mounts, a Mermaid diagram), and the server's
// pages overlap by one event. While a page loads the top says so; when it lands the row the reader
// was looking at stays where it was on screen; nothing is drawn twice or left out; and the very top
// says the session begins there.
import { useEffect, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ChatView, createCardRegistry, defineCard, type AgentEvent, type CardProps } from '@blerglab/chat'
import { useTranscriptStore } from '../../../../packages/chat/src/hooks/useTranscriptStore'
import { inputClock } from '../../../../packages/chat/src/model/clock'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import type { OlderPage } from '../../../../packages/chat/src/transport/types'
import '@blerglab/chat/blerg.css'

const SESSION = 'history-scroll'
const TOTAL = 2000
const TAIL = 200

// A card that draws small and grows a moment after it mounts (an image or a chart sizing late).
function LateCard({ data }: CardProps) {
  const [h, setH] = useState(14)
  useEffect(() => {
    const t = setTimeout(() => setH(Number(data.h) || 120), 30 + (Number(data.d) || 0))
    return () => clearTimeout(t)
  }, [data.h, data.d])
  return <div className="late-card" data-ready={h === (Number(data.h) || 120)} style={{ height: h, background: 'currentColor', opacity: 0.1 }} />
}
const cards = createCardRegistry([defineCard('late', LateCard)])

const words = 'The quick brown fox jumps over the lazy dog while the build runs and the tests pass. '
function history(): AgentEvent[] {
  const out: AgentEvent[] = []
  const at = Date.parse('2026-10-20T08:00:00Z')
  for (let i = 0; out.length < TOTAL; i++) {
    const seq = out.length + 1
    const ts = new Date(at + seq * 1000).toISOString()
    const base = { type: 'agent_event' as const, session_id: SESSION, client_event_id: `ev-${seq}`, seq, ts }
    const marker = `[#${seq}]`
    switch (i % 6) {
      case 0:
        out.push({ ...base, kind: 'user_message', payload: { text: `${marker} ${words.repeat(1 + (i % 5))}`, source: 'chat' } })
        break
      case 1: {
        let text = `${marker} ${words.repeat(1 + ((i * 7) % 9))}`
        if (i % 4 === 1) text += `\n\n\`\`\`card:late\n{"h": ${80 + ((i * 13) % 200)}, "d": ${(i * 17) % 120}}\n\`\`\`\n`
        if (i % 12 === 7) text += '\n\n```ts\n' + Array.from({ length: 8 + (i % 15) }, (_, k) => `const v${k} = ${k} * 2`).join('\n') + '\n```\n'
        if (i % 150 === 1) text += '\n\n```mermaid\nflowchart TD\n  A[Start] --> B[Build]\n  B --> C[Test]\n  C --> D[Ship]\n```\n'
        out.push({ ...base, kind: 'assistant_text', payload: { text, done: true } })
        break
      }
      case 2:
        out.push({ ...base, kind: 'tool_call', payload: { tool: 'Bash', call_id: `c${seq}`, input: { command: `make step-${seq}` } } })
        break
      case 3:
        out.push({ ...base, kind: 'tool_result', payload: { call_id: `c${seq - 1}`, output: 'ok', is_error: false, duration_ms: 3 } })
        break
      case 4:
        out.push({ ...base, kind: 'assistant_text', payload: { text: `${marker} ${words.repeat(1 + (i % 3))}`, done: true } })
        break
      default:
        out.push({ ...base, kind: 'turn_done', payload: { stop_reason: 'end_turn', model: 'm', usage: {} } })
    }
  }
  return out
}
const markersOf = (events: AgentEvent[]) => events.flatMap(e => {
  const text = (e.payload as { text?: string }).text
  const m = text ? /^\[#(\d+)\]/.exec(text) : null
  return m ? [Number(m[1])] : []
})

async function frames(n = 2) {
  for (let i = 0; i < n; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
}
// fn's answer as the next frame is painted: a new ResizeObserver's first callback comes after every
// older observer's in that frame (the transcript's included), so it sees what the reader sees.
function atPaint<T>(fn: () => T): Promise<T> {
  return new Promise(resolve => {
    const probe = document.createElement('div')
    document.body.appendChild(probe)
    const ro = new ResizeObserver(() => {
      ro.disconnect()
      probe.remove()
      resolve(fn())
    })
    ro.observe(probe)
  })
}
const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))
// Every late card drawn has grown and every diagram has drawn: the sizes that move rows are settled.
// Waiting on that state, not on a time, holds however slowly a loaded machine runs.
async function sized(timeline: HTMLElement) {
  await waitFor(() => {
    expect(timeline.querySelectorAll('.late-card[data-ready="false"]').length).toBe(0)
    expect([...timeline.querySelectorAll('.visual-block')].every(b => b.querySelector('svg, img, canvas, .md-card-note'))).toBe(true)
  }, { timeout: 30_000 })
  await frames(2)
}
// The page in flight has gone in (the timeline no longer hides its scrollbar thumb for it).
// Waits on a condition frame by frame (not on timers, which a test may have faked).
async function inFrames(ok: () => boolean, max = 3000) {
  for (let i = 0; i < max && !ok(); i++) await frames(1)
  expect(ok()).toBe(true)
}
async function landed(timeline: HTMLElement) {
  await waitFor(() => expect(timeline.classList.contains('backfilling')).toBe(false), { timeout: 30_000 })
}

// The first message card wholly in view, by its marker, and where it sits on screen.
function firstVisible(timeline: HTMLElement): { marker: string; top: number } {
  const top = timeline.getBoundingClientRect().top
  for (const card of timeline.querySelectorAll<HTMLElement>('.agent-card')) {
    const r = card.getBoundingClientRect()
    if (r.top < top || r.height === 0) continue
    const m = /\[#\d+\]/.exec(card.textContent ?? '')
    if (m) return { marker: m[0], top: r.top }
  }
  throw new Error('no message card in view')
}
function topOf(timeline: HTMLElement, marker: string): number {
  for (const card of timeline.querySelectorAll<HTMLElement>('.agent-card')) {
    if ((card.textContent ?? '').includes(marker)) return card.getBoundingClientRect().top
  }
  throw new Error(`${marker} is not drawn`)
}
function inView(timeline: HTMLElement, el: Element | null): boolean {
  if (!el) return false
  const v = timeline.getBoundingClientRect()
  const r = el.getBoundingClientRect()
  return r.bottom > v.top && r.top < v.bottom && r.height > 0
}
// The whole element is in the timeline's view (not a sliver of it).
function wholly(timeline: HTMLElement, el: Element | null): boolean {
  if (!el) return false
  const v = timeline.getBoundingClientRect()
  const r = el.getBoundingClientRect()
  return r.height > 0 && r.top >= v.top - 0.5 && r.bottom <= v.bottom + 0.5
}
const markerCount = (timeline: HTMLElement) => (timeline.textContent ?? '').match(/\[#\d+\]/g)?.length ?? 0
// The reader jumps to the very top in one move (the Home key).
async function home(timeline: HTMLElement) {
  timeline.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }))
  timeline.scrollTop = 0
  await frames(2)
}
// Where `marker` sits relative to `top` in every frame whose layout changed, as painted: a
// ResizeObserver made after the transcript's own runs after it in the same frame, so it sees the
// view as the transcript left it (sampling from a timer could land between a late card's render and
// that frame's correction, which the reader never sees).
function watchDrift(timeline: HTMLElement, marker: string, top: number) {
  const samples: number[] = []
  const ro = new ResizeObserver(() => { samples.push(topOf(timeline, marker) - top) })
  const observeAll = () => { for (const el of timeline.children) ro.observe(el) }
  observeAll()
  ro.observe(timeline)
  const mo = new MutationObserver(observeAll)
  mo.observe(timeline, { childList: true })
  return { samples, stop: () => { ro.disconnect(); mo.disconnect() } }
}

// The transcript's input clock, stopped at its present time and moved on only by the test.
function handClock() {
  const real = inputClock.now
  const clock = { t: real(), restore: () => { inputClock.now = real } }
  inputClock.now = () => clock.t
  return clock
}
// A small page has landed and been drawn (the stream that follows starts right after).
async function landedSmall(firstSeq: number) {
  await waitFor(() => expect(useTranscriptStore.getState().sessions[SESSION].events[0].seq).toBe(firstSeq), { interval: 5 })
  await frames(1)
}
// The reader scrolling up by `by` px: the wheel says so, then the view moves.
async function scrollUp(timeline: HTMLElement, by: number) {
  timeline.dispatchEvent(new WheelEvent('wheel', { deltaY: -by, bubbles: true }))
  timeline.scrollTop = Math.max(0, timeline.scrollTop - by)
  await frames(2)
}

function setup() {
  const all = history()
  const fake = createFakeTransport({ meta: { status: 'idle' } })
  const requests: number[] = []
  // Where the view was when each page was asked for (0: the reader was at the very top).
  const askedFrom: number[] = []
  const askedAt: number[] = []
  const waiting: Array<(page?: OlderPage) => void> = []
  // The server's older page: up to `limit` events before beforeSeq, and (as a server whose pages
  // overlap) the event at beforeSeq again. It waits until the test lets it go.
  fake.transport.loadOlder = (_sid, beforeSeq, limit) => {
    requests.push(beforeSeq)
    askedFrom.push(document.querySelector('.agent-timeline')?.scrollTop ?? -1)
    askedAt.push(performance.now())
    return new Promise<OlderPage>(resolve => {
      waiting.push(override => {
        if (override) { resolve(override); return }
        const end = all.findIndex(e => e.seq === beforeSeq)
        const from = Math.max(0, end - limit)
        const events = all.slice(from, end + 1)
        resolve({ events, hasOlder: from > 0, firstSeq: all[from].seq ?? 0 })
      })
    })
  }
  const release = (override?: OlderPage) => { const w = waiting.shift(); if (!w) throw new Error('no page is waiting'); w(override) }
  return { all, fake, requests, askedFrom, askedAt, waiting, release }
}

async function open(width: number, height: number) {
  await page.viewport(width, height)
  const s = setup()
  const host = document.createElement('div')
  host.style.cssText = `position:fixed;inset:0;width:${width}px;height:${height}px;display:flex;flex-direction:column`
  document.body.appendChild(host)
  render(<ChatView session={SESSION} transport={s.fake.transport} meta={{ status: 'idle', engine: 'claude', model: 'm', started_at: '2026-10-20T08:00:00Z' }} cards={cards} />, { container: host })
  const tail = s.all.slice(-TAIL)
  for (const e of tail) s.fake.emit(e)
  s.fake.replayDone({ lastSeq: tail[tail.length - 1].seq, hasMore: false, older: true, hasOlder: true, firstSeq: tail[0].seq })
  const timeline = await waitFor(() => {
    const t = host.querySelector<HTMLElement>('.agent-timeline:not(.loading)')
    if (!t || t.scrollHeight <= t.clientHeight) throw new Error('not drawn yet')
    return t
  })
  // The page asked for right after the tail: the reader is at the end, so it simply lands.
  await waitFor(() => expect(s.waiting.length).toBe(1))
  // Following the end: the page lands above, and is not drawn until the reader goes up to it (a
  // session opens drawing only its newest rows).
  const opened = markerCount(timeline)
  s.release()
  await waitFor(() => expect(useTranscriptStore.getState().sessions[SESSION].events.length).toBeGreaterThan(TAIL))
  await frames(2)
  expect(markerCount(timeline)).toBe(opened)
  // Following: the end stays in view while the rows above size late.
  await waitFor(() => expect(timeline.scrollHeight - timeline.clientHeight - timeline.scrollTop).toBeLessThanOrEqual(2))
  await sized(timeline)
  expect(await atPaint(() => timeline.scrollHeight - timeline.clientHeight - timeline.scrollTop)).toBeLessThanOrEqual(2)
  return { ...s, host, timeline }
}

// Watches for the loading pill: when it came in, and how much the transcript's height and its first
// row moved because of it (measured with the pill and again with it taken out, in the same task).
function watchPill(timeline: HTMLElement) {
  const seen: Array<{ at: number; height: number; row: number }> = []
  const mo = new MutationObserver(() => {
    // The loader and whatever holds it in the timeline (the dock; an inline row is its own).
    const pill = timeline.querySelector<HTMLElement>('[data-testid="older-loading"]')
    const dock = pill?.closest<HTMLElement>('.agent-timeline > *') ?? null
    if (!dock || dock.dataset.seen) return
    dock.dataset.seen = '1'
    const row = timeline.querySelector('.agent-card')!
    const withIt = { h: timeline.scrollHeight, top: row.getBoundingClientRect().top }
    dock.style.display = 'none'
    const without = { h: timeline.scrollHeight, top: row.getBoundingClientRect().top }
    dock.style.display = ''
    seen.push({ at: performance.now(), height: withIt.h - without.h, row: withIt.top - without.top })
  })
  mo.observe(timeline, { childList: true })
  return { seen, stop: () => mo.disconnect() }
}

// Jumps to the very top (Home) until a page is asked for. A page slower than 300 ms puts the pill
// over the top of the view without moving anything; when the page lands, the row the reader looked
// at does not move (0 px: less than one).
async function loadOnePage(o: Awaited<ReturnType<typeof open>>) {
  const { timeline } = o
  const pill = watchPill(timeline)
  for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
  expect(o.waiting.length).toBe(1)
  // Asked for on the way up (ahead of the view): to the top, where the pill is to be seen.
  if (o.askedFrom[o.askedFrom.length - 1] !== 0) await home(timeline)
  // The page is slow (it waits for the test): the pill comes, at the earliest 300 ms after the
  // request (later when drawing held the page up).
  const line = await waitFor(() => {
    const el = timeline.querySelector('[data-testid="older-loading"]')
    if (!el) throw new Error('no pill yet')
    return el
  }, { timeout: 30_000 })
  await frames(2)
  pill.stop()
  expect(line.textContent).toContain('Loading earlier messages')
  expect(wholly(timeline, line), `pill at ${line?.getBoundingClientRect().top} in a view from ${timeline.getBoundingClientRect().top}`).toBe(true)
  expect(pill.seen).toHaveLength(1)
  expect(pill.seen[0].at - o.askedAt[o.askedAt.length - 1]).toBeGreaterThanOrEqual(290)
  expect(pill.seen[0], JSON.stringify(pill.seen)).toMatchObject({ height: 0, row: 0 })
  const before = firstVisible(timeline)
  const watch = watchDrift(timeline, before.marker, before.top)
  o.release()
  // Late sizes above the view settle over the next frames: the row stays put through all of them.
  await landed(timeline)
  await sized(timeline)
  const last = await atPaint(() => topOf(timeline, before.marker) - before.top)
  watch.stop()
  const drift = [...watch.samples, last]
  expect(watch.samples.length).toBeGreaterThan(0)
  for (const d of drift) expect(Math.abs(d), `${before.marker} drifted ${drift.join(', ')}`).toBeLessThan(1)
  expect(timeline.scrollTop).toBeGreaterThan(0)
}

describe('scrolling up into older history', () => {
  beforeEach(() => {
    useTranscriptStore.setState({ sessions: {} })
  })
  afterEach(async () => {
    await page.viewport(1280, 800)
  })

  it.each([[390, 844, 500], [3840, 2160, 1200]] as const)('from a fresh load at %i × %i: loading line, no jump, no duplicates, the beginning', async (width, height, step) => {
    const o = await open(width, height)
    const pages = Math.ceil((TOTAL - TAIL) / 200) - 1 // one landed already, behind the tail
    for (let p = 0; p < pages; p++) await loadOnePage(o)
    const { timeline } = o
    // Every held row drawn up to the start of the session.
    for (let i = 0; i < 100 && timeline.scrollTop > 0; i++) await scrollUp(timeline, step)
    await frames(3)
    const start = timeline.querySelector('[data-testid="history-start"]')
    expect(start?.textContent).toContain('Beginning of the session')
    expect(inView(timeline, start)).toBe(true)
    expect(timeline.querySelector('[data-testid="older-loading"]')).toBeNull()
    // Exactly the session's messages, in order: no page boundary drew one twice or lost one.
    const drawn = [...(timeline.textContent ?? '').matchAll(/\[#(\d+)\]/g)].map(m => Number(m[1]))
    expect(drawn).toEqual(markersOf(o.all))
    // Each older page was asked for once.
    expect(new Set(o.requests).size).toBe(o.requests.length)
  }, 240_000)

  it('reveals rows already held above the window without moving the row in view', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    // Up to two view heights below the top of what is drawn (outside the 1.5-view reach, so nothing
    // is revealed yet); the page behind the tail is held, not drawn.
    const stopAt = 2 * timeline.clientHeight
    for (let i = 0; i < 2000 && timeline.scrollTop > stopAt; i++) {
      await scrollUp(timeline, Math.min(100, timeline.scrollTop - stopAt + 1))
    }
    const drawnBefore = markerCount(timeline)
    timeline.dispatchEvent(new WheelEvent('wheel', { deltaY: -300, bubbles: true }))
    timeline.scrollTop = 600
    const before = firstVisible(timeline)
    const watch = watchDrift(timeline, before.marker, before.top)
    await waitFor(() => expect(markerCount(timeline)).toBeGreaterThan(drawnBefore), { timeout: 30_000 })
    await sized(timeline)
    const last = await atPaint(() => topOf(timeline, before.marker) - before.top)
    watch.stop()
    expect(markerCount(timeline)).toBeGreaterThan(drawnBefore)
    const drift = [...watch.samples, last]
    for (const d of drift) expect(Math.abs(d), `${before.marker} drifted ${drift.join(', ')}`).toBeLessThanOrEqual(2)
  }, 120_000)

  it('after a page that moved the top out of reach, sitting still asks for nothing more', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    o.release()
    const asked = o.requests.length
    await sleep(1500)
    expect(timeline.scrollTop).toBeGreaterThan(1.5 * timeline.clientHeight)
    expect(o.requests.length).toBe(asked)
  }, 120_000)

  // A page that adds nothing above the view (events that draw no row), or so little the top is still
  // within reach (a few tool calls folding into one row), leaves the reader at the top: the next is
  // asked for without a scroll they could not make (a scrollbar at 0, keys outside the timeline).
  it.each([
    ['events that draw no row', (seq: number) => ({ kind: 'status_changed', payload: { status: 'idle' }, seq })],
    ['a few tool calls in one row', (seq: number) => (seq % 2
      ? { kind: 'tool_call', payload: { tool: 'Read', call_id: `f${seq}`, input: { file_path: 'a.ts' } }, seq }
      : { kind: 'tool_result', payload: { call_id: `f${seq - 1}`, output: 'ok', is_error: false, duration_ms: 1 }, seq })],
  ] as const)('at the top, a page of %s is followed by the next without any input', async (_name, make) => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    if (o.askedFrom[o.askedFrom.length - 1] !== 0) await home(timeline)
    const before = o.requests[o.requests.length - 1]
    const page = Array.from({ length: 6 }, (_, i) => {
      const seq = before - 6 + i
      return { type: 'agent_event', session_id: SESSION, client_event_id: `ev-${seq}`, ts: o.all[seq - 1].ts, ...make(seq) } as AgentEvent
    })
    const asked = o.requests.length
    o.release({ events: page, hasOlder: true, firstSeq: page[0].seq ?? 0 })
    // No input at all: the next page is asked for (after the 400 ms gap).
    await waitFor(() => expect(o.requests.length).toBe(asked + 1), { timeout: 30_000 })
  }, 120_000)

  it('Home pressed as a page lands does not leave the reader stuck at the top', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    const asked = o.requests.length
    // Home, and the page lands before Home's scroll event.
    timeline.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }))
    timeline.scrollTop = 0
    o.release()
    await landed(timeline)
    // Either the page pushed the top out of reach, or the next page is asked for: never at the top
    // with nothing coming. (Waited on, not timed: the next request waits out its 400 ms gap.)
    await waitFor(() => {
      const sentinel = timeline.querySelector('[data-testid="transcript-more"]')
      const reach = sentinel ? timeline.getBoundingClientRect().top - sentinel.getBoundingClientRect().top : Infinity
      const stuck = reach <= 1.5 * timeline.clientHeight && o.requests.length === asked && o.waiting.length === 0
      expect(stuck, `top ${reach}px above the view, ${o.requests.length - asked} more requests`).toBe(false)
    }, { timeout: 30_000 })
  }, 120_000)

  // A page that lands in the frame between a jump (Home, the scrollbar) and that jump's scroll event:
  // the place kept is the row the reader jumped to, not where they were before.
  it('a page landing just after a jump keeps the row jumped to in place', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    // Down two and a half views (its scroll events seen), the page still on its way.
    timeline.scrollTop += 2.5 * timeline.clientHeight
    await frames(3)
    // Home, and the page lands in the same task, before Home's scroll event.
    timeline.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }))
    timeline.scrollTop = 0
    const before = firstVisible(timeline)
    const drawn = markerCount(timeline)
    await act(async () => { o.release() })
    // Measured as painted, once the page is drawn and every late size in it has settled (a timed
    // read could land between a late card's render and the frame's correction).
    await waitFor(() => expect(markerCount(timeline)).toBeGreaterThan(drawn), { timeout: 30_000 })
    await sized(timeline)
    const moved = await atPaint(() => topOf(timeline, before.marker) - before.top)
    expect(Math.abs(moved), `${before.marker} moved ${moved}`).toBeLessThan(1)
  }, 120_000)

  it('dragging the scrollbar up after a page lands asks for the next one', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    o.release()
    await landed(timeline)
    await sized(timeline)
    const asked = o.requests.length
    expect(timeline.scrollTop).toBeGreaterThan(1.5 * timeline.clientHeight)
    // Up by the scrollbar: scroll events, no wheel, key or touch.
    for (let i = 0; i < 200 && o.requests.length === asked; i++) {
      timeline.scrollTop = Math.max(0, timeline.scrollTop - 200)
      await frames(2)
    }
    expect(o.requests.length).toBe(asked + 1)
  }, 120_000)

  it.each([[390, 844], [3840, 2160]] as const)('asks for the next page well before the top at %i × %i', async (width, height) => {
    const o = await open(width, height)
    const { timeline } = o
    // Up in small steps (a jump to three view heights from the top first, each time rows are
    // revealed above).
    const view = timeline.clientHeight
    for (let i = 0; i < 3000 && o.waiting.length === 0; i++) {
      await scrollUp(timeline, timeline.scrollTop > 3 * view ? timeline.scrollTop - 3 * view : 30)
    }
    expect(o.waiting.length).toBe(1)
    // About one and a half view heights still to scroll through when it was asked for.
    expect(o.askedFrom[o.askedFrom.length - 1]).toBeGreaterThanOrEqual(1.4 * view)
  }, 240_000)

  it('never shows the pill for a fast page', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    const pill = watchPill(timeline)
    // The pill's 300 ms timer runs on fake time from the request on: 250 ms of loading pass, the page
    // lands (waited on in frames, not timers) with no more time passed however slow the machine,
    // then far more than 300 ms passes. A page landing before its timer fired never shows the pill.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
      expect(o.waiting.length).toBe(1)
      // 250 ms of the page's loading pass: less than the pill waits for.
      await frames(2)
      vi.advanceTimersByTime(250)
      await frames(3)
      o.release()
      await inFrames(() => !timeline.classList.contains('backfilling'))
      vi.advanceTimersByTime(1000)
      await frames(3)
    } finally {
      vi.useRealTimers()
    }
    pill.stop()
    expect(pill.seen).toHaveLength(0)
  }, 120_000)

  it('asks once while the top stays in view and a page brings nothing new', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 400 && !(o.waiting.length > 0 && timeline.scrollTop === 0); i++) await scrollUp(timeline, 800)
    const asked = o.requests.length
    // A page that is all overlap (the link cut it short): nothing new, so nothing asked again.
    const held = useTranscriptStore.getState().sessions[SESSION]
    o.release({ events: held.events.slice(0, 1), hasOlder: true, firstSeq: held.firstSeq })
    await landed(timeline)
    await sleep(500)
    await frames(3)
    expect(o.requests.length).toBe(asked)
    expect(o.waiting.length).toBe(0)
  }, 120_000)

  it('stops a glide that is still going when the page lands', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 400 && !(o.waiting.length > 0 && timeline.scrollTop < 1500); i++) await scrollUp(timeline, 300)
    expect(o.waiting.length).toBe(1)
    // A long smooth glide up (a flick's momentum) is under way when the page lands.
    timeline.dispatchEvent(new WheelEvent('wheel', { deltaY: -100, bubbles: true }))
    timeline.scrollTo({ top: 0, behavior: 'smooth' })
    await frames(2)
    const drawn = markerCount(timeline)
    o.release()
    // From the frame the page is in: the row then in view stays put while the glide would go on.
    await inFrames(() => markerCount(timeline) > drawn)
    const at = await atPaint(() => firstVisible(timeline))
    await sleep(800)
    await sized(timeline)
    // The glide did not carry on into the rows that just came in.
    expect(Math.abs(await atPaint(() => topOf(timeline, at.marker)) - at.top)).toBeLessThanOrEqual(2)
    expect(timeline.scrollTop).toBeGreaterThan(1000)
  }, 120_000)

  // A touch glide and a trackpad's momentum cannot be played in a headless browser: what stops them
  // is checked instead. A finger lifted a moment ago: the timeline stops scrolling for a frame or two
  // as the page lands (which ends a glide on every browser), its gutter kept so nothing reflows.
  it('takes scrolling away for a moment when a page lands just after a flick', async () => {
    const o = await open(390, 844)
    const { timeline } = o
    for (let i = 0; i < 400 && !(o.waiting.length > 0 && timeline.scrollTop === 0); i++) await scrollUp(timeline, 500)
    const touch = (type: string) => timeline.dispatchEvent(new TouchEvent(type, {
      bubbles: true,
      changedTouches: [new Touch({ identifier: 1, target: timeline, clientX: 100, clientY: 300 })],
      touches: type === 'touchend' ? [] : [new Touch({ identifier: 1, target: timeline, clientX: 100, clientY: 300 })],
    }))
    // The input clock stands still, so the page lands "just after" the lift however long drawing
    // takes; every style the timeline takes on its way is recorded, not sampled at a time.
    const clock = handClock()
    const during: Array<{ overflow: string; gutter: string }> = []
    const mo = new MutationObserver(() => {
      during.push({ overflow: timeline.style.overflowY, gutter: timeline.style.scrollbarGutter })
    })
    mo.observe(timeline, { attributes: true, attributeFilter: ['style'] })
    try {
      touch('touchstart')
      touch('touchend')
      o.release()
      await landed(timeline)
      await inFrames(() => during.length > 0 && timeline.style.overflowY === '')
    } finally {
      mo.disconnect()
      clock.restore()
    }
    expect(during).toContainEqual({ overflow: 'hidden', gutter: 'stable' })
    expect(timeline.style.overflowY).toBe('')
    expect(getComputedStyle(timeline).overflowY).toBe('auto')
  }, 120_000)

  it('leaves scrolling alone when a page lands after keys (no momentum to stop)', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    const styles: string[] = []
    const mo = new MutationObserver(() => { styles.push(timeline.style.overflowY) })
    mo.observe(timeline, { attributes: true, attributeFilter: ['style'] })
    o.release()
    await landed(timeline)
    await frames(5)
    mo.disconnect()
    expect(styles.filter(v => v === 'hidden')).toEqual([])
  }, 120_000)

  // The wheel hold is timed on the transcript's input clock, driven here by hand: every event below
  // is sent at a set time, so a busy machine (a full parallel run) cannot stretch the gaps.
  it('holds back a wheel stream still coming in when the page lands, until it pauses', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    const clock = handClock()
    try {
      const wheel = () => {
        const e = new WheelEvent('wheel', { deltaY: -40, bubbles: true, cancelable: true })
        timeline.dispatchEvent(e)
        return e.defaultPrevented
      }
      expect(wheel()).toBe(false)
      const page = o.all.filter(e => (e.seq ?? 0) < o.requests[o.requests.length - 1]).slice(-4)
      o.release({ events: page, hasOlder: true, firstSeq: page[0].seq ?? 0 })
      await landedSmall(page[0].seq ?? 0)
      // The momentum stream goes on (16 ms, then 40 ms apart): held back.
      clock.t += 16
      expect(wheel()).toBe(true)
      clock.t += 40
      expect(wheel()).toBe(true)
      // It paused (250 ms): the next wheel is the reader scrolling again, and so is the one after.
      clock.t += 250
      expect(wheel()).toBe(false)
      clock.t += 16
      expect(wheel()).toBe(false)
    } finally {
      clock.restore()
    }
  }, 120_000)

  it('holds a stream back for a moment at most, and never a mouse wheel', async () => {
    const o = await open(1280, 800)
    const { timeline } = o
    const wheel = (deltaY: number) => {
      const e = new WheelEvent('wheel', { deltaY, bubbles: true, cancelable: true })
      timeline.dispatchEvent(e)
      return e.defaultPrevented
    }
    for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
    const clock = handClock()
    try {
      // A notched mouse wheel spun on through the landing: nothing held.
      wheel(-120)
      let page = o.all.filter(e => (e.seq ?? 0) < o.requests[o.requests.length - 1]).slice(-4)
      o.release({ events: page, hasOlder: true, firstSeq: page[0].seq ?? 0 })
      await landedSmall(page[0].seq ?? 0)
      clock.t += 16
      expect(wheel(-120)).toBe(false)
      // A small-step stream (a trackpad) kept up every 16 ms for 900 ms: held for the first 500 ms
      // after the page lands, then let go.
      clock.t += 1000
      await scrollUp(timeline, 200)
      for (let i = 0; i < 50 && o.waiting.length === 0; i++) await home(timeline)
      page = o.all.filter(e => (e.seq ?? 0) < o.requests[o.requests.length - 1]).slice(-4)
      wheel(-30)
      o.release({ events: page, hasOlder: true, firstSeq: page[0].seq ?? 0 })
      await landedSmall(page[0].seq ?? 0)
      const held: number[] = []
      for (let dt = 16; dt <= 900; dt += 16) {
        clock.t += 16
        if (wheel(-30)) held.push(dt)
      }
      expect(held[0]).toBe(16)
      expect(Math.max(...held)).toBeLessThan(500)
      expect(Math.max(...held)).toBeGreaterThanOrEqual(480)
    } finally {
      clock.restore()
    }
  }, 120_000)
})
