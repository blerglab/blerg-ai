import { commands, page } from 'vitest/browser'
import { describe, expect, it } from 'vitest'
import { scrollToEvent } from '../../../../packages/chat/src/components/scrollToCall'
import { render, waitFor } from '@testing-library/react'
import Transcript, { type TranscriptProps } from '../../../../packages/chat/src/components/Transcript'
import type { AgentEvent } from '@blerglab/chat'
import '@blerglab/chat/tokens.css'
import '../../../../packages/chat/src/components/chat.css'

function props(sessionId: string): TranscriptProps {
  const events: AgentEvent[] = Array.from({ length: 20 }, (_, i) => ({
    type: 'agent_event', session_id: sessionId, client_event_id: `${sessionId}-${i}`,
    seq: i + 1, ts: '2026-10-13T00:00:00Z', kind: 'user_message',
    payload: { text: `Message ${i}: ${'A long message to make the transcript scroll. '.repeat(8)}`, source: 'chat' },
  }))
  return {
    sessionId, events, streaming: '', compact: true, liveIds: new Set(),
    showSkeleton: false, loadingOlder: false, hasOlder: false, starting: false, attempt: null,
    clockOffset: 0, readyCard: null, working: false, waiting: false, since: null, waitingOn: null,
    queued: [], queueState: 'sending', steers: false, pin: 0,
  }
}
function mount() {
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;top:0;left:0;width:500px;height:500px;display:flex;flex-direction:column;--chat-density:1'
  document.body.appendChild(host)
  const view = render(<Transcript {...props('first')} />, { container: host })
  const timeline = host.querySelector('.agent-timeline') as HTMLDivElement
  return { ...view, host, timeline }
}
function gap(el: HTMLElement) { return el.scrollHeight - el.clientHeight - el.scrollTop }
async function frames() {
  for (let i = 0; i < 3; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
}

describe('Transcript scroll position', () => {
  it('opens at the actual end, including the timeline bottom padding', async () => {
    const { timeline } = mount()
    await waitFor(() => expect(gap(timeline)).toBeLessThanOrEqual(1))
  })

  it('follows the end when the viewport settles after opening a session', async () => {
    const { host, timeline } = mount()
    timeline.scrollTop = timeline.scrollHeight
    await frames()
    host.style.height = '250px'
    await waitFor(() => expect(gap(timeline)).toBeLessThanOrEqual(1))
  })

  it('opens another session at its end even when both have the same event count', async () => {
    const { timeline, rerender } = mount()
    timeline.scrollTop = 0
    timeline.dispatchEvent(new Event('scroll'))
    await frames()
    rerender(<Transcript {...props('second')} />)
    await waitFor(() => expect(gap(timeline)).toBeLessThanOrEqual(1))
  })

  it('does not pull a reader to the end when resizing after they scrolled up', async () => {
    const { host, timeline } = mount()
    timeline.scrollTop = timeline.scrollHeight
    await frames()
    timeline.dispatchEvent(new WheelEvent('wheel', { deltaY: -300, bubbles: true }))
    timeline.scrollTop -= 300
    timeline.dispatchEvent(new Event('scroll'))
    await frames()
    host.style.height = '250px'
    await frames()
    expect(gap(timeline)).toBeGreaterThan(200)
  })
})

it.each([390, 3840])('keeps following across a layout clamp at %i px', async width => {
  await page.viewport(width, 800)
  const { host, timeline } = mount()
  host.style.width = `${width}px`
  await frames()
  await new Promise(resolve => setTimeout(resolve, 100))
  host.style.height = '100000px'
  expect(timeline.scrollTop).toBe(0)
  await frames() // Let the rendered clamp be observed before restoring the viewport.
  host.style.height = '500px'
  await frames()
  await waitFor(() => expect(gap(timeline)).toBeLessThanOrEqual(1))
  expect(host.querySelector('[data-testid="new-output"]')).toBeNull()
})

it.each([390, 3840])('keeps following when switching busy sessions at %i px', async width => {
  await page.viewport(width, 800)
  const host = document.createElement('div')
  host.style.cssText = `height:500px;width:${width}px;display:flex;flex-direction:column`
  document.body.appendChild(host)
  const saved = new Map<string, Parameters<NonNullable<TranscriptProps['onScrollLeave']>>[0]>()
  let active = 'a', tick = 0
  const draw = () => {
    const id = active
    return <Transcript key={id} {...props(id)}
      streaming={'Live output '.repeat(tick + 1)} scrollFrom={saved.get(id)}
      onScrollLeave={position => saved.set(id, position)} />
  }
  const view = render(draw(), { container: host })
  const timer = setInterval(() => { tick++; view.rerender(draw()) }, 75)
  try {
    for (const delay of [35, 95, 55, 85]) {
      await new Promise(resolve => setTimeout(resolve, delay))
      // A transient panel/composer layout can clamp the outgoing viewport before
      // the next output burst. No reader input accompanied that scroll.
      host.style.height = '100000px'
      expect((host.querySelector('.agent-timeline') as HTMLElement).scrollTop).toBe(0)
      await frames()
      host.style.height = '500px'
      await frames()
      active = 'b'
      view.rerender(draw())
      await frames()
      active = 'a'
      view.rerender(draw())
      await frames()
      const timeline = host.querySelector('.agent-timeline') as HTMLElement
      await waitFor(() => expect(gap(timeline)).toBeLessThanOrEqual(1))
      expect(host.querySelector('[data-testid="new-output"]')).toBeNull()
      expect(saved.get('a')).toEqual({ follow: true })
    }
  } finally { clearInterval(timer) }
})


it.each(['jump', 'selection drag', 'keyboard', 'removed touch row'])('keeps a reader in place after %s during output', async kind => {
  await page.viewport(390, 800)
  const { timeline, rerender } = mount()
  rerender(<Transcript {...props('first')} streaming="Output before navigation" />)
  await frames()
  const row = timeline.querySelectorAll<HTMLElement>('[data-event-id]')[5]
  if (kind === 'jump') {
    scrollToEvent(row.dataset.eventId!)
    rerender(<Transcript {...props('first')} streaming="Output during the smooth jump" />)
    await new Promise(resolve => setTimeout(resolve, 1200))
  } else {
    if (kind === 'selection drag') row.dispatchEvent(new PointerEvent('pointerdown', { button: 0, bubbles: true }))
    if (kind === 'keyboard') {
      await commands.transcriptInput('PageUp')
    }
    if (kind === 'removed touch row') {
      row.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
      row.remove()
      rerender(<Transcript {...props('first')} streaming="output while touching" />)
    }
    timeline.scrollTop -= 250
    timeline.dispatchEvent(new Event('scroll'))
  }
  await frames()
  const top = timeline.scrollTop
  rerender(<Transcript {...props('first')} streaming="More output after leaving the end" />)
  if (kind === 'removed touch row') await new Promise(resolve => setTimeout(resolve, 1700))
  await frames()
  expect(timeline.scrollTop).toBeCloseTo(top, 0)
  expect(gap(timeline)).toBeGreaterThan(100)
})
