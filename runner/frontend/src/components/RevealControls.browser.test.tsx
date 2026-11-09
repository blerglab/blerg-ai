import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { renderEvent } from '@blerglab/chat'
import { canFollowReveal } from '../lib/uiReveal'
import { pulse } from '../lib/changePulse'

// Keep the mounted UI on the test page; the production socket starts sign-in on import.
vi.mock('../ws', () => ({ onMessage: () => () => {}, onClose: () => () => {} }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => 'person' }))

afterEach(cleanup)
for (const width of [390, 3840]) it(`keeps reveal suggestions inert until clicked at ${width}`, async () => {
 const { page } = await import('vitest/browser')
 await page.viewport(width, 900)
 const listener = vi.fn()
 window.addEventListener('blerg-ui-reveal', listener)
 render(<>{renderEvent({type:'agent_event',session_id:'fixture',client_event_id:'r',ts:new Date().toISOString(),kind:'ui_reveal',payload:{action:'open',target:'src/readme.md'}},new Map())}</>)
 expect(listener).not.toHaveBeenCalled()
 fireEvent.click(screen.getByRole('button', {name:'Show'}))
 expect(listener).toHaveBeenCalledOnce()
 expect(listener.mock.calls[0][0].detail).toEqual({session:'fixture',action:'open',target:'src/readme.md'})
 const card = screen.getByTestId('ui-reveal')
 expect(card.getBoundingClientRect().width).toBeLessThanOrEqual(width)
 window.removeEventListener('blerg-ui-reveal',listener)
})
it('blocks follow around real focus and modal ownership; pulses preserve focus', () => {
 render(<><textarea aria-label="Draft"/><div role="dialog">Viewer</div><div data-testid="row">Changed</div></>)
 screen.getByRole('textbox').focus()
 pulse(screen.getByTestId('row'))
 expect(document.activeElement).toBe(screen.getByRole('textbox'))
 expect(canFollowReveal()).toBe(false)
 screen.getByRole('textbox').blur()
 expect(canFollowReveal()).toBe(false)
})
it('uses a steady highlight under reduced motion and clears it after the short pulse', async () => {
 const { commands } = await import('vitest/browser')
 await commands.setUsageReducedMotion(true)
 try {
  render(<div data-testid="changed-row" tabIndex={0}>Changed</div>)
  const row = screen.getByTestId('changed-row')
  row.focus()
  pulse(row)
  expect(getComputedStyle(row).animationTimingFunction).toBe('steps(1)')
  const animation = row.getAnimations()[0]
  expect(animation).toBeTruthy()
  const frames = (animation.effect as KeyframeEffect).getKeyframes()
  expect(frames[0].backgroundColor).toBe(frames[frames.length - 1].backgroundColor)
  expect(document.activeElement).toBe(row)
  await animation.finished
  await vi.waitFor(() => expect(row.classList.contains('change-pulse')).toBe(false))
 } finally { await commands.setUsageReducedMotion(false) }
})
it('describes an attachment action without exposing its storage identifier', () => {
 const id = '11111111111111111111111111111111'
 render(<>{renderEvent({type:'agent_event',session_id:'fixture',client_event_id:'attachment',ts:new Date().toISOString(),kind:'ui_reveal',payload:{action:'attachment',target:id}},new Map())}</>)
 expect(screen.getByTestId('ui-reveal').textContent).not.toContain(id)
 expect(screen.getByTestId('ui-reveal').textContent).toContain('open an attachment')
})

it('allows Follow while its checkbox or an untouched automatic composer owns focus', () => {
 render(<><input type="checkbox" aria-label="Follow" data-reveal-follow=""/><textarea aria-label="Composer" data-autofocused="true" /></>)
 screen.getByRole('checkbox').focus()
 expect(canFollowReveal()).toBe(true)
 const box=screen.getByRole('textbox') as HTMLTextAreaElement
 box.focus();expect(canFollowReveal()).toBe(true)
 box.value='Draft';expect(canFollowReveal()).toBe(false)
})
for (const width of [390, 3840]) it(`applies explicit Show through the mounted controls at ${width}`, async () => {
 const { page } = await import('vitest/browser')
 const { default: RevealControls } = await import('./RevealControls')
 await page.viewport(width, 900)
 const attachment = vi.fn()
 const view = render(<RevealControls sessionId="mounted-fixture" onAttachment={attachment} host={document.body} />)
 screen.getByRole('checkbox').focus()
 expect(canFollowReveal()).toBe(true)
 const request = new CustomEvent('blerg-ui-reveal', { cancelable: true, detail: { session: 'mounted-fixture', action: 'attachment', target: '11111111111111111111111111111111' } })
 fireEvent(window, request)
 expect(request.defaultPrevented).toBe(true)
 expect(attachment).toHaveBeenCalledOnce()
 expect(screen.getByRole('status').textContent).not.toContain('11111111111111111111111111111111')
 expect(view.container.getBoundingClientRect().width).toBeLessThanOrEqual(width)
})
