import { describe, expect, it } from 'vitest'
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
