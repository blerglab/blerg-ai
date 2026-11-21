import { act, render, waitFor } from '@testing-library/react'
import { expect, it } from 'vitest'
import { page } from 'vitest/browser'
import SessionTabStrip from './SessionTabStrip'
import '../index.css'
import './SessionTabBar.css'

// Measure the synchronous switch commit separately from the browser's deferred layout. A large
// newly drawn session makes a layout read in the strip pull all of that work into the commit.
for (const width of [390, 3840]) it(`defers tab strip layout on switch at ${width}`, async () => {
  await page.viewport(width, 1000)
  const items = Array.from({ length: 30 }, (_, i) => ({ id: `${i}`, label: `Session tab ${i} with a long file name for overflow.ts`, icon: null }))
  const view = (active: number) => <div style={{ width: '100%' }}>
    <SessionTabStrip active={`${active}`} items={items} label="Workspace tabs" onSelect={() => {}} onDragPosition={() => {}}>
      {items.map(item => <div key={item.id} className={`session-tab${item.id === `${active}` ? ' active' : ''}`}><button className="icon-btn" role="tab"><span>{item.label}</span></button></div>)}
    </SessionTabStrip>
    <div key={active} style={{ display: 'grid', gridTemplateColumns: 'repeat(8, 1fr)' }}>
      {Array.from({ length: 3000 }, (_, i) => <div key={i} style={{ containerType: 'inline-size' }}><span>Message {i} in session {active}</span></div>)}
    </div>
  </div>
  const mounted = render(view(0))
  const element = document.querySelector<HTMLElement>('.session-tabs')!
  await waitFor(() => expect(document.querySelector('.session-tab-overflow')).not.toBeNull())
  const descriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'clientWidth')!
  let reads = 0, committing = false
  Object.defineProperty(Element.prototype, 'clientWidth', { configurable: true, get(this: Element) {
    if (committing && this === element) reads++
    return descriptor.get!.call(this)
  } })
  const loops: string[] = []
  const onError = (event: ErrorEvent) => { if (/ResizeObserver loop/.test(event.message)) loops.push(event.message) }
  window.addEventListener('error', onError)
  const samples: number[] = []
  try {
    for (let i = 1; i <= 7; i++) {
      performance.mark('tab-switch-start')
      committing = true
      act(() => mounted.rerender(view(i)))
      committing = false
      samples.push(performance.measure('tab-switch', 'tab-switch-start').duration)
      // Finish each deferred reveal before beginning the next switch.
      await waitFor(() => {
        const tab = element.querySelector<HTMLElement>('.active')!.getBoundingClientRect(), box = element.getBoundingClientRect()
        expect(tab.left).toBeGreaterThanOrEqual(box.left - 1)
        expect(tab.right).toBeLessThanOrEqual(box.right + 1)
      })
      await new Promise<void>(resolve => { const observer = new ResizeObserver(() => { observer.disconnect(); resolve() }); observer.observe(element) })
      // A promise resumes inside ResizeObserver delivery. Begin the next commit on the next
      // frame so reconnecting the strip observer cannot leave a same-depth report undelivered.
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    }
    console.warn(JSON.stringify({ width, commitMedianMs: [...samples].sort((a, b) => a - b)[3], layoutReads: reads }))
    // Largest measured median was 139 ms before this pass, 125 ms under load here.
    expect([...samples].sort((a, b) => a - b)[3]).toBeLessThan(250)
    expect(reads).toBe(0)
    expect(loops).toEqual([])
  } finally {
    window.removeEventListener('error', onError)
    Object.defineProperty(Element.prototype, 'clientWidth', descriptor)
    performance.clearMarks('tab-switch-start'); performance.clearMeasures('tab-switch')
  }
})
