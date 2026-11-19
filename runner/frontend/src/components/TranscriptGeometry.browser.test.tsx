import { Activity, useState } from 'react'
import { commands, page } from 'vitest/browser'
import { expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import Transcript, { type TranscriptProps } from '../../../../packages/chat/src/components/Transcript'
import { jumpToElement } from '../../../../packages/chat/src/components/scrollToCall'
import '@blerglab/chat/tokens.css'
import '../../../../packages/chat/src/components/chat.css'

const gap = (root: HTMLElement) => root.scrollHeight - root.clientHeight - root.scrollTop
const frames = async () => { for (let i = 0; i < 4; i++) await new Promise(requestAnimationFrame) }
function mount(width: number, kept = false) {
  const host = document.createElement('div')
  host.style.cssText = `position:fixed;inset:0 auto auto 0;width:${width}px;height:500px;display:flex;flex-direction:column`
  document.body.append(host)
  const p: TranscriptProps = {
    sessionId: 'geometry', compact: true, liveIds: new Set(),
    events: Array.from({ length: 60 }, (_, i) => ({ type: 'agent_event', session_id: 'geometry', client_event_id: `g${i}`, seq: i + 1, ts: '2026-10-13T00:00:00Z', kind: 'user_message', payload: { text: `Row ${i}: ${'Selectable transcript text. '.repeat(30)}` } })),
    streaming: '', showSkeleton: false, loadingOlder: false, starting: false, attempt: null,
    clockOffset: 0, readyCard: <a href="#earlier" id="earlier">Earlier link</a>, working: false, waiting: false,
    since: null, waitingOn: null, queued: [], queueState: 'sending', steers: false, pin: 0,
    ...(kept ? { scrollFrom: { follow: false as const, first: 'g0', top: 2000 } } : {}),
  }
  // The jump control must stay still while the restore fixture resizes the transcript.
  // Otherwise Playwright waits for click stability instead of exercising jump cancellation.
  function Surface() {
    const [hidden, setHidden] = useState(false)
    return <><Activity mode={hidden ? 'hidden' : 'visible'}><Transcript {...p} /></Activity><button tabIndex={-1} onClick={() => setHidden(!hidden)}>Switch session</button><button tabIndex={-1} onClick={() => jumpToElement(host.querySelector('[data-event-id="g59"]')!, { block: 'end', behavior: 'instant' })}>Jump to end row</button><button id="outside" style={{ position: 'fixed', bottom: 0, left: 0 }} onClick={() => jumpToElement(host.querySelector('[data-event-id="g0"]')!)}>Jump</button></>
  }
  const draw = () => <Surface />
  const view = render(draw(), { container: host })
  const root = host.querySelector<HTMLElement>('.agent-timeline')!
  // Keep a real classic scrollbar available to the pointer at both viewport widths.
  const style = document.createElement('style')
  style.textContent = '.agent-timeline { scrollbar-width:auto } .agent-timeline::-webkit-scrollbar { width:14px } .agent-timeline::-webkit-scrollbar-thumb { background:#888 } .agent-timeline::-webkit-scrollbar-track { background:#eee }'
  host.append(style)
  root.querySelectorAll('button').forEach(button => { button.tabIndex = -1 })
  return { host, root, p, update: () => view.rerender(draw()) }
}
for (const width of [390, 3840]) {
  it('releases a long touch whose row is removed before the finger lifts', async () => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    await frames()
    let touched = ''
    root.addEventListener('touchstart', e => { touched = (e.target as Element).closest<HTMLElement>('[data-event-id]')?.dataset.eventId ?? '' }, { once: true })
    await commands.transcriptInput('touchStart')
    expect(touched).not.toBe('')
    await new Promise(resolve => setTimeout(resolve, 2000))
    p.events = p.events.filter(event => event.client_event_id !== touched)
    update()
    await commands.transcriptInput('touchEnd')
    await new Promise(resolve => setTimeout(resolve, 2500))
    p.streaming = 'Output after the lost touch'
    update()
    await frames()
    expect(gap(root)).toBeLessThanOrEqual(1)
  })

  it.each(['PageUp', 'wheel', 'scrollbar', 'selection', 'Shift+Tab', 'jump', 'removed touch row'])(`leaves following through real %s at ${width}px`, async kind => {
    await page.viewport(width, 800)
    const { host, root, p, update } = mount(width)
    await frames()
    expect(gap(root)).toBeLessThanOrEqual(1)
    if (kind === 'jump') {
      await page.getByRole('button', { name: 'Jump', exact: true }).click()
      await new Promise(resolve => setTimeout(resolve, 2100))
    }
    else if (kind === 'removed touch row') {
      let touched = ''
      root.addEventListener('touchstart', e => { touched = (e.target as Element).closest<HTMLElement>('[data-event-id]')?.dataset.eventId ?? '' }, { once: true })
      await commands.transcriptInput('touchStart')
      expect(touched).not.toBe('')
      p.events = p.events.filter(event => event.client_event_id !== touched)
      update()
      await commands.transcriptInput('touchMove')
      await commands.transcriptInput('touchEnd')
    } else await commands.transcriptInput(kind)
    await frames()
    await waitFor(() => expect(gap(root)).toBeGreaterThan(8))
    const top = root.scrollTop
    p.streaming = 'More output below the reader'
    update()
    await frames()
    expect(root.scrollTop).toBeCloseTo(top, 0)
    expect(host.querySelector('[data-testid="new-output"]')).not.toBeNull()
  })
  it.each(['outside:ArrowUp', 'outside:PageUp', 'outside:Home', 'click', 'idle clamp', 'busy clamp'])(`keeps following after %s at ${width}px`, async kind => {
    await page.viewport(width, 800)
    const { host, root, p, update } = mount(width)
    await frames()
    if (kind.includes('clamp')) {
      p.working = kind === 'busy clamp'
      update()
      host.style.height = '100000px'
      expect(gap(root)).toBeLessThanOrEqual(1)
      await frames()
      host.style.height = '500px'
    } else {
      await commands.transcriptInput(kind)
      // Layout immediately after click must not be attributed to that click.
      host.style.height = '450px'
    }
    p.streaming = 'Follow this output'
    update()
    await frames()
    expect(gap(root)).toBeLessThanOrEqual(1)
  })
  it.each(['hold', 'touchStart'])(`holds text under a selection press (%s) at ${width}px`, async kind => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    await frames()
    await commands.transcriptInput(kind)
    const top = root.scrollTop
    p.streaming = 'Growing output\n'.repeat(100)
    update()
    await frames()
    await new Promise(resolve => setTimeout(resolve, 2100))
    expect(root.scrollTop).toBeCloseTo(top, 0)
    const row = Array.from(root.querySelectorAll<HTMLElement>('[data-event-id]')).find(el => el.getBoundingClientRect().top >= root.getBoundingClientRect().top)!
    const rowTop = row.getBoundingClientRect().top
    p.events = [{ ...p.events[0], client_event_id: 'g-before', seq: 0 }, ...p.events]
    update()
    await frames()
    expect(row.getBoundingClientRect().top).toBeCloseTo(rowTop, 0)
    await commands.transcriptInput(kind === 'hold' ? 'release' : 'touchEnd')
    expect(gap(root)).toBeLessThanOrEqual(1)
  })
  it.each(['PageUp', 'wheel', 'scrollbar', 'selection', 'Shift+Tab', 'jump', 'removed touch row'])(`cancels kept-place restore on %s at ${width}px`, async kind => {
    await page.viewport(width, 800)
    const { host, root, p, update } = mount(width, true)
    // Repeated layout changes exercise the two-second restore, rather than its still-layout exit.
    let n = 0
    const timer = setInterval(() => { host.style.height = `${450 + (n++ % 2) * 30}px` }, 15)
    try {
      if (kind === 'jump') {
        await page.getByRole('button', { name: 'Jump', exact: true }).click()
        await new Promise(resolve => setTimeout(resolve, 2100))
      } else if (kind === 'removed touch row') {
        let touched = ''
        root.addEventListener('touchstart', e => { touched = (e.target as Element).closest<HTMLElement>('[data-event-id]')?.dataset.eventId ?? '' }, { once: true })
        await commands.transcriptInput('touchStart')
        expect(touched).not.toBe('')
        p.events = p.events.filter(event => event.client_event_id !== touched)
        update()
        await commands.transcriptInput('touchMove')
        await commands.transcriptInput('touchEnd')
      } else await commands.transcriptInput(kind)
      clearInterval(timer)
      await frames()
      const top = root.scrollTop
      p.streaming = 'Output after input during placement'
      update()
      await new Promise(resolve => setTimeout(resolve, 2100))
      expect(root.scrollTop).toBeCloseTo(top, 0)
    } finally { clearInterval(timer) }
  })
  it.each(['PageUpTop', 'selectionTop', 'jump'])(`loads older rows through %s at ${width}px`, async kind => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    p.events = p.events.slice(0, 20)
    p.hasOlder = true
    const load = vi.fn()
    p.onLoadOlder = load
    update()
    await frames()
    expect(load).not.toHaveBeenCalled()
    if (kind === 'jump') await page.getByRole('button', { name: 'Jump', exact: true }).click()
    else await commands.transcriptInput(kind)
    await waitFor(() => expect(load).toHaveBeenCalled(), { timeout: 3000 })
    expect(gap(root)).toBeGreaterThan(8)
  })
  it(`keeps following on a session return via real clicks at ${width}px`, async () => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    await frames()
    await page.getByRole('button', { name: 'Switch session', exact: true }).click()
    await frames()
    await page.getByRole('button', { name: 'Switch session', exact: true }).click()
    p.streaming = 'Returned output'
    update()
    await frames()
    expect(gap(root)).toBeLessThanOrEqual(1)
  })

  it(`stops following even for a jump to an already visible row at ${width}px`, async () => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    await frames()
    await page.getByRole('button', { name: 'Jump to end row', exact: true }).click()
    await frames()
    const top = root.scrollTop
    p.streaming = 'Output following an explicit jump'
    update()
    await frames()
    expect(root.scrollTop).toBeCloseTo(top, 0)
    expect(gap(root)).toBeGreaterThan(8)
  })

  it(`checks native find-in-page availability at ${width}px`, async context => {
    await page.viewport(width, 800)
    const { root, p, update } = mount(width)
    await frames()
    await commands.transcriptInput('find')
    if (gap(root) <= 8) context.skip('Headless Chromium did not expose native find-in-page through keyboard input')
    const top = root.scrollTop
    p.streaming = 'Output after find'
    update()
    await frames()
    expect(root.scrollTop).toBeCloseTo(top, 0)
  })

}
