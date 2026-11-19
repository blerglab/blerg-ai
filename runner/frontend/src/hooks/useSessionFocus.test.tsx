import { act, renderHook } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { useSessionFocus } from './useSessionFocus'
it('review: restores composers by view identity after their DOM order changes', async () => {
  const root = document.createElement('div')
  root.innerHTML = '<div data-view-id="session-chat:side"><div class="agent-composer"><textarea></textarea></div></div><div data-view-id="chat"><div class="agent-composer"><textarea></textarea></div></div>'
  document.body.append(root)
  const side = root.querySelector('textarea')!, main = root.querySelector('[data-view-id="chat"] textarea') as HTMLTextAreaElement
  for (const input of [side, main]) vi.spyOn(input, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
  const hook = renderHook(({ id }) => useSessionFocus({ current: root }, id, true, false, { current: null }), { initialProps: { id: 'main' } })
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(main).toHaveFocus()
  act(() => side.focus())
  hook.rerender({ id: 'other' })
  root.prepend(root.lastElementChild!)
  hook.rerender({ id: 'main' })
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(side).toHaveFocus()
  hook.unmount(); root.remove()
})
it('review: remembers a side composer when its saved index shifts', async () => {
  const root = document.createElement('div')
  root.innerHTML = '<div data-view-id="chat"><div class="agent-composer"><textarea></textarea></div></div><div data-view-id="session-chat:side"><div class="agent-composer"><textarea></textarea></div></div>'
  document.body.append(root)
  const main = root.querySelector('textarea')!, side = root.querySelector('[data-view-id="session-chat:side"] textarea') as HTMLTextAreaElement
  for (const input of [main, side]) vi.spyOn(input, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
  const hook = renderHook(({ id }) => useSessionFocus({ current: root }, id, true, false, { current: null }), { initialProps: { id: 'main' } })
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  act(() => side.focus())
  hook.rerender({ id: 'other' })
  root.prepend(root.lastElementChild!)
  hook.rerender({ id: 'main' })
  await act(async () => { await new Promise(resolve => requestAnimationFrame(resolve)) })
  expect(side).toHaveFocus()
  hook.unmount(); root.remove()
})
